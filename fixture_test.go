// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"encoding/binary"
)

// fixture geometry — kept deliberately small so the in-memory image
// builds in microseconds and the math stays easy to follow.
//
//	Bsize  = 4096   (one fragment == one block, frag==1)
//	Fsize  = 4096
//	Inopb  = 16     (4096 / 256)
//	Ipg    = 32     (two inode blocks)
//	Fpg    = 256    (each cylinder group spans 1 MiB)
//	Ncg    = 1      (one cylinder group is enough for the tests)
//
// On-disk layout within cg 0:
//
//	fragments 0..15   bootstrap area (zero-filled)
//	fragments 16..17  superblock (SblockUFS2 = 65536 == 16*4096)
//	fragment  18      cylinder-group block (we leave it zero-filled,
//	                  the read-only driver never consults it)
//	fragments 24..25  inode table (32 inodes * 256 B = 8 KiB)
//	fragments 26..    data blocks (one fragment per file)
//
// Inode assignments:
//
//	inode 2   /          (root directory)
//	inode 3   /boot      (directory)
//	inode 4   /boot/loader.conf   (regular file)
//	inode 5   /boot/kernel        (directory)
//	inode 6   /boot/kernel/kernel (regular file)
//	inode 7   /etc                (directory)
//	inode 8   /etc/fstab          (regular file)
//	inode 9   /etc/rc.conf        (symlink — inline shortlink)
//	inode 10  /var                (symlink — inline shortlink)
//	inode 11  /big                (regular file > 12 blocks; uses
//	                              single-indirect)
const (
	fxBsize         = 4096
	fxFsize         = 4096
	fxFrag          = 1
	fxInopb         = fxBsize / InodeSize
	fxIpg           = 32
	fxFpg           = 256
	fxNcg           = 1
	fxSblkno        = SblockUFS2 / fxFsize // 16
	fxCblkno        = fxSblkno + 2         // 18
	fxIblkno        = 24
	fxDblkno        = 26
	fxFsbtodb       = 3 // 4096 / 512 = 8 → shift 3
	fxBshift        = 12
	fxFshift        = 12
	fxNindir        = fxBsize / 8 // 512 pointers per indirect block

	// inode numbers
	inoRoot          uint64 = 2
	inoBoot          uint64 = 3
	inoLoaderConf    uint64 = 4
	inoKernelDir     uint64 = 5
	inoKernelFile    uint64 = 6
	inoEtc           uint64 = 7
	inoFstab         uint64 = 8
	inoRcConfLink    uint64 = 9
	inoVarLink       uint64 = 10
	inoBig           uint64 = 11

	// data fragment assignments (one fragment per file/dir
	// payload, indexed from fxDblkno)
	fragRootDir       = fxDblkno + 0
	fragBootDir       = fxDblkno + 1
	fragLoaderConf    = fxDblkno + 2
	fragKernelDir     = fxDblkno + 3
	fragKernelFile    = fxDblkno + 4 // 4 blocks → fragments 4..7
	fragEtcDir        = fxDblkno + 8
	fragFstab         = fxDblkno + 9
	fragBigIndirect   = fxDblkno + 10 // indirect block
	fragBigData       = fxDblkno + 11 // first big-file data block (13 in total)
)

// fxLoaderConf and fxFstab are the regular-file payloads we ship in
// the fixture so the integration test has something it can compare
// against.
var (
	fxLoaderConf = []byte("kernel=\"kernel\"\nautoboot_delay=\"3\"\n")
	fxFstab      = []byte("/dev/ufs/rootfs / ufs rw 1 1\n")
	fxKernelStub = bytesPattern(4096*3+512, 0xA5)
	fxBigData    = bytesPattern(fxBsize*13, 0xC3) // 13 blocks → 1 indirect entry
)

