// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

// Memory-bomb regression tests. Each hands the driver a 1 MiB fixture whose
// superblock or inode claims far more, and asserts the read is refused and that
// less than bombBudget was allocated doing it (runtime.MemStats TotalAlloc
// before/after).

import (
	"bytes"
	"errors"
	"runtime"
	"testing"

	"github.com/go-volumes/safeio"
)

const bombBudget = 256 << 10

func totalAlloc(f func()) uint64 {
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

// bombFS returns the fixture image with the superblock's cylinder-group count
// patched to ncg, which is what ImageBytes -- and with it the old read ceiling
// -- is derived from.
func bombFS(t *testing.T, ncg uint32) ([]byte, *Superblock) {
	t.Helper()
	img := buildFixture()
	le.PutUint32(img[SblockUFS2+offNcg:], ncg)
	sb, err := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	if err != nil {
		t.Fatalf("parseSuperblock: %v", err)
	}
	return img, sb
}

// A superblock claiming a terabyte of cylinder groups raised the ceiling on a
// read to a terabyte, so a regular file declaring 1 GiB whose block pointers
// lead past the end of the 1 MiB image reserved the 1 GiB first and failed on
// the first block after.
func TestBomb_DenseFileBeyondTheImage(t *testing.T) {
	img, sb := bombFS(t, 1<<20)
	in := &Inode{Mode: IFREG | 0o644, Size: 1 << 30}
	for i := range in.Direct {
		in.Direct[i] = 1 << 20 // fragment 1M = byte 4 GiB, past the image
	}
	var err error
	n := totalAlloc(func() { _, err = ReadFileAll(bytes.NewReader(img), sb, in) })
	if err == nil {
		t.Error("a 1 GiB file with every block past the image was read")
	}
	if n >= bombBudget {
		t.Errorf("allocated %d bytes before failing", n)
	}
}

// Ncg is a uint32 off the image: the allocator header list was sized by it.
func TestBomb_LoadAllocatorHugeNcg(t *testing.T) {
	img, sb := bombFS(t, 1<<24)
	fs := &FS{rs: bytes.NewReader(img), sb: sb}
	var err error
	n := totalAlloc(func() { _, err = loadAllocator(fs) })
	if err == nil {
		t.Error("16M cylinder groups were loaded from a 1 MiB image")
	}
	if n >= bombBudget {
		t.Errorf("allocated %d bytes", n)
	}
}

// A symlink target is a path: a symlink inode declaring 1 GiB must not build a
// gigabyte string for splitPath.
func TestBomb_SymlinkTargetIsBounded(t *testing.T) {
	img, sb := bombFS(t, 1<<20)
	rs := bytes.NewReader(img)
	in := &Inode{Mode: IFLNK | 0o777, Size: 1 << 30}
	var err error
	n := totalAlloc(func() { _, err = readSymlinkTarget(rs, sb, in) })
	if !errors.Is(err, safeio.ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	if n >= bombBudget {
		t.Errorf("allocated %d bytes", n)
	}
	// The bound is exact: PATH_MAX-sized targets still read.
	in.Size = 4096
	if s, err := readSymlinkTarget(rs, sb, in); err != nil || len(s) != 4096 {
		t.Errorf("4096-byte target: len=%d err=%v", len(s), err)
	}
	in.Size = 4097
	if _, err := readSymlinkTarget(rs, sb, in); err == nil {
		t.Error("4097-byte target accepted")
	}
}

// A block holds at most MAXFRAG = 8 fragments.
func TestBomb_FragmentsPerBlockCeilingIsExact(t *testing.T) {
	for _, tc := range []struct {
		fsize int32
		ok    bool
	}{{4096, true}, {512, true}, {256, false}, {128, false}} {
		img := buildFixture()
		base := SblockUFS2
		le.PutUint32(img[base+offFsize:], uint32(tc.fsize))
		le.PutUint32(img[base+offFrag:], uint32(4096/tc.fsize))
		_, err := parseSuperblock(img[base : base+1376])
		if (err == nil) != tc.ok {
			t.Errorf("fsize %d (frag %d): err=%v, want ok=%v", tc.fsize, 4096/tc.fsize, err, tc.ok)
		}
	}
}
