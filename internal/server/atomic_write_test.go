package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteFileAtomic_InterruptedBeforeRenameKeepsPreviousFile stops the write
// at the exact instant a crash would: the new bytes are on disk under the temp
// name, the rename has not happened. The previously valid file must still be
// there, intact and parseable, and the temp file must not be left behind to be
// mistaken for state.
func TestWriteFileAtomic_InterruptedBeforeRenameKeepsPreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	old := []byte(`{"keep":"this"}`)
	if err := writeFileAtomic(path, old, 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	interrupted := errors.New("simulated crash before rename")
	atomicWriteStageHook = func(stage string) error {
		if stage == atomicStageBeforeRename {
			return interrupted
		}
		return nil
	}
	defer func() { atomicWriteStageHook = nil }()

	err := writeFileAtomic(path, []byte(`{"replacement":true}`), 0o644)
	if err == nil {
		t.Fatal("expected the interrupted write to report an error")
	}

	got, rerr := os.ReadFile(path)
	if rerr != nil {
		t.Fatalf("previous file is gone after an interrupted write: %v", rerr)
	}
	if string(got) != string(old) {
		t.Fatalf("previous file was modified by a write that never published: got %q, want %q", got, old)
	}
	if _, serr := os.Stat(path + ".tmp"); !os.IsNotExist(serr) {
		t.Errorf("temp file %s.tmp survived the interrupted write (err=%v)", path, serr)
	}
}

// TestWriteFileAtomic_InterruptedAfterRenameHasNewData pins the residual
// window rather than claiming atomicity in general: once the rename has
// landed, the new content IS the file even if the write reports an error
// afterwards. Nothing here promises durability of the directory entry.
func TestWriteFileAtomic_InterruptedAfterRenameHasNewData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writeFileAtomic(path, []byte(`{"old":true}`), 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	atomicWriteStageHook = func(stage string) error {
		if stage == atomicStageAfterRename {
			return errors.New("simulated crash after rename")
		}
		return nil
	}
	defer func() { atomicWriteStageHook = nil }()

	if err := writeFileAtomic(path, []byte(`{"new":true}`), 0o644); err == nil {
		t.Fatal("expected the post-rename interruption to report an error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after rename: %v", err)
	}
	if !strings.Contains(string(got), `"new":true`) {
		t.Fatalf("rename did not publish the new content: %q", got)
	}
}

// TestWriteFileAtomic_ModeAndDir documents the two side conditions every caller
// relies on: the parent directory is created, and the requested permissions
// survive the rename.
func TestWriteFileAtomic_ModeAndDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "state.json")
	if err := writeFileAtomic(path, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write into a missing directory: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode after rename: got %o, want 600", perm)
	}
}
