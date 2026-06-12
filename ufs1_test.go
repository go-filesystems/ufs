// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"bytes"
	"encoding/binary"
	"testing"

	filesystem "github.com/go-filesystems/interface"
)

// This file exercises the read paths against a hand-built, in-memory
// UFS1 image. No external mkfs tooling is required: we lay out a
// minimal superblock (at SblockUFS1), a 128-byte ufs1_dinode table
// with 32-bit block pointers, a couple of directories, a small regular
// file, an inline ("fast") symlink, and a file large enough to spill
// into a single-indirect block. The geometry mirrors the UFS2 fixture
// in fixture_test.go but uses the UFS1 on-disk format.
//
// Geometry (deliberately tiny so the math is easy to follow):
//
//	Bsize  = 4096   (one fragment == one block, frag == 1)
//	Fsize  = 4096
//	Inopb  = 32      (4096 / 128)
//	Ipg    = 64      (inode table = 64*128 = 8192 B = 2 fragments)
//	Fpg    = 256     (cylinder group spans 1 MiB)
//	Ncg    = 1
//	Nindir = 1024    (4096 / 4 — UFS1 pointers are 32-bit)
//
// On-disk layout within cg 0 (fragment numbers, each 4096 B):
//
//	0..1    boot area (zero-filled)
//	2..3    superblock (SblockUFS1 == 8192 == 2*4096)
//	4       cylinder-group block (left zero; never consulted on read)
//	5..6    inode table (64 inodes * 128 B = 8 KiB)
//	7..     data blocks
const (
	u1Bsize   = 4096
	u1Fsize   = 4096
	u1Frag    = 1
	u1Inopb   = u1Bsize / UFS1InodeSize // 32
	u1Ipg     = 64
	u1Fpg     = 256
	u1Ncg     = 1
	u1Sblkno  = SblockUFS1 / u1Fsize // 2
	u1Cblkno  = 4
	u1Iblkno  = 5
	u1Dblkno  = 7
	u1Fsbtodb = 3 // 4096 / 512 = 8 -> shift 3
	u1Bshift  = 12
	u1Fshift  = 12
	u1Nindir  = u1Bsize / 4 // 1024 pointers per indirect block (32-bit)

	// inode numbers
	u1InoRoot  uint64 = 2
	u1InoEtc   uint64 = 3
	u1InoFstab uint64 = 4
	u1InoLink  uint64 = 5
	u1InoBig   uint64 = 6

	// data fragment assignments (one fragment per dir/small-file body)
	u1FragRoot     = u1Dblkno + 0
	u1FragEtc      = u1Dblkno + 1
	u1FragFstab    = u1Dblkno + 2
	u1FragBigData  = u1Dblkno + 3  // 13 contiguous data blocks: 3..15
	u1FragBigIndir = u1Dblkno + 16 // single-indirect block
)

var (
	u1Fstab   = []byte("/dev/ad0s1a / ufs rw 1 1\n")
	u1BigData = bytesPattern(u1Bsize*13, 0x5C) // 13 blocks -> 1 indirect entry
)

