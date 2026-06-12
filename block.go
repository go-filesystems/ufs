// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"encoding/binary"
	"fmt"
	"io"
)

// ReadFileBody reads up to `n` bytes starting at `off` from the file
// described by `in`. It honours the UFS2 layout — direct fragments
// for the first 12 logical blocks, single-indirect for the next
// fs_nindir, and ErrUnsupportedIndirect for anything beyond that.
//
// The function caps `n` at di_size − off so callers never observe
// trailing slack from the underlying fragment.
func ReadFileBody(rs io.ReaderAt, sb *Superblock, in *Inode, off int64, n int) ([]byte, error) {
	if off < 0 || n < 0 {
		return nil, fmt.Errorf("ufs: negative read off=%d n=%d", off, n)
	}
	if uint64(off) >= in.Size {
		return nil, nil
	}
	if uint64(off)+uint64(n) > in.Size {
		n = int(in.Size - uint64(off))
	}
	out := make([]byte, n)
	bsize := int64(sb.Bsize)
	done := 0
	for done < n {
		fileOff := off + int64(done)
		lbn := fileOff / bsize
		inBlk := fileOff % bsize
		need := int64(n - done)
		if avail := bsize - inBlk; avail < need {
			need = avail
		}
		frag, err := blockForLBN(rs, sb, in, lbn)
		if err != nil {
			return nil, err
		}
		if frag == 0 {
			// Sparse hole — UFS represents implicit zero-fill
			// with a zero block pointer. Leave `out[done:]`
			// alone; it's already zero from make.
			done += int(need)
			continue
		}
		blkOff := sb.FragOffset(frag) + inBlk
		if _, err := rs.ReadAt(out[done:done+int(need)], blkOff); err != nil {
			return nil, fmt.Errorf("ufs: read data at %d: %w", blkOff, err)
		}
		done += int(need)
	}
	return out, nil
}

// ReadFileAll is a convenience wrapper that reads the entire file.
func ReadFileAll(rs io.ReaderAt, sb *Superblock, in *Inode) ([]byte, error) {
	return ReadFileBody(rs, sb, in, 0, int(in.Size))
}

// blockForLBN maps a logical block number (0-indexed, counting in
// fs_bsize-sized blocks from the start of the file) to its on-disk
// fragment address. Returns 0 for sparse holes.
func blockForLBN(rs io.ReaderAt, sb *Superblock, in *Inode, lbn int64) (uint64, error) {
	if lbn < 0 {
		return 0, fmt.Errorf("ufs: negative lbn %d", lbn)
	}
	if lbn < int64(NumDirect) {
		return in.Direct[lbn], nil
	}
	rel := lbn - int64(NumDirect)
	nindir := int64(sb.Nindir)
	if nindir <= 0 {
		// Defensive: every reasonable UFS2 image populates this,
		// but treat zero as "no indirect reach" rather than
		// panicking on a divide by zero.
		return 0, ErrUnsupportedIndirect
	}
	if rel < nindir {
		// Single-indirect: in.Indirect[0] points at a block of
		// uint64 fragment addresses.
		if in.Indirect[0] == 0 {
			return 0, nil
		}
		idx := rel
		return readIndirectEntry(rs, sb, in.Indirect[0], idx)
	}
	rel -= nindir
	// Double-indirect: in.Indirect[1] points at a block of nindir
	// uint64 pointers, each of which points at a single-indirect
	// block of nindir entries. Total reach = nindir² × bsize.
	if rel < nindir*nindir {
		if in.Indirect[1] == 0 {
			return 0, nil
		}
		outerIdx := rel / nindir
		innerIdx := rel % nindir
		mid, err := readIndirectEntry(rs, sb, in.Indirect[1], outerIdx)
		if err != nil {
			return 0, err
		}
		if mid == 0 {
			return 0, nil
		}
		return readIndirectEntry(rs, sb, mid, innerIdx)
	}
	// Triple-indirect intentionally not implemented: nindir³ × bsize
	// = 1 PiB at bsize=32768; sprint 2D doesn't need it.
	return 0, ErrUnsupportedIndirect
}

// readIndirectEntry reads one fragment address from the indirect
// block at `frag`. Each entry is a little-endian daddr_t: 8 bytes on
// UFS2, 4 bytes on UFS1.
func readIndirectEntry(rs io.ReaderAt, sb *Superblock, frag uint64, idx int64) (uint64, error) {
	if idx < 0 || idx >= int64(sb.Nindir) {
		return 0, fmt.Errorf("ufs: indirect index %d out of range [0,%d)", idx, sb.Nindir)
	}
	ptr := sb.pointerSize()
	var buf [8]byte
	off := sb.FragOffset(frag) + idx*ptr
	if _, err := rs.ReadAt(buf[:ptr], off); err != nil {
		return 0, fmt.Errorf("ufs: read indirect entry at %d: %w", off, err)
	}
	if sb.IsUFS1 {
		return uint64(binary.LittleEndian.Uint32(buf[:4])), nil
	}
	return binary.LittleEndian.Uint64(buf[:]), nil
}
