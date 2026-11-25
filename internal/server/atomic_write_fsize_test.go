//go:build linux

package server

import (
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

// TestRegistrySave_SurvivesFullDisk proves the persistence contract by
// interruption rather than by inspection.
//
// It writes two agent records, captures the file, then forces the NEXT save to
// hit RLIMIT_FSIZE. That is the kernel-level shape of a disk that fills
// mid-write: the write returns EFBIG after a partial transfer, exactly where a
// bare os.WriteFile leaves a truncated file behind. What must survive is the
// previously valid registry — not merely an error return.
//
// The test drives only the existing save path, so it compiles and runs against
// the pre-fix tree; there it fails on the truncation.
func TestRegistrySave_SurvivesFullDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	r := NewRegistry(path)
	defer r.Stop()

	r.Register("agent-1", "alpha", "v1.19.1", "linux", "amd64", "outbound",
		[]string{"exec", "file_write"})
	r.Register("agent-2", "beta", "v1.19.1", "windows", "amd64", "outbound",
		[]string{"exec", "file_write", "update"})

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("precondition: registry.json not written: %v", err)
	}
	var want map[string]*AgentRecord
	if err := json.Unmarshal(before, &want); err != nil {
		t.Fatalf("precondition: registry.json unparseable: %v", err)
	}
	if len(want) != 2 {
		t.Fatalf("precondition: expected 2 persisted agents, got %d", len(want))
	}

	// 256 bytes is smaller than any of the marshalled documents involved, so
	// both the old file's replacement and its temp file hit the cap.
	const capBytes = 256

	var saved syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &saved); err != nil {
		t.Skipf("RLIMIT_FSIZE unavailable on this host: %v", err)
	}
	// EFBIG raises SIGXFSZ, whose default action kills the process; ignore it
	// for the window so the save path can report the write error normally.
	signal.Ignore(syscall.SIGXFSZ)
	defer signal.Reset(syscall.SIGXFSZ)

	limited := syscall.Rlimit{Cur: capBytes, Max: saved.Max}
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Skipf("cannot lower RLIMIT_FSIZE on this host: %v", err)
	}

	// This save is the interrupted write.
	r.Register("agent-3", "gamma", "v1.19.1", "linux", "amd64", "outbound",
		[]string{"exec", "file_write", "update"})

	// Restore before any assertion so a failure message can be written.
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &saved); err != nil {
		t.Fatalf("could not restore RLIMIT_FSIZE: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("registry.json vanished after an interrupted write: %v", err)
	}
	var got map[string]*AgentRecord
	if err := json.Unmarshal(after, &got); err != nil {
		t.Fatalf("registry.json is unparseable after an interrupted write (truncated to %d bytes): %v\nbytes on disk: %q",
			len(after), err, string(after))
	}
	for _, id := range []string{"agent-1", "agent-2"} {
		if _, ok := got[id]; !ok {
			t.Errorf("previously persisted record %q was lost by an interrupted write; file holds %d record(s)", id, len(got))
		}
	}
}
