// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"encoding/binary"
	"os"
	"testing"
	"time"

	filesystem "github.com/go-filesystems/interface"
)

func TestMetadataSetter(t *testing.T) {
	const size = 16 * 1024 * 1024
	img := newMemImage(size)
	fs, err := Mkfs(img, size)
	if err != nil {
		t.Fatalf("Mkfs: %v", err)
	}

	var _ filesystem.MetadataSetter = fs // capability is exposed

	if err := fs.WriteFile("/f", []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	before := uint64(time.Now().Unix()) - 1

	// Chmod: replace perm bits incl. setuid, keep the regular-file type bits.
	if err := fs.Chmod("/f", 0o600|os.ModeSetuid); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	_, in, err := resolve(fs.rs, fs.sb, "/f", true)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if in.Mode&IFMT != IFREG {
		t.Fatalf("Chmod clobbered type bits: mode=0o%o", in.Mode)
	}
	if in.Mode&0o7777 != 0o4600 {
		t.Fatalf("Chmod perm = 0o%o, want 0o4600", in.Mode&0o7777)
	}

	// Chown: 32-bit uid/gid.
	const wantUID, wantGID = uint32(0x12345), uint32(0x6789A)
	if err := fs.Chown("/f", wantUID, wantGID); err != nil {
		t.Fatalf("Chown: %v", err)
	}
	_, in, _ = resolve(fs.rs, fs.sb, "/f", true)
	if in.UID != wantUID || in.GID != wantGID {
		t.Fatalf("Chown = uid %#x gid %#x, want %#x/%#x", in.UID, in.GID, wantUID, wantGID)
	}
	if in.Mode&0o7777 != 0o4600 {
		t.Fatalf("Chown changed mode: 0o%o", in.Mode&0o7777)
	}

	// Chtimes: explicit atime/mtime, ctime refreshed.
	at := time.Unix(1_000_000_000, 0)
	mt := time.Unix(1_500_000_000, 0)
	if err := fs.Chtimes("/f", at, mt); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	_, in, _ = resolve(fs.rs, fs.sb, "/f", true)
	le := binary.LittleEndian
	atime := le.Uint64(in.Raw[inoOffAtime:])
	mtime := le.Uint64(in.Raw[inoOffMtime:])
	ctime := le.Uint64(in.Raw[inoOffCtime:])
	if atime != uint64(at.Unix()) || mtime != uint64(mt.Unix()) {
		t.Fatalf("Chtimes = atime %d mtime %d, want %d/%d", atime, mtime, at.Unix(), mt.Unix())
	}
	if ctime < before {
		t.Fatalf("ctime %d not refreshed (>= %d expected)", ctime, before)
	}
}
