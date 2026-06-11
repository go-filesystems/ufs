// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"encoding/binary"
	"fmt"
	"os"
	"path"
	"strings"
	"time"
)

// writeInodeRaw flushes the given 256-byte inode buffer to its
// canonical on-disk slot. Caller must hold the inode contents in the
// 256-byte `buf` already (typically via newInode or by mutating an
// inode loaded with ReadInode + .Raw).
func (fs *FS) writeInodeRaw(ino uint64, buf []byte) error {
	if fs.wa == nil {
		return ErrReadOnly
	}
	if len(buf) != InodeSize {
		return fmt.Errorf("ufs: bad inode buffer length %d", len(buf))
	}
	if _, err := fs.wa.WriteAt(buf, fs.sb.InodeOffset(ino)); err != nil {
		return fmt.Errorf("ufs: write inode %d: %w", ino, err)
	}
	return nil
}

// writeInode serialises an in-memory *Inode back to disk, refreshing
// only the fields the writer mutates (mode/nlink/size/blocks/direct/
// indirect/timestamps). The Raw byte slot is updated in lock-step so
// subsequent reads see the new state.
func (fs *FS) writeInode(ino uint64, in *Inode) error {
	le := binary.LittleEndian
	buf := in.Raw[:]
	le.PutUint16(buf[inoOffMode:], in.Mode)
	le.PutUint16(buf[inoOffNlink:], in.Nlink)
	le.PutUint32(buf[inoOffUID:], in.UID)
	le.PutUint32(buf[inoOffGID:], in.GID)
	le.PutUint64(buf[inoOffSize:], in.Size)
	le.PutUint64(buf[inoOffBlocks:], in.Blocks)
	le.PutUint32(buf[inoOffFlags:], in.Flags)
	le.PutUint32(buf[inoOffExtsize:], in.Extsize)
	for i := 0; i < NumDirect; i++ {
		le.PutUint64(buf[inoOffDirect+i*8:], in.Direct[i])
	}
	for i := 0; i < NumIndirect; i++ {
		le.PutUint64(buf[inoOffIndirect+i*8:], in.Indirect[i])
	}
	return fs.writeInodeRaw(ino, buf)
}

// newInode constructs a fresh *Inode of the given mode (file-type bits
// included), stamps mtime/ctime/atime/birthtime to "now", and clears
// all block pointers. The Raw slot is also zeroed so the serialised
// form has no stale bytes.
func newInode(mode uint16) *Inode {
	in := &Inode{
		Mode:  mode,
		Nlink: 1,
	}
	// Stamp ufs_time_t (int64 seconds + int32 nanoseconds, 8 bytes
	// seconds + 4 bytes nsec, on UFS2). The Raw slot holds the
	// canonical bytes; the decoded Inode struct doesn't model
	// timestamps so we splat them directly into Raw here.
	now := time.Now()
	stampTime(in.Raw[inoOffAtime:], now)
	stampTime(in.Raw[inoOffMtime:], now)
	stampTime(in.Raw[inoOffCtime:], now)
	stampTime(in.Raw[inoOffBirth:], now)
	return in
}

// stampTime writes a ufs_time_t (int64 sec + int32 nsec) at the front
// of `slot`. Slot length is assumed >= 12.
func stampTime(slot []byte, t time.Time) {
	le := binary.LittleEndian
	le.PutUint64(slot[0:], uint64(t.Unix()))
	le.PutUint32(slot[8:], uint32(t.Nanosecond()))
}

// writeFileData writes `data` into the file inode `in`, allocating
// fresh blocks as needed. The file's existing block pointers are
// freed first; we always start from a clean slate. di_size, di_blocks,
// and the direct/indirect pointers are updated. The caller still has
// to call fs.writeInode(ino, in) to persist them.
func (fs *FS) writeFileData(in *Inode, data []byte) error {
	// 1) Free any existing blocks.
	if err := fs.freeFileBlocks(in); err != nil {
		return err
	}

	bsize := int(fs.sb.Bsize)
	if len(data) == 0 {
		in.Size = 0
		in.Blocks = 0
		return nil
	}
	// Cap at direct + single-indirect reach.
	maxLBN := NumDirect + int(fs.sb.Nindir)
	totalBlocks := (len(data) + bsize - 1) / bsize
	if totalBlocks > maxLBN {
		return fmt.Errorf("%w: needs %d blocks", ErrFileTooLarge, totalBlocks)
	}

	frag := int(fs.sb.Frag)
	for lbn := 0; lbn < totalBlocks; lbn++ {
		// Allocate a full block (frag fragments) per LBN.
		fragNum, err := fs.alloc.allocBlock(frag)
		if err != nil {
			return err
		}
		// Write the data slice for this LBN; pad short tail with
		// zeros up to the block boundary (the rest of the fragment).
		start := lbn * bsize
		end := start + bsize
		if end > len(data) {
			end = len(data)
		}
		chunk := make([]byte, bsize)
		copy(chunk, data[start:end])
		if _, err := fs.wa.WriteAt(chunk, fs.sb.FragOffset(fragNum)); err != nil {
			return fmt.Errorf("ufs: write data lbn %d: %w", lbn, err)
		}
		if lbn < NumDirect {
			in.Direct[lbn] = fragNum
			continue
		}
		// Single-indirect: lazy-allocate the indirect block on first
		// over-direct LBN.
		if in.Indirect[0] == 0 {
			ind, err := fs.alloc.allocBlock(frag)
			if err != nil {
				return err
			}
			// Zero-fill the indirect block so unused slots read as 0
			// (sparse).
			if _, err := fs.wa.WriteAt(make([]byte, bsize), fs.sb.FragOffset(ind)); err != nil {
				return fmt.Errorf("ufs: zero indirect: %w", err)
			}
			in.Indirect[0] = ind
		}
		// Write one entry into the indirect block.
		entryIdx := lbn - NumDirect
		var entry [8]byte
		binary.LittleEndian.PutUint64(entry[:], fragNum)
		off := fs.sb.FragOffset(in.Indirect[0]) + int64(entryIdx)*8
		if _, err := fs.wa.WriteAt(entry[:], off); err != nil {
			return fmt.Errorf("ufs: write indirect entry: %w", err)
		}
	}
	in.Size = uint64(len(data))
	// di_blocks counts 512-byte sectors. Every allocated full block is
	// (bsize/512) sectors; the indirect block (if any) is also one
	// full block.
	sectorsPerBlock := uint64(bsize / 512)
	in.Blocks = uint64(totalBlocks) * sectorsPerBlock
	if totalBlocks > NumDirect {
		in.Blocks += sectorsPerBlock
	}
	return nil
}

