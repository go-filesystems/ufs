// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"fmt"
	"io"
	"os"

	filesystem "github.com/go-filesystems/interface"
)

// FS is an opened, read-only UFS2 filesystem.
//
// The struct deliberately holds only an io.ReaderAt plus the decoded
// superblock. All on-disk traversal is recomputed from the
// superblock geometry on every call — no caches, no locks, no
// background goroutines. That keeps the driver trivially safe for
// concurrent readers and trivially correct against a frozen image
// (the only environment a read-only client cares about).
type FS struct {
	rs     io.ReaderAt
	wa     io.WriterAt // optional; non-nil only when opened via OpenRW or Mkfs
	size   int64
	sb     *Superblock
	closer io.Closer // optional; nil when the caller supplied a bare ReaderAt

	// alloc is the live cylinder-group allocator. nil for read-only
	// handles; loaded by OpenRW / Mkfs.
	alloc *allocator
}

// Verify the package satisfies the common filesystem interface.
var _ filesystem.Filesystem = (*FS)(nil)

// Open parses the UFS2 superblock at SblockUFS2 and returns a ready
// filesystem handle. The caller retains ownership of rs unless they
// pass a value that also implements io.Closer — in that case Close
// will be forwarded.
//
// `size` is the total addressable byte size of the backing image. It
// is currently only used for diagnostics; pass -1 if unknown.
func Open(rs io.ReaderAt, size int64) (*FS, error) {
	sb, err := ReadSuperblock(rs)
	if err != nil {
		return nil, err
	}
	fs := &FS{rs: rs, size: size, sb: sb}
	if c, ok := rs.(io.Closer); ok {
		fs.closer = c
	}
	return fs, nil
}

// OpenFile is a convenience constructor that opens `path` read-only
// and wires it into Open. The returned FS owns the file handle and
// will close it on Close.
func OpenFile(path string) (*FS, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ufs: open %s: %w", path, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("ufs: stat %s: %w", path, err)
	}
	fs, err := Open(f, st.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	// Open above will have stored f as its closer because *os.File
	// satisfies io.Closer; nothing else needed.
	return fs, nil
}

// Superblock returns a pointer to the decoded superblock so callers
// (e.g. EFI_SIMPLE_FILE_SYSTEM_PROTOCOL plumbing) can introspect the
// on-disk geometry without re-reading it. The returned pointer is
// owned by FS; do not mutate.
func (fs *FS) Superblock() *Superblock { return fs.sb }

// Close releases the backing file handle if FS opened one. Calling
// Close on a filesystem built from a caller-owned io.ReaderAt is a
// no-op; the caller stays responsible for releasing their handle.
func (fs *FS) Close() error {
	if fs.closer != nil {
		return fs.closer.Close()
	}
	return nil
}

// ReadFile loads the entire contents of the regular file at `path`.
// Symlinks along the path are followed transparently; if `path`
// itself resolves to a symlink, the link is followed too.
func (fs *FS) ReadFile(path string) ([]byte, error) {
	_, in, err := resolve(fs.rs, fs.sb, path, true)
	if err != nil {
		return nil, err
	}
	if !in.IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	return ReadFileAll(fs.rs, fs.sb, in)
}

// ListDir enumerates the entries of the directory at `path`. The
// special "." and ".." entries are included as the on-disk format
// stores them; callers that want a POSIX-clean view should filter
// them out.
func (fs *FS) ListDir(path string) ([]filesystem.DirEntry, error) {
	_, in, err := resolve(fs.rs, fs.sb, path, true)
	if err != nil {
		return nil, err
	}
	if !in.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotDirectory, path)
	}
	data, err := ReadFileAll(fs.rs, fs.sb, in)
	if err != nil {
		return nil, err
	}
	raw, err := ParseDirents(data)
	if err != nil {
		return nil, err
	}
	out := make([]filesystem.DirEntry, 0, len(raw))
	for _, e := range raw {
		out = append(out, filesystem.NewDirEntry(uint64(e.Ino), e.Name, e.Type))
	}
	return out, nil
}

// Stat resolves `path` and returns a Stat carrying mode, size and
// inode number. Symlinks are followed.
func (fs *FS) Stat(path string) (filesystem.Stat, error) {
	ino, in, err := resolve(fs.rs, fs.sb, path, true)
	if err != nil {
		return nil, err
	}
	return filesystem.NewStat(in.Mode, in.Size, ino), nil
}

// ReadLink returns the target string of the symbolic link at `path`.
// The path's last component is NOT followed.
func (fs *FS) ReadLink(path string) (string, error) {
	_, in, err := resolve(fs.rs, fs.sb, path, false)
	if err != nil {
		return "", err
	}
	if !in.IsSymlink() {
		return "", fmt.Errorf("%w: %s", ErrNotSymlink, path)
	}
	return readSymlinkTarget(fs.rs, fs.sb, in)
}

// OpenRW parses the superblock at SblockUFS2 from `rs`, loads the
// cylinder-group allocator, and wires the writer side through `wa`.
// Use this when the caller wants both read and write access — e.g.
// mutating an existing UFS2 image. The caller retains ownership of
// the backing handle unless it satisfies io.Closer (in which case
// Close will be forwarded).
func OpenRW(rs io.ReaderAt, wa io.WriterAt, size int64) (*FS, error) {
	sb, err := ReadSuperblock(rs)
	if err != nil {
		return nil, err
	}
	fs := &FS{rs: rs, wa: wa, size: size, sb: sb}
	if c, ok := rs.(io.Closer); ok {
		fs.closer = c
	}
	a, err := loadAllocator(fs)
	if err != nil {
		return nil, err
	}
	fs.alloc = a
	return fs, nil
}
