// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Cylinder-group header field offsets within the on-disk struct cg.
// Mirrors sys/ufs/ffs/fs.h (FreeBSD 14.x amd64). We only decode the
// pointers and counters the writer actually mutates; everything else
// stays untouched in the raw image bytes.
const (
	cgOffMagic     = 0
	cgOffOldTime   = 4
	cgOffCgx       = 8
	cgOffNdblk     = 16
	cgOffCs        = 20    // struct csum (ndir/nbfree/nifree/nffree)
	cgOffRotor     = 36
	cgOffFrotor    = 40
	cgOffIrotor    = 44
	cgOffFrsum     = 48    // int32[MAXFRAG]
	cgOffIusedoff  = 84    // offset to inode-used bitmap (from start of cg)
	cgOffFreeoff   = 88    // offset to free-block bitmap (from start of cg)
	cgOffNextfree  = 92
	cgOffClustersumoff = 96
	cgOffClusteroff    = 100
	cgOffNclusterblks  = 104
	cgOffNiblk     = 108
	cgOffInitediblk = 112
	cgOffUnrefs     = 116
	cgOffSparecon32 = 120
	cgOffCkhash     = 132
	cgOffTime       = 136
	// SparecOn64 etc. follow; we don't need them.

	// CgMagic is the magic constant in a UFS cylinder group header.
	CgMagic uint32 = 0x090255
)

// rwAt is the union of io.ReaderAt and io.WriterAt that the writer
// requires. Open returns an FS whose underlying handle satisfies only
// ReaderAt; Mkfs / OpenRW require a full ReadWriterAt.
type rwAt interface {
	io.ReaderAt
	io.WriterAt
}

// allocator hosts the per-cylinder-group bitmap arithmetic the writer
// needs. We hold the cg headers in memory as raw byte buffers and
// flush them back through fs.wa on every modification. This keeps the
// on-disk state immediately coherent — a crash mid-WriteFile leaves a
// well-formed (if slightly leaky) filesystem rather than a torn one.
type allocator struct {
	fs *FS
	// cg[i] is the raw bytes for cylinder group i, length sb.Bsize.
	cg [][]byte
}

// loadAllocator pulls every cylinder-group header off disk into memory.
// The header lives at the cgBase + Cblkno*Fsize offset and spans one
// block (sb.Bsize). For tiny fixtures with a single cylinder group this
// is cheap; for production-sized images with hundreds of groups it
// reads ~hundreds of KiB at open time.
func loadAllocator(fs *FS) (*allocator, error) {
	a := &allocator{fs: fs, cg: make([][]byte, fs.sb.Ncg)}
	for i := uint32(0); i < fs.sb.Ncg; i++ {
		buf := make([]byte, fs.sb.Bsize)
		off := fs.sb.CgBase(i) + int64(fs.sb.Cblkno)*int64(fs.sb.Fsize)
		if _, err := fs.rs.ReadAt(buf, off); err != nil {
			return nil, fmt.Errorf("ufs: read cg %d at %d: %w", i, off, err)
		}
		a.cg[i] = buf
	}
	return a, nil
}

// flushCg writes cylinder group cg's header back to its on-disk slot.
func (a *allocator) flushCg(cg uint32) error {
	if a.fs.wa == nil {
		return ErrReadOnly
	}
	off := a.fs.sb.CgBase(cg) + int64(a.fs.sb.Cblkno)*int64(a.fs.sb.Fsize)
	if _, err := a.fs.wa.WriteAt(a.cg[cg], off); err != nil {
		return fmt.Errorf("ufs: flush cg %d: %w", cg, err)
	}
	return nil
}

// inodeBitmap returns the inode-allocated bitmap slice within cg's
// header buffer.
func (a *allocator) inodeBitmap(cg uint32) []byte {
	off := int(binary.LittleEndian.Uint32(a.cg[cg][cgOffIusedoff:]))
	n := (int(a.fs.sb.Ipg) + 7) / 8
	return a.cg[cg][off : off+n]
}

// blockBitmap returns the free-fragment bitmap slice within cg's
// header buffer. Note: this bitmap tracks fragments (one bit per
// fragment), not whole blocks.
func (a *allocator) blockBitmap(cg uint32) []byte {
	off := int(binary.LittleEndian.Uint32(a.cg[cg][cgOffFreeoff:]))
	// Number of fragments tracked. Use Fpg for safety; the bitmap is
	// usually sized at exactly Fpg bits.
	n := (int(a.fs.sb.Fpg) + 7) / 8
	return a.cg[cg][off : off+n]
}

// allocInode finds a free inode anywhere in the filesystem, marks it
// used, decrements cs_nifree on the owning cg, and returns the
// global inode number. Returns ErrNoSpace when every group is full.
func (a *allocator) allocInode(isDir bool) (uint64, error) {
	for cg := uint32(0); cg < a.fs.sb.Ncg; cg++ {
		bm := a.inodeBitmap(cg)
		// Skip inode 0 in the very first cg (reserved by UFS).
		start := uint32(0)
		if cg == 0 {
			start = 1
		}
		for idx := start; idx < a.fs.sb.Ipg; idx++ {
			byteIdx := idx / 8
			bitIdx := idx % 8
			if bm[byteIdx]&(1<<bitIdx) == 0 {
				bm[byteIdx] |= 1 << bitIdx
				// Update cs counters.
				le := binary.LittleEndian
				nifree := le.Uint32(a.cg[cg][cgOffCs+8:])
				le.PutUint32(a.cg[cg][cgOffCs+8:], nifree-1)
				if isDir {
					ndir := le.Uint32(a.cg[cg][cgOffCs+0:])
					le.PutUint32(a.cg[cg][cgOffCs+0:], ndir+1)
				}
				if err := a.flushCg(cg); err != nil {
					return 0, err
				}
				// Mirror the cg's cs into the superblock's
				// in-line summary array so fsck stays quiet.
				if err := a.fs.refreshSummary(); err != nil {
					return 0, err
				}
				return uint64(cg)*uint64(a.fs.sb.Ipg) + uint64(idx), nil
			}
		}
	}
	return 0, ErrNoSpace
}

