package platform

import (
	"bytes"
	"io"
	"testing"
)

// countingReaderAt records how many bytes were requested from the underlying
// source. It is the memory/IO shape of the read, not just its result.
type countingReaderAt struct {
	data []byte
	read int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.read += int64(len(p))
	if off >= int64(len(c.data)) {
		return 0, io.EOF
	}
	n := copy(p, c.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// A5: a 4 KB request against a 1.5 MB file must touch 4 KB, not 1.5 MB. This is
// what fails when the range is applied after a full read.
func TestReadFileRangeReadsOnlyTheWindow(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789"), 1<<16) // 655 KB
	src := &countingReaderAt{data: data}

	got, err := readFileRange(src, int64(len(data)), 400000, 4096)
	if err != nil {
		t.Fatalf("readFileRange: %v", err)
	}
	if !bytes.Equal(got, data[400000:404096]) {
		t.Fatal("window mismatch")
	}
	if src.read > 4096 {
		t.Fatalf("read %d bytes from the source for a 4096-byte window (range applied after a full read)", src.read)
	}
}

func TestReadFileRangeSemantics(t *testing.T) {
	data := []byte("abcdefghij")
	src := &countingReaderAt{data: data}

	cases := []struct {
		name          string
		offset, limit int
		want          []byte
	}{
		{"whole", 0, 0, data},
		{"from offset", 2, 0, data[2:]},
		{"window", 2, 3, data[2:5]},
		{"limit past end", 8, 99, data[8:]},
		{"offset at EOF", 10, 0, nil},
		{"offset past EOF", 42, 5, nil},
		{"negative offset", -3, 2, data[:2]},
		{"zero limit means rest", 5, 0, data[5:]},
	}
	for _, tc := range cases {
		got, err := readFileRange(src, int64(len(data)), tc.offset, tc.limit)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
