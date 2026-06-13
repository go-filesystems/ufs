package ufs

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func ffsTool(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, d := range []string{"/usr/local/sbin", "/usr/sbin", "/sbin", "/usr/bin"} {
		c := filepath.Join(d, name)
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// TestInterop_MakefsFFS builds NetBSD-style FFS images with makefs (FFSv1 and
// FFSv2, little-endian) and verifies the ufs driver reads them back. NetBSD /
// OpenBSD FFS shares the Berkeley on-disk format with FreeBSD UFS (FFSv1=UFS1,
// FFSv2=UFS2), so the ufs driver covers all three. Skipped without makefs.
func TestInterop_MakefsFFS(t *testing.T) {
	makefs := ffsTool("makefs")
	if makefs == "" {
		t.Skip("makefs not available")
	}
	src := t.TempDir()
	files := map[string][]byte{
		"readme.txt":     []byte("netbsd ffs via makefs\n"),
		"data.bin":       ffsPattern(20000),
		"sub/nested.txt": []byte("nested entry\n"),
	}
	for name, data := range files {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	for _, ver := range []string{"1", "2"} {
		t.Run("ffsv"+ver, func(t *testing.T) {
			img := filepath.Join(t.TempDir(), "ffs"+ver+".img")
			cmd := exec.Command(makefs, "-t", "ffs", "-B", "le",
				"-o", "version="+ver, "-s", "16m", img, src)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("makefs v%s: %v\n%s", ver, err, out)
			}
			fs, err := OpenFile(img)
			if err != nil {
				t.Fatalf("OpenFile (ffsv%s): %v", ver, err)
			}
			defer fs.Close()
			for name, want := range files {
				got, err := fs.ReadFile("/" + name)
				if err != nil {
					t.Errorf("[v%s] ReadFile(/%s): %v", ver, name, err)
					continue
				}
				if !bytes.Equal(got, want) {
					t.Errorf("[v%s] ReadFile(/%s): %d bytes, content mismatch (want %d)", ver, name, len(got), len(want))
				}
			}
			entries, err := fs.ListDir("/")
			if err != nil {
				t.Fatalf("[v%s] ListDir(/): %v", ver, err)
			}
			seen := map[string]bool{}
			for _, e := range entries {
				seen[e.Name()] = true
			}
			for _, n := range []string{"readme.txt", "data.bin", "sub"} {
				if !seen[n] {
					t.Errorf("[v%s] ListDir(/) missing %q", ver, n)
				}
			}
		})
	}
}

func ffsPattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*37 + 11)
	}
	return b
}
