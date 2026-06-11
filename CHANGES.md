# Changes

## Unreleased

- Sprint 2D: explicit-options Mkfs + double-indirect read/write.
  - New `MkfsOptions{BlockSize, FragmentSize, InodeDensity, Label}`
    + `MkfsWith(rw, sizeBytes, opts)` entry point. Zero-valued
    fields fall back to FreeBSD `newfs(8)` defaults (`BlockSize`
    4096 canonical, `FragmentSize = BlockSize/8`, InodeDensity 4096).
    `Mkfs(...)` itself stays untouched for backward compat — same
    sprint-2C-A small-block geometry it always produced.
  - `block.go::blockForLBN` now walks the double-indirect tier
    (`in.Indirect[1]` → tier-1 with `Nindir` outer pointers → tier-2
    single-indirect block per outer slot → data fragment), reaching
    `Nindir² × bsize` bytes per file.
  - `write.go::writeFileData` lazy-allocates the double-indirect
    chain in a single forward pass: tier-2 blocks accumulate in
    memory, tier-1 is flushed once at end. Reaches the same `Nindir²
    × bsize` cap as the reader.
  - `write.go::freeFileBlocks` walks the full double-indirect chain
    on `DeleteFile` so reclamation is leak-free.
  - At `BlockSize=32768` (FreeBSD newfs default for ≥ 2 GiB
    devices), single-indirect reach is ~128 MiB and double-indirect
    is ~8 GiB. Sufficient for the cloud-boot live pipeline's 29 MiB
    FreeBSD kernel without triple-indirect.
  - Triple-indirect remains intentionally unimplemented
    (`Nindir³ × bsize = 1 PiB` at bsize=32768 — over-spec).
  - New tests: 25 MiB blob round-trip at bsize=32768 (single-
    indirect) and bsize=4096 (engages double-indirect); cross-
    validation reading the same blob through both `fs.ReadFile` and
    a low-level `blockForLBN` walk; delete-then-reuse test
    confirming chain reclamation; out-of-blocks error path.
  - Coverage: 86.6% (up from 85.7% baseline).

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