// freeInode clears the bit for `ino` and bumps cs_nifree (and decrements
// cs_ndir if isDir).
func (a *allocator) freeInode(ino uint64, isDir bool) error {
	cg := uint32(ino / uint64(a.fs.sb.Ipg))
	idx := uint32(ino % uint64(a.fs.sb.Ipg))
	bm := a.inodeBitmap(cg)
	bm[idx/8] &^= 1 << (idx % 8)
	le := binary.LittleEndian
	nifree := le.Uint32(a.cg[cg][cgOffCs+8:])
	le.PutUint32(a.cg[cg][cgOffCs+8:], nifree+1)
	if isDir {
		ndir := le.Uint32(a.cg[cg][cgOffCs+0:])
		le.PutUint32(a.cg[cg][cgOffCs+0:], ndir-1)
	}
	if err := a.flushCg(cg); err != nil {
		return err
	}
	return a.fs.refreshSummary()
}

// allocBlock finds `nFrags` contiguous free fragments and returns the
// global fragment number of the first one. For full-block allocations
// (nFrags == Frag) we align to block boundaries — that's what FreeBSD
// does and it keeps the free-fragment summary trivial.
func (a *allocator) allocBlock(nFrags int) (uint64, error) {
	if nFrags <= 0 || nFrags > int(a.fs.sb.Frag) {
		return 0, fmt.Errorf("ufs: bad allocBlock nFrags=%d", nFrags)
	}
	for cg := uint32(0); cg < a.fs.sb.Ncg; cg++ {
		bm := a.blockBitmap(cg)
		fpg := int(a.fs.sb.Fpg)
		// Start scanning from Dblkno (the first data fragment in
		// the group). Everything before it is metadata.
		startFrag := int(a.fs.sb.Dblkno)
		step := 1
		if nFrags == int(a.fs.sb.Frag) {
			// Align to block boundaries.
			if rem := startFrag % nFrags; rem != 0 {
				startFrag += nFrags - rem
			}
			step = nFrags
		}
		for frag := startFrag; frag+nFrags <= fpg; frag += step {
			if rangeFree(bm, frag, nFrags) {
				rangeSet(bm, frag, nFrags, true)
				le := binary.LittleEndian
				nbfree := le.Uint32(a.cg[cg][cgOffCs+4:])
				// cs_nbfree counts WHOLE blocks free, but we only
				// account perfectly for full-block allocations; partial
				// allocations leak into cs_nffree. For simplicity (and
				// because we only allocate full blocks in this sprint),
				// decrement nbfree only for full-block allocations.
				if nFrags == int(a.fs.sb.Frag) {
					le.PutUint32(a.cg[cg][cgOffCs+4:], nbfree-1)
				} else {
					nffree := le.Uint32(a.cg[cg][cgOffCs+12:])
					le.PutUint32(a.cg[cg][cgOffCs+12:], nffree-uint32(nFrags))
				}
				if err := a.flushCg(cg); err != nil {
					return 0, err
				}
				if err := a.fs.refreshSummary(); err != nil {
					return 0, err
				}
				return uint64(cg)*uint64(fpg) + uint64(frag), nil
			}
		}
	}
	return 0, ErrNoSpace
}

// freeBlock returns nFrags contiguous fragments to the free pool.
func (a *allocator) freeBlock(frag uint64, nFrags int) error {
	if frag == 0 {
		return nil
	}
	fpg := uint64(a.fs.sb.Fpg)
	cg := uint32(frag / fpg)
	idx := int(frag % fpg)
	bm := a.blockBitmap(cg)
	rangeSet(bm, idx, nFrags, false)
	le := binary.LittleEndian
	if nFrags == int(a.fs.sb.Frag) {
		nbfree := le.Uint32(a.cg[cg][cgOffCs+4:])
		le.PutUint32(a.cg[cg][cgOffCs+4:], nbfree+1)
	} else {
		nffree := le.Uint32(a.cg[cg][cgOffCs+12:])
		le.PutUint32(a.cg[cg][cgOffCs+12:], nffree+uint32(nFrags))
	}
	if err := a.flushCg(cg); err != nil {
		return err
	}
	return a.fs.refreshSummary()
}

// rangeFree reports whether [start, start+n) in bm is entirely zero.
func rangeFree(bm []byte, start, n int) bool {
	for i := start; i < start+n; i++ {
		if bm[i/8]&(1<<(i%8)) != 0 {
			return false
		}
	}
	return true
}

// rangeSet flips [start, start+n) to the given value.
func rangeSet(bm []byte, start, n int, set bool) {
	for i := start; i < start+n; i++ {
		if set {
			bm[i/8] |= 1 << (i % 8)
		} else {
			bm[i/8] &^= 1 << (i % 8)
		}
	}
}
