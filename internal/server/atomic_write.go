package server

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// atomicWriteStages are the two points at which a test may interrupt a write.
// The names are the stage that has just COMPLETED, so "before_rename" means
// the data is on disk under the temp name and the name has not been published.
const (
	atomicStageBeforeRename = "before_rename"
	atomicStageAfterRename  = "after_rename"
)

// atomicWriteStageHook lets a test stop a write between stages, which is the
// only way to reproduce a crash at that instant from inside the process. It is
// nil in production; tests set it for the duration of one call and reset it.
// Not safe for concurrent use by design — a hook is a fault injector, not a
// feature.
var atomicWriteStageHook func(stage string) error

// writeFileAtomic replaces path with data so a reader sees either the previous
// file or the new one, never a partial one.
//
// Contract for callers: pass the finished bytes. The helper creates the parent
// directory, writes <path>.tmp in the SAME directory (so the rename stays on
// one filesystem), fsyncs the data, closes, renames over path, then fsyncs the
// directory so the new name is durable. perm is applied to the temp file and
// therefore survives the rename.
//
// What it does not do: it holds no lock; callers keep their existing locking
// (every current caller already serialises its own saves). A process killed
// between the rename and the directory fsync can still leave the rename
// unrecorded — the data is in the file, the name may not be. That window is
// narrowed, not closed; only the directory sync below reduces it.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if path == "" {
		return errors.New("atomic write: empty path")
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("atomic write %s: mkdir: %w", path, err)
		}
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("atomic write %s: open temp: %w", path, err)
	}

	werr := error(nil)
	n, werr := f.Write(data)
	if werr == nil && n < len(data) {
		werr = io.ErrShortWrite
	}
	if werr == nil {
		werr = f.Sync() // data must reach the disk before the name is published
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomic write %s: %w", path, werr)
	}

	if atomicWriteStageHook != nil {
		if err := atomicWriteStageHook(atomicStageBeforeRename); err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("atomic write %s: interrupted before rename: %w", path, err)
		}
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("atomic write %s: rename: %w", path, err)
	}

	if atomicWriteStageHook != nil {
		if err := atomicWriteStageHook(atomicStageAfterRename); err != nil {
			return fmt.Errorf("atomic write %s: interrupted after rename: %w", path, err)
		}
	}

	// A rename is not durable until the directory entry itself is synced.
	// Best-effort: a failure here does not mean the write failed, and every
	// caller of this helper reports a write result, not a durability verdict.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
