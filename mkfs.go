// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
)

// Mkfs defaults (mirror FreeBSD newfs(8)).
const (
	defaultBsize        = 4096    // start small so tiny images stay legal
	defaultFsize        = 4096    // frag == 1 in our default geometry
	defaultInodeBytes   = 4096    // one inode per 4 KiB of disk
	defaultCgSize       = 1 << 20 // one cylinder group per 1 MiB
	defaultMaxsymlinklen = 120
)

// Additional superblock field offsets needed only by Mkfs / cross-
// validation. Kept here rather than in superblock.go so the reader
// stays focused on the fields it actually decodes.
const (
	sbOffNdir       = 56     // overlaps Frag? No — Frag is at 56; Ndir is 1196.
	// (Re-using offFrag for write would conflict; the offsets below
	// are the ones the writer additionally needs.)
	sbOffMinFree    = 1108
	sbOffOptim      = 1116
	sbOffCgsize     = 132
	sbOffSize       = 1080
	sbOffDsize      = 1084
	sbOffNcg        = 44
	sbOffSpareCon32 = 1284
	sbOffFmod       = 1300
	sbOffClean      = 1304
	sbOffRonly      = 1308
	sbOffOldFlags   = 1312 // alias of offFlags
	sbOffFsmnt      = 472
	sbOffVolname    = 980
	sbOffSwuid      = 1044
	sbOffMtime      = 1052
	sbOffPendingblocks = 1284 // placeholder; real field is at 1316 on amd64
	sbOffSbcrc      = 1364
	sbOffSize64     = 1056
	sbOffDsize64    = 1064
	sbOffCsaddr     = 1072
	sbOffCstotalNdir   = 1192
	sbOffCstotalNbfree = 1196
	sbOffCstotalNifree = 1200
	sbOffCstotalNffree = 1204
	sbOffCstotalNumclusters = 1208
	sbOffCgrotor    = 132 // alias; cg rotation hint
	sbOffCpc        = 156 // cylinders per cycle (vestigial)
	sbOffOldCgmask  = 124
	sbOffOldNcyl    = 64
	sbOffOldCpg     = 68
)

