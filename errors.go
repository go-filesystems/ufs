// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

// Package ufs is a pure-Go driver for the FreeBSD UFS on-disk format.
// It reads both UFS2 and UFS1 images (the latter with 128-byte dinodes
// and 32-bit block pointers); the write/Mkfs surface targets UFS2 only.
// Sprint 2A targets the surface that the FreeBSD loader.efi needs to
// read a kernel + modules off a UFS root partition; unsupported write
// operations from the filesystem.Filesystem interface are stubbed out
// with ErrReadOnly.
package ufs

import "errors"

// Sentinel errors returned by the UFS2 driver. Callers should compare
// with errors.Is rather than == so wrapped errors continue to match.
var (
	// ErrReadOnly is returned by every mutating method on the
	// filesystem.Filesystem interface (WriteFile, MkDir, DeleteFile,
	// DeleteDir, Rename). The sprint-2A driver is read-only by
	// construction; writes will land in a later sprint.
	ErrReadOnly = errors.New("ufs: filesystem is read-only")

	// ErrNotFound is returned when a path component cannot be located
	// in its parent directory.
	ErrNotFound = errors.New("ufs: path not found")

	// ErrInvalidPath is returned when a caller-supplied path is
	// syntactically invalid (e.g. empty, or containing an inode jump
	// the driver cannot honour).
	ErrInvalidPath = errors.New("ufs: invalid path")

	// ErrBadSuperblock is returned by Open when the on-disk
	// superblock at SBLOCK_UFS2 fails validation (wrong magic, wrong
	// fs_inodefmt, or numeric fields outside their sane ranges).
	ErrBadSuperblock = errors.New("ufs: superblock validation failed")

	// ErrUnsupportedIndirect is returned when a read would traverse a
	// double- or triple-indirect block. Sprint 2A handles direct
	// blocks and a single level of indirection only.
	ErrUnsupportedIndirect = errors.New("ufs: double/triple indirect blocks not supported")

	// ErrNotDirectory is returned when ListDir is called on an inode
	// that is not a directory.
	ErrNotDirectory = errors.New("ufs: not a directory")

	// ErrNotRegular is returned when ReadFile is called on an inode
	// that is neither a regular file nor a symbolic link.
	ErrNotRegular = errors.New("ufs: not a regular file")

	// ErrNotSymlink is returned when ReadLink is called on a
	// non-symlink inode.
	ErrNotSymlink = errors.New("ufs: not a symbolic link")

	// ErrTooManyLinks is returned when path resolution loops through
	// more than maxSymlinkHops symbolic links, indicating either a
	// genuine cycle or a pathological tree.
	ErrTooManyLinks = errors.New("ufs: too many symbolic link traversals")

	// ErrNoSpace is returned when an allocation cannot find a free
	// inode or block in any cylinder group.
	ErrNoSpace = errors.New("ufs: no space left on device")

	// ErrExists is returned when a mutating operation would overwrite
	// an existing path (WriteFile/MkDir/Symlink/Rename target).
	ErrExists = errors.New("ufs: path already exists")

	// ErrNotEmpty is returned when DeleteDir is called on a directory
	// that still contains entries other than "." and "..".
	ErrNotEmpty = errors.New("ufs: directory not empty")

	// ErrFileTooLarge is returned when a write would require
	// double/triple indirect blocks the writer does not yet implement.
	ErrFileTooLarge = errors.New("ufs: file too large for single-indirect")
)
