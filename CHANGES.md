# Changes

## Unreleased

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
