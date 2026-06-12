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

// Inode field offsets within the 128-byte ufs1_dinode. Mirrored from
// struct ufs1_dinode in sys/ufs/ufs/dinode.h. UFS1 keeps di_size as a
// 64-bit field but uses 32-bit (ufs1_daddr_t) block pointers and a
// 32-bit di_blocks, and stores uid/gid near the end of the inode.
const (
	ino1OffMode      = 0
	ino1OffNlink     = 2
	ino1OffSize      = 8  // u_int64_t di_size
	ino1OffDirect    = 40 // int32_t di_db[12]
	ino1OffIndirect  = 88 // int32_t di_ib[3]
	ino1OffFlags     = 100
	ino1OffBlocks    = 104 // int32_t di_blocks
	ino1OffUID       = 112
	ino1OffGID       = 116
	ino1OffShortlink = ino1OffDirect
	// ino1ShortlinkLen is the size of the di_db[]+di_ib[] area that
	// holds an inline ("fast") symlink target on UFS1: 15 32-bit
	// pointers = 60 bytes.
	ino1ShortlinkLen = (NumDirect + NumIndirect) * 4
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

	// Raw is the full on-disk inode image, useful for callers that
	// need to peek at fields we don't decode (e.g. the embedded
	// shortlink target). For UFS2 the whole 256 bytes are populated;
	// for UFS1 only the first 128 bytes carry meaningful data.
	Raw [InodeSize]byte

	// isUFS1 records the on-disk format this inode was decoded from.
	// It controls the inline-symlink offset/length and is mirrored
	// from the superblock at parse time so helper methods on Inode
	// (e.g. Shortlink) stay self-contained.
	isUFS1 bool
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
	n := sb.dinodeSize()
	if _, err := rs.ReadAt(buf[:n], off); err != nil {
		return nil, fmt.Errorf("ufs: read inode %d at %d: %w", ino, off, err)
	}
	return parseInodeFmt(buf[:n], sb.IsUFS1)
}

// parseInode decodes a UFS2 on-disk dinode in `buf`. The buffer length
// is checked so a short read doesn't panic the decoder. It is kept as
// a thin wrapper around parseInodeFmt for the UFS2 case.
func parseInode(buf []byte) (*Inode, error) {
	return parseInodeFmt(buf, false)
}

// parseInodeFmt decodes an on-disk dinode in `buf`. When isUFS1 is set
// the buffer is interpreted as a 128-byte struct ufs1_dinode with
// 32-bit block pointers; otherwise as a 256-byte struct ufs2_dinode
// with 64-bit pointers.
func parseInodeFmt(buf []byte, isUFS1 bool) (*Inode, error) {
	le := binary.LittleEndian
	if isUFS1 {
		if len(buf) < UFS1InodeSize {
			return nil, fmt.Errorf("ufs: short ufs1 inode buffer %d < %d", len(buf), UFS1InodeSize)
		}
		in := &Inode{
			Mode:   le.Uint16(buf[ino1OffMode:]),
			Nlink:  le.Uint16(buf[ino1OffNlink:]),
			UID:    le.Uint32(buf[ino1OffUID:]),
			GID:    le.Uint32(buf[ino1OffGID:]),
			Size:   le.Uint64(buf[ino1OffSize:]),
			Blocks: uint64(le.Uint32(buf[ino1OffBlocks:])),
			Flags:  le.Uint32(buf[ino1OffFlags:]),
			isUFS1: true,
		}
		for i := 0; i < NumDirect; i++ {
			in.Direct[i] = uint64(le.Uint32(buf[ino1OffDirect+i*4:]))
		}
		for i := 0; i < NumIndirect; i++ {
			in.Indirect[i] = uint64(le.Uint32(buf[ino1OffIndirect+i*4:]))
		}
		copy(in.Raw[:UFS1InodeSize], buf[:UFS1InodeSize])
		return in, nil
	}

	if len(buf) < InodeSize {
		return nil, fmt.Errorf("ufs: short inode buffer %d < %d", len(buf), InodeSize)
	}
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
	// The inline-target area lives in the di_db[]+di_ib[] block
	// pointers, whose offset and length depend on the on-disk format
	// (60 bytes at offset 40 for UFS1, 120 bytes at offset 112 for
	// UFS2).
	off, maxLen := inoOffShortlink, inoShortlinkLen
	if in.isUFS1 {
		off, maxLen = ino1OffShortlink, ino1ShortlinkLen
	}
	if in.Size == 0 || in.Size > uint64(maxLen) {
		return "", false
	}
	// Defensive: also obey the superblock's own maxsymlinklen if it
	// is positive (some images set it explicitly).
	if sb.Maxsymlinklen > 0 && in.Size > uint64(sb.Maxsymlinklen) {
		return "", false
	}
	return string(in.Raw[off : off+int(in.Size)]), true
}
