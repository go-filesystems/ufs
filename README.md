# ufs

Pure-Go driver for the FreeBSD UFS2 on-disk format. Reads and writes
both supported.

## Status

Sprint 2C-A: write surface + `Mkfs` entry point sufficient to
construct a UFS2 partition entirely in-process for FreeBSD's
`loader.efi` consumption.

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

## Write support

Sprint 2C-A adds a coherent-image writer on top of the read-only
surface from sprint 2A. The writer satisfies the same
`filesystem.Filesystem` interface — `WriteFile`, `MkDir`,
`DeleteFile`, `DeleteDir`, `Rename` — plus the optional `Symlinker`
capability, and exposes a `Mkfs(w, sizeBytes)` entry point that
formats a fresh UFS2 image into an empty backing store.

Constructors:

- `Open(rs, size)` — read-only handle.
- `OpenRW(rs, wa, size)` — read + write on an existing image.
- `Mkfs(rw, sizeBytes)` — fresh format; returns an `*FS` already
  open for read+write.

Operations are flushed coherently — each individual call leaves the
on-disk image in a well-formed state (superblock + per-cg counters
+ inode bitmap + block bitmap all consistent). A crash mid-call
leaves a fresh-but-potentially-leaky filesystem rather than a torn
one.

**Soft updates / journaling are deliberately absent.** The on-disk
format does not require them — soft updates are a runtime
optimisation. FreeBSD's `fsck_ufs` will recover any UFS2 image we
produce on first mount. For our use case (producing a UFS2 boot
partition in-process from `tamago-uefi`) that is the correct
trade-off.

### Out of scope (sprint 2C-A)

- Double / triple indirect blocks. Files larger than ~2 MiB at the
  default 4 KiB block size return `ErrFileTooLarge`. loader.efi
  plus a FreeBSD `/boot/kernel` tree fits comfortably below this
  limit.
- Cluster summary updates (`fs_clustersumoff`). These are an
  allocator-locality hint, not a correctness requirement.
- Extended attributes (`di_extb` / `di_extsize`).
- Snapshots, gjournal.

### Out of scope (sprint 2A — still applies)

- UFS1 (deferred to sprint 3 if needed for older NetBSD / OpenBSD).
- Metadata-checksum verification.

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

`Open(rs io.ReaderAt, size int64)` is the lower-level read-only
constructor when the caller already owns a backing store (e.g. an
EFI block-IO shim). `OpenRW` adds the writer side; `Mkfs` formats
a fresh image.

Building a boot partition from scratch:

```go
img := make([]byte, 16*1024*1024)
ba := newBackingArray(img) // any io.ReaderAt+io.WriterAt
fs, err := ufs.Mkfs(ba, int64(len(img)))
if err != nil {
    log.Fatal(err)
}
_ = fs.MkDir("/boot", 0o755)
_ = fs.MkDir("/boot/kernel", 0o755)
_ = fs.WriteFile("/boot/loader.conf", []byte("kernel=\"kernel\"\n"), 0o644)
_ = fs.WriteFile("/boot/kernel/kernel", kernelBytes, 0o755)
```

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
- `ErrNoSpace` — no free inode or block in any cylinder group.
- `ErrExists` — write-side target path already exists.
- `ErrNotEmpty` — `DeleteDir` on a non-empty directory.
- `ErrFileTooLarge` — file would require double/triple indirect
  blocks the writer does not implement.

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
