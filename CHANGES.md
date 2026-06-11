# Changes

## Unreleased

- Sprint 2C-A: add UFS2 write surface and `Mkfs` entry point.
  - `Mkfs(rw, sizeBytes)` formats a fresh UFS2 image (superblock,
    per-cg headers + bitmaps, inode table, root directory) entirely
    in-process — no shell-outs, no native `newfs`.
  - `OpenRW(rs, wa, size)` opens an existing image read+write,
    loading the cylinder-group allocator into memory.
  - `WriteFile`, `MkDir`, `DeleteFile`, `DeleteDir`, `Rename` and
    the optional `Symlinker.Symlink` now mutate the on-disk image
    coherently (each call leaves the filesystem well-formed; soft
    updates are deliberately absent — fsck-on-mount recovers).
  - Free-inode / free-block bitmap arithmetic and `cs_*` summary
    accounting in `alloc.go`.
  - Round-trip tests: write via `*FS`, re-open via the read-side,
    verify payload + directory layout. Includes a 16 MiB
    create-from-scratch test that lays down `/boot/loader.conf` +
    `/boot/kernel/kernel`.
  - New sentinels: `ErrNoSpace`, `ErrExists`, `ErrNotEmpty`,
    `ErrFileTooLarge`.
  - LOC delta: ~1200 lines (alloc + write + mkdir + delete + rename
    + mkfs + tests).
  - Coverage: 85%+ on the package (up-front: read-side 96%, the
    new write paths add bitmap-edge branches not yet hit by
    happy-path tests — to be tightened in 2C-A.1).

- Initial sprint-2A scaffold: pure-Go, read-only UFS2 driver.
  - Superblock parser (primary location at `SBLOCK_UFS2`).
  - Inode parser (`ufs2_dinode`, 256 bytes).
  - Directory entry parser with vacant-slot skipping.
  - Block reader supporting 12 direct pointers + 1 level of
    indirection.
  - Path resolver with inline-symlink support and cycle protection
    (`ErrTooManyLinks`).
  - `filesystem.Filesystem` interface implementation: `Open`,
    `OpenFile`, `Close`, `ReadFile`, `ListDir`, `Stat`, `ReadLink`.
  - Write-side methods all return `ErrReadOnly`.
  - In-process fixture builder for hermetic, repeatable tests.
  - ~96% test coverage; CI runs `task ci` (vet + build + test
    -race -coverprofile).
