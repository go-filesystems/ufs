// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"fmt"
	"os"
)

// MkDir creates a new directory inode at `p`, populates it with
// canonical "." and ".." entries, links it into the parent, and bumps
// the parent's nlink count by 1 (UFS counts subdirs via the "..")
// backpointer).
func (fs *FS) MkDir(p string, perm os.FileMode) error {
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
	ino, err := fs.alloc.allocInode(true)
	if err != nil {
		return err
	}
	in := newInode(IFDIR | uint16(perm&0o7777))
	in.Nlink = 2 // "." pointing back at us, + the entry the parent will add
	// Build initial dir body: "." + ".." in a single block.
	bsize := int(fs.sb.Bsize)
	body := make([]byte, bsize)
	dotLen := DirentReclen(1)
	dotdotLen := bsize - dotLen
	EncodeDirent(body[0:dotLen], uint32(ino), DtDir, ".", uint16(dotLen))
	EncodeDirent(body[dotLen:bsize], uint32(parentIno), DtDir, "..", uint16(dotdotLen))
	if err := fs.writeFileData(in, body); err != nil {
		return err
	}
	if err := fs.writeInode(ino, in); err != nil {
		return err
	}
	// Link into parent.
	if err := fs.dirAppendEntry(parentIno, parent, name, ino, DtDir); err != nil {
		return err
	}
	// Bump parent's nlink for our ".." backpointer.
	parent.Nlink++
	return fs.writeInode(parentIno, parent)
}
