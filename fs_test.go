// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	filesystem "github.com/go-filesystems/interface"
)

// openFromFixture opens an in-memory fixture as an *FS and returns it.
func openFromFixture(t *testing.T) *FS {
	t.Helper()
	img := buildFixture()
	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatalf("Open fixture: %v", err)
	}
	return fs
}

func TestFS_Open_BadImage(t *testing.T) {
	zero := make([]byte, 1<<17) // > SblockUFS2 but no magic
	_, err := Open(bytes.NewReader(zero), int64(len(zero)))
	if !errors.Is(err, ErrBadSuperblock) {
		t.Fatalf("err = %v, want ErrBadSuperblock", err)
	}
}

func TestFS_ReadFile_Root(t *testing.T) {
	fs := openFromFixture(t)
	got, err := fs.ReadFile("/boot/loader.conf")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, fxLoaderConf) {
		t.Errorf("got %q, want %q", got, fxLoaderConf)
	}
}

func TestFS_ReadFile_DeepPath(t *testing.T) {
	fs := openFromFixture(t)
	got, err := fs.ReadFile("/boot/kernel/kernel")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, fxKernelStub) {
		t.Errorf("kernel stub mismatch (len %d vs %d)", len(got), len(fxKernelStub))
	}
}

func TestFS_ReadFile_FollowsSymlink(t *testing.T) {
	fs := openFromFixture(t)
	// /etc/rc.conf is a symlink → /etc/fstab; ReadFile should follow it.
	got, err := fs.ReadFile("/etc/rc.conf")
	if err != nil {
		t.Fatalf("ReadFile via symlink: %v", err)
	}
	if !bytes.Equal(got, fxFstab) {
		t.Errorf("got %q, want %q", got, fxFstab)
	}
}

