// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

// memImage is an in-memory io.ReaderAt + io.WriterAt that grows on
// demand. We use it for the round-trip tests so we can write, then
// re-open via the read-side without any disk I/O.
type memImage struct {
	mu  sync.Mutex
	buf []byte
}

func newMemImage(size int) *memImage {
	return &memImage{buf: make([]byte, size)}
}

func (m *memImage) ReadAt(p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if off >= int64(len(m.buf)) {
		return 0, nil
	}
	n := copy(p, m.buf[off:])
	return n, nil
}

func (m *memImage) WriteAt(p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if int(off)+len(p) > len(m.buf) {
		grow := int(off) + len(p) - len(m.buf)
		m.buf = append(m.buf, make([]byte, grow)...)
	}
	return copy(m.buf[off:], p), nil
}

func (m *memImage) Bytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]byte, len(m.buf))
	copy(out, m.buf)
	return out
}

// freshFS builds a fresh Mkfs'd image and primes it with a /boot
// subtree mirroring (a subset of) the read-side fixture so the write
// tests have an interesting structure to mutate. Round-trip tests
// re-open this image read-only and verify the writes survived.
func freshFS(t *testing.T) (*FS, *memImage) {
	t.Helper()
	const size = 16 * 1024 * 1024
	img := newMemImage(size)
	fs, err := Mkfs(img, size)
	if err != nil {
		t.Fatalf("Mkfs: %v", err)
	}
	if err := fs.MkDir("/boot", 0o755); err != nil {
		t.Fatalf("mkdir /boot: %v", err)
	}
	if err := fs.MkDir("/boot/kernel", 0o755); err != nil {
		t.Fatalf("mkdir /boot/kernel: %v", err)
	}
	if err := fs.MkDir("/etc", 0o755); err != nil {
		t.Fatalf("mkdir /etc: %v", err)
	}
	if err := fs.WriteFile("/boot/loader.conf", fxLoaderConf, 0o644); err != nil {
		t.Fatalf("write loader.conf: %v", err)
	}
	if err := fs.WriteFile("/etc/fstab", fxFstab, 0o644); err != nil {
		t.Fatalf("write fstab: %v", err)
	}
	return fs, img
}

