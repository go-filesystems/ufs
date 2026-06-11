// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"fmt"
)

// DeleteFile removes the regular-file (or symlink) entry at `p`,
// frees the inode and any blocks it referenced, and updates the
// parent directory.
func (fs *FS) DeleteFile(p string) error {
	if fs.wa == nil || fs.alloc == nil {
		return ErrReadOnly
	}
	parentIno, parent, name, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	childIno, dtype, err := fs.dirRemoveEntry(parentIno, parent, name)
	if err != nil {
		return err
	}
	if dtype == DtDir {
		return fmt.Errorf("%w: %s is a directory", ErrNotRegular, p)
	}
	child, err := ReadInode(fs.rs, fs.sb, childIno)
	if err != nil {
		return err
	}
	if child.IsSymlink() && child.Blocks == 0 {
		// Inline shortlink: no data blocks to free.
	} else {
		if err := fs.freeFileBlocks(child); err != nil {
			return err
		}
	}
	return fs.alloc.freeInode(childIno, false)
}

// DeleteDir removes an empty directory at `p`. Returns ErrNotEmpty if
// the directory still contains entries other than "." and "..".
func (fs *FS) DeleteDir(p string) error {
	if fs.wa == nil || fs.alloc == nil {
		return ErrReadOnly
	}
	parentIno, parent, name, err := fs.resolveParent(p)
	if err != nil {
		return err
	}
	// First check emptiness without mutating anything.
	body, err := ReadFileAll(fs.rs, fs.sb, parent)
	if err != nil {
		return err
	}
	ents, err := ParseDirents(body)
	if err != nil {
		return err
	}
	var childIno uint64
	for _, e := range ents {
		if e.Name == name {
			if e.Type != DtDir {
				return fmt.Errorf("%w: %s is not a directory", ErrNotDirectory, p)
			}
			childIno = uint64(e.Ino)
			break
		}
	}
	if childIno == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	child, err := ReadInode(fs.rs, fs.sb, childIno)
	if err != nil {
		return err
	}
	if !child.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotDirectory, p)
	}
	childBody, err := ReadFileAll(fs.rs, fs.sb, child)
	if err != nil {
		return err
	}
	cEnts, err := ParseDirents(childBody)
	if err != nil {
		return err
	}
	for _, e := range cEnts {
		if e.Name != "." && e.Name != ".." {
			return fmt.Errorf("%w: %s", ErrNotEmpty, p)
		}
	}
	if _, _, err := fs.dirRemoveEntry(parentIno, parent, name); err != nil {
		return err
	}
	if err := fs.freeFileBlocks(child); err != nil {
		return err
	}
	if err := fs.alloc.freeInode(childIno, true); err != nil {
		return err
	}
	// Parent's nlink drops by 1 (we removed the ".." backpointer).
	if parent.Nlink > 0 {
		parent.Nlink--
	}
	return fs.writeInode(parentIno, parent)
}
