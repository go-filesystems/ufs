// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"encoding/binary"
	"testing"
)

func TestDirentReclen(t *testing.T) {
	cases := []struct {
		namlen int
		want   int
	}{
		{1, 12},  // "." → 8+1+1 = 10 → roundup4 → 12
		{2, 12},  // ".." → 8+2+1 = 11 → 12
		{3, 12},  // "abc" → 8+3+1 = 12
		{4, 16},
		{7, 16},
		{8, 20},
		{255, 264},
	}
	for _, c := range cases {
		if got := DirentReclen(c.namlen); got != c.want {
			t.Errorf("DirentReclen(%d) = %d, want %d", c.namlen, got, c.want)
		}
	}
}

func TestEncodeAndParseDirents_Roundtrip(t *testing.T) {
	buf := make([]byte, fxBsize)
	d := newDir(binary.LittleEndian)
	d.add(2, DtDir, ".")
	d.add(2, DtDir, "..")
	d.add(3, DtReg, "hello.txt")
	d.add(4, DtLnk, "lnk")
	d.writeTo(buf)

	entries, err := ParseDirents(buf)
	if err != nil {
		t.Fatalf("ParseDirents: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}
	wantNames := []string{".", "..", "hello.txt", "lnk"}
	wantInos := []uint32{2, 2, 3, 4}
	wantTypes := []uint8{DtDir, DtDir, DtReg, DtLnk}
	for i, e := range entries {
		if e.Name != wantNames[i] {
			t.Errorf("entry %d name = %q, want %q", i, e.Name, wantNames[i])
		}
		if e.Ino != wantInos[i] {
			t.Errorf("entry %d ino = %d, want %d", i, e.Ino, wantInos[i])
		}
		if e.Type != wantTypes[i] {
			t.Errorf("entry %d type = %d, want %d", i, e.Type, wantTypes[i])
		}
	}
}

func TestParseDirents_SkipsVacantSlots(t *testing.T) {
	buf := make([]byte, 64)
	// First record: vacant (ino=0, reclen=24).
	binary.LittleEndian.PutUint32(buf[0:], 0)
	binary.LittleEndian.PutUint16(buf[4:], 24)
	buf[6] = DtUnknown
	buf[7] = 0
	// Second record: live, ino=7, name="x", reclen=fills the rest.
	binary.LittleEndian.PutUint32(buf[24:], 7)
	binary.LittleEndian.PutUint16(buf[24+4:], 40)
	buf[24+6] = DtReg
	buf[24+7] = 1
	buf[24+8] = 'x'
	entries, err := ParseDirents(buf)
	if err != nil {
		t.Fatalf("ParseDirents: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "x" {
		t.Errorf("expected one entry 'x', got %+v", entries)
	}
}

func TestParseDirents_BadReclen(t *testing.T) {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint32(buf[0:], 7)
	binary.LittleEndian.PutUint16(buf[4:], 3) // less than header
	buf[6] = DtReg
	buf[7] = 0
	if _, err := ParseDirents(buf); err == nil {
		t.Fatal("expected error for tiny reclen")
	}
}

func TestParseDirents_UnalignedReclen(t *testing.T) {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint32(buf[0:], 7)
	binary.LittleEndian.PutUint16(buf[4:], 9) // not multiple of 4
	buf[6] = DtReg
	buf[7] = 1
	if _, err := ParseDirents(buf); err == nil {
		t.Fatal("expected error for unaligned reclen")
	}
}

func TestParseDirents_NamlenOverflow(t *testing.T) {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint32(buf[0:], 7)
	binary.LittleEndian.PutUint16(buf[4:], 12)
	buf[6] = DtReg
	buf[7] = 50 // namlen way larger than reclen-header
	if _, err := ParseDirents(buf); err == nil {
		t.Fatal("expected error for oversized namlen")
	}
}

func TestParseDirents_StopsOnPartialTrailer(t *testing.T) {
	// One full record (16 bytes), then 4 bytes of trailing slack
	// that look like an underread header.
	buf := make([]byte, 20)
	binary.LittleEndian.PutUint32(buf[0:], 7)
	binary.LittleEndian.PutUint16(buf[4:], 16)
	buf[6] = DtReg
	buf[7] = 1
	buf[8] = 'a'
	// 16..20 left as zero — header is short but parser should
	// detect it.
	binary.LittleEndian.PutUint32(buf[16:], 9)
	// reclen at [20:22] doesn't exist; parser must stop cleanly.
	entries, err := ParseDirents(buf)
	if err != nil {
		t.Fatalf("ParseDirents: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
}

func TestEncodeDirent_RejectsShortBuf(t *testing.T) {
	if n := EncodeDirent(make([]byte, 4), 1, DtReg, "x", 12); n != 0 {
		t.Errorf("expected 0 (short buf), got %d", n)
	}
}