func TestWriteFile_RoundTrip(t *testing.T) {
	fs, m := freshFS(t)
	payload := []byte("hello-ufs2-writer\n")
	if err := fs.WriteFile("/boot/newfile.txt", payload, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ro, err := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	got, err := ro.ReadFile("/boot/newfile.txt")
	if err != nil {
		t.Fatalf("ReadFile after write: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch: got %q want %q", got, payload)
	}
	// Existing files still readable.
	if got, err := ro.ReadFile("/boot/loader.conf"); err != nil || !bytes.Equal(got, fxLoaderConf) {
		t.Errorf("loader.conf round-trip broken: %v", err)
	}
}

func TestWriteFile_LargeMultiBlock(t *testing.T) {
	fs, m := freshFS(t)
	// 5 blocks worth → uses 5 direct pointers, no indirect needed.
	payload := bytesPattern(fxBsize*5+123, 0x77)
	if err := fs.WriteFile("/boot/big.bin", payload, 0o644); err != nil {
		t.Fatalf("WriteFile big: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	got, err := ro.ReadFile("/boot/big.bin")
	if err != nil {
		t.Fatalf("read big: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("big mismatch len=%d want=%d", len(got), len(payload))
	}
}

func TestWriteFile_IndirectBlock(t *testing.T) {
	fs, m := freshFS(t)
	// 13 blocks → needs single-indirect.
	payload := bytesPattern(fxBsize*13, 0x33)
	if err := fs.WriteFile("/boot/idx.bin", payload, 0o644); err != nil {
		t.Fatalf("WriteFile indirect: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	got, err := ro.ReadFile("/boot/idx.bin")
	if err != nil {
		t.Fatalf("read indirect: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("indirect mismatch len=%d want=%d", len(got), len(payload))
	}
}

func TestWriteFile_Exists(t *testing.T) {
	fs, _ := freshFS(t)
	err := fs.WriteFile("/boot/loader.conf", []byte("x"), 0o644)
	if !errors.Is(err, ErrExists) {
		t.Errorf("err = %v, want ErrExists", err)
	}
}

func TestWriteFile_BadParent(t *testing.T) {
	fs, _ := freshFS(t)
	err := fs.WriteFile("/nope/file", []byte("x"), 0o644)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestMkDir_RoundTrip(t *testing.T) {
	fs, m := freshFS(t)
	if err := fs.MkDir("/boot/extra", 0o755); err != nil {
		t.Fatalf("MkDir: %v", err)
	}
	payload := []byte("sub-file\n")
	if err := fs.WriteFile("/boot/extra/note.txt", payload, 0o644); err != nil {
		t.Fatalf("WriteFile in new dir: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	entries, err := ro.ListDir("/boot/extra")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Name()] = true
	}
	for _, want := range []string{".", "..", "note.txt"} {
		if !have[want] {
			t.Errorf("missing %q in /boot/extra: %v", want, have)
		}
	}
	got, _ := ro.ReadFile("/boot/extra/note.txt")
	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch")
	}
}

func TestMkDir_Exists(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.MkDir("/boot", 0o755); !errors.Is(err, ErrExists) {
		t.Errorf("err = %v, want ErrExists", err)
	}
}

func TestDeleteFile(t *testing.T) {
	fs, m := freshFS(t)
	if err := fs.DeleteFile("/boot/loader.conf"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	_, err := ro.ReadFile("/boot/loader.conf")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound after delete", err)
	}
}

func TestDeleteFile_OnDirectory(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.DeleteFile("/boot/kernel"); !errors.Is(err, ErrNotRegular) {
		t.Errorf("err = %v, want ErrNotRegular", err)
	}
}

func TestDeleteDir_Empty(t *testing.T) {
	fs, m := freshFS(t)
	if err := fs.MkDir("/etc/empty", 0o755); err != nil {
		t.Fatalf("mkdir empty: %v", err)
	}
	if err := fs.DeleteDir("/etc/empty"); err != nil {
		t.Fatalf("DeleteDir: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	_, err := ro.ListDir("/etc/empty")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteDir_NotEmpty(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.DeleteDir("/boot"); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("err = %v, want ErrNotEmpty", err)
	}
}

func TestRename_SameDir(t *testing.T) {
	fs, m := freshFS(t)
	if err := fs.Rename("/boot/loader.conf", "/boot/loader.conf.bak"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	if _, err := ro.ReadFile("/boot/loader.conf"); !errors.Is(err, ErrNotFound) {
		t.Errorf("old path still resolves: %v", err)
	}
	got, err := ro.ReadFile("/boot/loader.conf.bak")
	if err != nil {
		t.Fatalf("read renamed: %v", err)
	}
	if !bytes.Equal(got, fxLoaderConf) {
		t.Errorf("payload changed across rename")
	}
}

func TestRename_AcrossDirs(t *testing.T) {
	fs, m := freshFS(t)
	if err := fs.Rename("/etc/fstab", "/boot/fstab.moved"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	got, err := ro.ReadFile("/boot/fstab.moved")
	if err != nil {
		t.Fatalf("read moved: %v", err)
	}
	if !bytes.Equal(got, fxFstab) {
		t.Errorf("moved payload mismatch")
	}
}

func TestRename_TargetExists(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.Rename("/etc/fstab", "/boot/loader.conf"); !errors.Is(err, ErrExists) {
		t.Errorf("err = %v, want ErrExists", err)
	}
}

func TestSymlink(t *testing.T) {
	fs, m := freshFS(t)
	if err := fs.Symlink("/etc/fstab", "/boot/fstab.lnk"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	tgt, err := ro.ReadLink("/boot/fstab.lnk")
	if err != nil {
		t.Fatalf("ReadLink: %v", err)
	}
	if tgt != "/etc/fstab" {
		t.Errorf("target = %q, want /etc/fstab", tgt)
	}
	// Read-through.
	got, err := ro.ReadFile("/boot/fstab.lnk")
	if err != nil {
		t.Fatalf("ReadFile via symlink: %v", err)
	}
	if !bytes.Equal(got, fxFstab) {
		t.Errorf("read-through mismatch")
	}
}

func TestWriteMethods_ReadOnly_StillErr(t *testing.T) {
	// Sanity: opening read-only must still surface ErrReadOnly on
	// mutations.
	fs := openFromFixture(t)
	if err := fs.WriteFile("/x", []byte("y"), 0o644); !errors.Is(err, ErrReadOnly) {
		t.Errorf("WriteFile RO: %v", err)
	}
	if err := fs.MkDir("/x", 0o755); !errors.Is(err, ErrReadOnly) {
		t.Errorf("MkDir RO: %v", err)
	}
	if err := fs.DeleteFile("/x"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("DeleteFile RO: %v", err)
	}
	if err := fs.DeleteDir("/x"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("DeleteDir RO: %v", err)
	}
	if err := fs.Rename("/x", "/y"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Rename RO: %v", err)
	}
	if err := fs.Symlink("/t", "/l"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Symlink RO: %v", err)
	}
}