// freeFileBlocks releases every fragment currently referenced by `in`
// and clears the pointers. Indirect blocks themselves are also freed.
func (fs *FS) freeFileBlocks(in *Inode) error {
	frag := int(fs.sb.Frag)
	for i := 0; i < NumDirect; i++ {
		if in.Direct[i] != 0 {
			if err := fs.alloc.freeBlock(in.Direct[i], frag); err != nil {
				return err
			}
			in.Direct[i] = 0
		}
	}
	if in.Indirect[0] != 0 {
		// Walk the indirect block, freeing every non-zero entry.
		bsize := int(fs.sb.Bsize)
		buf := make([]byte, bsize)
		if _, err := fs.rs.ReadAt(buf, fs.sb.FragOffset(in.Indirect[0])); err != nil {
			return fmt.Errorf("ufs: read indirect for free: %w", err)
		}
		for off := 0; off+8 <= bsize; off += 8 {
			ent := binary.LittleEndian.Uint64(buf[off:])
			if ent != 0 {
				if err := fs.alloc.freeBlock(ent, frag); err != nil {
					return err
				}
			}
		}
		if err := fs.alloc.freeBlock(in.Indirect[0], frag); err != nil {
			return err
		}
		in.Indirect[0] = 0
	}
	// Indirect[1] and [2] are intentionally ignored — the writer
	// doesn't allocate them, so they should always be zero on a
	// file we created ourselves.
	return nil
}

// dirAppendEntry appends a fresh dirent (ino, dtype, name) into the
// directory inode `dirIno`. The directory body is read, the new
// record is spliced in, the last record's reclen is extended to span
// the trailing free space (matching UFS on-disk convention), and the
// updated body is written back. If the new record won't fit, a fresh
// block is allocated and appended.
func (fs *FS) dirAppendEntry(dirIno uint64, dir *Inode, name string, ino uint64, dtype uint8) error {
	if len(name) == 0 || len(name) > 255 {
		return fmt.Errorf("%w: bad name length %d", ErrInvalidPath, len(name))
	}
	body, err := ReadFileAll(fs.rs, fs.sb, dir)
	if err != nil {
		return err
	}
	bsize := int(fs.sb.Bsize)
	newRec := DirentReclen(len(name))
	// Walk existing entries; for each entry, see if the slot between
	// header+namelen (rounded to 4) and reclen is large enough to fit
	// newRec. If so, split: shrink that entry's reclen and write the
	// new dirent into the freed tail.
	cur := 0
	le := binary.LittleEndian
	for cur+direntHeaderSize <= len(body) {
		ino0 := le.Uint32(body[cur:])
		reclen := int(le.Uint16(body[cur+4:]))
		if reclen < direntHeaderSize {
			return fmt.Errorf("ufs: corrupt dirent at off %d", cur)
		}
		var minHere int
		if ino0 == 0 {
			minHere = 0
		} else {
			namlen := int(body[cur+7])
			minHere = DirentReclen(namlen)
		}
		slack := reclen - minHere
		if slack >= newRec {
			// Split: shrink the original entry (or simply drop a
			// vacant slot) and put the new entry in the tail.
			if ino0 == 0 {
				// Vacant slot — overwrite in place, leaving the
				// remainder as our new record's tail padding.
				EncodeDirent(body[cur:cur+reclen], uint32(ino), dtype, name, uint16(reclen))
			} else {
				le.PutUint16(body[cur+4:], uint16(minHere))
				newOff := cur + minHere
				EncodeDirent(body[newOff:newOff+slack], uint32(ino), dtype, name, uint16(slack))
			}
			return fs.rewriteDirBody(dirIno, dir, body)
		}
		cur += reclen
	}
	// No slot fit — append a fresh block to the directory.
	newBlock := make([]byte, bsize)
	EncodeDirent(newBlock, uint32(ino), dtype, name, uint16(bsize))
	body = append(body, newBlock...)
	return fs.rewriteDirBody(dirIno, dir, body)
}

