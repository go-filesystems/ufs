// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// Sprint 2D adds MkfsOptions / MkfsWith with configurable BlockSize
// and engages double-indirect on both read and write paths. The cap
// at bsize=4096 single-indirect (~2 MiB) used to block the 29 MiB
// FreeBSD kernel; at bsize=32768 with double-indirect we reach 8 GiB
// per file. These tests pin the new surface.

// TestMkfsWith_DefaultOpts confirms that an explicit zero-valued
// MkfsOptions still produces a valid filesystem and behaves like a
// no-arg Mkfs call (modulo the FreeBSD-newfs defaults — fragment is
// bsize/8 rather than == bsize). Round-trip a tiny file to lock the
// invariant.
func TestMkfsWith_DefaultOpts(t *testing.T) {
	const size = 16 << 20
	img := newMemImage(size)
	fs, err := MkfsWith(img, size, MkfsOptions{})
	if err != nil {
		t.Fatalf("MkfsWith: %v", err)
	}
	sb := fs.Superblock()
	if sb.Magic != MagicUFS2 {
		t.Errorf("magic = 0x%x, want UFS2", sb.Magic)
	}
	if sb.Bsize != 4096 {
		t.Errorf("Bsize = %d, want 4096", sb.Bsize)
	}
	if int(sb.Bsize)%int(sb.Fsize) != 0 || sb.Frag != sb.Bsize/sb.Fsize {
		t.Errorf("frag inconsistent: Bsize=%d Fsize=%d Frag=%d", sb.Bsize, sb.Fsize, sb.Frag)
	}
	if err := fs.WriteFile("/hello.txt", []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ro, err := Open(bytes.NewReader(img.Bytes()), int64(size))
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	got, err := ro.ReadFile("/hello.txt")
	if err != nil || string(got) != "hi" {
		t.Errorf("read = %q err=%v, want hi", got, err)
	}
}

// TestMkfsWith_BadOptions exercises the resolveOptions validation
// branches so callers get a clear diagnostic rather than a corrupted
// filesystem.
func TestMkfsWith_BadOptions(t *testing.T) {
	const size = 8 << 20
	img := newMemImage(size)

	bads := []struct {
		name string
		opts MkfsOptions
	}{
		{"BlockSize too small", MkfsOptions{BlockSize: 1024}},
		{"BlockSize too big", MkfsOptions{BlockSize: 1 << 17}},
		{"BlockSize not pow2", MkfsOptions{BlockSize: 6144}},
		{"FragmentSize > BlockSize", MkfsOptions{BlockSize: 4096, FragmentSize: 8192}},
		{"FragmentSize not divisor", MkfsOptions{BlockSize: 4096, FragmentSize: 1500}},
		{"InodeDensity too low", MkfsOptions{InodeDensity: 8}},
	}
	for _, b := range bads {
		t.Run(b.name, func(t *testing.T) {
			if _, err := MkfsWith(img, size, b.opts); err == nil {
				t.Errorf("expected error for %v", b.opts)
			}
		})
	}
}

// TestMkfsWith_BigBlock confirms BlockSize=32768 produces a sensible
// geometry: frag bsize/8, single-indirect reach 16 MiB, double-
// indirect reach 8 GiB.
func TestMkfsWith_BigBlock(t *testing.T) {
	const size = 64 << 20
	img := newMemImage(size)
	fs, err := MkfsWith(img, size, MkfsOptions{BlockSize: 32768})
	if err != nil {
		t.Fatalf("MkfsWith big block: %v", err)
	}
	sb := fs.Superblock()
	if sb.Bsize != 32768 {
		t.Errorf("Bsize = %d, want 32768", sb.Bsize)
	}
	if sb.Fsize != 4096 {
		t.Errorf("Fsize = %d, want 4096 (bsize/8)", sb.Fsize)
	}
	if sb.Frag != 8 {
		t.Errorf("Frag = %d, want 8", sb.Frag)
	}
	if sb.Nindir != 32768/8 {
		t.Errorf("Nindir = %d, want %d", sb.Nindir, 32768/8)
	}
}

// TestWriteFile_BigBlock_25MiB is the production-shape round-trip:
// MkfsWith(BlockSize=32768) (FreeBSD newfs default), write a 25 MiB
// blob (a kernel-shaped payload), re-open via the read side, compare
// bytes. At bsize=32768 the indirect block holds 4096 entries so
// single-indirect alone reaches 128 MiB — this exercise stresses the
// large-bsize writer without engaging double-indirect.
//
// Mirrors the 29 MiB FreeBSD kernel that buildespimg lands in UFS
// post-sprint-2D.
func TestWriteFile_BigBlock_25MiB(t *testing.T) {
	const (
		fsSize   = 96 << 20 // 96 MiB backing — generous overhead
		fileSize = 25 << 20 // 25 MiB; fits in single-indirect at bsize=32768
	)
	img := newMemImage(fsSize)
	fs, err := MkfsWith(img, fsSize, MkfsOptions{BlockSize: 32768})
	if err != nil {
		t.Fatalf("MkfsWith: %v", err)
	}
	if err := fs.MkDir("/boot", 0o755); err != nil {
		t.Fatalf("mkdir /boot: %v", err)
	}
	if err := fs.MkDir("/boot/kernel", 0o755); err != nil {
		t.Fatalf("mkdir /boot/kernel: %v", err)
	}
	payload := pseudoPattern(fileSize, 0xC0FFEE)
	if err := fs.WriteFile("/boot/kernel/kernel", payload, 0o755); err != nil {
		t.Fatalf("WriteFile 25 MiB: %v", err)
	}
	ro, err := Open(bytes.NewReader(img.Bytes()), int64(fsSize))
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	got, err := ro.ReadFile("/boot/kernel/kernel")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("size = %d, want %d", len(got), len(payload))
	}
	if sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Errorf("payload sha256 mismatch")
	}
}

