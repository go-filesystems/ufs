// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"io"

	filesystem "github.com/go-filesystems/interface"
)

// OpenReader opens the UFS filesystem at the start of r, exactly as Open does, and
// returns it as a filesystem.Filesystem.
//
// It exists so every driver in go-filesystems answers to ONE name for the same
// thing. github.com/go-filesystems/detect's Opener is
// func(io.ReaderAt, int64) (filesystem.Filesystem, error): Open here is the
// right shape but returns a concrete *FS, which is a different type and does
// not fit. The drivers that could only be opened from a PATH have grown this
// function; this one is that same function, so a caller can write
//
//	detect.Register(detect.T, driver.OpenReader)
//
// for every driver instead of a closure per driver.
//
// The concrete type is still there for a caller that wants what it has beyond
// the common interface: Open returns it.
func OpenReader(r io.ReaderAt, size int64) (filesystem.Filesystem, error) {
	fs, err := Open(r, size)
	if err != nil {
		// A typed nil inside an interface is not nil, so the error path
		// returns an untyped one: a caller that checks the value rather than
		// the error would otherwise be told it has a filesystem.
		return nil, err
	}
	return fs, nil
}
