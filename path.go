// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"fmt"
	"io"
	"path"
	"strings"
)

// maxSymlinkHops bounds the number of symlink hops a single path
// resolution may take before we bail with ErrTooManyLinks. POSIX
// suggests 8; Linux uses 40. We split the difference at 32 — plenty
// for legitimate trees, low enough to surface obvious cycles.
const maxSymlinkHops = 32

// splitPath canonicalises `p` (collapses //, removes . and trailing
// /) and returns its slash-separated components. The empty list
// represents the root directory.
func splitPath(p string) ([]string, error) {
	if p == "" {
		return nil, ErrInvalidPath
	}
	cleaned := path.Clean(p)
	if cleaned == "/" || cleaned == "." {
		return nil, nil
	}
	if !strings.HasPrefix(cleaned, "/") {
		// We intentionally only accept absolute paths — the
		// driver has no concept of a current working directory.
		return nil, fmt.Errorf("%w: relative path %q", ErrInvalidPath, p)
	}
	parts := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	// Reject any internal "" segment defensively (path.Clean
	// should have removed them, but a caller may have passed
	// something Clean does not normalise the same way).
	for _, seg := range parts {
		if seg == "" {
			return nil, fmt.Errorf("%w: empty segment in %q", ErrInvalidPath, p)
		}
	}
	return parts, nil
}

// resolve walks `pathStr` from the root inode, following symbolic
// links along intermediate components. The final component is not
// followed when `followLast` is false (used by ReadLink). Returns
// the inode number of the resolved entry plus the loaded inode.
//
// The walk is iterative — when we encounter a symlink that needs to
// be expanded, we rewrite the remaining `parts` list and restart
// from the root with a bumped hop counter, rather than recursing.
// This keeps the call graph shallow and the hop accounting honest.
func resolve(rs io.ReaderAt, sb *Superblock, pathStr string, followLast bool) (uint64, *Inode, error) {
	parts, err := splitPath(pathStr)
	if err != nil {
		return 0, nil, err
	}
	hops := 0
	for {
		cur := uint64(RootInode)
		curIno, err := ReadInode(rs, sb, cur)
		if err != nil {
			return 0, nil, err
		}
		// followed signals that the inner loop expanded a
		// symlink and rewrote `parts`; we restart the outer
		// loop. If the inner loop completes without expansion,
		// we return the final cur/curIno.
		followed := false
		for i, seg := range parts {
			if !curIno.IsDir() {
				return 0, nil, fmt.Errorf("%w: %s", ErrNotDirectory, strings.Join(parts[:i], "/"))
			}
			dirData, err := ReadFileAll(rs, sb, curIno)
			if err != nil {
				return 0, nil, err
			}
			entries, err := ParseDirents(dirData)
			if err != nil {
				return 0, nil, err
			}
			found := false
			for _, e := range entries {
				if e.Name == seg {
					cur = uint64(e.Ino)
					found = true
					break
				}
			}
			if !found {
				return 0, nil, fmt.Errorf("%w: %s", ErrNotFound, strings.Join(parts[:i+1], "/"))
			}
			curIno, err = ReadInode(rs, sb, cur)
			if err != nil {
				return 0, nil, err
			}
			isLast := i == len(parts)-1
			if curIno.IsSymlink() && (!isLast || followLast) {
				hops++
				if hops > maxSymlinkHops {
					return 0, nil, ErrTooManyLinks
				}
				target, err := readSymlinkTarget(rs, sb, curIno)
				if err != nil {
					return 0, nil, err
				}
				rest := parts[i+1:]
				newPath := target
				if !strings.HasPrefix(newPath, "/") {
					prefix := "/" + strings.Join(parts[:i], "/")
					newPath = path.Join(prefix, target)
				}
				if len(rest) > 0 {
					newPath = path.Join(newPath, strings.Join(rest, "/"))
				}
				parts, err = splitPath(newPath)
				if err != nil {
					return 0, nil, err
				}
				followed = true
				break
			}
		}
		if !followed {
			return cur, curIno, nil
		}
	}
}

// readSymlinkTarget returns the target string for a symlink inode,
// preferring the inline shortlink representation when available.
func readSymlinkTarget(rs io.ReaderAt, sb *Superblock, in *Inode) (string, error) {
	if !in.IsSymlink() {
		return "", ErrNotSymlink
	}
	if s, ok := in.Shortlink(sb); ok {
		return s, nil
	}
	data, err := ReadFileAll(rs, sb, in)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
