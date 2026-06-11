// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"encoding/binary"
	"fmt"
	"io"
)

// File type bits in di_mode (UFS uses BSD-style octal constants
// shared with ext2/POSIX). The IFMT mask isolates the type.
const (
	IFMT  uint16 = 0o170000
	IFIFO uint16 = 0o010000
	IFCHR uint16 = 0o020000
	IFDIR uint16 = 0o040000
	IFBLK uint16 = 0o060000
	IFREG uint16 = 0o100000
	IFLNK uint16 = 0o120000
	IFSCK uint16 = 0o140000
)

// Inode field offsets within the 256-byte ufs2_dinode. Mirrored
// from sys/ufs/ufs/dinode.h.
const (
	inoOffMode      = 0
	inoOffNlink     = 2
	inoOffUID       = 4
	inoOffGID       = 8
	inoOffBlksize   = 12
	inoOffSize      = 16
	inoOffBlocks    = 24
	inoOffAtime     = 32
	inoOffMtime     = 40
	inoOffCtime     = 48
	inoOffBirth     = 56
	inoOffGen       = 80
	inoOffFlags     = 88
	inoOffExtsize   = 92
	inoOffDirect    = 112
	inoOffIndirect  = 208
	inoOffModrev    = 232
	inoOffShortlink = 112
	inoShortlinkLen = (NumDirect + NumIndirect) * 8
)

// Inode mirrors the fields of struct ufs2_dinode that the read-only
// driver consults. We carry the raw 256 bytes alongside the decoded
// view so callers that need the inline shortlink can reach for it
// without a re-read.
type Inode struct {
	// Mode is di_mode: high four bits are the file type (IFMT),
	// low twelve are the permission bits.
	Mode uint16
	// Nlink is the POSIX link count.
	Nlink uint16
	// UID is the owner uid.
	UID uint32
	// GID is the owner gid.
	GID uint32
	// Size is the file size in bytes (di_size).
	Size uint64
	// Blocks is the number of 512-byte disk blocks actually
	// allocated to the file (di_blocks).
	Blocks uint64
	// Flags is the BSD chflags(2) word (di_flags).
	Flags uint32
	// Extsize is the size of the extended-attribute area in bytes;
	// non-zero means the inode carries di_extb[] block pointers we
	// currently ignore.
	Extsize uint32

	// Direct holds the 12 direct fragment pointers (di_db).
	Direct [NumDirect]uint64
	// Indirect holds the 3 indirect-block pointers (di_ib): single,
	// double, triple.
	Indirect [NumIndirect]uint64

	// Raw is the full 256-byte on-disk inode image, useful for
	// callers that need to peek at fields we don't decode (e.g.
	// the embedded shortlink target at offset 112).
	Raw [InodeSize]byte
}

// ReadInode pulls one inode out of the on-disk inode table using the
// superblock's geometry, decodes the fields the driver needs and
// returns the result.
func ReadInode(rs io.ReaderAt, sb *Superblock, ino uint64) (*Inode, error) {
	if ino == 0 {
		return nil, fmt.Errorf("ufs: inode 0 is reserved")
	}
	off := sb.InodeOffset(ino)
	var buf [InodeSize]byte
	if _, err := rs.ReadAt(buf[:], off); err != nil {
		return nil, fmt.Errorf("ufs: read inode %d at %d: %w", ino, off, err)
	}
	return parseInode(buf[:])
}

// parseInode decodes the on-disk dinode in `buf`. The buffer length
// is checked so a short read doesn't panic the decoder.
func parseInode(buf []byte) (*Inode, error) {
	if len(buf) < InodeSize {
		return nil, fmt.Errorf("ufs: short inode buffer %d < %d", len(buf), InodeSize)
	}
	le := binary.LittleEndian
	in := &Inode{
		Mode:    le.Uint16(buf[inoOffMode:]),
		Nlink:   le.Uint16(buf[inoOffNlink:]),
		UID:     le.Uint32(buf[inoOffUID:]),
		GID:     le.Uint32(buf[inoOffGID:]),
		Size:    le.Uint64(buf[inoOffSize:]),
		Blocks:  le.Uint64(buf[inoOffBlocks:]),
		Flags:   le.Uint32(buf[inoOffFlags:]),
		Extsize: le.Uint32(buf[inoOffExtsize:]),
	}
	for i := 0; i < NumDirect; i++ {
		in.Direct[i] = le.Uint64(buf[inoOffDirect+i*8:])
	}
	for i := 0; i < NumIndirect; i++ {
		in.Indirect[i] = le.Uint64(buf[inoOffIndirect+i*8:])
	}
	copy(in.Raw[:], buf[:InodeSize])
	return in, nil
}

// IsDir reports whether the inode is a directory (IFDIR set in di_mode).
func (in *Inode) IsDir() bool { return in.Mode&IFMT == IFDIR }

// IsRegular reports whether the inode is a regular file (IFREG).
func (in *Inode) IsRegular() bool { return in.Mode&IFMT == IFREG }

// IsSymlink reports whether the inode is a symbolic link (IFLNK).
func (in *Inode) IsSymlink() bool { return in.Mode&IFMT == IFLNK }

// FileType returns the four high bits of di_mode, useful when the
// caller wants to compare against IFREG/IFDIR/IFLNK without the perm
// bits in the way.
func (in *Inode) FileType() uint16 { return in.Mode & IFMT }

// Shortlink returns the target of an inline ("fast") symbolic link
// embedded in the 120-byte block-pointer area. Returns false if the
// inode is not a symlink or the target spills into a data block.
//
// The UFS2 "fast symlink" optimisation stores the link target in the
// inode itself when the target is short enough to fit in the
// (UFS_NDADDR + UFS_NIADDR) * sizeof(ufs2_daddr_t) = 120 bytes
// otherwise used for block pointers.
func (in *Inode) Shortlink(sb *Superblock) (string, bool) {
	if !in.IsSymlink() {
		return "", false
	}
	if in.Blocks != 0 {
		// Target was spilled to a data block; caller must read
		// it through the block reader.
		return "", false
	}
	if in.Size == 0 || in.Size > uint64(inoShortlinkLen) {
		return "", false
	}
	// Defensive: also obey the superblock's own maxsymlinklen if it
	// is positive (some images set it explicitly).
	if sb.Maxsymlinklen > 0 && in.Size > uint64(sb.Maxsymlinklen) {
		return "", false
	}
	return string(in.Raw[inoOffShortlink : inoOffShortlink+int(in.Size)]), true
}