func TestFS_ReadFile_NotFound(t *testing.T) {
	fs := openFromFixture(t)
	_, err := fs.ReadFile("/nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFS_ReadFile_NotRegular(t *testing.T) {
	fs := openFromFixture(t)
	_, err := fs.ReadFile("/boot")
	if !errors.Is(err, ErrNotRegular) {
		t.Fatalf("err = %v, want ErrNotRegular", err)
	}
}

func TestFS_ListDir_Root(t *testing.T) {
	fs := openFromFixture(t)
	entries, err := fs.ListDir("/")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	names := dirNames(entries)
	sort.Strings(names)
	want := []string{".", "..", "big", "boot", "etc", "var"}
	if !equalStrings(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestFS_ListDir_NotDirectory(t *testing.T) {
	fs := openFromFixture(t)
	_, err := fs.ListDir("/boot/loader.conf")
	if !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("err = %v, want ErrNotDirectory", err)
	}
}

func TestFS_Stat_Regular(t *testing.T) {
	fs := openFromFixture(t)
	st, err := fs.Stat("/boot/loader.conf")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Mode()&IFMT != IFREG {
		t.Errorf("mode = %#o, want IFREG", st.Mode())
	}
	if st.Size() != uint64(len(fxLoaderConf)) {
		t.Errorf("size = %d, want %d", st.Size(), len(fxLoaderConf))
	}
	if st.Inode() != inoLoaderConf {
		t.Errorf("ino = %d, want %d", st.Inode(), inoLoaderConf)
	}
}

func TestFS_Stat_Symlink_Follows(t *testing.T) {
	fs := openFromFixture(t)
	st, err := fs.Stat("/etc/rc.conf")
	if err != nil {
		t.Fatalf("Stat symlink: %v", err)
	}
	// Stat follows the link, so we expect to see fstab's metadata.
	if st.Size() != uint64(len(fxFstab)) {
		t.Errorf("size = %d, want %d", st.Size(), len(fxFstab))
	}
	if st.Inode() != inoFstab {
		t.Errorf("ino = %d, want %d", st.Inode(), inoFstab)
	}
}

func TestFS_ReadLink(t *testing.T) {
	fs := openFromFixture(t)
	tgt, err := fs.ReadLink("/etc/rc.conf")
	if err != nil {
		t.Fatalf("ReadLink: %v", err)
	}
	if tgt != "/etc/fstab" {
		t.Errorf("target = %q, want /etc/fstab", tgt)
	}
}

func TestFS_ReadLink_NotASymlink(t *testing.T) {
	fs := openFromFixture(t)
	_, err := fs.ReadLink("/boot/loader.conf")
	if !errors.Is(err, ErrNotSymlink) {
		t.Fatalf("err = %v, want ErrNotSymlink", err)
	}
}

func TestFS_WriteMethodsAreReadOnly(t *testing.T) {
	fs := openFromFixture(t)
	if err := fs.WriteFile("/foo", []byte("x"), 0o644); !errors.Is(err, ErrReadOnly) {
		t.Errorf("WriteFile err = %v", err)
	}
	if err := fs.MkDir("/foo", 0o755); !errors.Is(err, ErrReadOnly) {
		t.Errorf("MkDir err = %v", err)
	}
	if err := fs.DeleteFile("/foo"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("DeleteFile err = %v", err)
	}
	if err := fs.DeleteDir("/foo"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("DeleteDir err = %v", err)
	}
	if err := fs.Rename("/a", "/b"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("Rename err = %v", err)
	}
}

func TestFS_Close_NoCloser(t *testing.T) {
	fs := openFromFixture(t)
	// bytes.Reader is not an io.Closer, so Close must be a no-op.
	if err := fs.Close(); err != nil {
		t.Errorf("Close with non-closer reader: %v", err)
	}
}

// closingReader wraps a bytes.Reader with a counting Close so we can
// verify FS.Close forwards to closer correctly.
type closingReader struct {
	*bytes.Reader
	closed bool
}

func (c *closingReader) Close() error {
	c.closed = true
	return nil
}

func TestFS_Close_ForwardsToCloser(t *testing.T) {
	img := buildFixture()
	cr := &closingReader{Reader: bytes.NewReader(img)}
	fs, err := Open(cr, int64(len(img)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !cr.closed {
		t.Error("closer not invoked")
	}
}

func TestFS_OpenFile_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.img")
	if err := os.WriteFile(path, buildFixture(), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	fs, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer fs.Close()
	got, err := fs.ReadFile("/boot/loader.conf")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, fxLoaderConf) {
		t.Error("payload mismatch")
	}
	if fs.Superblock().Magic != MagicUFS2 {
		t.Error("Superblock() returned wrong magic")
	}
}

func TestFS_OpenFile_Missing(t *testing.T) {
	if _, err := OpenFile("/definitely/does/not/exist/xyz"); err == nil {
		t.Error("expected error opening missing file")
	}
}

func TestFS_OpenFile_BadImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.img")
	if err := os.WriteFile(path, make([]byte, 1<<17), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := OpenFile(path)
	if !errors.Is(err, ErrBadSuperblock) {
		t.Fatalf("err = %v, want ErrBadSuperblock", err)
	}
}

func TestFS_RejectsRelativePath(t *testing.T) {
	fs := openFromFixture(t)
	if _, err := fs.ReadFile("boot/loader.conf"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("err = %v, want ErrInvalidPath", err)
	}
}

func TestFS_ReadFile_BigUsesIndirect(t *testing.T) {
	fs := openFromFixture(t)
	got, err := fs.ReadFile("/big")
	if err != nil {
		t.Fatalf("ReadFile /big: %v", err)
	}
	if !bytes.Equal(got, fxBigData) {
		t.Errorf("/big mismatch (len %d vs %d)", len(got), len(fxBigData))
	}
}

func TestFS_FilesystemInterface(t *testing.T) {
	// Compile-time check that FS satisfies the interface; this is
	// also asserted in fs.go but we duplicate in the test so a
	// regression flips a test failure rather than a compile error.
	var _ filesystem.Filesystem = (*FS)(nil)
}

// Helpers

func dirNames(entries []filesystem.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// io.Discard reference to silence the unused import if test set
// changes.
var _ = io.Discard
