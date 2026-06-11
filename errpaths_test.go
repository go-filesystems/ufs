// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync/atomic"
	"testing"
)

// flakyReader wraps a base ReaderAt and forces an I/O failure once
// the cumulative number of ReadAt calls reaches `failAt`. Used to
// drive the error branches of code paths that wrap io.ReaderAt
// failures.
type flakyReader struct {
	base   io.ReaderAt
	failAt int64
	count  int64
}

func (f *flakyReader) ReadAt(p []byte, off int64) (int, error) {
	n := atomic.AddInt64(&f.count, 1)
	if n >= f.failAt {
		return 0, io.ErrUnexpectedEOF
	}
	return f.base.ReadAt(p, off)
}

func TestReadInode_IOError(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	r := &flakyReader{base: bytes.NewReader(img), failAt: 1}
	if _, err := ReadInode(r, sb, inoRoot); err == nil {
		t.Fatal("expected I/O error")
	}
}

func TestReadFileBody_IOError(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in, _ := ReadInode(bytes.NewReader(img), sb, inoLoaderConf)
	r := &flakyReader{base: bytes.NewReader(img), failAt: 1}
	if _, err := ReadFileBody(r, sb, in, 0, 10); err == nil {
		t.Fatal("expected I/O error from data block read")
	}
}

func TestReadIndirectEntry_OutOfRange(t *testing.T) {
	sb := &Superblock{Nindir: 4, Fsize: 4096}
	if _, err := readIndirectEntry(bytes.NewReader(make([]byte, 1<<14)), sb, 0, 99); err == nil {
		t.Fatal("expected out-of-range error")
	}
	if _, err := readIndirectEntry(bytes.NewReader(make([]byte, 1<<14)), sb, 0, -1); err == nil {
		t.Fatal("expected error for negative index")
	}
}

func TestReadIndirectEntry_IOError(t *testing.T) {
	sb := &Superblock{Nindir: 512, Fsize: 4096}
	r := &flakyReader{base: bytes.NewReader(make([]byte, 1<<14)), failAt: 1}
	if _, err := readIndirectEntry(r, sb, 0, 0); err == nil {
		t.Fatal("expected I/O error")
	}
}

func TestBlockForLBN_Negative(t *testing.T) {
	sb := &Superblock{}
	if _, err := blockForLBN(nil, sb, &Inode{}, -1); err == nil {
		t.Fatal("expected error for negative lbn")
	}
}

func TestBlockForLBN_NindirZero(t *testing.T) {
	sb := &Superblock{Nindir: 0}
	in := &Inode{}
	_, err := blockForLBN(nil, sb, in, int64(NumDirect))
	if !errors.Is(err, ErrUnsupportedIndirect) {
		t.Fatalf("err = %v, want ErrUnsupportedIndirect", err)
	}
}

func TestBlockForLBN_NullIndirect(t *testing.T) {
	sb := &Superblock{Nindir: 512}
	in := &Inode{} // Indirect[0] == 0 → sparse indirect block
	frag, err := blockForLBN(nil, sb, in, int64(NumDirect))
	if err != nil {
		t.Fatalf("blockForLBN: %v", err)
	}
	if frag != 0 {
		t.Errorf("frag = %d, want 0 (sparse)", frag)
	}
}

func TestReadFile_DirReadIOError(t *testing.T) {
	// Drive an I/O failure on the second ReadAt — the first reads
	// the root inode (succeeds), the second reads root dir data
	// (fails), so ReadFile must surface the error.
	img := buildFixture()
	fs, _ := Open(bytes.NewReader(img), int64(len(img)))
	r := &flakyReader{base: bytes.NewReader(img), failAt: 2}
	fs.rs = r
	if _, err := fs.ReadFile("/boot"); err == nil {
		t.Fatal("expected I/O error")
	}
}

func TestListDir_IOError(t *testing.T) {
	img := buildFixture()
	fs, _ := Open(bytes.NewReader(img), int64(len(img)))
	fs.rs = &flakyReader{base: bytes.NewReader(img), failAt: 1}
	if _, err := fs.ListDir("/"); err == nil {
		t.Fatal("expected I/O error")
	}
}

func TestStat_IOError(t *testing.T) {
	img := buildFixture()
	fs, _ := Open(bytes.NewReader(img), int64(len(img)))
	fs.rs = &flakyReader{base: bytes.NewReader(img), failAt: 1}
	if _, err := fs.Stat("/"); err == nil {
		t.Fatal("expected I/O error")
	}
}

func TestReadLink_IOError(t *testing.T) {
	img := buildFixture()
	fs, _ := Open(bytes.NewReader(img), int64(len(img)))
	fs.rs = &flakyReader{base: bytes.NewReader(img), failAt: 1}
	if _, err := fs.ReadLink("/etc/rc.conf"); err == nil {
		t.Fatal("expected I/O error")
	}
}

func TestReadSymlinkTarget_Spilled(t *testing.T) {
	// Build an inode whose target is "spilled" into the loader.conf
	// data block (so ReadFileAll returns the spilled bytes). We
	// re-use the existing fragment to avoid carving a new one.
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in := &Inode{
		Mode:   IFLNK | 0o777,
		Size:   uint64(len(fxLoaderConf)),
		Blocks: 1,
	}
	in.Direct[0] = fragLoaderConf
	target, err := readSymlinkTarget(bytes.NewReader(img), sb, in)
	if err != nil {
		t.Fatalf("readSymlinkTarget: %v", err)
	}
	if []byte(target)[0] != fxLoaderConf[0] {
		t.Errorf("spilled target = %q, want prefix %q", target, fxLoaderConf)
	}
}

