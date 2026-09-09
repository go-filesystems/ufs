package ufs

import (
	"bytes"
	"io"
	"testing"
)

// A real image, built the way this package's own tests build one.
func TestOpenReaderOpensWhatOpenOpens(t *testing.T) {
	const size = 16 << 20
	buf := &sizedBuffer{b: make([]byte, size)}
	if _, err := Mkfs(buf, size); err != nil {
		t.Fatal(err)
	}
	through, err := OpenReader(bytes.NewReader(buf.b), size)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer through.Close()
	if _, err := through.ListDir("/"); err != nil {
		t.Errorf("listing through OpenReader: %v", err)
	}
}

// What the wrapper adds beyond Open is the error path, and the one thing that
// can go wrong there: a typed nil inside an interface is NOT nil, so a caller
// checking the value rather than the error would be told it has a filesystem.
func TestOpenReaderReturnsNothingWhenItFails(t *testing.T) {
	fs, err := OpenReader(bytes.NewReader(make([]byte, 4096)), 4096)
	if err == nil {
		t.Fatal("OpenReader accepted 4 KiB of zeros as an image")
	}
	if fs != nil {
		t.Error("OpenReader returned an error AND a filesystem")
	}
}

// sizedBuffer is an image in memory that can be read and written at any
// offset, which is what Mkfs writes into.
type sizedBuffer struct{ b []byte }

func (s *sizedBuffer) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (s *sizedBuffer) WriteAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > int64(len(s.b)) {
		return 0, io.ErrShortWrite
	}
	return copy(s.b[off:], p), nil
}
