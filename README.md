# ufs

Pure-Go, read-only driver for the FreeBSD UFS2 on-disk format.

## Status

Sprint 2A: read-only surface sufficient to satisfy FreeBSD's
`loader.efi` against a UFS2 root partition.

- No CGO.
- No external tools.
- No root privileges required.
- Compatible with FreeBSD 14.x UFS2 images (little-endian, 64-bit
  inode pointers).

## Supported on-disk features

| Area | Status | Notes |
|---|---:|---|
| Superblock decode | yes | Primary superblock at `SBLOCK_UFS2` (65536). UFS1 magic rejected with a clear error. |
| Inode decode | yes | `ufs2_dinode` (256 bytes) — mode, size, link count, direct/indirect pointers. |
| Directory walk | yes | Variable-length `direct` entries; 4-byte aligned reclen; vacant slot skipping. |
| File data | yes | Direct (12) + single indirect block traversal. Double / triple indirect return `ErrUnsupportedIndirect`. |
| Sparse holes | yes | Zero block pointer materialises as implicit zero-fill. |
| Symlinks (inline) | yes | "Fast" symlinks decoded directly from the inode's block-pointer area. |
| Symlinks (spilled) | yes | Falls back to reading a data block when `di_blocks > 0`. |
| Cycle protection | yes | `maxSymlinkHops = 32` before `ErrTooManyLinks`. |

## Out of scope (sprint 2A)

- Write support (`WriteFile`, `MkDir`, `DeleteFile`, `DeleteDir`,
  `Rename` all return `ErrReadOnly`).
- UFS1 (deferred to sprint 3 if needed for older NetBSD / OpenBSD).
- Double / triple indirect blocks (loader.efi and FreeBSD kernel +
  modules fit comfortably in direct + single indirect).
- Extended attributes (`di_extb` / `di_extsize` block pointers).
- Soft-updates journaling metadata (read-only doesn't care).
- Checksums (UFS2 has optional metadata check hashes; we don't
  verify them).

## Module

```
github.com/go-filesystems/ufs
```

## Usage

```go
import (
    "log"

    "github.com/go-filesystems/ufs"
)

func main() {
    fs, err := ufs.OpenFile("/path/to/rootfs.img")
    if err != nil {
        log.Fatal(err)
    }
    defer fs.Close()

    entries, _ := fs.ListDir("/boot")
    for _, e := range entries {
        log.Printf("%s (ino %d, type %d)", e.Name(), e.Inode(), e.FileType())
    }

    kernel, err := fs.ReadFile("/boot/kernel/kernel")
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("kernel image: %d bytes", len(kernel))
}
```

`Open(rs io.ReaderAt, size int64)` is the lower-level constructor
when the caller already owns a backing store (e.g. an EFI block-IO
shim).

## Errors

All errors returned by the driver are wrapped sentinels — compare
with `errors.Is`:

- `ErrReadOnly` — write-side methods (sprint 2A is read-only).
- `ErrNotFound` — path component missing.
- `ErrInvalidPath` — empty or relative path passed in.
- `ErrBadSuperblock` — magic mismatch, UFS1, or sanity-check
  failure.
- `ErrUnsupportedIndirect` — file requires double / triple indirect
  traversal (deferred).
- `ErrNotDirectory` / `ErrNotRegular` / `ErrNotSymlink` — type
  mismatch.
- `ErrTooManyLinks` — symlink chain exceeded 32 hops.

## Tests

```
go test -race -coverprofile=cover.out ./...
go tool cover -func=cover.out | tail -1
```

Coverage as of release: ~96%. The remaining 4% is defensive
unreachable code and OS-fault paths (e.g. `os.File.Stat` failure
on an already-opened descriptor).

The test image is built deterministically in Go via
`buildFixture()` (see `fixture_test.go`) — no external `newfs` /
`makefs` dependency and no binary blob in the repo. Generation
procedure for a real-FreeBSD image is documented in
[`testdata/README.md`](testdata/README.md) for future sprints.

## References

- FreeBSD source — `sys/ufs/ffs/fs.h`, `sys/ufs/ufs/dinode.h`,
  `sys/ufs/ufs/dir.h`.
- The 4.4BSD design discussion of the Fast File System by Marshall
  McKusick et al.