// buildUFS1Fixture returns a freshly-built in-memory UFS1 image.
func buildUFS1Fixture() []byte {
	imgSize := u1Fpg * u1Fsize // 1 MiB
	img := make([]byte, imgSize)
	le := binary.LittleEndian

	// 1) Superblock at SblockUFS1. The struct fs geometry fields share
	//    their offsets with UFS2; only the magic and the inode/pointer
	//    widths differ.
	sb := img[SblockUFS1 : SblockUFS1+1376]
	le.PutUint32(sb[offSblkno:], u1Sblkno)
	le.PutUint32(sb[offCblkno:], u1Cblkno)
	le.PutUint32(sb[offIblkno:], u1Iblkno)
	le.PutUint32(sb[offDblkno:], u1Dblkno)
	le.PutUint32(sb[offNcg:], u1Ncg)
	le.PutUint32(sb[offBsize:], u1Bsize)
	le.PutUint32(sb[offFsize:], u1Fsize)
	le.PutUint32(sb[offFrag:], u1Frag)
	le.PutUint32(sb[offBshift:], u1Bshift)
	le.PutUint32(sb[offFshift:], u1Fshift)
	le.PutUint32(sb[offFsbtodb:], u1Fsbtodb)
	le.PutUint32(sb[offSbsize:], 1376)
	le.PutUint32(sb[offNindir:], u1Nindir)
	le.PutUint32(sb[offInopb:], u1Inopb)
	le.PutUint32(sb[offIpg:], u1Ipg)
	le.PutUint32(sb[offFpg:], u1Fpg)
	le.PutUint32(sb[offFlags:], 0)
	le.PutUint32(sb[offMaxsymlinklen:], ino1ShortlinkLen)
	le.PutUint32(sb[offOldInodefmt:], 2) // FS_44INODEFMT
	le.PutUint64(sb[offMaxfilesize:], 1<<40)
	le.PutUint32(sb[offMagic:], MagicUFS1)

	// 2) Inode table (u1Iblkno..). Inodes 0/1 left zero.
	writeUFS1Inode(img, u1InoRoot, IFDIR|0o755, []uint32{u1FragRoot}, nil, u1DirRoot().size())
	writeUFS1Inode(img, u1InoEtc, IFDIR|0o755, []uint32{u1FragEtc}, nil, u1DirEtc().size())
	writeUFS1Inode(img, u1InoFstab, IFREG|0o644, []uint32{u1FragFstab}, nil, uint64(len(u1Fstab)))
	writeUFS1Symlink(img, u1InoLink, "/etc/fstab")

	bigDirect := make([]uint32, NumDirect)
	for i := range bigDirect {
		bigDirect[i] = uint32(u1FragBigData + i)
	}
	writeUFS1Inode(img, u1InoBig, IFREG|0o644, bigDirect,
		[]uint32{uint32(u1FragBigIndir)}, uint64(len(u1BigData)))

	// 3) Data blocks.
	u1DirRoot().writeTo(img[u1FragRoot*u1Fsize:])
	u1DirEtc().writeTo(img[u1FragEtc*u1Fsize:])
	copy(img[u1FragFstab*u1Fsize:], u1Fstab)

	// Big file: first 12 blocks via direct pointers, 13th via the
	// single-indirect block (a 32-bit pointer at index 0).
	bigOff := 0
	for i := 0; i < NumDirect; i++ {
		copy(img[(u1FragBigData+i)*u1Fsize:], u1BigData[bigOff:bigOff+u1Bsize])
		bigOff += u1Bsize
	}
	le.PutUint32(img[u1FragBigIndir*u1Fsize:], uint32(u1FragBigData+NumDirect))
	copy(img[(u1FragBigData+NumDirect)*u1Fsize:], u1BigData[bigOff:])

	return img
}

// writeUFS1Inode places a 128-byte ufs1_dinode at the canonical offset.
func writeUFS1Inode(img []byte, ino uint64, mode uint16, direct, indirect []uint32, size uint64) {
	off := u1Iblkno*u1Fsize + int(ino)*UFS1InodeSize
	buf := img[off : off+UFS1InodeSize]
	le := binary.LittleEndian
	le.PutUint16(buf[ino1OffMode:], mode)
	le.PutUint16(buf[ino1OffNlink:], 1)
	le.PutUint64(buf[ino1OffSize:], size)
	le.PutUint32(buf[ino1OffUID:], 0)
	le.PutUint32(buf[ino1OffGID:], 0)
	blocks := (size + 511) / 512
	le.PutUint32(buf[ino1OffBlocks:], uint32(blocks))
	for i, frag := range direct {
		if i >= NumDirect {
			break
		}
		le.PutUint32(buf[ino1OffDirect+i*4:], frag)
	}
	for i, frag := range indirect {
		if i >= NumIndirect {
			break
		}
		le.PutUint32(buf[ino1OffIndirect+i*4:], frag)
	}
}

// writeUFS1Symlink writes an inline ("fast") symlink whose target is
// embedded in the di_db[]/di_ib[] area. di_blocks stays zero so the
// reader picks the inline path.
func writeUFS1Symlink(img []byte, ino uint64, target string) {
	off := u1Iblkno*u1Fsize + int(ino)*UFS1InodeSize
	buf := img[off : off+UFS1InodeSize]
	le := binary.LittleEndian
	le.PutUint16(buf[ino1OffMode:], IFLNK|0o777)
	le.PutUint16(buf[ino1OffNlink:], 1)
	le.PutUint64(buf[ino1OffSize:], uint64(len(target)))
	le.PutUint32(buf[ino1OffBlocks:], 0)
	copy(buf[ino1OffShortlink:ino1OffShortlink+ino1ShortlinkLen], target)
}

// u1DirBuilder mirrors dirBuilder but pads to the UFS1 fixture's block
// size. The on-disk dirent format is identical between UFS1 and UFS2.
type u1DirBuilder struct {
	entries []dirEnt
}

func u1NewDir() *u1DirBuilder { return &u1DirBuilder{} }

func (d *u1DirBuilder) add(ino uint32, dtype uint8, name string) {
	d.entries = append(d.entries, dirEnt{
		ino:    ino,
		dtype:  dtype,
		name:   name,
		reclen: uint16(DirentReclen(len(name))),
	})
}

func (d *u1DirBuilder) size() uint64 {
	total := 0
	for _, e := range d.entries {
		total += int(e.reclen)
	}
	if rem := total % u1Bsize; rem != 0 {
		total += u1Bsize - rem
	}
	return uint64(total)
}

