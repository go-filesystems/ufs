// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestParseInode_Good(t *testing.T) {
	img := buildFixture()
	off := fxIblkno*fxFsize + int(inoRoot)*InodeSize
	in, err := parseInode(img[off : off+InodeSize])
	if err != nil {
		t.Fatalf("parseInode: %v", err)
	}
	if !in.IsDir() {
		t.Errorf("root inode is not a directory: mode=%#o", in.Mode)
	}
	if in.Direct[0] != fragRootDir {
		t.Errorf("Direct[0] = %d, want %d", in.Direct[0], fragRootDir)
	}
	if in.Mode&0o777 != 0o755 {
		t.Errorf("perm bits = %#o, want 0755", in.Mode&0o777)
	}
}

func TestParseInode_Short(t *testing.T) {
	_, err := parseInode(make([]byte, 10))
	if err == nil {
		t.Fatal("expected error for short buffer")
	}
}

func TestReadInode_ZeroInodeRejected(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	_, err := ReadInode(bytes.NewReader(img), sb, 0)
	if err == nil {
		t.Fatal("expected error for inode 0")
	}
}

func TestInode_FileTypePredicates(t *testing.T) {
	cases := []struct {
		mode uint16
		dir  bool
		reg  bool
		lnk  bool
	}{
		{IFDIR | 0o755, true, false, false},
		{IFREG | 0o644, false, true, false},
		{IFLNK | 0o777, false, false, true},
		{IFCHR | 0o600, false, false, false},
	}
	for _, c := range cases {
		in := &Inode{Mode: c.mode}
		if in.IsDir() != c.dir {
			t.Errorf("mode=%#o IsDir=%v, want %v", c.mode, in.IsDir(), c.dir)
		}
		if in.IsRegular() != c.reg {
			t.Errorf("mode=%#o IsRegular=%v, want %v", c.mode, in.IsRegular(), c.reg)
		}
		if in.IsSymlink() != c.lnk {
			t.Errorf("mode=%#o IsSymlink=%v, want %v", c.mode, in.IsSymlink(), c.lnk)
		}
		if in.FileType() != (c.mode & IFMT) {
			t.Errorf("FileType mask mismatch")
		}
	}
}

func TestInode_Shortlink(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in, err := ReadInode(bytes.NewReader(img), sb, inoVarLink)
	if err != nil {
		t.Fatalf("ReadInode: %v", err)
	}
	target, ok := in.Shortlink(sb)
	if !ok {
		t.Fatal("expected shortlink to be inline")
	}
	if target != "/etc/var-target" {
		t.Errorf("shortlink target = %q, want %q", target, "/etc/var-target")
	}
}

func TestInode_ShortlinkRejectsNonSymlink(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in, _ := ReadInode(bytes.NewReader(img), sb, inoLoaderConf)
	if _, ok := in.Shortlink(sb); ok {
		t.Error("Shortlink on regular file should return false")
	}
}

func TestInode_ShortlinkRejectsSpilled(t *testing.T) {
	// Forge a symlink inode with Blocks > 0 (target spilled to a
	// data block) and ensure Shortlink returns false.
	var buf [InodeSize]byte
	le := binary.LittleEndian
	le.PutUint16(buf[inoOffMode:], IFLNK|0o777)
	le.PutUint64(buf[inoOffSize:], 50)
	le.PutUint64(buf[inoOffBlocks:], 1)
	in, _ := parseInode(buf[:])
	sb := &Superblock{Maxsymlinklen: 120}
	if _, ok := in.Shortlink(sb); ok {
		t.Error("Shortlink on spilled symlink should return false")
	}
}

func TestInode_ShortlinkRejectsTooLong(t *testing.T) {
	var buf [InodeSize]byte
	le := binary.LittleEndian
	le.PutUint16(buf[inoOffMode:], IFLNK|0o777)
	le.PutUint64(buf[inoOffSize:], 200) // > 120
	le.PutUint64(buf[inoOffBlocks:], 0)
	in, _ := parseInode(buf[:])
	sb := &Superblock{Maxsymlinklen: 120}
	if _, ok := in.Shortlink(sb); ok {
		t.Error("Shortlink longer than 120 bytes must spill")
	}
}

func TestInode_ShortlinkRejectsZeroSize(t *testing.T) {
	var buf [InodeSize]byte
	binary.LittleEndian.PutUint16(buf[inoOffMode:], IFLNK|0o777)
	in, _ := parseInode(buf[:])
	if _, ok := in.Shortlink(&Superblock{}); ok {
		t.Error("zero-length shortlink should fail")
	}
}

func TestInode_ShortlinkHonoursMaxsymlinklen(t *testing.T) {
	var buf [InodeSize]byte
	le := binary.LittleEndian
	le.PutUint16(buf[inoOffMode:], IFLNK|0o777)
	le.PutUint64(buf[inoOffSize:], 64)
	le.PutUint64(buf[inoOffBlocks:], 0)
	in, _ := parseInode(buf[:])
	// Superblock claims max inline is 32 bytes; 64 must spill.
	sb := &Superblock{Maxsymlinklen: 32}
	if _, ok := in.Shortlink(sb); ok {
		t.Error("shortlink larger than fs_maxsymlinklen must spill")
	}
}
