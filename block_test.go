// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"errors"
	"testing"
)

func TestReadFileBody_Direct(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in, _ := ReadInode(bytes.NewReader(img), sb, inoLoaderConf)
	data, err := ReadFileAll(bytes.NewReader(img), sb, in)
	if err != nil {
		t.Fatalf("ReadFileAll: %v", err)
	}
	if !bytes.Equal(data, fxLoaderConf) {
		t.Errorf("loader.conf mismatch:\n got %q\nwant %q", data, fxLoaderConf)
	}
}

func TestReadFileBody_MultiDirect(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in, _ := ReadInode(bytes.NewReader(img), sb, inoKernelFile)
	data, err := ReadFileAll(bytes.NewReader(img), sb, in)
	if err != nil {
		t.Fatalf("ReadFileAll: %v", err)
	}
	if !bytes.Equal(data, fxKernelStub) {
		t.Errorf("kernel stub mismatch (len %d vs %d)", len(data), len(fxKernelStub))
	}
}

func TestReadFileBody_SingleIndirect(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in, _ := ReadInode(bytes.NewReader(img), sb, inoBig)
	data, err := ReadFileAll(bytes.NewReader(img), sb, in)
	if err != nil {
		t.Fatalf("ReadFileAll big: %v", err)
	}
	if !bytes.Equal(data, fxBigData) {
		t.Errorf("big-file mismatch (len %d vs %d)", len(data), len(fxBigData))
	}
}

func TestReadFileBody_Partial(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in, _ := ReadInode(bytes.NewReader(img), sb, inoLoaderConf)
	data, err := ReadFileBody(bytes.NewReader(img), sb, in, 7, 6)
	if err != nil {
		t.Fatalf("ReadFileBody: %v", err)
	}
	want := fxLoaderConf[7:13]
	if !bytes.Equal(data, want) {
		t.Errorf("partial read mismatch: got %q, want %q", data, want)
	}
}

func TestReadFileBody_OffsetPastEOF(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in, _ := ReadInode(bytes.NewReader(img), sb, inoLoaderConf)
	data, err := ReadFileBody(bytes.NewReader(img), sb, in, 999999, 5)
	if err != nil {
		t.Fatalf("ReadFileBody: %v", err)
	}
	if data != nil {
		t.Errorf("expected nil, got %v", data)
	}
}

func TestReadFileBody_NegativeArgs(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in, _ := ReadInode(bytes.NewReader(img), sb, inoLoaderConf)
	if _, err := ReadFileBody(bytes.NewReader(img), sb, in, -1, 4); err == nil {
		t.Error("expected error for negative offset")
	}
	if _, err := ReadFileBody(bytes.NewReader(img), sb, in, 0, -4); err == nil {
		t.Error("expected error for negative length")
	}
}

func TestReadFileBody_SparseHole(t *testing.T) {
	// Construct an inode with size 8192 but only block 1 mapped;
	// block 0 left as zero pointer → should read back zeros.
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])

	// Reuse the loader-conf fragment as the second block of a
	// synthetic sparse file so we don't have to allocate more.
	in := &Inode{
		Mode: IFREG | 0o644,
		Size: 8192,
	}
	in.Direct[0] = 0
	in.Direct[1] = fragLoaderConf
	data, err := ReadFileAll(bytes.NewReader(img), sb, in)
	if err != nil {
		t.Fatalf("ReadFileAll sparse: %v", err)
	}
	if len(data) != 8192 {
		t.Fatalf("got %d bytes, want 8192", len(data))
	}
	// First 4096 bytes should be zero.
	for i := 0; i < 4096; i++ {
		if data[i] != 0 {
			t.Fatalf("hole byte %d not zero: %d", i, data[i])
		}
	}
	// Bytes [4096:4096+len(fxLoaderConf)] match.
	if !bytes.Equal(data[4096:4096+len(fxLoaderConf)], fxLoaderConf) {
		t.Errorf("second block payload mismatch")
	}
}

func TestReadFileBody_TripleIndirectUnsupported(t *testing.T) {
	// Sprint 2D: single + double-indirect are now supported. Only the
	// triple-indirect tier (nindir³ × bsize) remains intentionally
	// unimplemented. We call blockForLBN directly with a high LBN to
	// exercise the triple-indirect branch without driving a multi-GiB
	// read through the byte slice.
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in := &Inode{Mode: IFREG | 0o644}
	in.Indirect[2] = uint64(fragBigIndirect) // pretend triple-indirect
	highLBN := int64(NumDirect + fxNindir + fxNindir*fxNindir + 1)
	_, err := blockForLBN(bytes.NewReader(img), sb, in, highLBN)
	if !errors.Is(err, ErrUnsupportedIndirect) {
		t.Fatalf("err = %v, want ErrUnsupportedIndirect", err)
	}
}