func TestReadSymlinkTarget_NotSymlink(t *testing.T) {
	_, err := readSymlinkTarget(nil, &Superblock{}, &Inode{Mode: IFREG | 0o644})
	if !errors.Is(err, ErrNotSymlink) {
		t.Fatalf("err = %v, want ErrNotSymlink", err)
	}
}

func TestParseDirents_TrailingPartial(t *testing.T) {
	// First record: valid 16-byte entry. Then 8 bytes whose reclen
	// claims to extend past the buffer — must stop cleanly.
	buf := make([]byte, 24)
	le := binary.LittleEndian
	le.PutUint32(buf[0:], 7)
	le.PutUint16(buf[4:], 16)
	buf[6] = DtReg
	buf[7] = 1
	buf[8] = 'a'
	le.PutUint32(buf[16:], 9)
	le.PutUint16(buf[20:], 64) // overruns buffer
	buf[22] = DtReg
	buf[23] = 1
	entries, err := ParseDirents(buf)
	if err != nil {
		t.Fatalf("ParseDirents: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
}

func TestResolve_TooManySymlinks(t *testing.T) {
	// Build an image with a symlink loop: /loop -> /loop. Construct
	// a synthetic image by patching the fixture's /var symlink to
	// point at itself.
	img := buildFixture()
	off := fxIblkno*fxFsize + int(inoVarLink)*InodeSize
	buf := img[off : off+InodeSize]
	// Overwrite the inline target to "/var" (4 bytes) — root has
	// a "var" entry that points to this inode, creating a loop.
	for i := inoOffShortlink; i < inoOffShortlink+inoShortlinkLen; i++ {
		buf[i] = 0
	}
	copy(buf[inoOffShortlink:], "/var")
	binary.LittleEndian.PutUint64(buf[inoOffSize:], 4)
	binary.LittleEndian.PutUint64(buf[inoOffBlocks:], 0)
	fs, _ := Open(bytes.NewReader(img), int64(len(img)))
	_, err := fs.ReadFile("/var")
	if !errors.Is(err, ErrTooManyLinks) {
		t.Fatalf("err = %v, want ErrTooManyLinks", err)
	}
}

func TestSplitPath_RootDotPath(t *testing.T) {
	if got, _ := splitPath("."); got != nil {
		t.Errorf("splitPath('.') = %v, want nil", got)
	}
}

// corruptingReader wraps a backing image and zeroes out specific
// directory data at read time so ParseDirents fails with a corrupt
// reclen. Lets us cover the dirent-error branches of resolve/ListDir.
type corruptingReader struct {
	base       io.ReaderAt
	corruptOff int64
	corruptLen int
}

func (c *corruptingReader) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.base.ReadAt(p, off)
	// If this read intersects the corrupt region, scribble a
	// reclen=0 word to trip ParseDirents.
	if c.corruptLen > 0 {
		end := off + int64(n)
		cend := c.corruptOff + int64(c.corruptLen)
		if off < cend && end > c.corruptOff {
			// Zero the d_ino + reclen word at the start of
			// the directory block. ParseDirents rejects
			// reclen < 8 with an explicit error.
			lo := c.corruptOff - off
			if lo < 0 {
				lo = 0
			}
			hi := lo + 8
			if hi > int64(n) {
				hi = int64(n)
			}
			for i := lo; i < hi; i++ {
				p[i] = 0
			}
		}
	}
	return n, err
}

func TestListDir_DirentParseError(t *testing.T) {
	img := buildFixture()
	fs, _ := Open(bytes.NewReader(img), int64(len(img)))
	// Corrupt the root directory's data block to produce an invalid
	// reclen during ParseDirents.
	fs.rs = &corruptingReader{
		base:       bytes.NewReader(img),
		corruptOff: int64(fragRootDir * fxFsize),
		corruptLen: 8,
	}
	if _, err := fs.ListDir("/"); err == nil {
		t.Fatal("expected dirent-parse error")
	}
}

func TestResolve_DirentParseError(t *testing.T) {
	img := buildFixture()
	rs := &corruptingReader{
		base:       bytes.NewReader(img),
		corruptOff: int64(fragRootDir * fxFsize),
		corruptLen: 8,
	}
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	if _, _, err := resolve(rs, sb, "/boot", true); err == nil {
		t.Fatal("expected error from corrupted root dirents")
	}
}

func TestReadSymlinkTarget_SpilledIOError(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in := &Inode{
		Mode:   IFLNK | 0o777,
		Size:   uint64(len(fxLoaderConf)),
		Blocks: 1,
	}
	in.Direct[0] = fragLoaderConf
	r := &flakyReader{base: bytes.NewReader(img), failAt: 1}
	if _, err := readSymlinkTarget(r, sb, in); err == nil {
		t.Fatal("expected I/O error reading spilled symlink")
	}
}

func TestReadFileBody_OffsetClipsCount(t *testing.T) {
	img := buildFixture()
	sb, _ := parseSuperblock(img[SblockUFS2 : SblockUFS2+1376])
	in, _ := ReadInode(bytes.NewReader(img), sb, inoLoaderConf)
	// Ask for more bytes than remain; ReadFileBody must clip n
	// down to (Size - off) without returning an error.
	data, err := ReadFileBody(bytes.NewReader(img), sb, in, 0, int(in.Size)+1024)
	if err != nil {
		t.Fatalf("ReadFileBody: %v", err)
	}
	if len(data) != int(in.Size) {
		t.Errorf("got %d bytes, want %d", len(data), in.Size)
	}
}
