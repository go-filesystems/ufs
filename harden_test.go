// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/go-volumes/safeio"
)

// This file holds the security-hardening regression suite: a fuzz target
// plus deterministic unit tests proving that a malicious or corrupt image
// is turned into a graceful error rather than a panic, an out-of-bounds
// read, an integer overflow, an unbounded loop, or an OOM allocation.
//
// THREAT MODEL: the io.ReaderAt handed to Open() is fully attacker
// controlled. Every code path reachable from Open / ReadFile / ListDir /
// Stat / ReadLink must terminate with (nil, error) on garbage input.

// le is shared little-endian helper for the table-driven mutators below.
var le = binary.LittleEndian

// inodeByteOffset returns the absolute byte offset of inode `ino` within a
// fixture-geometry image, matching writeInode's placement.
func inodeByteOffset(ino uint64) int {
	return fxIblkno*fxFsize + int(ino)*InodeSize
}

// TestHarden_DiSizeOOM is the headline critical case: a crafted inode whose
// di_size is 2^40 (~1 TiB) must NOT drive a ~1 TiB make([]byte, …). The
// hardened ReadFileBody rejects any di_size past the image-derived ceiling
// with safeio.ErrTooLarge instead of attempting the allocation.
func TestHarden_DiSizeOOM(t *testing.T) {
	img := buildFixture()
	// /etc/fstab is inode 8, a regular file. Overwrite its di_size with
	// 2^40 — a value that fits in the 64-bit field, passes the old 32-bit
	// n<0 guard, and would otherwise allocate ~1 TiB.
	off := inodeByteOffset(inoFstab)
	le.PutUint64(img[off+inoOffSize:], 1<<40)

	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = fs.ReadFile("/etc/fstab")
	if err == nil {
		t.Fatal("expected error for oversized di_size, got nil")
	}
	if !errors.Is(err, safeio.ErrTooLarge) {
		t.Fatalf("expected safeio.ErrTooLarge, got %v", err)
	}
}

// TestHarden_DiSizeOOM_Directory proves the same guard protects directory
// reads (ListDir → ReadFileAll). The root directory's di_size is poisoned.
func TestHarden_DiSizeOOM_Directory(t *testing.T) {
	img := buildFixture()
	off := inodeByteOffset(inoRoot)
	le.PutUint64(img[off+inoOffSize:], 1<<40)

	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := fs.ListDir("/"); !errors.Is(err, safeio.ErrTooLarge) {
		t.Fatalf("expected safeio.ErrTooLarge from ListDir, got %v", err)
	}
}

// TestHarden_DiSizeMaxfilesizeBound exercises the Maxfilesize arm of
// maxFileBytes: when the superblock advertises a Maxfilesize smaller than
// the image size, di_size beyond Maxfilesize is rejected too.
func TestHarden_DiSizeMaxfilesizeBound(t *testing.T) {
	img := buildFixture()
	// Tighten Maxfilesize to 1 KiB; image is 1 MiB. A 64 KiB di_size now
	// exceeds the (tighter) Maxfilesize ceiling.
	le.PutUint64(img[SblockUFS2+offMaxfilesize:], 1024)
	off := inodeByteOffset(inoFstab)
	le.PutUint64(img[off+inoOffSize:], 64*1024)

	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := fs.ReadFile("/etc/fstab"); !errors.Is(err, safeio.ErrTooLarge) {
		t.Fatalf("expected safeio.ErrTooLarge (Maxfilesize bound), got %v", err)
	}
}

// TestHarden_InodeNumberHuge poisons a dirent so it names an enormous inode
// number; ReadInode's range check (ino < Ncg*Ipg) must reject it instead of
// pointing InodeOffset at a wild offset.
func TestHarden_InodeNumberHuge(t *testing.T) {
	img := buildFixture()
	// Locate the "boot" dirent in the root directory block and rewrite its
	// d_ino to a huge value. Resolving "/boot" then forces a ReadInode of
	// that number, which the range check (ino < Ncg*Ipg) must reject.
	dirOff := fragRootDir * fxFsize
	dirData := img[dirOff : dirOff+fxFsize]
	ents, err := ParseDirents(dirData)
	if err != nil {
		t.Fatalf("ParseDirents: %v", err)
	}
	var bootRecOff int = -1
	cursor := 0
	for _, e := range ents {
		if e.Name == "boot" {
			bootRecOff = cursor
			break
		}
		// d_reclen is the 2-byte field at offset +4 of each dirent.
		cursor += int(le.Uint16(dirData[cursor+4:]))
	}
	if bootRecOff < 0 {
		t.Fatal("could not locate boot dirent")
	}
	le.PutUint32(dirData[bootRecOff:], 0xFFFFFFF0)

	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := fs.Stat("/boot"); err == nil {
		t.Fatal("expected error for huge inode number, got nil")
	}
}

