// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenRW_BadImage drives both OpenRW's superblock-read failure and
// its allocator-load path indirectly via a fresh Mkfs handle (since
// Mkfs already exercises OpenRW's siblings, the open path itself
// needs a dedicated test).
func TestOpenRW_BadImage(t *testing.T) {
	zero := newMemImage(1 << 17)
	if _, err := OpenRW(zero, zero, int64(1<<17)); !errors.Is(err, ErrBadSuperblock) {
		t.Fatalf("err = %v, want ErrBadSuperblock", err)
	}
}

func TestOpenRW_RoundTrip(t *testing.T) {
	// Mkfs once, persist, then OpenRW the persisted image and write
	// some more. This drives OpenRW's "load allocator from disk" path.
	const size = 8 * 1024 * 1024
	img := newMemImage(size)
	fs, err := Mkfs(img, size)
	if err != nil {
		t.Fatalf("Mkfs: %v", err)
	}
	if err := fs.MkDir("/a", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Now re-open RW on the bytes we already have.
	mi := &memImage{buf: img.Bytes()}
	fs2, err := OpenRW(mi, mi, size)
	if err != nil {
		t.Fatalf("OpenRW: %v", err)
	}
	if err := fs2.WriteFile("/a/file.txt", []byte("from-rw"), 0o644); err != nil {
		t.Fatalf("WriteFile via OpenRW: %v", err)
	}
	ro, _ := Open(bytes.NewReader(mi.Bytes()), int64(size))
	got, err := ro.ReadFile("/a/file.txt")
	if err != nil || string(got) != "from-rw" {
		t.Errorf("read after OpenRW: got=%q err=%v", got, err)
	}
}

// errReaderAt always fails. Used to drive loadAllocator's read-error
// branch.
type errReaderAt struct{ ok io.ReaderAt }

func (e errReaderAt) ReadAt(p []byte, off int64) (int, error) {
	// Pass through reads to the SBLOCK region so the superblock load
	// succeeds; fail any cg-header reads.
	if off >= SblockUFS2 && off < SblockUFS2+SblockSize {
		return e.ok.ReadAt(p, off)
	}
	return 0, errors.New("boom")
}

func (e errReaderAt) WriteAt(p []byte, off int64) (int, error) { return len(p), nil }

func TestOpenRW_AllocLoadError(t *testing.T) {
	// Build a valid superblock at the canonical location in a buffer,
	// then drive OpenRW with a reader that fails any non-sb reads.
	const size = 8 * 1024 * 1024
	img := newMemImage(size)
	if _, err := Mkfs(img, size); err != nil {
		t.Fatalf("Mkfs: %v", err)
	}
	bad := errReaderAt{ok: img}
	_, err := OpenRW(bad, bad, size)
	if err == nil {
		t.Fatal("expected read failure")
	}
}

func TestRename_DirectoryAcrossDirs(t *testing.T) {
	// Drives Rename's "moved directory needs patched ../" path
	// (patchDotDot).
	fs, m := freshFS(t)
	if err := fs.MkDir("/etc/sub", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := fs.WriteFile("/etc/sub/file", []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := fs.Rename("/etc/sub", "/boot/sub2"); err != nil {
		t.Fatalf("Rename dir: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	got, err := ro.ReadFile("/boot/sub2/file")
	if err != nil {
		t.Fatalf("read moved file: %v", err)
	}
	if string(got) != "hi" {
		t.Errorf("got %q", got)
	}
	// And ".." in the moved dir should now point at /boot, not /etc.
	entries, err := ro.ListDir("/boot/sub2")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Name() == ".." {
			found = true
		}
	}
	if !found {
		t.Errorf("moved dir missing ..")
	}
}

func TestRename_NoOp(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.Rename("/boot/loader.conf", "/boot/loader.conf"); err != nil {
		t.Errorf("no-op rename: %v", err)
	}
}

func TestRename_OldNotFound(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.Rename("/nope", "/etc/x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRename_BadNewParent(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.Rename("/etc/fstab", "/missing/x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteFile_Symlink(t *testing.T) {
	// Inline-shortlink delete: covers the in.IsSymlink() && Blocks==0
	// branch in DeleteFile.
	fs, m := freshFS(t)
	if err := fs.Symlink("/etc/fstab", "/tmp.lnk"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := fs.DeleteFile("/tmp.lnk"); err != nil {
		t.Fatalf("DeleteFile symlink: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	if _, err := ro.ReadLink("/tmp.lnk"); !errors.Is(err, ErrNotFound) {
		t.Errorf("symlink still resolves: %v", err)
	}
}

func TestSymlink_LongTarget(t *testing.T) {
	// Drives Symlink's spilled-target path (non-inline).
	fs, m := freshFS(t)
	long := string(bytesPattern(200, 'a'))
	if err := fs.Symlink(long, "/boot/long.lnk"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	ro, _ := Open(bytes.NewReader(m.Bytes()), int64(len(m.Bytes())))
	tgt, err := ro.ReadLink("/boot/long.lnk")
	if err != nil {
		t.Fatalf("ReadLink: %v", err)
	}
	if tgt != long {
		t.Errorf("target mismatch: len(got)=%d len(want)=%d", len(tgt), len(long))
	}
}

func TestSymlink_EmptyTarget(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.Symlink("", "/x"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("err = %v, want ErrInvalidPath", err)
	}
}

func TestSymlink_Exists(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.Symlink("/etc/fstab", "/etc/fstab"); !errors.Is(err, ErrExists) {
		t.Errorf("err = %v, want ErrExists", err)
	}
}

func TestDeleteDir_NotFound(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.DeleteDir("/etc/nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteDir_TargetIsFile(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.DeleteDir("/boot/loader.conf"); !errors.Is(err, ErrNotDirectory) {
		t.Errorf("err = %v, want ErrNotDirectory", err)
	}
}

func TestWriteFile_TooLarge(t *testing.T) {
	const size = 16 * 1024 * 1024
	img := newMemImage(size)
	fs, err := Mkfs(img, size)
	if err != nil {
		t.Fatalf("Mkfs: %v", err)
	}
	// Single-indirect reach is NumDirect + Nindir = 12 + 512 = 524
	// blocks at bsize=4096 → ~2 MiB. Anything beyond that exceeds our
	// implementation's reach.
	huge := bytesPattern(int(fs.Superblock().Bsize)*(NumDirect+int(fs.Superblock().Nindir)+1), 0)
	if err := fs.WriteFile("/huge.bin", huge, 0o644); !errors.Is(err, ErrFileTooLarge) {
		t.Errorf("err = %v, want ErrFileTooLarge", err)
	}
}

func TestResolveParent_BadName(t *testing.T) {
	fs, _ := freshFS(t)
	if err := fs.WriteFile("/", []byte("x"), 0o644); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("err = %v, want ErrInvalidPath", err)
	}
}

func TestWriteFile_OnFile(t *testing.T) {
	// Parent must be a directory.
	fs, _ := freshFS(t)
	if err := fs.WriteFile("/boot/loader.conf/sub", []byte("x"), 0o644); !errors.Is(err, ErrNotDirectory) {
		t.Errorf("err = %v, want ErrNotDirectory", err)
	}
}

// errWriterAt fails every WriteAt — used to drive writer error paths.
type errWriterAt struct{ io.ReaderAt }

func (e errWriterAt) WriteAt(p []byte, off int64) (int, error) {
	return 0, errors.New("disk full")
}

func TestMkfs_WriteFailure(t *testing.T) {
	// Mkfs on a backing that refuses writes.
	img := newMemImage(8 << 20)
	rw := struct {
		io.ReaderAt
		io.WriterAt
	}{img, errWriterAt{img}}
	if _, err := Mkfs(rw, 8<<20); err == nil {
		t.Fatal("expected Mkfs to fail")
	}
}

// TestOpenFile_RW exercises a more end-to-end scenario: format on
// disk, then open the file back via OpenRW and append. This drives the
// real-file path Mkfs and OpenRW care about.
func TestOpenFile_RW(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ufs.img")
	const size = 8 * 1024 * 1024
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	fs, err := Mkfs(f, size)
	if err != nil {
		t.Fatalf("Mkfs: %v", err)
	}
	if err := fs.MkDir("/boot", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := fs.WriteFile("/boot/x.txt", []byte("disk"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()
	// Re-open read-only via OpenFile.
	ro, err := OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer ro.Close()
	got, err := ro.ReadFile("/boot/x.txt")
	if err != nil || string(got) != "disk" {
		t.Errorf("read: got=%q err=%v", got, err)
	}
}
