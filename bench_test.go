// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs_test

// Performance benchmarks for the standard filesystem operations, exercised
// through the public filesystem.Filesystem interface. They establish a
// throughput baseline for the pure-Go UFS2 driver across the format, write,
// read, lookup and directory paths.
//
// Run:  GOWORK=off go test -bench=. -benchmem -run='^$'
//
// A file-backed image under b.TempDir() is used so the numbers include real
// block I/O, not just in-memory work. UFS2 images are minted with Mkfs over
// an *os.File (which satisfies io.ReaderAt + io.WriterAt); the returned *FS
// is open for read+write and implements filesystem.Filesystem.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	filesystem "github.com/go-filesystems/interface"
	ufs "github.com/go-filesystems/ufs"
)

const (
	benchImageSize = int64(64 << 20) // 64 MiB — room for a large file + many inodes
	benchBigFile   = 8 << 20         // 8 MiB sequential payload
)

// newBenchImage creates a fresh, zeroed file-backed image of benchImageSize
// under b.TempDir() and returns it open. The caller owns the *os.File.
func newBenchImage(b *testing.B) *os.File {
	b.Helper()
	path := filepath.Join(b.TempDir(), fmt.Sprintf("bench-%d.img", b.N))
	f, err := os.Create(path)
	if err != nil {
		b.Fatalf("create image: %v", err)
	}
	if err := f.Truncate(benchImageSize); err != nil {
		f.Close()
		b.Fatalf("truncate image: %v", err)
	}
	return f
}

// newBenchFS formats a fresh file-backed UFS2 image and returns the mounted,
// read+write filesystem. The backing file is closed when fs.Close runs.
func newBenchFS(b *testing.B) filesystem.Filesystem {
	b.Helper()
	f := newBenchImage(b)
	fs, err := ufs.Mkfs(f, benchImageSize)
	if err != nil {
		f.Close()
		b.Fatalf("Mkfs: %v", err)
	}
	return &closingFS{FS: fs, f: f}
}

// closingFS pairs an *ufs.FS with the backing file so Close releases both.
// (ufs.Mkfs does not take ownership of a caller-supplied io.WriterAt.)
type closingFS struct {
	*ufs.FS
	f *os.File
}

func (c *closingFS) Close() error {
	err := c.FS.Close()
	if cerr := c.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// BenchmarkFormat measures the cost of laying down a fresh filesystem.
func BenchmarkFormat(b *testing.B) {
	dir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := filepath.Join(dir, fmt.Sprintf("fmt-%d.img", i))
		f, err := os.Create(path)
		if err != nil {
			b.Fatalf("create: %v", err)
		}
		if err := f.Truncate(benchImageSize); err != nil {
			b.Fatalf("truncate: %v", err)
		}
		fs, err := ufs.Mkfs(f, benchImageSize)
		if err != nil {
			b.Fatalf("Mkfs: %v", err)
		}
		fs.Close()
		f.Close()
	}
}

// BenchmarkWriteFileSeq measures sequential write throughput of a large file.
//
// UFS WriteFile does not overwrite an existing path, so each iteration writes
// to a fresh name and the previous file is unlinked outside the timer to keep
// the (fixed-size) image from filling up.
func BenchmarkWriteFileSeq(b *testing.B) {
	fs := newBenchFS(b)
	defer fs.Close()
	data := make([]byte, benchBigFile)
	for i := range data {
		data[i] = byte(i)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := fmt.Sprintf("/big-%d.bin", i)
		if err := fs.WriteFile(path, data, 0o644); err != nil {
			b.Fatalf("WriteFile: %v", err)
		}
		b.StopTimer()
		if err := fs.DeleteFile(path); err != nil {
			b.Fatalf("cleanup DeleteFile: %v", err)
		}
		b.StartTimer()
	}
}

// BenchmarkReadFileSeq measures sequential read throughput of a large file.
func BenchmarkReadFileSeq(b *testing.B) {
	fs := newBenchFS(b)
	defer fs.Close()
	data := make([]byte, benchBigFile)
	if err := fs.WriteFile("/big.bin", data, 0o644); err != nil {
		b.Fatalf("setup WriteFile: %v", err)
	}
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := fs.ReadFile("/big.bin")
		if err != nil {
			b.Fatalf("ReadFile: %v", err)
		}
		if len(got) != len(data) {
			b.Fatalf("short read: %d", len(got))
		}
	}
}

// BenchmarkStat measures path lookup + inode read latency.
func BenchmarkStat(b *testing.B) {
	fs := newBenchFS(b)
	defer fs.Close()
	for _, d := range []string{"/a", "/a/b", "/a/b/c"} {
		if err := fs.MkDir(d, 0o755); err != nil {
			b.Fatalf("MkDir %s: %v", d, err)
		}
	}
	if err := fs.WriteFile("/a/b/c/file.txt", []byte("x"), 0o644); err != nil {
		b.Fatalf("setup WriteFile: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := fs.Stat("/a/b/c/file.txt"); err != nil {
			b.Fatalf("Stat: %v", err)
		}
	}
}

// BenchmarkListDir measures directory enumeration for a directory holding
// many entries.
func BenchmarkListDir(b *testing.B) {
	fs := newBenchFS(b)
	defer fs.Close()
	const entries = 200
	if err := fs.MkDir("/d", 0o755); err != nil {
		b.Fatalf("MkDir: %v", err)
	}
	for i := 0; i < entries; i++ {
		if err := fs.WriteFile(fmt.Sprintf("/d/f%04d", i), nil, 0o644); err != nil {
			b.Fatalf("setup file %d: %v", i, err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := fs.ListDir("/d")
		if err != nil {
			b.Fatalf("ListDir: %v", err)
		}
		if len(got) < entries {
			b.Fatalf("ListDir returned %d entries", len(got))
		}
	}
}

// BenchmarkCreateFiles measures small-file creation throughput. Each iteration
// creates a fixed batch on a freshly formatted image (setup excluded from the
// timer) and reports files/op.
func BenchmarkCreateFiles(b *testing.B) {
	const batch = 200
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		fs := newBenchFS(b)
		b.StartTimer()
		for j := 0; j < batch; j++ {
			if err := fs.WriteFile(fmt.Sprintf("/f%05d", j), nil, 0o644); err != nil {
				b.Fatalf("WriteFile %d: %v", j, err)
			}
		}
		b.StopTimer()
		fs.Close()
		b.StartTimer()
	}
	b.ReportMetric(200, "files/op")
}

// BenchmarkDeleteFiles measures unlink throughput.
func BenchmarkDeleteFiles(b *testing.B) {
	const batch = 200
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		fs := newBenchFS(b)
		for j := 0; j < batch; j++ {
			if err := fs.WriteFile(fmt.Sprintf("/f%05d", j), nil, 0o644); err != nil {
				b.Fatalf("setup WriteFile %d: %v", j, err)
			}
		}
		b.StartTimer()
		for j := 0; j < batch; j++ {
			if err := fs.DeleteFile(fmt.Sprintf("/f%05d", j)); err != nil {
				b.Fatalf("DeleteFile %d: %v", j, err)
			}
		}
		b.StopTimer()
		fs.Close()
		b.StartTimer()
	}
	b.ReportMetric(200, "files/op")
}
