package upgrade

import (
	"errors"
	"testing"
)

// p1205FS is a minimal SwapFS with a scripted rename failure, used to pin the
// code a failed swap reports. It is deliberately independent of the fakeFS
// fixtures used elsewhere: this test is about the code on the error, not about
// the file semantics fakeFS models.
type p1205FS struct {
	files       map[string][]byte
	renameCalls int
	failOn      map[int]bool
}

func newP1205FS(files map[string]string) *p1205FS {
	f := &p1205FS{files: map[string][]byte{}, failOn: map[int]bool{}}
	for p, body := range files {
		f.files[p] = []byte(body)
	}
	return f
}

func (f *p1205FS) Stat(path string) (SwapFile, error) {
	b, ok := f.files[path]
	if !ok {
		return SwapFile{}, nil
	}
	return SwapFile{Exists: true, Size: int64(len(b))}, nil
}

func (f *p1205FS) Hash(path string) (string, error) {
	b, ok := f.files[path]
	if !ok {
		return "", errors.New("no such file: " + path)
	}
	return SHA256Hex(b), nil
}

func (f *p1205FS) Rename(from, to string) error {
	f.renameCalls++
	if f.failOn[f.renameCalls] {
		return errors.New("forced rename failure")
	}
	b, ok := f.files[from]
	if !ok {
		return errors.New("no such file: " + from)
	}
	f.files[to] = b
	delete(f.files, from)
	return nil
}

func p1205Codes(t *testing.T, failOn map[int]bool) (*StepError, error) {
	t.Helper()
	const canonical = `C:\Probe\probe-agent.exe`
	fs := newP1205FS(map[string]string{
		canonical:        "pre-update-binary",
		canonical + ".next": "staged-new-binary",
	})
	fs.failOn = failOn

	_, err := SwapRunningBinary(SwapRequest{
		Canonical: canonical,
		Staged:    canonical + ".next",
		Platform:  PlatformLinux,
		FS:        fs,
	})
	if err == nil {
		t.Fatal("swap reported success although the promotion rename failed")
	}
	var se *StepError
	if !errors.As(err, &se) {
		t.Fatalf("error is not a *StepError: %v", err)
	}
	return se, err
}

// TestP1205RecoveredRollbackHasItsOwnCode is the F3 acceptance: when the swap
// fails but the pre-update image was restored and verified, the code must not be
// ROLLBACK_FAILED — the operator and any automated reader must be able to tell
// "host intact at the old version" from "restore did not verify".
func TestP1205RecoveredRollbackHasItsOwnCode(t *testing.T) {
	// rename 1 = park, 2 = promote (fails), 3 = restore (succeeds).
	se, err := p1205Codes(t, map[int]bool{2: true})
	if se.Code == CodeRollbackFailed {
		t.Fatalf("recovered branch returned %s; log reads ROLLBACK_FAILED on a rollback that verified: %v", se.Code, err)
	}
	if se.Code != CodeRollbackRecovered {
		t.Fatalf("recovered branch code = %s, want %s", se.Code, CodeRollbackRecovered)
	}
	if se.Step != "SW-24" {
		t.Fatalf("step = %s, want SW-24 (the promote step)", se.Step)
	}
}

// TestP1205UnrecoveredRollbackKeepsFailedCode is the other half of the same
// acceptance: a restore that cannot verify still reports ROLLBACK_FAILED.
func TestP1205UnrecoveredRollbackKeepsFailedCode(t *testing.T) {
	// 2 = promote fails, 3 = restore fails → the rollback did not recover.
	se, err := p1205Codes(t, map[int]bool{2: true, 3: true})
	if se.Code != CodeRollbackFailed {
		t.Fatalf("unrecovered branch code = %s, want %s: %v", se.Code, CodeRollbackFailed, err)
	}
}
