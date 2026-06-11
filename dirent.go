// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"encoding/binary"
	"fmt"
)

// d_type values exposed by the UFS direntry. Mirrors the DT_*
// constants in sys/ufs/ufs/dir.h.
const (
	DtUnknown uint8 = 0
	DtFifo    uint8 = 1
	DtChr     uint8 = 2
	DtDir     uint8 = 4
	DtBlk     uint8 = 6
	DtReg     uint8 = 8
	DtLnk     uint8 = 10
	DtSock    uint8 = 12
	DtWht     uint8 = 14
)

// direntHeaderSize is the size of the fixed-length prefix of a UFS
// directory entry: d_ino(4) + d_reclen(2) + d_type(1) + d_namlen(1).
const direntHeaderSize = 8

// Dirent is a decoded directory entry.
type Dirent struct {
	// Ino is the inode number of the named file. Zero marks a
	// vacant slot inside the directory block — callers must skip
	// these.
	Ino uint32
	// Reclen is the on-disk size of this record, including header,
	// name and 4-byte padding.
	Reclen uint16
	// Type is the file-type byte (DT_DIR, DT_REG, etc.). May be
	// DT_UNKNOWN on very old images; consumers should fall back to
	// stat-ing the inode in that case.
	Type uint8
	// Namlen is the byte length of Name, NOT including any
	// trailing NUL.
	Namlen uint8
	// Name is the entry's name. Not NUL-terminated.
	Name string
}

// ParseDirents walks a directory data buffer and returns every
// directory entry it finds. UFS directories are arrays of
// variable-length records; each record's d_reclen advances the
// cursor. We skip records whose d_ino is zero (vacant) but still
// honour their reclen so the iteration stays aligned.
//
// The function tolerates a trailing partial record (the kernel pads
// to a fragment boundary) by stopping once the next reclen would
// overrun the buffer.
func ParseDirents(buf []byte) ([]Dirent, error) {
	var out []Dirent
	cur := 0
	for cur+direntHeaderSize <= len(buf) {
		le := binary.LittleEndian
		ino := le.Uint32(buf[cur:])
		reclen := le.Uint16(buf[cur+4:])
		dtype := buf[cur+6]
		namlen := buf[cur+7]

		if reclen < direntHeaderSize {
			return nil, fmt.Errorf("ufs: invalid dirent reclen %d at offset %d", reclen, cur)
		}
		if int(reclen)%4 != 0 {
			return nil, fmt.Errorf("ufs: dirent reclen %d at offset %d not 4-byte aligned", reclen, cur)
		}
		if cur+int(reclen) > len(buf) {
			// Partial trailing record; stop cleanly rather than panic.
			break
		}
		if int(namlen)+direntHeaderSize > int(reclen) {
			return nil, fmt.Errorf("ufs: namlen %d exceeds reclen %d at offset %d", namlen, reclen, cur)
		}

		if ino != 0 {
			name := string(buf[cur+direntHeaderSize : cur+direntHeaderSize+int(namlen)])
			out = append(out, Dirent{
				Ino:    ino,
				Reclen: reclen,
				Type:   dtype,
				Namlen: namlen,
				Name:   name,
			})
		}
		cur += int(reclen)
	}
	return out, nil
}

// EncodeDirent writes one variable-length directory record into buf
// and returns the number of bytes written (= reclen). It is used by
// the in-process fixture builder; the read-side driver never calls
// it at runtime. The caller is responsible for picking a reclen that
// is 4-byte aligned and at least direntHeaderSize+namlen.
func EncodeDirent(buf []byte, ino uint32, dtype uint8, name string, reclen uint16) int {
	if len(buf) < int(reclen) {
		return 0
	}
	le := binary.LittleEndian
	le.PutUint32(buf[0:], ino)
	le.PutUint16(buf[4:], reclen)
	buf[6] = dtype
	buf[7] = uint8(len(name))
	copy(buf[direntHeaderSize:], name)
	// Zero-pad the tail so unused bytes (including the gap up to
	// reclen) are deterministic.
	for i := direntHeaderSize + len(name); i < int(reclen); i++ {
		buf[i] = 0
	}
	return int(reclen)
}

// DirentReclen returns the minimum reclen (rounded up to 4) required
// to encode a directory entry whose name is `namlen` bytes long.
// Equivalent to FreeBSD's DIRECTSIZ macro.
func DirentReclen(namlen int) int {
	n := direntHeaderSize + namlen + 1 // +1 for the implicit NUL
	if r := n % 4; r != 0 {
		n += 4 - r
	}
	return n
}
