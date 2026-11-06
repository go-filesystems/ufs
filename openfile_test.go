// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"errors"
	"io"
	"testing"

	filesystem "github.com/go-filesystems/interface"
)

// bigImage is a UFS2 image holding /big, a file past 64 MiB whose bytes are
// a function of their offset, so that a range read can be checked without
// holding the file.
func bigImage(t testing.TB, fileSize int) (*FS, []byte) {
	t.Helper()
	size := fileSize + 48<<20
	img := newMemImage(size)
	fs, err := Mkfs(img, int64(size))
	if err != nil {
		t.Fatalf("Mkfs: %v", err)
	}
	data := make([]byte, fileSize)
	for i := range data {
		data[i] = byte(i*7 + i>>12)
	}
	if err := fs.WriteFile("/big", data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ro, err := Open(bytes.NewReader(img.Bytes()), int64(size))
	if err != nil {
		t.Fatal(err)
	}
	return ro, data
}

// A READ at an offset past 64 MiB of a file on this driver must not need the
// file whole: OpenFile and ReadAt read the range.
func TestOpenFileReadsARangePast64MiB(t *testing.T) {
	const fileSize = 72 << 20
	fs, want := bigImage(t, fileSize)
	var o filesystem.Opener = fs
	f, err := o.OpenFile("/big")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Size() != fileSize {
		t.Fatalf("Size = %d", f.Size())
	}
	for _, off := range []int64{0, 4095, 64 << 20, 66<<20 + 123, fileSize - 10} {
		buf := make([]byte, 4096)
		n, err := f.ReadAt(buf, off)
		wantN := min(int64(len(buf)), fileSize-off)
		if int64(n) != wantN || (n < len(buf)) != errors.Is(err, io.EOF) || (n == len(buf) && err != nil) {
			t.Fatalf("ReadAt(%d) = %d, %v; want %d", off, n, err, wantN)
		}
		if !bytes.Equal(buf[:n], want[off:off+int64(n)]) {
			t.Fatalf("ReadAt(%d): wrong bytes", off)
		}
	}
	if n, err := f.ReadAt(make([]byte, 8), fileSize); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("ReadAt at the end = %d, %v", n, err)
	}
	if _, err := f.ReadAt(make([]byte, 8), -1); err == nil {
		t.Error("a negative offset was read")
	}
	if n, err := f.ReadAt(nil, 5); n != 0 || err != nil {
		t.Errorf("an empty read = %d, %v", n, err)
	}
}

func TestOpenFileRefusals(t *testing.T) {
	fs, _ := bigImage(t, 1<<20)
	if _, err := fs.OpenFile("/nope"); err == nil {
		t.Error("a missing file was opened")
	}
	if _, err := fs.OpenFile("/"); !errors.Is(err, ErrNotRegular) {
		t.Errorf("a directory: %v", err)
	}
	big := &file{fs: fs, in: &Inode{Size: 1 << 63}}
	if big.Size() != 1<<62 {
		t.Errorf("a crafted size: %d", big.Size())
	}
	if err := big.Close(); err != nil {
		t.Error(err)
	}
	// A failing image fails the read, not the process.
	in := &Inode{Size: 1 << 20}
	in.Direct[0] = 1 // a data block, which the reader cannot give
	bad := &file{fs: &FS{rs: failingReader{}, sb: fs.sb}, in: in}
	if _, err := bad.ReadAt(make([]byte, 16), 0); err == nil {
		t.Error("a failing read was reported as data")
	}
}

type failingReader struct{}

func (failingReader) ReadAt([]byte, int64) (int, error) { return 0, errors.New("image: read failed") }

// BenchmarkReadRangeOfBigFile: a 1 MiB read at 66 MiB of a 72 MiB file, through
// OpenFile/ReadAt and through ReadFile (the whole file, then a slice). The
// allocations are exact; the times are only comparable on a quiet machine.
func BenchmarkReadRangeOfBigFile(b *testing.B) {
	fs, _ := bigImage(b, 72<<20)
	b.Run("OpenFile-ReadAt", func(b *testing.B) {
		f, err := fs.OpenFile("/big")
		if err != nil {
			b.Fatal(err)
		}
		buf := make([]byte, 1<<20)
		b.SetBytes(int64(len(buf)))
		b.ReportAllocs()
		for b.Loop() {
			if _, err := f.ReadAt(buf, 66<<20); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ReadFile-whole", func(b *testing.B) {
		b.SetBytes(1 << 20)
		b.ReportAllocs()
		for b.Loop() {
			data, err := fs.ReadFile("/big")
			if err != nil {
				b.Fatal(err)
			}
			_ = data[66<<20 : 67<<20]
		}
	})
}