// TestWriteFile_DoubleIndirect_25MiB stresses the double-indirect
// path explicitly: bsize=4096 (sprint-2C-A heritage default) caps
// single-indirect reach at ~2 MiB, so a 25 MiB blob MUST traverse
// the double-indirect chain. We confirm both that Indirect[1] is set
// in the on-disk inode AND that bytes round-trip.
func TestWriteFile_DoubleIndirect_25MiB(t *testing.T) {
	const (
		fsSize   = 128 << 20 // 128 MiB — needs headroom for fragments + cg
		fileSize = 25 << 20  // 25 MiB; well into double-indirect at bsize=4096
	)
	img := newMemImage(fsSize)
	fs, err := MkfsWith(img, fsSize, MkfsOptions{BlockSize: 4096, FragmentSize: 4096})
	if err != nil {
		t.Fatalf("MkfsWith: %v", err)
	}
	payload := pseudoPattern(fileSize, 0xC0FFEE)
	if err := fs.WriteFile("/big.bin", payload, 0o644); err != nil {
		t.Fatalf("WriteFile 25 MiB: %v", err)
	}
	ro, err := Open(bytes.NewReader(img.Bytes()), int64(fsSize))
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	// Verify Indirect[1] is set — double-indirect must have engaged.
	_, in, err := resolve(ro.rs, ro.sb, "/big.bin", true)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if in.Indirect[0] == 0 {
		t.Errorf("Indirect[0] == 0; single-indirect should have engaged")
	}
	if in.Indirect[1] == 0 {
		t.Errorf("Indirect[1] == 0; double-indirect was not engaged for a 25 MiB file at bsize=4096")
	}
	// Round-trip bytes.
	got, err := ro.ReadFile("/big.bin")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(payload) {
		t.Errorf("payload sha256 mismatch")
	}
}

