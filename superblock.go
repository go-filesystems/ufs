// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"encoding/binary"
	"fmt"
	"io"
)

// On-disk superblock layout constants. See sys/ufs/ffs/fs.h in the
// FreeBSD source tree for the canonical definitions.
const (
	// SblockUFS2 is the byte offset of the primary UFS2 superblock on
	// the backing device. Each UFS2 cylinder group additionally holds
	// a copy for crash recovery, but the read-only driver only ever
	// consults the primary.
	SblockUFS2 = 65536

	// SblockSize is the on-disk size reserved for the superblock
	// region (8 KiB). The struct fs payload is ~1376 bytes; the rest
	// is padding.
	SblockSize = 8192

	// MagicUFS2 identifies a valid UFS2 superblock.
	MagicUFS2 uint32 = 0x19540119
	// MagicUFS1 identifies a UFS1 superblock. UFS1 is out of scope
	// for sprint 2A; we expose the constant so Open can produce a
	// useful diagnostic.
	MagicUFS1 uint32 = 0x00011954

	// RootInode is the inode number of the filesystem root directory
	// in both UFS1 and UFS2 (UFS_ROOTINO in dinode.h).
	RootInode = 2

	// InodeSize is the size of a UFS2 on-disk dinode in bytes
	// (struct ufs2_dinode). UFS1 uses 128-byte inodes.
	InodeSize = 256

	// NumDirect is the count of direct block pointers in a UFS2
	// inode (UFS_NDADDR).
	NumDirect = 12
	// NumIndirect is the count of indirect block-pointer levels
	// (UFS_NIADDR): single, double, triple.
	NumIndirect = 3
)

// Field offsets within struct fs (on-disk). These mirror the layout
// in sys/ufs/ffs/fs.h on FreeBSD 14.x amd64 (where sizeof(void*) ==
// 8 so the fs_ocsp[NOCSPTRS] padding sums to 120 bytes). The struct
// is asserted to be 1376 bytes long by FreeBSD's CTASSERT.
const (
	offSblkno         = 8
	offCblkno         = 12
	offIblkno         = 16
	offDblkno         = 20
	offNcg            = 44
	offBsize          = 48
	offFsize          = 52
	offFrag           = 56
	offBshift         = 80
	offFshift         = 84
	offFsbtodb        = 100
	offSbsize         = 104
	offNindir         = 116
	offInopb          = 120
	offIpg            = 184
	offFpg            = 188
	offFlags          = 1312
	offMaxsymlinklen  = 1320
	offOldInodefmt    = 1324
	offMaxfilesize    = 1328
	offMagic          = 1372
)

// Superblock holds the decoded UFS2 superblock fields needed by the
// driver. We intentionally decode only the read-side subset; many
// fields (allocation hints, snapshot lists, journal pointers) are not
// consulted by a read-only client.
type Superblock struct {
	// Sblkno is the offset of the super-block within a cylinder
	// group, measured in fragments.
	Sblkno int32
	// Cblkno is the offset of the cylinder-group block within a
	// cylinder group, measured in fragments.
	Cblkno int32
	// Iblkno is the offset of the inode table within a cylinder
	// group, measured in fragments.
	Iblkno int32
	// Dblkno is the offset of the first data area within a cylinder
	// group, measured in fragments.
	Dblkno int32

	// Ncg is the total number of cylinder groups in the filesystem.
	Ncg uint32

	// Bsize is the filesystem block size in bytes (e.g. 32768).
	Bsize int32
	// Fsize is the fragment size in bytes (e.g. 4096).
	Fsize int32
	// Frag is the number of fragments per block (Bsize / Fsize).
	Frag int32

	// Bshift is log2(Bsize); used to convert byte offsets to logical
	// block numbers (file-relative).
	Bshift int32
	// Fshift is log2(Fsize); used to convert byte offsets to
	// fragment counts.
	Fshift int32

	// Fsbtodb is the shift constant for converting filesystem
	// fragments to 512-byte disk blocks. A fragment occupies
	// (1 << Fsbtodb) sectors.
	Fsbtodb int32

	// Sbsize is the actual on-disk superblock size in bytes.
	Sbsize int32

	// Nindir is the number of pointers per indirect block
	// (Bsize / 8 on UFS2).
	Nindir int32

	// Inopb is the number of inodes per filesystem block
	// (Bsize / InodeSize).
	Inopb uint32

	// Ipg is the number of inodes per cylinder group.
	Ipg uint32
	// Fpg is the number of fragments per cylinder group.
	Fpg int32

	// Flags is the FS_ flag bitfield (see fs.h FS_UNCLEAN etc.).
	Flags int32

	// Maxsymlinklen is the maximum length of an inline ("fast")
	// symbolic link whose target is stored in the inode's direct
	// block pointers rather than in a data block.
	Maxsymlinklen int32

	// OldInodefmt is FS_44INODEFMT (2) for any UFS2 image; the
	// "old" naming is historical (the field predates the UFS1 vs
	// UFS2 distinction).
	OldInodefmt int32

	// Maxfilesize is the largest representable file size.
	Maxfilesize uint64

	// Magic is FS_UFS2_MAGIC (0x19540119) for a valid UFS2 image.
	Magic uint32
}

// ReadSuperblock pulls the primary UFS2 superblock off the backing
// device at SblockUFS2, decodes the fields the driver needs, and
// validates magic/inodefmt/sizing invariants. Returns ErrBadSuperblock
// on any failure so callers can distinguish "not a UFS2 image" from a
// generic I/O error.
func ReadSuperblock(rs io.ReaderAt) (*Superblock, error) {
	buf := make([]byte, SblockSize)
	if _, err := rs.ReadAt(buf, SblockUFS2); err != nil {
		return nil, fmt.Errorf("ufs: read superblock at %d: %w", SblockUFS2, err)
	}
	sb, err := parseSuperblock(buf)
	if err != nil {
		return nil, err
	}
	return sb, nil
}

