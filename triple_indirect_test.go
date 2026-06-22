// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestReadFileBody_TripleIndirect hand-builds a synthetic UFS2 image
// whose only populated path is a single triple-indirect chain. A file
// large enough to *require* triple-indirect addressing would span
// gigabytes on disk, so instead we build a sparse file: every block
// except the one we probe is a hole, and the indirect chain leading to
// that one block is materialised explicitly.
//
// The probe targets logical block:
//
//	lbn = NumDirect + nindir + nindir² + rel
//	rel = topIdx·nindir² + midIdx·nindir + innerIdx
//
// with topIdx=1, midIdx=2, innerIdx=3 so that every level of the
// index decomposition (rel/(nindir²), (rel/nindir)%nindir, rel%nindir)
// carries a distinct non-zero value — i.e. a wrong shift or modulus in
// the traversal would land on a hole and fail the assertion.
func TestReadFileBody_TripleIndirect(t *testing.T) {
	const (
		bsize  = fxBsize         // 4096
		fsize  = fxFsize         // 4096
		nindir = int64(fxNindir) // 512

		topIdx   = int64(1)
		midIdx   = int64(2)
		innerIdx = int64(3)
	)

	rel := topIdx*nindir*nindir + midIdx*nindir + innerIdx
	lbn := int64(NumDirect) + nindir + nindir*nindir + rel

	// On-disk fragment layout (one fragment == one block here). We pack
	// the four chain blocks plus one data block immediately after the
	// superblock region so the backing image stays a few hundred KiB
	// rather than the multi-GiB the file's logical size implies.
	const (
		fragTop   = uint64(32) // triple-indirect block (Indirect[2])
		fragMid   = uint64(33) // double-indirect block
		fragInner = uint64(34) // single-indirect block
		fragData  = uint64(35) // the lone data block we read back
	)

	imgSize := int(fragData+1) * fsize
	img := make([]byte, imgSize)
	le := binary.LittleEndian

	// Minimal but valid UFS2 superblock — same geometry as the shared
	// fixture so parseSuperblock accepts it, except Ncg is advertised
	// large enough that the image-derived size bound (Ncg*Fpg*Fsize) can
	// hold this file's multi-gigabyte *logical* size. The file is sparse
	// (only a handful of fragments are materialised in `img`), but di_size
	// must not exceed the addressable image size — the same invariant the
	// hardened allocation guard enforces against a crafted oversized
	// di_size. fxNcg=1 would advertise only a 1 MiB image, so we pick a
	// cylinder-group count whose Ncg*Fpg*Fsize comfortably exceeds di_size.
	const tiNcg = 4096 // 4096*256*4096 = 4 GiB >= ~2.15 GiB di_size
	sb := img[SblockUFS2 : SblockUFS2+1376]
	le.PutUint32(sb[offSblkno:], fxSblkno)
	le.PutUint32(sb[offCblkno:], fxCblkno)
	le.PutUint32(sb[offIblkno:], fxIblkno)
	le.PutUint32(sb[offDblkno:], fxDblkno)
	le.PutUint32(sb[offNcg:], tiNcg)
	le.PutUint32(sb[offBsize:], bsize)
	le.PutUint32(sb[offFsize:], fsize)
	le.PutUint32(sb[offFrag:], fxFrag)
	le.PutUint32(sb[offBshift:], fxBshift)
	le.PutUint32(sb[offFshift:], fxFshift)
	le.PutUint32(sb[offFsbtodb:], fxFsbtodb)
	le.PutUint32(sb[offSbsize:], 1376)
	le.PutUint32(sb[offNindir:], uint32(nindir))
	le.PutUint32(sb[offInopb:], fxInopb)
	le.PutUint32(sb[offIpg:], fxIpg)
	le.PutUint32(sb[offFpg:], fxFpg)
	le.PutUint32(sb[offMaxsymlinklen:], 120)
	le.PutUint32(sb[offOldInodefmt:], 2)
	le.PutUint64(sb[offMaxfilesize:], 1<<48)
	le.PutUint32(sb[offMagic:], MagicUFS2)

	parsed, err := parseSuperblock(sb)
	if err != nil {
		t.Fatalf("parseSuperblock: %v", err)
	}

	// Build the chain: Indirect[2] → fragTop[topIdx] → fragMid[midIdx]
	// → fragInner[innerIdx] → fragData. Each indirect entry is an
	// 8-byte little-endian daddr_t (UFS2).
	le.PutUint64(img[int64(fragTop)*fsize+topIdx*8:], fragMid)
	le.PutUint64(img[int64(fragMid)*fsize+midIdx*8:], fragInner)
	le.PutUint64(img[int64(fragInner)*fsize+innerIdx*8:], fragData)

	// Place a recognisable marker at a known offset within the data
	// block so we can prove we decoded the right block *and* the right
	// in-block offset.
	const markerInBlk = 17
	want := []byte("triple-indirect!")
	copy(img[int64(fragData)*fsize+markerInBlk:], want)

	// Sparse file whose size reaches just past our probe block.
	in := &Inode{
		Mode: IFREG | 0o644,
		Size: uint64((lbn + 1) * bsize),
	}
	in.Indirect[2] = fragTop

	// First, verify the raw block mapping resolves to fragData.
	frag, err := blockForLBN(bytes.NewReader(img), parsed, in, lbn)
	if err != nil {
		t.Fatalf("blockForLBN(lbn=%d): %v", lbn, err)
	}
	if frag != fragData {
		t.Fatalf("blockForLBN(lbn=%d) = %d, want %d", lbn, frag, fragData)
	}

	// Now read the marker bytes through the full ReadFileBody path at
	// the logical byte offset only triple-indirect addressing reaches.
	fileOff := lbn*bsize + markerInBlk
	got, err := ReadFileBody(bytes.NewReader(img), parsed, in, fileOff, len(want))
	if err != nil {
		t.Fatalf("ReadFileBody(off=%d): %v", fileOff, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("triple-indirect read = %q, want %q", got, want)
	}

	// A neighbouring logical block that was never mapped must read back
	// as a sparse hole (all zeros), confirming we are not accidentally
	// aliasing onto a populated fragment.
	holeOff := (lbn - 1) * bsize
	hole, err := ReadFileBody(bytes.NewReader(img), parsed, in, holeOff, 8)
	if err != nil {
		t.Fatalf("ReadFileBody(hole off=%d): %v", holeOff, err)
	}
	if !bytes.Equal(hole, make([]byte, 8)) {
		t.Fatalf("expected sparse hole zeros, got %v", hole)
	}
}
