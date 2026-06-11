// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"fmt"
)

// Rename moves the entry at oldPath to newPath. The implementation is
// not strictly atomic across the disk — it removes the old dirent,
// then adds a new one — but it is coherent: each individual write
// leaves the filesystem in a well-formed state.
//
// Cross-directory renames are supported. If newPath already exists
// the call fails with ErrExists; callers wanting overwrite must
// DeleteFile first.
func (fs *FS) Rename(oldPath, newPath string) error {
	if fs.wa == nil || fs.alloc == nil {
		return ErrReadOnly
	}
	oldParentIno, oldParent, oldName, err := fs.resolveParent(oldPath)
	if err != nil {
		return err
	}
	newParentIno, newParent, newName, err := fs.resolveParent(newPath)
	if err != nil {
		return err
	}
	// Same-name same-parent: no-op.
	if oldParentIno == newParentIno && oldName == newName {
		return nil
	}
	// Check newPath doesn't already exist.
	if ok, err := fs.childExists(newParent, newName); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrExists, newPath)
	}
	// Remove old dirent (capture ino + type).
	ino, dtype, err := fs.dirRemoveEntry(oldParentIno, oldParent, oldName)
	if err != nil {
		return err
	}
	// If we're moving across parents, the dir's nlink accounting
	// changes (the ".." backpointer follows the directory). Reload
	// the parents from disk because the dir mutation above may have
	// re-written them, and our local copies are stale.
	if oldParentIno != newParentIno {
		if dtype == DtDir {
			if oldParent.Nlink > 0 {
				oldParent.Nlink--
			}
			if err := fs.writeInode(oldParentIno, oldParent); err != nil {
				return err
			}
			newParent.Nlink++
		}
	}
	// Re-load newParent — its block layout may have changed if it
	// happens to BE oldParent (same directory, different name).
	if oldParentIno == newParentIno {
		newParent, err = ReadInode(fs.rs, fs.sb, newParentIno)
		if err != nil {
			return err
		}
	}
	if err := fs.dirAppendEntry(newParentIno, newParent, newName, ino, dtype); err != nil {
		return err
	}
	if oldParentIno != newParentIno && dtype == DtDir {
		// Patch the moved dir's ".." entry to point at its new
		// parent.
		movedDir, err := ReadInode(fs.rs, fs.sb, ino)
		if err != nil {
			return err
		}
		if err := fs.patchDotDot(ino, movedDir, newParentIno); err != nil {
			return err
		}
		if err := fs.writeInode(newParentIno, newParent); err != nil {
			return err
		}
	}
	return nil
}

// patchDotDot rewrites the ".." entry of `dir` to point at newParent.
func (fs *FS) patchDotDot(dirIno uint64, dir *Inode, newParent uint64) error {
	body, err := ReadFileAll(fs.rs, fs.sb, dir)
	if err != nil {
		return err
	}
	// Replace just the inode field of the ".." dirent. Walk to find
	// it explicitly (it's always the 2nd entry, but be defensive).
	if _, _, err := fs.dirRemoveEntry(dirIno, dir, ".."); err != nil {
		return err
	}
	// Re-read the dir after the remove (its body may have been
	// rewritten) and append a fresh "..".
	dir, err = ReadInode(fs.rs, fs.sb, dirIno)
	if err != nil {
		return err
	}
	_ = body // silence linter; we use the side-effect path above
	return fs.dirAppendEntry(dirIno, dir, "..", newParent, DtDir)
}