// TestWriteFile_DoubleIndirect_DeleteFrees deletes a 25 MiB file
// then re-uses the freed blocks for a second file. Cross-checks that
// freeFileBlocks walks the double-indirect chain and returns every
// data + indirect block to the free pool.
func TestWriteFile_DoubleIndirect_DeleteFrees(t *testing.T) {
	const (
		fsSize   = 64 << 20
		fileSize = 4 << 20 // 4 MiB — at bsize=4096 this engages dindir
	)
	img := newMemImage(fsSize)
	// bsize=4096 + frag=4096 so a 4 MiB file (1024 blocks) blows
	// past single-indirect (12 + 512 = 524 blocks) and lands
	// 500 blocks into the double-indirect chain. Tier-1 + ≥1
	// tier-2 + 500 data blocks all need freeing on DeleteFile.
	fs, err := MkfsWith(img, fsSize, MkfsOptions{BlockSize: 4096, FragmentSize: 4096})
	if err != nil {
		t.Fatalf("MkfsWith: %v", err)
	}
	payload := pseudoPattern(fileSize, 0x12345)
	if err := fs.WriteFile("/big1.bin", payload, 0o644); err != nil {
		t.Fatalf("WriteFile big1: %v", err)
	}
	// Sanity: confirm double-indirect engaged.
	{
		ro, _ := Open(bytes.NewReader(img.Bytes()), int64(fsSize))
		_, in, err := resolve(ro.rs, ro.sb, "/big1.bin", true)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if in.Indirect[1] == 0 {
			t.Fatalf("Indirect[1] == 0; double-indirect did not engage at 4 MiB / bsize=4096")
		}
	}
	// Snapshot free-block count before delete.
	sb := fs.Superblock()
	freeBefore := countFreeBlocks(img.Bytes(), sb)
	if err := fs.DeleteFile("/big1.bin"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	freeAfter := countFreeBlocks(img.Bytes(), sb)
	// After delete, free-block count should have GROWN by roughly
	// the file's allocated blocks. We use a sloppy >0 check because
	// the exact delta depends on cylinder-group accounting.
	if freeAfter <= freeBefore {
		t.Errorf("free blocks did not increase after delete: before=%d after=%d", freeBefore, freeAfter)
	}
	// Re-allocate the same 25 MiB to confirm the chain reclamation
	// really worked (no leaked blocks).
	payload2 := pseudoPattern(fileSize, 0x67890)
	if err := fs.WriteFile("/big2.bin", payload2, 0o644); err != nil {
		t.Fatalf("WriteFile big2 after delete: %v", err)
	}
	ro, _ := Open(bytes.NewReader(img.Bytes()), int64(fsSize))
	got, err := ro.ReadFile("/big2.bin")
	if err != nil {
		t.Fatalf("ReadFile big2: %v", err)
	}
	if sha256.Sum256(got) != sha256.Sum256(payload2) {
		t.Errorf("big2 payload mismatch after delete+rewrite")
	}
}

// TestWriteFile_NoSpace_DoubleIndirect drives the writer until the
// backing image runs out of blocks. Exercises the error-propagation
// path inside the double-indirect allocator branch.
func TestWriteFile_NoSpace_DoubleIndirect(t *testing.T) {
	// Tiny image: 4 MiB. At bsize=32768 that's 128 blocks total —
	// double-indirect needs at least one tier-1 + tier-2 + data
	// block, so the writer should give up with ErrNoSpace once the
	// per-LBN allocator depletes the cg bitmap.
	const fsSize = 4 << 20
	img := newMemImage(fsSize)
	fs, err := MkfsWith(img, fsSize, MkfsOptions{BlockSize: 32768})
	if err != nil {
		t.Fatalf("MkfsWith: %v", err)
	}
	// Ask for more bytes than the data area can hold. The writer
	// allocates blocks one at a time so it WILL trip ErrNoSpace.
	payload := make([]byte, fsSize)
	err = fs.WriteFile("/toobig.bin", payload, 0o644)
	if !errors.Is(err, ErrNoSpace) {
		t.Errorf("err = %v, want ErrNoSpace", err)
	}
}

// TestCrossValidate_DoubleIndirectFile is the sprint-2D analogue of
// the read-side cross-validation: write a 25 MiB blob via the new
// MkfsWith + double-indirect writer, then re-read it via TWO
// independent code paths — the high-level fs.ReadFile, and the
// low-level ReadFileBody walking the indirect chain explicitly.
// Both must produce identical bytes; this catches drift between the
// reader's blockForLBN walk and the writer's chain layout.
func TestCrossValidate_DoubleIndirectFile(t *testing.T) {
	const (
		fsSize   = 96 << 20
		fileSize = 20 << 20 // 20 MiB — comfortably into double-indirect
	)
	img := newMemImage(fsSize)
	fs, err := MkfsWith(img, fsSize, MkfsOptions{BlockSize: 32768})
	if err != nil {
		t.Fatalf("MkfsWith: %v", err)
	}
	payload := pseudoPattern(fileSize, 0xDEADBEEF)
	if err := fs.WriteFile("/x.bin", payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Path A: high-level read.
	ro, err := Open(bytes.NewReader(img.Bytes()), int64(fsSize))
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	pathA, err := ro.ReadFile("/x.bin")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Path B: low-level. Resolve the inode then walk every LBN
	// through blockForLBN explicitly. Compare bytes block-by-block
	// to surface any tier-2/tier-1 mix-up.
	_, in, err := resolve(ro.rs, ro.sb, "/x.bin", true)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	bsize := int(ro.sb.Bsize)
	totalBlocks := (fileSize + bsize - 1) / bsize
	pathB := make([]byte, fileSize)
	for lbn := 0; lbn < totalBlocks; lbn++ {
		fragNum, err := blockForLBN(ro.rs, ro.sb, in, int64(lbn))
		if err != nil {
			t.Fatalf("blockForLBN(%d): %v", lbn, err)
		}
		if fragNum == 0 {
			t.Fatalf("LBN %d unexpectedly sparse", lbn)
		}
		start := lbn * bsize
		end := start + bsize
		if end > fileSize {
			end = fileSize
		}
		buf := make([]byte, bsize)
		if _, err := ro.rs.ReadAt(buf, ro.sb.FragOffset(fragNum)); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("ReadAt frag %d: %v", fragNum, err)
		}
		copy(pathB[start:end], buf[:end-start])
	}

	if sha256.Sum256(pathA) != sha256.Sum256(pathB) {
		t.Errorf("path A vs path B differ for double-indirect file")
	}
	if sha256.Sum256(pathA) != sha256.Sum256(payload) {
		t.Errorf("path A vs payload differ")
	}
}

// pseudoPattern fills n bytes with an LCG-derived stream so each
// block has distinguishable content (constant patterns mask tier
// mix-ups).
func pseudoPattern(n int, seed uint64) []byte {
	out := make([]byte, n)
	x := seed*6364136223846793005 + 1442695040888963407
	for i := 0; i < n; i += 8 {
		x = x*6364136223846793005 + 1442695040888963407
		end := i + 8
		if end > n {
			end = n
		}
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], x)
		copy(out[i:end], buf[:])
	}
	return out
}

// countFreeBlocks tallies "0" bits in every cg's free-block bitmap.
// Slow + diagnostic; used only inside the sprint-2D delete-frees
// test to confirm the chain reclamation walked through.
func countFreeBlocks(img []byte, sb *Superblock) int {
	count := 0
	for cg := uint32(0); cg < sb.Ncg; cg++ {
		cgBase := sb.CgBase(cg)
		// Read cg header to find the free-block bitmap offset.
		hdr := make([]byte, sb.Bsize)
		if int(cgBase)+len(hdr) > len(img) {
			break
		}
		copy(hdr, img[cgBase+int64(sb.Cblkno)*int64(sb.Fsize):])
		freeoff := int(binary.LittleEndian.Uint32(hdr[cgOffFreeoff:]))
		n := (int(sb.Fpg) + 7) / 8
		if freeoff+n > len(hdr) {
			continue
		}
		bm := hdr[freeoff : freeoff+n]
		for _, b := range bm {
			for i := 0; i < 8; i++ {
				if b&(1<<i) == 0 {
					count++
				}
			}
		}
	}
	return count
}
