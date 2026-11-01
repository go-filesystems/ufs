// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"errors"
	"testing"
)

// A sparse file legitimately reads back bigger than the image, so it has a
// ceiling: at the ceiling passes, one over is refused before anything is
// allocated.
func TestBomb_SparseCeilingIsExact(t *testing.T) {
	img, sb := bombFS(t, 1<<20)
	fs := &FS{rs: bytes.NewReader(img), sb: sb}
	fs.SetMaxFileBytes(1 << 20)
	in := &Inode{Mode: IFREG | 0o644, Size: 1 << 20} // all holes
	if b, err := ReadFileAll(fs.rs, fs.sb, in); err != nil || len(b) != 1<<20 {
		t.Fatalf("at the ceiling: len=%d err=%v", len(b), err)
	}
	in.Size = 1<<20 + 1
	var err error
	n := totalAlloc(func() { _, err = ReadFileAll(fs.rs, fs.sb, in) })
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("one over the ceiling: err=%v, want ErrTooLarge", err)
	}
	if n >= bombBudget {
		t.Errorf("allocated %d bytes before refusing", n)
	}
	fs.SetMaxFileBytes(0)
	if got := fs.sb.readLimit(); got != DefaultMaxFileBytes {
		t.Errorf("SetMaxFileBytes(0) left the limit at %d", got)
	}
}

// The default ceiling applies to a superblock that claims a terabyte.
func TestBomb_DefaultCeiling(t *testing.T) {
	_, sb := bombFS(t, 1<<20)
	if got := sb.maxFileBytes(); got != DefaultMaxFileBytes {
		t.Errorf("maxFileBytes = %d, want %d", got, int64(DefaultMaxFileBytes))
	}
}