func bytesPattern(n int, b byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// buildFixture returns a freshly-built in-memory UFS2 image and the
// raw byte size of the resulting image.
func buildFixture() []byte {
	imgSize := fxFpg * fxFsize // 1 MiB
	img := make([]byte, imgSize)
	le := binary.LittleEndian

	// 1) Superblock at SblockUFS2.
	sb := img[SblockUFS2 : SblockUFS2+1376]
	le.PutUint32(sb[offSblkno:], fxSblkno)
	le.PutUint32(sb[offCblkno:], fxCblkno)
	le.PutUint32(sb[offIblkno:], fxIblkno)
	le.PutUint32(sb[offDblkno:], fxDblkno)
	le.PutUint32(sb[offNcg:], fxNcg)
	le.PutUint32(sb[offBsize:], fxBsize)
	le.PutUint32(sb[offFsize:], fxFsize)
	le.PutUint32(sb[offFrag:], fxFrag)
	le.PutUint32(sb[offBshift:], fxBshift)
	le.PutUint32(sb[offFshift:], fxFshift)
	le.PutUint32(sb[offFsbtodb:], fxFsbtodb)
	le.PutUint32(sb[offSbsize:], 1376)
	le.PutUint32(sb[offNindir:], fxNindir)
	le.PutUint32(sb[offInopb:], fxInopb)
	le.PutUint32(sb[offIpg:], fxIpg)
	le.PutUint32(sb[offFpg:], fxFpg)
	le.PutUint32(sb[offFlags:], 0)
	le.PutUint32(sb[offMaxsymlinklen:], 120)
	le.PutUint32(sb[offOldInodefmt:], 2)
	le.PutUint64(sb[offMaxfilesize:], 1<<48)
	le.PutUint32(sb[offMagic:], MagicUFS2)

	// 2) Inode table (fxIblkno..). Inode index 0/1 left zero.
	writeInode(img, inoRoot, IFDIR|0o755, []uint64{fragRootDir}, nil, dirRoot(le).size())
	writeInode(img, inoBoot, IFDIR|0o755, []uint64{fragBootDir}, nil, dirBoot(le).size())
	writeInode(img, inoLoaderConf, IFREG|0o644, []uint64{fragLoaderConf}, nil, uint64(len(fxLoaderConf)))
	writeInode(img, inoKernelDir, IFDIR|0o755, []uint64{fragKernelDir}, nil, dirKernel(le).size())
	writeInode(img, inoKernelFile, IFREG|0o644,
		[]uint64{fragKernelFile, fragKernelFile + 1, fragKernelFile + 2, fragKernelFile + 3},
		nil, uint64(len(fxKernelStub)))
	writeInode(img, inoEtc, IFDIR|0o755, []uint64{fragEtcDir}, nil, dirEtc(le).size())
	writeInode(img, inoFstab, IFREG|0o644, []uint64{fragFstab}, nil, uint64(len(fxFstab)))

	// Symlinks (inline shortlinks). The shortlink target lives in
	// the block-pointer area, so we pass nil for direct/indirect
	// and copy the bytes after.
	writeSymlink(img, inoRcConfLink, "/etc/fstab")
	writeSymlink(img, inoVarLink, "/etc/var-target")

	// Big file: 13 blocks. First 12 in direct pointers; 13th via
	// single-indirect.
	bigDirect := make([]uint64, NumDirect)
	for i := range bigDirect {
		bigDirect[i] = uint64(fragBigData + i)
	}
	writeInode(img, inoBig, IFREG|0o644, bigDirect,
		[]uint64{uint64(fragBigIndirect)}, uint64(len(fxBigData)))

	// 3) Data blocks.
	dirRoot(le).writeTo(img[fragRootDir*fxFsize:])
	dirBoot(le).writeTo(img[fragBootDir*fxFsize:])
	dirKernel(le).writeTo(img[fragKernelDir*fxFsize:])
	dirEtc(le).writeTo(img[fragEtcDir*fxFsize:])
	copy(img[fragLoaderConf*fxFsize:], fxLoaderConf)
	copy(img[fragFstab*fxFsize:], fxFstab)
	copy(img[fragKernelFile*fxFsize:], fxKernelStub)

	// Big file: 12 blocks worth of data directly addressed, then
	// the indirect block holds one pointer to the 13th block.
	bigOff := 0
	for i := 0; i < NumDirect; i++ {
		copy(img[(fragBigData+i)*fxFsize:], fxBigData[bigOff:bigOff+fxBsize])
		bigOff += fxBsize
	}
	// 13th block via indirect
	le.PutUint64(img[fragBigIndirect*fxFsize:], uint64(fragBigData+NumDirect))
	copy(img[(fragBigData+NumDirect)*fxFsize:], fxBigData[bigOff:])

	return img
}

// writeInode places a 256-byte ufs2_dinode at the canonical offset.
func writeInode(img []byte, ino uint64, mode uint16, direct []uint64, indirect []uint64, size uint64) {
	off := fxIblkno*fxFsize + int(ino)*InodeSize
	buf := img[off : off+InodeSize]
	le := binary.LittleEndian
	le.PutUint16(buf[inoOffMode:], mode)
	le.PutUint16(buf[inoOffNlink:], 1)
	le.PutUint32(buf[inoOffUID:], 0)
	le.PutUint32(buf[inoOffGID:], 0)
	le.PutUint64(buf[inoOffSize:], size)
	// Approximate "blocks held" as ceil(size/512). Not strictly
	// required by the read-side validator, but lets the Shortlink
	// helper distinguish inline vs spilled targets when in.Blocks
	// > 0 means "data lives in a block".
	blocks := (size + 511) / 512
	le.PutUint64(buf[inoOffBlocks:], blocks)
	for i, frag := range direct {
		if i >= NumDirect {
			break
		}
		le.PutUint64(buf[inoOffDirect+i*8:], frag)
	}
	for i, frag := range indirect {
		if i >= NumIndirect {
			break
		}
		le.PutUint64(buf[inoOffIndirect+i*8:], frag)
	}
}

// writeSymlink writes an inline ("fast") symlink whose target is
// embedded in the inode itself. di_blocks must remain zero so the
// reader picks the inline path.
func writeSymlink(img []byte, ino uint64, target string) {
	off := fxIblkno*fxFsize + int(ino)*InodeSize
	buf := img[off : off+InodeSize]
	le := binary.LittleEndian
	le.PutUint16(buf[inoOffMode:], IFLNK|0o777)
	le.PutUint16(buf[inoOffNlink:], 1)
	le.PutUint64(buf[inoOffSize:], uint64(len(target)))
	le.PutUint64(buf[inoOffBlocks:], 0)
	copy(buf[inoOffShortlink:inoOffShortlink+inoShortlinkLen], target)
}

// dirBuilder accumulates directory entries and serialises them with
// the last entry's reclen padded to the fragment boundary, matching
// what the kernel does on disk.
type dirBuilder struct {
	le      binary.ByteOrder
	entries []dirEnt
}

type dirEnt struct {
	ino    uint32
	dtype  uint8
	name   string
	reclen uint16
}

func newDir(le binary.ByteOrder) *dirBuilder { return &dirBuilder{le: le} }

func (d *dirBuilder) add(ino uint32, dtype uint8, name string) {
	d.entries = append(d.entries, dirEnt{
		ino:    ino,
		dtype:  dtype,
		name:   name,
		reclen: uint16(DirentReclen(len(name))),
	})
}

func (d *dirBuilder) size() uint64 {
	// Pad the last entry's reclen out to a block boundary so the
	// directory file occupies a whole fragment, matching FreeBSD's
	// allocation behaviour.
	total := 0
	for _, e := range d.entries {
		total += int(e.reclen)
	}
	if rem := total % fxBsize; rem != 0 {
		total += fxBsize - rem
	}
	return uint64(total)
}

func (d *dirBuilder) writeTo(buf []byte) {
	cur := 0
	for i, e := range d.entries {
		reclen := e.reclen
		// Last entry absorbs the rest of the block as padding.
		if i == len(d.entries)-1 {
			block := (cur / fxBsize) * fxBsize
			endOfBlock := block + fxBsize
			reclen = uint16(endOfBlock - cur)
		}
		EncodeDirent(buf[cur:cur+int(reclen)], e.ino, e.dtype, e.name, reclen)
		cur += int(reclen)
	}
}

func dirRoot(le binary.ByteOrder) *dirBuilder {
	d := newDir(le)
	d.add(uint32(inoRoot), DtDir, ".")
	d.add(uint32(inoRoot), DtDir, "..")
	d.add(uint32(inoBoot), DtDir, "boot")
	d.add(uint32(inoEtc), DtDir, "etc")
	d.add(uint32(inoVarLink), DtLnk, "var")
	d.add(uint32(inoBig), DtReg, "big")
	return d
}

func dirBoot(le binary.ByteOrder) *dirBuilder {
	d := newDir(le)
	d.add(uint32(inoBoot), DtDir, ".")
	d.add(uint32(inoRoot), DtDir, "..")
	d.add(uint32(inoLoaderConf), DtReg, "loader.conf")
	d.add(uint32(inoKernelDir), DtDir, "kernel")
	return d
}

func dirKernel(le binary.ByteOrder) *dirBuilder {
	d := newDir(le)
	d.add(uint32(inoKernelDir), DtDir, ".")
	d.add(uint32(inoBoot), DtDir, "..")
	d.add(uint32(inoKernelFile), DtReg, "kernel")
	return d
}

func dirEtc(le binary.ByteOrder) *dirBuilder {
	d := newDir(le)
	d.add(uint32(inoEtc), DtDir, ".")
	d.add(uint32(inoRoot), DtDir, "..")
	d.add(uint32(inoFstab), DtReg, "fstab")
	d.add(uint32(inoRcConfLink), DtLnk, "rc.conf")
	return d
}
