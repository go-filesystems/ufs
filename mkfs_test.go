// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"testing"
)

func TestMkfs_FromScratch(t *testing.T) {
	const size = 16 * 1024 * 1024 // 16 MiB
	img := newMemImage(size)
	fs, err := Mkfs(img, size)
	if err != nil {
		t.Fatalf("Mkfs: %v", err)
	}
	if fs.Superblock().Magic != MagicUFS2 {
		t.Fatalf("bad superblock magic")
	}
	// Create the boot tree.
	if err := fs.MkDir("/boot", 0o755); err != nil {
		t.Fatalf("mkdir /boot: %v", err)
	}
	if err := fs.MkDir("/boot/kernel", 0o755); err != nil {
		t.Fatalf("mkdir /boot/kernel: %v", err)
	}
	loaderConf := []byte("kernel=\"kernel\"\nautoboot_delay=\"3\"\n")
	if err := fs.WriteFile("/boot/loader.conf", loaderConf, 0o644); err != nil {
		t.Fatalf("write loader.conf: %v", err)
	}
	// "kernel" binary — a few blocks of pattern.
	kernel := bytesPattern(64*1024, 0xA5)
	if err := fs.WriteFile("/boot/kernel/kernel", kernel, 0o755); err != nil {
		t.Fatalf("write kernel: %v", err)
	}
	// Re-open via the read-side and verify.
	ro, err := Open(bytes.NewReader(img.Bytes()), int64(size))
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	gotConf, err := ro.ReadFile("/boot/loader.conf")
	if err != nil {
		t.Fatalf("read loader.conf: %v", err)
	}
	if !bytes.Equal(gotConf, loaderConf) {
		t.Errorf("loader.conf mismatch")
	}
	gotK, err := ro.ReadFile("/boot/kernel/kernel")
	if err != nil {
		t.Fatalf("read kernel: %v", err)
	}
	if !bytes.Equal(gotK, kernel) {
		t.Errorf("kernel mismatch len=%d want=%d", len(gotK), len(kernel))
	}
	// Walk root.
	entries, err := ro.ListDir("/")
	if err != nil {
		t.Fatalf("ListDir /: %v", err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Name()] = true
	}
	for _, n := range []string{".", "..", "boot"} {
		if !have[n] {
			t.Errorf("root missing %q (have %v)", n, have)
		}
	}
}

func TestMkfs_TooSmall(t *testing.T) {
	img := newMemImage(1024)
	if _, err := Mkfs(img, 1024); err == nil {
		t.Errorf("expected error for tiny image")
	}
}

func TestMkfs_RoundTripDelete(t *testing.T) {
	const size = 8 * 1024 * 1024
	img := newMemImage(size)
	fs, err := Mkfs(img, size)
	if err != nil {
		t.Fatalf("Mkfs: %v", err)
	}
	if err := fs.WriteFile("/temp.txt", []byte("delete me"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := fs.DeleteFile("/temp.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Reuse the inode/block — create another file.
	if err := fs.WriteFile("/again.txt", []byte("recycled"), 0o644); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	ro, _ := Open(bytes.NewReader(img.Bytes()), int64(size))
	got, err := ro.ReadFile("/again.txt")
	if err != nil {
		t.Fatalf("read 2: %v", err)
	}
	if string(got) != "recycled" {
		t.Errorf("got %q", got)
	}
}
