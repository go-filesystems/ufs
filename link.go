// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

// link.go — POSIX hard-link creation. A hard link is a second directory entry
// referencing an existing inode, with the inode's link count bumped.
// Directories may not be hard-linked, and the final component of oldPath is
// not dereferenced (linking a symlink links the symlink inode itself).

import (
	"fmt"

	filesystem "github.com/go-filesystems/interface"
)

var _ filesystem.HardLinker = (*FS)(nil)

// Link adds a new directory entry at newPath referencing the same inode as
// oldPath and bumps that inode's link count. oldPath must not be a directory;
// newPath must not already exist. Requires a read-write filesystem.
func (fs *FS) Link(oldPath, newPath string) error {
	if fs.wa == nil || fs.alloc == nil {
		return ErrReadOnly
	}
	// followLast=false: link the named inode itself, not a symlink's target.
	srcIno, srcIn, err := resolve(fs.rs, fs.sb, oldPath, false)
	if err != nil {
		return err
	}
	if srcIn.IsDir() {
		return fmt.Errorf("ufs: hard link to directory %q not permitted", oldPath)
	}

	parentIno, parent, name, err := fs.resolveParent(newPath)
	if err != nil {
		return err
	}
	if ok, err := fs.childExists(parent, name); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrExists, newPath)
	}

	dt := DtReg
	if srcIn.IsSymlink() {
		dt = DtLnk
	}
	if err := fs.dirAppendEntry(parentIno, parent, name, srcIno, dt); err != nil {
		return err
	}

	srcIn.Nlink++
	return fs.writeInode(srcIno, srcIn)
}