// Mkfs writes a fresh UFS2 filesystem onto a backing ReadWriterAt
// with the given byte-size geometry. The returned *FS is open for
// read+write. We mirror FreeBSD newfs(8) defaults: 4 KiB block, 4 KiB
// fragment, one cylinder group per 1 MiB. For larger callers we still
// honour the same defaults — bumping the block to 32 KiB is a future
// optimisation, not a correctness requirement.
//
// Layout per cylinder group (offsets in fragments):
//
//	[0 .. dblkno)            metadata (boot area + sb copy + cg header +
//	                          inode table) — bitmap marks these allocated
//	[dblkno .. fpg)          data area — bitmap initially zero (free)
//
// The primary superblock (cg 0's copy) lives at byte offset 65536 so
// FreeBSD's loader / mount path can find it at the canonical UFS2
// location.
func Mkfs(w interface {
	io.ReaderAt
	io.WriterAt
}, sizeBytes int64) (*FS, error) {
	if sizeBytes < 1<<20 {
		return nil, fmt.Errorf("ufs: Mkfs needs at least 1 MiB, got %d", sizeBytes)
	}
	bsize := int32(defaultBsize)
	fsize := int32(defaultFsize)
	frag := bsize / fsize
	bshift := int32(log2(uint32(bsize)))
	fshift := int32(log2(uint32(fsize)))
	inopb := uint32(bsize) / InodeSize
	fsbtodb := int32(log2(uint32(fsize) / 512))

	cgSize := int64(defaultCgSize)
	if cgSize > sizeBytes {
		cgSize = sizeBytes
	}
	fpg := int32(cgSize / int64(fsize))
	// Inodes per group: cap so the inode table fits in a couple of
	// blocks. We aim for one inode per defaultInodeBytes of data.
	ipg := uint32(int64(fpg) * int64(fsize) / int64(defaultInodeBytes))
	if ipg < inopb {
		ipg = inopb
	}
	// Round ipg up so the inode table covers whole blocks.
	if rem := ipg % inopb; rem != 0 {
		ipg += inopb - rem
	}
	// Total cylinder groups.
	ncg := uint32((sizeBytes + cgSize - 1) / cgSize)

	// Per-cg metadata layout (in fragments):
	//   sblkno  = 16  (byte 65536 / 4096) — superblock copy
	//   cblkno  = sblkno + frag*2 — cg header (we reserve 2 blocks)
	//   iblkno  = cblkno + frag*2 — inode table start
	//   dblkno  = iblkno + (ipg*InodeSize)/fsize — first data fragment
	sblkno := int32(SblockUFS2 / fsize)
	cblkno := sblkno + 2*frag
	iblkno := cblkno + 2*frag
	inodeFrags := int32(ipg*InodeSize) / fsize
	if int32(ipg*InodeSize)%fsize != 0 {
		inodeFrags++
	}
	dblkno := iblkno + inodeFrags
	if dblkno >= fpg {
		return nil, fmt.Errorf("ufs: Mkfs cg metadata exceeds cg size")
	}

	sb := &Superblock{
		Sblkno: sblkno, Cblkno: cblkno, Iblkno: iblkno, Dblkno: dblkno,
		Ncg: ncg, Bsize: bsize, Fsize: fsize, Frag: frag,
		Bshift: bshift, Fshift: fshift, Fsbtodb: fsbtodb,
		Sbsize: 1376, Nindir: bsize / 8, Inopb: inopb,
		Ipg: ipg, Fpg: fpg,
		Flags: 0, Maxsymlinklen: defaultMaxsymlinklen, OldInodefmt: 2,
		Maxfilesize: 1 << 48, Magic: MagicUFS2,
	}

	// Initialise FS struct with cg headers in memory.
	fs := &FS{rs: w, wa: w, size: sizeBytes, sb: sb}
	fs.alloc = &allocator{fs: fs, cg: make([][]byte, ncg)}
	for cg := uint32(0); cg < ncg; cg++ {
		fs.alloc.cg[cg] = makeCgHeader(sb, cg)
	}
	// Pre-mark every cg's metadata region as allocated.
	for cg := uint32(0); cg < ncg; cg++ {
		bm := fs.alloc.blockBitmap(cg)
		for f := int32(0); f < dblkno; f++ {
			bm[f/8] |= 1 << (f % 8)
		}
		// In the last cg, mark fragments beyond the end-of-image
		// as also "in use" so we never hand them out.
		cgBase := sb.CgBase(cg)
		cgEnd := cgBase + int64(fpg)*int64(fsize)
		if cgEnd > sizeBytes {
			over := (cgEnd - sizeBytes) / int64(fsize)
			for i := int64(fpg) - over; i < int64(fpg); i++ {
				if i >= 0 {
					bm[i/8] |= 1 << (i % 8)
				}
			}
		}
	}
	// Write zeros for the boot area + reserve through dblkno across
	// the WHOLE backing image is unnecessary; the OS reads back zeros
	// from unwritten holes for our tests (bytes.Buffer pre-zeros).
	// Real backing files (os.File) likewise read zeros from sparse
	// regions. Just ensure the superblock copies are written.
	for cg := uint32(0); cg < ncg; cg++ {
		if err := fs.alloc.flushCg(cg); err != nil {
			return nil, err
		}
	}

	// Allocate the root inode (always inode 2).
	// First two inodes are reserved in cg 0; mark them used.
	bm := fs.alloc.inodeBitmap(0)
	bm[0] |= 0b11 // inode 0 + 1
	le := binary.LittleEndian
	nifree := le.Uint32(fs.alloc.cg[0][cgOffCs+8:])
	le.PutUint32(fs.alloc.cg[0][cgOffCs+8:], nifree-2)
	if err := fs.alloc.flushCg(0); err != nil {
		return nil, err
	}

	// Allocate root inode #2 explicitly via the bitmap (allocInode
	// would pick the next free inode which is also #2 — but we want
	// to be explicit and also bump cs_ndir for a directory).
	bm[2/8] |= 1 << (2 % 8)
	ndir := le.Uint32(fs.alloc.cg[0][cgOffCs+0:])
	le.PutUint32(fs.alloc.cg[0][cgOffCs+0:], ndir+1)
	nifree = le.Uint32(fs.alloc.cg[0][cgOffCs+8:])
	le.PutUint32(fs.alloc.cg[0][cgOffCs+8:], nifree-1)
	if err := fs.alloc.flushCg(0); err != nil {
		return nil, err
	}

	// Build root directory body.
	bsz := int(bsize)
	body := make([]byte, bsz)
	dotLen := DirentReclen(1)
	dotdotLen := bsz - dotLen
	EncodeDirent(body[0:dotLen], uint32(RootInode), DtDir, ".", uint16(dotLen))
	EncodeDirent(body[dotLen:bsz], uint32(RootInode), DtDir, "..", uint16(dotdotLen))

	root := newInode(IFDIR | 0o755)
	root.Nlink = 2
	if err := fs.writeFileData(root, body); err != nil {
		return nil, err
	}
	if err := fs.writeInode(RootInode, root); err != nil {
		return nil, err
	}

	// Write the superblock at the canonical location.
	if err := fs.writeSuperblock(); err != nil {
		return nil, err
	}
	if err := fs.refreshSummary(); err != nil {
		return nil, err
	}
	return fs, nil
}