func (d *u1DirBuilder) writeTo(buf []byte) {
	cur := 0
	for i, e := range d.entries {
		reclen := e.reclen
		if i == len(d.entries)-1 {
			block := (cur / u1Bsize) * u1Bsize
			reclen = uint16(block + u1Bsize - cur)
		}
		EncodeDirent(buf[cur:cur+int(reclen)], e.ino, e.dtype, e.name, reclen)
		cur += int(reclen)
	}
}

func u1DirRoot() *u1DirBuilder {
	d := u1NewDir()
	d.add(uint32(u1InoRoot), DtDir, ".")
	d.add(uint32(u1InoRoot), DtDir, "..")
	d.add(uint32(u1InoEtc), DtDir, "etc")
	d.add(uint32(u1InoLink), DtLnk, "fstab.link")
	d.add(uint32(u1InoBig), DtReg, "big")
	return d
}

func u1DirEtc() *u1DirBuilder {
	d := u1NewDir()
	d.add(uint32(u1InoEtc), DtDir, ".")
	d.add(uint32(u1InoRoot), DtDir, "..")
	d.add(uint32(u1InoFstab), DtReg, "fstab")
	return d
}

func TestUFS1_ParseSuperblock(t *testing.T) {
	img := buildUFS1Fixture()
	sb, err := ReadSuperblock(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("ReadSuperblock: %v", err)
	}
	if !sb.IsUFS1 {
		t.Fatalf("IsUFS1 = false, want true")
	}
	if sb.Magic != MagicUFS1 {
		t.Errorf("magic = %#x, want %#x", sb.Magic, MagicUFS1)
	}
	if got := sb.dinodeSize(); got != UFS1InodeSize {
		t.Errorf("dinodeSize = %d, want %d", got, UFS1InodeSize)
	}
	if sb.Inopb != u1Inopb {
		t.Errorf("inopb = %d, want %d", sb.Inopb, u1Inopb)
	}
	// InodeOffset must step by 128 bytes, not 256.
	d := sb.InodeOffset(u1InoEtc) - sb.InodeOffset(u1InoRoot)
	if d != UFS1InodeSize {
		t.Errorf("inode stride = %d, want %d", d, UFS1InodeSize)
	}
}

func TestUFS1_ReadInode(t *testing.T) {
	img := buildUFS1Fixture()
	sb, err := ReadSuperblock(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	in, err := ReadInode(bytes.NewReader(img), sb, u1InoRoot)
	if err != nil {
		t.Fatalf("ReadInode root: %v", err)
	}
	if !in.IsDir() {
		t.Errorf("root inode is not a directory: mode=%#o", in.Mode)
	}
	if in.Direct[0] != u1FragRoot {
		t.Errorf("root Direct[0] = %d, want %d", in.Direct[0], u1FragRoot)
	}
	if !in.isUFS1 {
		t.Errorf("decoded inode not flagged as UFS1")
	}
}

func TestUFS1_ReadSmallFile(t *testing.T) {
	img := buildUFS1Fixture()
	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := fs.ReadFile("/etc/fstab")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, u1Fstab) {
		t.Errorf("ReadFile = %q, want %q", got, u1Fstab)
	}
}

func TestUFS1_ListDir(t *testing.T) {
	img := buildUFS1Fixture()
	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ListDir("/")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	want := map[string]bool{".": true, "..": true, "etc": true, "fstab.link": true, "big": true}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("missing dir entry %q (got %v)", name, got)
		}
	}
}

func TestUFS1_ReadLink(t *testing.T) {
	img := buildUFS1Fixture()
	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	target, err := fs.ReadLink("/fstab.link")
	if err != nil {
		t.Fatalf("ReadLink: %v", err)
	}
	if target != "/etc/fstab" {
		t.Errorf("ReadLink = %q, want %q", target, "/etc/fstab")
	}
	// Following the symlink should reach the regular file body.
	body, err := fs.ReadFile("/fstab.link")
	if err != nil {
		t.Fatalf("ReadFile via symlink: %v", err)
	}
	if !bytes.Equal(body, u1Fstab) {
		t.Errorf("symlink-followed body = %q, want %q", body, u1Fstab)
	}
}

func TestUFS1_SingleIndirect(t *testing.T) {
	img := buildUFS1Fixture()
	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := fs.ReadFile("/big")
	if err != nil {
		t.Fatalf("ReadFile big: %v", err)
	}
	if len(got) != len(u1BigData) {
		t.Fatalf("len = %d, want %d", len(got), len(u1BigData))
	}
	if !bytes.Equal(got, u1BigData) {
		t.Errorf("big-file body mismatch (32-bit single-indirect read)")
	}
}

func TestUFS1_Stat(t *testing.T) {
	img := buildUFS1Fixture()
	fs, err := Open(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	st, err := fs.Stat("/etc/fstab")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Size() != uint64(len(u1Fstab)) {
		t.Errorf("size = %d, want %d", st.Size(), len(u1Fstab))
	}
	var _ filesystem.Stat = st
}
