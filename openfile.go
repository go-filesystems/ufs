// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"fmt"
	"io"

	filesystem "github.com/go-filesystems/interface"
)

// Verify the package offers positional reads (filesystem.Opener), so that a
// server reads a range of a file instead of the whole of it for each request.
var _ filesystem.Opener = (*FS)(nil)

// OpenFile opens the regular file at path for positional reads. Symlinks are
// followed as in ReadFile. Nothing of the file is read here: each ReadAt reads
// the blocks of its own range (ReadFileBody), so a read of a few bytes of a
// multi-gigabyte file costs a few blocks, not the file. The File is read-only
// and safe for concurrent use as far as the filesystem's io.ReaderAt is.
func (fs *FS) OpenFile(path string) (filesystem.File, error) {
	_, in, err := resolve(fs.rs, fs.sb, path, true)
	if err != nil {
		return nil, err
	}
	if !in.IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNotRegular, path)
	}
	return &file{fs: fs, in: in}, nil
}

type file struct {
	fs *FS
	in *Inode
}

func (f *file) Size() int64 {
	if f.in.Size > 1<<62 { // a crafted size is no size
		return 1 << 62
	}
	return int64(f.in.Size)
}

// ReadAt implements io.ReaderAt: it fills p from off, and says io.EOF with the
// bytes there are when the range runs past the end of the file.
func (f *file) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fmt.Errorf("ufs: negative offset %d", off)
	}
	if len(p) == 0 {
		return 0, nil
	}
	data, err := ReadFileBody(f.fs.rs, f.fs.sb, f.in, off, len(p))
	if err != nil {
		return 0, err
	}
	n := copy(p, data)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *file) Close() error { return nil }