// parseSuperblock decodes the on-disk struct fs out of an
// SblockSize-bytes buffer. Exposed (unexported) for direct testing
// without needing an io.ReaderAt.
func parseSuperblock(buf []byte) (*Superblock, error) {
	if len(buf) < 1376 {
		return nil, fmt.Errorf("%w: short buffer %d < 1376", ErrBadSuperblock, len(buf))
	}
	le := binary.LittleEndian
	sb := &Superblock{
		Sblkno:        int32(le.Uint32(buf[offSblkno:])),
		Cblkno:        int32(le.Uint32(buf[offCblkno:])),
		Iblkno:        int32(le.Uint32(buf[offIblkno:])),
		Dblkno:        int32(le.Uint32(buf[offDblkno:])),
		Ncg:           le.Uint32(buf[offNcg:]),
		Bsize:         int32(le.Uint32(buf[offBsize:])),
		Fsize:         int32(le.Uint32(buf[offFsize:])),
		Frag:          int32(le.Uint32(buf[offFrag:])),
		Bshift:        int32(le.Uint32(buf[offBshift:])),
		Fshift:        int32(le.Uint32(buf[offFshift:])),
		Fsbtodb:       int32(le.Uint32(buf[offFsbtodb:])),
		Sbsize:        int32(le.Uint32(buf[offSbsize:])),
		Nindir:        int32(le.Uint32(buf[offNindir:])),
		Inopb:         le.Uint32(buf[offInopb:]),
		Ipg:           le.Uint32(buf[offIpg:]),
		Fpg:           int32(le.Uint32(buf[offFpg:])),
		Flags:         int32(le.Uint32(buf[offFlags:])),
		Maxsymlinklen: int32(le.Uint32(buf[offMaxsymlinklen:])),
		OldInodefmt:   int32(le.Uint32(buf[offOldInodefmt:])),
		Maxfilesize:   le.Uint64(buf[offMaxfilesize:]),
		Magic:         le.Uint32(buf[offMagic:]),
	}
	if err := sb.validate(); err != nil {
		return nil, err
	}
	return sb, nil
}

// validate enforces the minimal invariants the read-side driver
// relies on. We intentionally do NOT cross-check every field against
// every other field (kernel does that for us when the image was
// formatted); the goal is to reject obviously-corrupt or wrong-format
// images early.
func (sb *Superblock) validate() error {
	switch sb.Magic {
	case MagicUFS2:
		// fall through
	case MagicUFS1:
		return fmt.Errorf("%w: UFS1 not supported (magic 0x%x)", ErrBadSuperblock, sb.Magic)
	default:
		return fmt.Errorf("%w: bad magic 0x%x", ErrBadSuperblock, sb.Magic)
	}
	if sb.Bsize < 4096 || sb.Bsize > 65536 {
		return fmt.Errorf("%w: bsize %d out of range", ErrBadSuperblock, sb.Bsize)
	}
	if sb.Bsize&(sb.Bsize-1) != 0 {
		return fmt.Errorf("%w: bsize %d not power of two", ErrBadSuperblock, sb.Bsize)
	}
	if sb.Fsize <= 0 || sb.Fsize > sb.Bsize {
		return fmt.Errorf("%w: fsize %d invalid", ErrBadSuperblock, sb.Fsize)
	}
	if sb.Frag <= 0 || int32(sb.Bsize/sb.Fsize) != sb.Frag {
		return fmt.Errorf("%w: frag %d inconsistent with bsize/fsize", ErrBadSuperblock, sb.Frag)
	}
	if sb.Inopb == 0 || sb.Inopb != uint32(sb.Bsize)/InodeSize {
		return fmt.Errorf("%w: inopb %d inconsistent with bsize", ErrBadSuperblock, sb.Inopb)
	}
	if sb.Ncg == 0 {
		return fmt.Errorf("%w: ncg is zero", ErrBadSuperblock)
	}
	if sb.Ipg == 0 {
		return fmt.Errorf("%w: ipg is zero", ErrBadSuperblock)
	}
	if sb.OldInodefmt != 2 {
		return fmt.Errorf("%w: unsupported inode format %d", ErrBadSuperblock, sb.OldInodefmt)
	}
	return nil
}

// CgBase returns the byte offset of cylinder-group cg within the
// backing device. UFS lays out cylinder groups at multiples of
// Fpg fragments from the start of the partition, each fragment being
// Fsize bytes wide.
func (sb *Superblock) CgBase(cg uint32) int64 {
	return int64(cg) * int64(sb.Fpg) * int64(sb.Fsize)
}

// InodeOffset returns the absolute byte offset of inode `ino` (1-based,
// per UFS convention; inode 0 is reserved).
func (sb *Superblock) InodeOffset(ino uint64) int64 {
	cg := uint32(ino / uint64(sb.Ipg))
	idx := uint32(ino % uint64(sb.Ipg))
	cgBase := sb.CgBase(cg)
	inodeTable := cgBase + int64(sb.Iblkno)*int64(sb.Fsize)
	return inodeTable + int64(idx)*int64(InodeSize)
}

// FragOffset returns the absolute byte offset of fragment `frag`
// (UFS "fs block number" — a daddr_t — addresses fragments, not full
// blocks).
func (sb *Superblock) FragOffset(frag uint64) int64 {
	return int64(frag) * int64(sb.Fsize)
}
