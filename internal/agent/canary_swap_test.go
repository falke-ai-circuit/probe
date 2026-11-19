package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestBinary creates a file with the given content and returns its path.
func writeTestBinary(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// sha8Of returns the first 8 hex chars of the sha256 of content, the same
// suffix archiveExistingBackup appends to a backup generation.
func sha8Of(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:8]
}

func mustHash(t *testing.T, path string) string {
	t.Helper()
	h, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashFile(%s): %v", path, err)
	}
	return h
}

// TestSwapBinaryPathsSecondUpdateKeepsOriginal is the defect regression: a
// second successful swap must not clobber the genuine pre-first-update binary.
// Before the fix, rename(canonical -> .old) replaced the v1 backup with v2, so
// .old and canonical held the same sha and no pre-update binary survived.
func TestSwapBinaryPathsSecondUpdateKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "probe-client")

	const v1 = "genuine-pre-update-v1.18.6-binary"
	const v2 = "second-version-binary"
	const v3 = "third-version-binary"

	originalSHA := mustHash(t, writeTestBinary(t, canonical, v1))

	// First update: v1 -> v2; the pre-update binary is the .old backup.
	if _, err := swapBinaryPaths(writeTestBinary(t, canonical+".next", v2), canonical); err != nil {
		t.Fatalf("first swap: %v", err)
	}
	if got, err := os.ReadFile(canonical + ".old"); err != nil || string(got) != v1 {
		t.Fatalf("after first swap .old = %q (err=%v), want %q", got, err, v1)
	}
	if got := mustHash(t, canonical+".old"); got != originalSHA {
		t.Fatalf("after first swap .old sha %s != pre-update sha %s", got, originalSHA)
	}

	// Second update: v2 -> v3. The v1 backup must survive.
	if _, err := swapBinaryPaths(writeTestBinary(t, canonical+".next", v3), canonical); err != nil {
		t.Fatalf("second swap: %v", err)
	}

	if got, err := os.ReadFile(canonical); err != nil || string(got) != v3 {
		t.Fatalf("canonical content = %q (err=%v), want %q", got, err, v3)
	}
	if got, err := os.ReadFile(canonical + ".old"); err != nil || string(got) != v2 {
		t.Fatalf(".old content = %q (err=%v), want %q (immediately previous)", got, err, v2)
	}

	archived := canonical + ".old-" + sha8Of(v1)
	got, err := os.ReadFile(archived)
	if err != nil {
		t.Fatalf("pre-first-update binary not retrievable at %s: %v", archived, err)
	}
	if string(got) != v1 {
		t.Fatalf("archived content = %q, want %q", got, v1)
	}
	archivedSHA := mustHash(t, archived)
	if archivedSHA != originalSHA {
		t.Fatalf("archived sha %s != pre-update sha %s", archivedSHA, originalSHA)
	}
	t.Logf("two consecutive updates: original sha %s still retrievable at %s", originalSHA, archived)
}

// TestSwapBinaryPathsRollbackRestoresPreUpdateSHA forces the commit rename to
// fail and asserts the rollback put back the exact pre-update binary, proven by
// sha, with the expected-vs-restored sha reported in the returned error.
func TestSwapBinaryPathsRollbackRestoresPreUpdateSHA(t *testing.T) {
	orig := swapRename
	defer func() { swapRename = orig }()

	dir := t.TempDir()
	canonical := filepath.Join(dir, "probe-client")
	const oldBinary = "pre-update-binary-v1.18.6"
	const newBinary = "staged-canary-binary"

	preSHA := mustHash(t, writeTestBinary(t, canonical, oldBinary))
	newExe := writeTestBinary(t, canonical+".next", newBinary)

	calls := 0
	swapRename = func(from, to string) error {
		calls++
		// 1 = canonical -> .old ; 2 = .next -> canonical (the commit rename).
		if calls == 2 {
			return errors.New("forced commit-rename failure")
		}
		return os.Rename(from, to)
	}

	_, err := swapBinaryPaths(newExe, canonical)
	if err == nil {
		t.Fatal("swapBinaryPaths returned nil error, want commit-rename failure")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("error does not report a rollback: %v", err)
	}
	if !strings.Contains(err.Error(), preSHA) {
		t.Fatalf("error does not report the verified pre-update sha: %v", err)
	}

	if got := mustHash(t, canonical); got != preSHA {
		t.Fatalf("restored canonical sha %s != pre-update sha %s", got, preSHA)
	}
	if got, readErr := os.ReadFile(canonical); readErr != nil || string(got) != oldBinary {
		t.Fatalf("restored content = %q (err=%v), want %q", got, readErr, oldBinary)
	}
	if _, statErr := os.Stat(canonical + ".old"); !os.IsNotExist(statErr) {
		t.Fatalf(".old still exists after rollback (err=%v)", statErr)
	}
	t.Logf("rollback restored sha %s (expected %s)", mustHash(t, canonical), preSHA)
}

// TestSwapBinaryPathsRollbackFailureIsLoud proves an unverifiable rollback is
// reported as ROLLBACK FAILED instead of being swallowed.
func TestSwapBinaryPathsRollbackFailureIsLoud(t *testing.T) {
	orig := swapRename
	defer func() { swapRename = orig }()

	dir := t.TempDir()
	canonical := filepath.Join(dir, "probe-client")
	mustHash(t, writeTestBinary(t, canonical, "pre-update-binary"))
	newExe := writeTestBinary(t, canonical+".next", "staged-canary-binary")

	calls := 0
	swapRename = func(from, to string) error {
		calls++
		if calls == 2 {
			return errors.New("forced commit-rename failure")
		}
		if calls == 3 {
			return errors.New("forced restore-rename failure")
		}
		return os.Rename(from, to)
	}

	_, err := swapBinaryPaths(newExe, canonical)
	if err == nil {
		t.Fatal("swapBinaryPaths returned nil error, want loud rollback failure")
	}
	if !strings.Contains(err.Error(), "ROLLBACK FAILED") {
		t.Fatalf("error does not escalate the failed rollback: %v", err)
	}
}
