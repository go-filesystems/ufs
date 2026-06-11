# testdata

The unit and integration tests do **not** consume a binary fixture
shipped in the repository.  Instead, `fixture_test.go` builds a
minimal UFS2 image in memory at test time via `buildFixture()`. The
generator is deterministic, fast (<1 ms), and easier to audit than
an opaque on-disk blob.

The fixture covers:

- Root directory (inode 2) holding `boot/`, `etc/`, `var` (symlink),
  `big` (a regular file that requires indirect-block traversal).
- `/boot/loader.conf`, `/boot/kernel/kernel` (12 KB direct-block file).
- `/etc/fstab`, `/etc/rc.conf` (inline symlink to `/etc/fstab`).
- `/big` — 13-block file exercising the single-indirect path.

## Generating a real FreeBSD UFS2 image (future sprints)

Sprint 2B will validate against a real FreeBSD-formatted UFS2
partition extracted from the bootonly ISO. Until then, the
following procedure documents how to produce one for local
testing:

### On a FreeBSD host

```sh
truncate -s 4M /tmp/ufs2.img
mdconfig -a -t vnode -f /tmp/ufs2.img
newfs -O 2 -m 0 -i 4096 /dev/md0
mount -t ufs /dev/md0 /mnt
# populate /mnt with /boot/loader.conf, /boot/kernel/kernel, etc.
umount /mnt
mdconfig -d -u 0
cp /tmp/ufs2.img testdata/ufs2-real.img
```

### Inside a FreeBSD container (cross-platform)

A pre-built FreeBSD container image (e.g. `dougrabson/freebsd-amd64`)
ships `newfs(8)`. The container needs `--privileged` plus
`/dev/loop` access on Linux hosts, which is straightforward in
podman/docker but **not available on macOS/Darwin without a Linux
VM**. The Go-side `buildFixture()` is therefore the canonical
fixture for CI; container-based generation is reserved for the
sprint-2B cross-check.

### Extracting UFS2 from a FreeBSD installer ISO

The `FreeBSD-14.x-RELEASE-amd64-bootonly.iso` images do **not**
contain a UFS2 filesystem at large — the ISO9660 file system holds
the loader, kernel and packages. UFS2 only appears on the freshly
installed root filesystem. To extract one for testing, install
FreeBSD into a VM and copy the resulting partition image.

For sprint 2B the plan is to mount the bootonly ISO inside a
FreeBSD VM, run `newfs -O 2` against a backing file, and snapshot
the resulting partition into `testdata/ufs2-real.img`. The
hermetic in-process fixture covers sprint 2A's verification needs.