// TestHarden_ReadInodeRange directly drives ReadInode with out-of-range
// inode numbers (0 and >= Ncg*Ipg) and asserts both are rejected.
func TestHarden_ReadInodeRange(t *testing.T) {
	img := buildFixture()
	sb, err := ReadSuperblock(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("ReadSuperblock: %v", err)
	}
	rs := bytes.NewReader(img)
	if _, err := ReadInode(rs, sb, 0); err == nil {
		t.Fatal("inode 0 must be rejected")
	}
	maxIno := uint64(sb.Ncg) * uint64(sb.Ipg)
	if _, err := ReadInode(rs, sb, maxIno); err == nil {
		t.Fatalf("inode %d (== Ncg*Ipg) must be rejected", maxIno)
	}
	if _, err := ReadInode(rs, sb, maxIno+1000); err == nil {
		t.Fatal("huge inode must be rejected")
	}
	// A valid inode still reads.
	if _, err := ReadInode(rs, sb, inoRoot); err != nil {
		t.Fatalf("valid inode %d unexpectedly rejected: %v", inoRoot, err)
	}
}

// TestHarden_FpgNegative poisons Fpg to a negative value; validate() must
// reject the superblock with ErrBadSuperblock instead of producing bad
// offset math downstream.
func TestHarden_FpgNegative(t *testing.T) {
	img := buildFixture()
	le.PutUint32(img[SblockUFS2+offFpg:], 0xFFFFFFFF) // int32(-1)

	_, err := Open(bytes.NewReader(img), int64(len(img)))
	if !errors.Is(err, ErrBadSuperblock) {
		t.Fatalf("expected ErrBadSuperblock for negative Fpg, got %v", err)
	}
}

// TestHarden_FpgZero covers the Fpg == 0 branch of validate().
func TestHarden_FpgZero(t *testing.T) {
	img := buildFixture()
	le.PutUint32(img[SblockUFS2+offFpg:], 0)
	if _, err := Open(bytes.NewReader(img), int64(len(img))); !errors.Is(err, ErrBadSuperblock) {
		t.Fatalf("expected ErrBadSuperblock for zero Fpg, got %v", err)
	}
}

// TestHarden_GeometryOverflow drives the Ncg*Fpg*Fsize int64-overflow guard
// in validate(): a huge Ncg combined with a large Fpg overflows int64.
func TestHarden_GeometryOverflow(t *testing.T) {
	img := buildFixture()
	// Fsize=4096 (2^12). Pick Fpg=2^30 and Ncg=2^30 → product 2^72 >> 2^63.
	le.PutUint32(img[SblockUFS2+offNcg:], 1<<30)
	le.PutUint32(img[SblockUFS2+offFpg:], 1<<30)
	if _, err := Open(bytes.NewReader(img), int64(len(img))); !errors.Is(err, ErrBadSuperblock) {
		t.Fatalf("expected ErrBadSuperblock for geometry overflow, got %v", err)
	}
}

// FuzzReadImage feeds arbitrary byte slices through the full Open + traversal
// surface. The contract under test is liveness + safety: NO panic, NO hang,
// NO OOM — only (result, nil) on a structurally valid image or (_, error) on
// garbage. The seed corpus pins the exact attack vectors from the threat
// model so they run under plain `go test` even without -fuzz.
func FuzzReadImage(f *testing.F) {
	// Seed 1: a pristine, fully valid fixture (the happy path).
	f.Add(buildFixture())

	// Seed 2: di_size = 2^40 on a regular file (the critical OOM vector).
	{
		img := buildFixture()
		le.PutUint64(img[inodeByteOffset(inoFstab)+inoOffSize:], 1<<40)
		f.Add(img)
	}
	// Seed 3: di_size = 2^40 on the root directory.
	{
		img := buildFixture()
		le.PutUint64(img[inodeByteOffset(inoRoot)+inoOffSize:], 1<<40)
		f.Add(img)
	}
	// Seed 4: a huge inode number in the root directory block.
	{
		img := buildFixture()
		le.PutUint32(img[fragRootDir*fxFsize:], 0xFFFFFFF0)
		f.Add(img)
	}
	// Seed 5: inode number 0 in the root directory block.
	{
		img := buildFixture()
		le.PutUint32(img[fragRootDir*fxFsize:], 0)
		f.Add(img)
	}
	// Seed 6: negative Fpg.
	{
		img := buildFixture()
		le.PutUint32(img[SblockUFS2+offFpg:], 0xFFFFFFFF)
		f.Add(img)
	}
	// Seed 7: geometry-overflow Ncg/Fpg.
	{
		img := buildFixture()
		le.PutUint32(img[SblockUFS2+offNcg:], 1<<30)
		le.PutUint32(img[SblockUFS2+offFpg:], 1<<30)
		f.Add(img)
	}
	// Seed 8: truncated image (superblock present, body chopped).
	{
		img := buildFixture()
		f.Add(img[:SblockUFS2+2000])
	}
	// Seed 9: empty input.
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Open may fail (most fuzz inputs are not valid superblocks); that
		// is fine. What must never happen is a panic or a runaway alloc.
		fs, err := Open(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return
		}
		// Exercise every read surface. Each may legitimately error; none
		// may panic or OOM. We deliberately ignore returned values.
		_, _ = fs.ListDir("/")
		_, _ = fs.ReadFile("/etc/fstab")
		_, _ = fs.ReadFile("/boot/loader.conf")
		_, _ = fs.Stat("/")
		_, _ = fs.Stat("/.")
		_, _ = fs.ReadLink("/etc/rc.conf")
		_ = fs.Close()
	})
}
