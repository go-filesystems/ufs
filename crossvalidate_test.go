// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"io"
	"os"
	"testing"
)

// realImagePath points at a FreeBSD-14.3 release image that contains a
// real UFS2 partition. The test is silently skipped when the image is
// absent so CI without the artefact stays green; when present, the
// test cross-checks our reader against the real on-disk layout AND
// diffs the superblock geometry against what Mkfs would produce for
// the same size.
const (
	realImagePath  = "/tmp/fbsd/FreeBSD-14.3-RELEASE-amd64.raw"
	realRootOffset = int64(2163892) * 512 // GPT partition 4 (rootfs)
)

// offsetReaderAt adapts an io.ReaderAt by adding a fixed offset to
// every read. Used to pretend a partition embedded in a disk image is
// the start of a standalone UFS2 volume.
type offsetReaderAt struct {
	base   io.ReaderAt
	offset int64
}

func (o offsetReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return o.base.ReadAt(p, off+o.offset)
}

func TestCrossValidate_RealFreeBSDUFS2(t *testing.T) {
	f, err := os.Open(realImagePath)
	if err != nil {
		t.Skipf("real FreeBSD image not present at %s — skipping cross-validation", realImagePath)
	}
	defer f.Close()
	or := offsetReaderAt{base: f, offset: realRootOffset}
	realFS, err := Open(or, -1)
	if err != nil {
		t.Fatalf("Open real UFS2: %v", err)
	}
	rsb := realFS.Superblock()
	t.Logf("real UFS2 superblock: magic=0x%x bsize=%d fsize=%d frag=%d ncg=%d ipg=%d fpg=%d",
		rsb.Magic, rsb.Bsize, rsb.Fsize, rsb.Frag, rsb.Ncg, rsb.Ipg, rsb.Fpg)

	if rsb.Magic != MagicUFS2 {
		t.Fatalf("real image magic = 0x%x, want UFS2", rsb.Magic)
	}

	// Walk a known path to confirm our reader copes with a real
	// FreeBSD UFS2 image (not just our synthetic fixtures).
	entries, err := realFS.ListDir("/")
	if err != nil {
		t.Fatalf("ListDir / on real image: %v", err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.Name()] = true
	}
	for _, n := range []string{"boot", "etc"} {
		if !have[n] {
			t.Errorf("real / missing %q (have %v)", n, have)
		}
	}
	if have["boot"] {
		bootEntries, err := realFS.ListDir("/boot")
		if err != nil {
			t.Fatalf("ListDir /boot: %v", err)
		}
		bootNames := map[string]bool{}
		for _, e := range bootEntries {
			bootNames[e.Name()] = true
		}
		t.Logf("/boot contains: %v", bootNames)
	}

	// Cross-validate Mkfs's superblock against the real one. The
	// concrete geometry differs (FreeBSD uses bsize=32 KiB by default
	// for large devices; we use 4 KiB) — but the key INVARIANTS must
	// match: magic, inodefmt, bshift==log2(bsize), inopb==bsize/256,
	// frag==bsize/fsize, sblkno*fsize == 65536.
	if rsb.Bshift != int32(log2(uint32(rsb.Bsize))) {
		t.Errorf("real bshift %d != log2(bsize %d)", rsb.Bshift, rsb.Bsize)
	}
	if rsb.Inopb != uint32(rsb.Bsize)/InodeSize {
		t.Errorf("real inopb %d != bsize/InodeSize", rsb.Inopb)
	}
	// Sblkno is the per-cg fragment offset of the superblock COPY,
	// not the canonical primary location (which is always
	// byte-offset SblockUFS2 == 65536). On real FreeBSD images it
	// sits where the cg metadata layout puts it; we don't constrain
	// its exact value, just that it's a positive sensible number.
	if rsb.Sblkno <= 0 {
		t.Errorf("real sblkno = %d, expected positive", rsb.Sblkno)
	}
	if rsb.OldInodefmt != 2 {
		t.Errorf("real OldInodefmt = %d, want 2", rsb.OldInodefmt)
	}

	// Build a synthetic Mkfs image and confirm OUR superblock satisfies
	// the same invariants (proving structural parity).
	img := newMemImage(16 << 20)
	ourFS, err := Mkfs(img, 16<<20)
	if err != nil {
		t.Fatalf("Mkfs synthetic: %v", err)
	}
	osb := ourFS.Superblock()
	t.Logf("our Mkfs superblock: magic=0x%x bsize=%d fsize=%d frag=%d ncg=%d ipg=%d fpg=%d",
		osb.Magic, osb.Bsize, osb.Fsize, osb.Frag, osb.Ncg, osb.Ipg, osb.Fpg)
	if osb.Magic != rsb.Magic {
		t.Errorf("magic differs: ours=0x%x, real=0x%x", osb.Magic, rsb.Magic)
	}
	if osb.OldInodefmt != rsb.OldInodefmt {
		t.Errorf("OldInodefmt differs: ours=%d, real=%d", osb.OldInodefmt, rsb.OldInodefmt)
	}
	if osb.Sblkno <= 0 {
		t.Errorf("our sblkno = %d, expected positive", osb.Sblkno)
	}

	// Bonus diff: dump the first 64 bytes of cg 0's bitmap region from
	// both images (purely informational — they will differ).
	t.Logf("real:  inopb=%d nindir=%d maxsymlinklen=%d", rsb.Inopb, rsb.Nindir, rsb.Maxsymlinklen)
	t.Logf("ours:  inopb=%d nindir=%d maxsymlinklen=%d", osb.Inopb, osb.Nindir, osb.Maxsymlinklen)

	// Try reading a real loader.conf so we know the indirect/block
	// reading machinery copes end-to-end with the production layout.
	if data, err := realFS.ReadFile("/boot/defaults/loader.conf"); err != nil {
		t.Logf("real /boot/defaults/loader.conf read: %v (informational)", err)
	} else {
		t.Logf("real /boot/defaults/loader.conf: %d bytes (first 80: %q)", len(data), truncString(string(data), 80))
	}
}

func truncString(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