// rewriteDirBody writes `body` as the new contents of directory
// `dir`, allocating blocks afresh. The size MUST be a multiple of
// bsize (directories always occupy whole blocks in UFS).
func (fs *FS) rewriteDirBody(dirIno uint64, dir *Inode, body []byte) error {
	if len(body)%int(fs.sb.Bsize) != 0 {
		return fmt.Errorf("ufs: dir body %d not bsize-aligned", len(body))
	}
	if err := fs.writeFileData(dir, body); err != nil {
		return err
	}
	return fs.writeInode(dirIno, dir)
}

// dirRemoveEntry locates the dirent named `name` in directory `dirIno`
// and removes it by absorbing its slot into the preceding entry (or
// marking it vacant if it's at the start of a block). The directory
// body is rewritten through writeFileData; freed inode/block accounting
// is the caller's responsibility.
func (fs *FS) dirRemoveEntry(dirIno uint64, dir *Inode, name string) (uint64, uint8, error) {
	body, err := ReadFileAll(fs.rs, fs.sb, dir)
	if err != nil {
		return 0, 0, err
	}
	bsize := int(fs.sb.Bsize)
	le := binary.LittleEndian
	for blkStart := 0; blkStart < len(body); blkStart += bsize {
		blkEnd := blkStart + bsize
		prevOff := -1
		cur := blkStart
		for cur+direntHeaderSize <= blkEnd {
			ino := le.Uint32(body[cur:])
			reclen := int(le.Uint16(body[cur+4:]))
			if reclen < direntHeaderSize {
				return 0, 0, fmt.Errorf("ufs: corrupt dirent in remove")
			}
			namlen := int(body[cur+7])
			entryName := string(body[cur+direntHeaderSize : cur+direntHeaderSize+namlen])
			if ino != 0 && entryName == name {
				dtype := body[cur+6]
				if prevOff >= 0 {
					// Absorb our reclen into the previous entry.
					prevRec := int(le.Uint16(body[prevOff+4:]))
					le.PutUint16(body[prevOff+4:], uint16(prevRec+reclen))
				} else {
					// First entry in its block — mark vacant.
					le.PutUint32(body[cur:], 0)
				}
				if err := fs.rewriteDirBody(dirIno, dir, body); err != nil {
					return 0, 0, err
				}
				return uint64(ino), dtype, nil
			}
			prevOff = cur
			cur += reclen
		}
	}
	return 0, 0, fmt.Errorf("%w: %s", ErrNotFound, name)
}

// resolveParent splits `p` into (parent path, last component) and
// resolves the parent inode. Errors if the parent isn't a directory
// or doesn't exist.
func (fs *FS) resolveParent(p string) (uint64, *Inode, string, error) {
	parent := path.Dir(p)
	name := path.Base(p)
	if name == "." || name == ".." || name == "/" || name == "" {
		return 0, nil, "", fmt.Errorf("%w: bad target name %q", ErrInvalidPath, name)
	}
	if strings.Contains(name, "/") {
		return 0, nil, "", fmt.Errorf("%w: name contains slash", ErrInvalidPath)
	}
	ino, dir, err := resolve(fs.rs, fs.sb, parent, true)
	if err != nil {
		return 0, nil, "", err
	}
	if !dir.IsDir() {
		return 0, nil, "", fmt.Errorf("%w: %s", ErrNotDirectory, parent)
	}
	return ino, dir, name, nil
}

// childExists reports whether `dir` already contains an entry named
// `name`.
func (fs *FS) childExists(dir *Inode, name string) (bool, error) {
	body, err := ReadFileAll(fs.rs, fs.sb, dir)
	if err != nil {
		return false, err
	}
	ents, err := ParseDirents(body)
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		if e.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// WriteFile creates a new regular file at `path`, links it into the
// parent directory, and writes `data`. It is an error for the path
// to already exist; callers that want overwrite semantics should
// DeleteFile first. perm is masked to 0o7777 (the high bits are
// reserved for file-type and are forced to IFREG).
func (fs *FS) WriteFile(p string, data []byte, perm os.FileMode) error {
	if fs.wa == nil || fs.alloc == nil {
		return ErrReadOnly
	}
	parentIno, parent, name, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	if ok, err := fs.childExists(parent, name); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrExists, p)
	}
	ino, err := fs.alloc.allocInode(false)
	if err != nil {
		return err
	}
	in := newInode(IFREG | uint16(perm&0o7777))
	if err := fs.writeFileData(in, data); err != nil {
		return err
	}
	if err := fs.writeInode(ino, in); err != nil {
		return err
	}
	return fs.dirAppendEntry(parentIno, parent, name, ino, DtReg)
}
