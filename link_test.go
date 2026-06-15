// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"testing"

	filesystem "github.com/go-filesystems/interface"
)

func TestLinkHardlink(t *testing.T) {
	const size = 16 * 1024 * 1024
	img := newMemImage(size)
	fs, err := Mkfs(img, size)
	if err != nil {
		t.Fatalf("Mkfs: %v", err)
	}

	var _ filesystem.HardLinker = fs // capability is exposed

	if err := fs.WriteFile("/orig", []byte("shared-bytes"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := fs.Link("/orig", "/alias"); err != nil {
		t.Fatalf("Link: %v", err)
	}

	// Both names resolve to the same inode, with link count bumped to 2.
	inoA, inA, err := resolve(fs.rs, fs.sb, "/orig", false)
	if err != nil {
		t.Fatalf("resolve /orig: %v", err)
	}
	inoB, _, err := resolve(fs.rs, fs.sb, "/alias", false)
	if err != nil {
		t.Fatalf("resolve /alias: %v", err)
	}
	if inoA != inoB {
		t.Fatalf("hardlink inode mismatch: %d vs %d", inoA, inoB)
	}
	if inA.Nlink != 2 {
		t.Fatalf("link count = %d, want 2", inA.Nlink)
	}

	// Both names read the same content.
	got, err := fs.ReadFile("/alias")
	if err != nil || string(got) != "shared-bytes" {
		t.Fatalf("ReadFile(/alias) = %q, %v; want shared-bytes", got, err)
	}

	// Directories cannot be hard-linked.
	if err := fs.MkDir("/d", 0o755); err != nil {
		t.Fatalf("MkDir: %v", err)
	}
	if err := fs.Link("/d", "/d2"); err == nil {
		t.Fatal("Link of directory: expected error")
	}
	// Existing target name is rejected.
	if err := fs.Link("/orig", "/alias"); err == nil {
		t.Fatal("Link onto existing name: expected error")
	}

	// Re-open read-side: both names are present and read the content.
	ro, err := Open(bytes.NewReader(img.Bytes()), int64(size))
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	for _, name := range []string{"/orig", "/alias"} {
		got, err := ro.ReadFile(name)
		if err != nil || string(got) != "shared-bytes" {
			t.Fatalf("post-reopen ReadFile(%s) = %q, %v; want shared-bytes", name, got, err)
		}
	}
}