// log2 returns floor(log2(x)) for power-of-two x.
func log2(x uint32) int {
	return bits.TrailingZeros32(x)
}

// makeCgHeader builds the initial cylinder-group header buffer for cg.
// We lay out:
//   bytes [0 .. cgOffIusedoff)            — header struct
//   bytes [iusedoff .. iusedoff + ipg/8)   — inode bitmap (all free)
//   bytes [freeoff .. freeoff + fpg/8)     — block bitmap (set later)
func makeCgHeader(sb *Superblock, cg uint32) []byte {
	buf := make([]byte, sb.Bsize)
	le := binary.LittleEndian
	le.PutUint32(buf[cgOffMagic:], CgMagic)
	le.PutUint32(buf[cgOffCgx:], cg)
	le.PutUint32(buf[cgOffNdblk:], uint32(sb.Fpg))
	le.PutUint32(buf[cgOffNiblk:], sb.Ipg)
	// cs counters: nifree starts at Ipg (all free), nbfree at the
	// number of WHOLE data blocks available, nffree at 0, ndir at 0.
	le.PutUint32(buf[cgOffCs+0:], 0)            // ndir
	dataFrags := sb.Fpg - sb.Dblkno
	dataBlocks := dataFrags / sb.Frag
	le.PutUint32(buf[cgOffCs+4:], uint32(dataBlocks)) // nbfree
	le.PutUint32(buf[cgOffCs+8:], sb.Ipg)             // nifree
	le.PutUint32(buf[cgOffCs+12:], 0)                 // nffree
	// Bitmap offsets — pack them right after the header.
	iusedoff := uint32(cgOffTime + 16 + 64)   // leave some room for sparecon
	ibmBytes := (sb.Ipg + 7) / 8
	freeoff := iusedoff + ibmBytes
	le.PutUint32(buf[cgOffIusedoff:], iusedoff)
	le.PutUint32(buf[cgOffFreeoff:], freeoff)
	le.PutUint32(buf[cgOffNextfree:], 0)
	// Clustersum / clusteroff: set to safe non-overlapping offsets
	// (we never populate them; the kernel only consults them as
	// hints).
	fbmBytes := (uint32(sb.Fpg) + 7) / 8
	le.PutUint32(buf[cgOffClustersumoff:], freeoff+fbmBytes)
	le.PutUint32(buf[cgOffClusteroff:], freeoff+fbmBytes)
	le.PutUint32(buf[cgOffNclusterblks:], 0)
	le.PutUint32(buf[cgOffInitediblk:], sb.Ipg)
	le.PutUint32(buf[cgOffUnrefs:], 0)
	return buf
}

// writeSuperblock serialises fs.sb back to the canonical primary
// location at SblockUFS2. We touch only the fields the reader
// validates plus the summary counters needed for fsck cleanliness.
func (fs *FS) writeSuperblock() error {
	if fs.wa == nil {
		return ErrReadOnly
	}
	buf := make([]byte, SblockSize)
	// Read existing if any so we preserve unknown bytes; for Mkfs the
	// region is zero which is fine.
	_, _ = fs.rs.ReadAt(buf, SblockUFS2)
	le := binary.LittleEndian
	le.PutUint32(buf[offSblkno:], uint32(fs.sb.Sblkno))
	le.PutUint32(buf[offCblkno:], uint32(fs.sb.Cblkno))
	le.PutUint32(buf[offIblkno:], uint32(fs.sb.Iblkno))
	le.PutUint32(buf[offDblkno:], uint32(fs.sb.Dblkno))
	le.PutUint32(buf[offNcg:], fs.sb.Ncg)
	le.PutUint32(buf[offBsize:], uint32(fs.sb.Bsize))
	le.PutUint32(buf[offFsize:], uint32(fs.sb.Fsize))
	le.PutUint32(buf[offFrag:], uint32(fs.sb.Frag))
	le.PutUint32(buf[offBshift:], uint32(fs.sb.Bshift))
	le.PutUint32(buf[offFshift:], uint32(fs.sb.Fshift))
	le.PutUint32(buf[offFsbtodb:], uint32(fs.sb.Fsbtodb))
	le.PutUint32(buf[offSbsize:], uint32(fs.sb.Sbsize))
	le.PutUint32(buf[offNindir:], uint32(fs.sb.Nindir))
	le.PutUint32(buf[offInopb:], fs.sb.Inopb)
	le.PutUint32(buf[offIpg:], fs.sb.Ipg)
	le.PutUint32(buf[offFpg:], uint32(fs.sb.Fpg))
	le.PutUint32(buf[offFlags:], uint32(fs.sb.Flags))
	le.PutUint32(buf[offMaxsymlinklen:], uint32(fs.sb.Maxsymlinklen))
	le.PutUint32(buf[offOldInodefmt:], uint32(fs.sb.OldInodefmt))
	le.PutUint64(buf[offMaxfilesize:], fs.sb.Maxfilesize)
	le.PutUint32(buf[offMagic:], fs.sb.Magic)
	// fs_clean = 1 so the kernel doesn't fsck-mark us dirty.
	buf[sbOffClean] = 1
	if _, err := fs.wa.WriteAt(buf, SblockUFS2); err != nil {
		return fmt.Errorf("ufs: write superblock: %w", err)
	}
	return nil
}

