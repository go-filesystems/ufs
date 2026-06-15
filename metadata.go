package ufs

// metadata.go — POSIX metadata mutators (chmod / chown / chtimes), bundled as
// filesystem.MetadataSetter. Writes go through the UFS2 on-disk layout (the
// only format this driver writes); each resolves the path, edits the inode,
// refreshes di_ctime, and rewrites it. writeInode patches the decoded fields
// (mode/uid/gid/…) into Raw and preserves the rest, so the timestamps stamped
// directly into Raw here survive the round-trip.

import (
	"os"
	"time"

	filesystem "github.com/go-filesystems/interface"
)

var _ filesystem.MetadataSetter = (*FS)(nil)

// updateInode resolves path to its inode, applies edit, refreshes di_ctime and
// writes the inode back. Mutating calls require a read-write filesystem.
func (fs *FS) updateInode(path string, edit func(in *Inode)) error {
	if fs.wa == nil || fs.alloc == nil {
		return ErrReadOnly
	}
	ino, in, err := resolve(fs.rs, fs.sb, path, true /* follow final symlink */)
	if err != nil {
		return err
	}
	edit(in)
	stampTime(in.Raw[inoOffCtime:], time.Now())
	return fs.writeInode(ino, in)
}

// Chmod replaces the permission + setuid/setgid/sticky bits at path, preserving
// the file-type bits. ctime is refreshed.
func (fs *FS) Chmod(path string, perm os.FileMode) error {
	return fs.updateInode(path, func(in *Inode) {
		bits := uint16(perm & 0o777)
		if perm&os.ModeSetuid != 0 {
			bits |= 0o4000
		}
		if perm&os.ModeSetgid != 0 {
			bits |= 0o2000
		}
		if perm&os.ModeSticky != 0 {
			bits |= 0o1000
		}
		in.Mode = (in.Mode &^ 0o7777) | bits
	})
}

// Chown updates uid/gid at path. ctime is refreshed; mode, body and the other
// timestamps are left alone.
func (fs *FS) Chown(path string, uid, gid uint32) error {
	return fs.updateInode(path, func(in *Inode) {
		in.UID = uid
		in.GID = gid
	})
}

// Chtimes sets di_atime and di_mtime at path. ctime is refreshed to now per
// POSIX; birth time is left untouched.
func (fs *FS) Chtimes(path string, atime, mtime time.Time) error {
	return fs.updateInode(path, func(in *Inode) {
		stampTime(in.Raw[inoOffAtime:], atime)
		stampTime(in.Raw[inoOffMtime:], mtime)
	})
}
