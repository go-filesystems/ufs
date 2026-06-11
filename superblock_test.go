// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

// errReader is an io.ReaderAt that always fails. Used to assert
// that ReadSuperblock wraps low-level I/O errors cleanly.
type errReader struct{}

func (errReader) ReadAt([]byte, int64) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestParseSuperblock_Good(t *testing.T) {
	img := buildFixture()
	sb, err := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	if err != nil {
		t.Fatalf("parseSuperblock: %v", err)
	}
	if sb.Magic != MagicUFS2 {
		t.Errorf("magic = %#x, want %#x", sb.Magic, MagicUFS2)
	}
	if sb.Bsize != fxBsize || sb.Fsize != fxFsize {
		t.Errorf("bsize/fsize = %d/%d, want %d/%d", sb.Bsize, sb.Fsize, fxBsize, fxFsize)
	}
	if sb.Inopb != fxInopb {
		t.Errorf("inopb = %d, want %d", sb.Inopb, fxInopb)
	}
	if sb.Ncg != fxNcg || sb.Ipg != fxIpg {
		t.Errorf("ncg/ipg = %d/%d, want %d/%d", sb.Ncg, sb.Ipg, fxNcg, fxIpg)
	}
	if sb.OldInodefmt != 2 {
		t.Errorf("inodefmt = %d, want 2", sb.OldInodefmt)
	}
}

func TestParseSuperblock_BadMagic(t *testing.T) {
	img := buildFixture()
	// Corrupt the magic word.
	binary.LittleEndian.PutUint32(img[SblockUFS2+offMagic:], 0xDEADBEEF)
	_, err := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	if !errors.Is(err, ErrBadSuperblock) {
		t.Fatalf("err = %v, want ErrBadSuperblock", err)
	}
}

func TestParseSuperblock_UFS1Magic(t *testing.T) {
	img := buildFixture()
	binary.LittleEndian.PutUint32(img[SblockUFS2+offMagic:], MagicUFS1)
	_, err := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	if !errors.Is(err, ErrBadSuperblock) {
		t.Fatalf("err = %v, want ErrBadSuperblock", err)
	}
}

func TestParseSuperblock_ShortBuffer(t *testing.T) {
	_, err := parseSuperblock(make([]byte, 100))
	if !errors.Is(err, ErrBadSuperblock) {
		t.Fatalf("err = %v, want ErrBadSuperblock", err)
	}
}

func TestParseSuperblock_BadBsize(t *testing.T) {
	cases := []struct {
		name  string
		patch func(b []byte)
	}{
		{
			name: "bsize-too-small",
			patch: func(b []byte) {
				binary.LittleEndian.PutUint32(b[offBsize:], 1024)
			},
		},
		{
			name: "bsize-non-pow2",
			patch: func(b []byte) {
				binary.LittleEndian.PutUint32(b[offBsize:], 5000)
			},
		},
		{
			name: "fsize-bigger-than-bsize",
			patch: func(b []byte) {
				binary.LittleEndian.PutUint32(b[offFsize:], 8192)
			},
		},
		{
			name: "frag-inconsistent",
			patch: func(b []byte) {
				binary.LittleEndian.PutUint32(b[offFrag:], 99)
			},
		},
		{
			name: "inopb-wrong",
			patch: func(b []byte) {
				binary.LittleEndian.PutUint32(b[offInopb:], 999)
			},
		},
		{
			name: "ncg-zero",
			patch: func(b []byte) {
				binary.LittleEndian.PutUint32(b[offNcg:], 0)
			},
		},
		{
			name: "ipg-zero",
			patch: func(b []byte) {
				binary.LittleEndian.PutUint32(b[offIpg:], 0)
			},
		},
		{
			name: "inodefmt-wrong",
			patch: func(b []byte) {
				binary.LittleEndian.PutUint32(b[offOldInodefmt:], 1)
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			img := buildFixture()
			c.patch(img[SblockUFS2 : SblockUFS2+1376])
			_, err := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
			if !errors.Is(err, ErrBadSuperblock) {
				t.Fatalf("err = %v, want ErrBadSuperblock", err)
			}
		})
	}
}

func TestReadSuperblock_FromReader(t *testing.T) {
	img := buildFixture()
	sb, err := ReadSuperblock(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("ReadSuperblock: %v", err)
	}
	if sb.Magic != MagicUFS2 {
		t.Fatalf("magic = %#x", sb.Magic)
	}
}

func TestReadSuperblock_IOError(t *testing.T) {
	_, err := ReadSuperblock(errReader{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrBadSuperblock) {
		t.Fatalf("unexpected ErrBadSuperblock wrap: %v", err)
	}
}

func TestSuperblockGeometry(t *testing.T) {
	img := buildFixture()
	sb, err := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	if err != nil {
		t.Fatal(err)
	}
	if got := sb.CgBase(0); got != 0 {
		t.Errorf("CgBase(0) = %d, want 0", got)
	}
	want := int64(fxIblkno*fxFsize) + int64(inoRoot)*int64(InodeSize)
	if got := sb.InodeOffset(inoRoot); got != want {
		t.Errorf("InodeOffset(root) = %d, want %d", got, want)
	}
	if got := sb.FragOffset(fragRootDir); got != int64(fragRootDir*fxFsize) {
		t.Errorf("FragOffset(fragRootDir) = %d, want %d", got, fragRootDir*fxFsize)
	}
}
