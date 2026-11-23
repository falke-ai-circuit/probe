package server

import (
	"os"
	"testing"
)

// P8-06 — Range parsing is a contract with the client (RFC 7233 semantics):
// satisfiable ranges are served, a range that starts past EOF is 416 with
// "bytes */size", and a malformed header is refused rather than guessed.

func TestParseByteRange(t *testing.T) {
	const size = 1000
	cases := []struct {
		name          string
		header        string
		wantStart     int64
		wantEnd       int64
		wantOK        bool
		wantUnsat     bool
	}{
		{"closed range", "bytes=100-199", 100, 199, true, false},
		{"open end", "bytes=900-", 900, 999, true, false},
		{"suffix", "bytes=-50", 950, 999, true, false},
		{"end past eof is clamped", "bytes=900-5000", 900, 999, true, false},
		{"whole file", "bytes=0-", 0, 999, true, false},
		{"start at eof is unsatisfiable", "bytes=1000-", 0, 0, false, true},
		{"start past eof is unsatisfiable", "bytes=1500-1600", 0, 0, false, true},
		{"malformed", "bytes=abc-def", 0, 0, false, false},
		{"wrong unit", "items=0-10", 0, 0, false, false},
		{"suffix larger than file", "bytes=-5000", 0, 999, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end, ok, unsat := parseByteRange(tc.header, size)
			if ok != tc.wantOK || unsat != tc.wantUnsat {
				t.Fatalf("ok=%v unsat=%v, want ok=%v unsat=%v", ok, unsat, tc.wantOK, tc.wantUnsat)
			}
			if ok && (start != tc.wantStart || end != tc.wantEnd) {
				t.Fatalf("range = %d-%d, want %d-%d", start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestParseContentRange(t *testing.T) {
	start, end, total, ok := parseContentRange("bytes 0-262143/52428800")
	if !ok || start != 0 || end != 262143 || total != 52428800 {
		t.Fatalf("got %d-%d/%d ok=%v", start, end, total, ok)
	}
	if _, _, _, ok := parseContentRange("garbage"); ok {
		t.Fatal("garbage Content-Range accepted")
	}
}

// The cap knob is the documented escape hatch (OD-3): 0 means unlimited.
func TestFSReadMaxBytesKnob(t *testing.T) {
	t.Setenv("PROBE_FS_READ_MAX_BYTES", "")
	if got := fsReadMaxBytes(); got != 8<<20 {
		t.Fatalf("default cap = %d, want 8 MiB", got)
	}
	t.Setenv("PROBE_FS_READ_MAX_BYTES", "0")
	if got := fsReadMaxBytes(); got != 0 {
		t.Fatalf("knob 0 = %d, want unlimited (0)", got)
	}
	t.Setenv("PROBE_FS_READ_MAX_BYTES", "1024")
	if got := fsReadMaxBytes(); got != 1024 {
		t.Fatalf("knob 1024 = %d", got)
	}
	os.Unsetenv("PROBE_FS_READ_MAX_BYTES")
}