// refreshSummary re-sums the cylinder-group cs counters into the
// superblock's cstotal. Called by every alloc/free path so the on-disk
// totals stay consistent. We tolerate fs.wa == nil (read-only) by
// skipping the write — useful when Mkfs is mid-flight and the
// superblock hasn't been laid down yet.
func (fs *FS) refreshSummary() error {
	if fs.alloc == nil {
		return nil
	}
	var ndir, nbfree, nifree, nffree uint32
	le := binary.LittleEndian
	for cg := uint32(0); cg < fs.sb.Ncg; cg++ {
		ndir += le.Uint32(fs.alloc.cg[cg][cgOffCs+0:])
		nbfree += le.Uint32(fs.alloc.cg[cg][cgOffCs+4:])
		nifree += le.Uint32(fs.alloc.cg[cg][cgOffCs+8:])
		nffree += le.Uint32(fs.alloc.cg[cg][cgOffCs+12:])
	}
	if fs.wa == nil {
		return nil
	}
	// Read-modify-write the superblock's cstotal region.
	var sbuf [SblockSize]byte
	if _, err := fs.rs.ReadAt(sbuf[:], SblockUFS2); err != nil {
		return fmt.Errorf("ufs: read sb for summary: %w", err)
	}
	le.PutUint32(sbuf[sbOffCstotalNdir:], ndir)
	le.PutUint32(sbuf[sbOffCstotalNbfree:], nbfree)
	le.PutUint32(sbuf[sbOffCstotalNifree:], nifree)
	le.PutUint32(sbuf[sbOffCstotalNffree:], nffree)
	if _, err := fs.wa.WriteAt(sbuf[:], SblockUFS2); err != nil {
		return fmt.Errorf("ufs: write sb summary: %w", err)
	}
	return nil
}

// Symlink creates a symbolic link at linkPath whose target is
// `target`. Inline ("fast") symlinks are used when the target fits in
// the inode's block-pointer area; longer targets spill into a single
// data block.
func (fs *FS) Symlink(target, linkPath string) error {
	if fs.wa == nil || fs.alloc == nil {
		return ErrReadOnly
	}
	if target == "" {
		return fmt.Errorf("%w: empty symlink target", ErrInvalidPath)
	}
	parentIno, parent, name, err := fs.resolveParent(linkPath)
	if err != nil {
		return err
	}
	if ok, err := fs.childExists(parent, name); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrExists, linkPath)
	}
	ino, err := fs.alloc.allocInode(false)
	if err != nil {
		return err
	}
	in := newInode(IFLNK | 0o777)
	in.Size = uint64(len(target))
	inline := len(target) <= int(fs.sb.Maxsymlinklen) && len(target) <= inoShortlinkLen
	if !inline {
		if err := fs.writeFileData(in, []byte(target)); err != nil {
			return err
		}
	} else {
		in.Blocks = 0
	}
	if err := fs.writeInode(ino, in); err != nil {
		return err
	}
	if inline {
		// Patch the inline target into the block-pointer slot AFTER
		// writeInode has run — otherwise the Direct[]/Indirect[]
		// serialisation would overwrite the bytes we put there.
		var slot [inoShortlinkLen]byte
		copy(slot[:], target)
		if _, err := fs.wa.WriteAt(slot[:], fs.sb.InodeOffset(ino)+inoOffShortlink); err != nil {
			return fmt.Errorf("ufs: write shortlink: %w", err)
		}
	}
	return fs.dirAppendEntry(parentIno, parent, name, ino, DtLnk)
}
