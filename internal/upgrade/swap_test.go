package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeFS reproduces the two filesystem facts the swap depends on, without a
// Windows node:
//
//   - on Windows a rename that would REPLACE a running image is refused
//     (ERROR_SHARING_VIOLATION), while renaming the running image aside is
//     allowed;
//   - a forced copy over a running image reports success and changes nothing —
//     the "Copy-Item -Force" trap.
//
// It records every operation so a test can assert not just the end state but
// that the verification read happened AFTER the rename it is meant to prove.
type fakeFS struct {
	files map[string][]byte
	// running is the path of the image the OS considers in use.
	running string
	// windows enables the replace-a-running-image refusal.
	windows bool
	// silentPark is a path whose rename reports success but moves nothing,
	// the signature of a copy-over-the-live-image step.
	silentPark string
	// failRenameOn forces the Nth rename (1-based) to fail.
	failRenameOn int
	renameCalls  int
	copyCalls    int
	ops          []string
}

func newFakeFS(windows bool, running string, files map[string]string) *fakeFS {
	f := &fakeFS{files: map[string][]byte{}, running: running, windows: windows}
	for p, body := range files {
		f.files[p] = []byte(body)
	}
	return f
}

func (f *fakeFS) Stat(path string) (SwapFile, error) {
	f.ops = append(f.ops, "stat:"+path)
	b, ok := f.files[path]
	if !ok {
		return SwapFile{}, nil
	}
	return SwapFile{Exists: true, Size: int64(len(b))}, nil
}

func (f *fakeFS) Hash(path string) (string, error) {
	f.ops = append(f.ops, "hash:"+path)
	b, ok := f.files[path]
	if !ok {
		return "", errors.New("no such file")
	}
	return SHA256Hex(b), nil
}

func (f *fakeFS) Rename(from, to string) error {
	f.renameCalls++
	f.ops = append(f.ops, "rename:"+from+"->"+to)
	if f.failRenameOn == f.renameCalls {
		return errors.New("forced rename failure")
	}
	if f.silentPark != "" && from == f.silentPark {
		// Reports success, moves nothing: the defect this guard exists for.
		return nil
	}
	body, ok := f.files[from]
	if !ok {
		return errors.New("no such file: " + from)
	}
	if f.windows && to == f.running {
		if _, occupied := f.files[to]; occupied {
			return errors.New("ERROR_SHARING_VIOLATION: cannot replace a running image")
		}
	}
	f.files[to] = body
	delete(f.files, from)
	return nil
}

// CopyOver mimics a forced copy onto dst. Over the running image it returns nil
// and leaves the old bytes in place — exactly the trap.
func (f *fakeFS) CopyOver(dst string, body string) error {
	f.copyCalls++
	f.ops = append(f.ops, "copy:"+dst)
	if f.windows && dst == f.running {
		return nil
	}
	f.files[dst] = []byte(body)
	return nil
}

func (f *fakeFS) body(path string) string { return string(f.files[path]) }

// TestSwapLinuxReplacesRunningBinary is the Linux acceptance: the rename-over
// path must actually put the new bytes at the executable path. The claim is
// made by hashing the path before and after — the shas must differ and the
// post-swap sha must be the staged binary's.
func TestSwapLinuxReplacesRunningBinary(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "probe-client")
	staged := canonical + ".next"

	const oldBinary = "old-binary-v1.19.1-payload"
	const newBinary = "new-binary-v1.19.2-payload-different-length"

	if err := os.WriteFile(canonical, []byte(oldBinary), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte(newBinary), 0755); err != nil {
		t.Fatal(err)
	}
	preSHA, err := FileSHA256(canonical)
	if err != nil {
		t.Fatalf("pre sha: %v", err)
	}

	res, err := SwapRunningBinary(SwapRequest{
		Canonical: canonical,
		Staged:    staged,
		Platform:  PlatformLinux,
		FS:        OSSwapFS{},
	})
	if err != nil {
		t.Fatalf("SwapRunningBinary: %v", err)
	}

	postSHA, err := FileSHA256(canonical)
	if err != nil {
		t.Fatalf("post sha: %v", err)
	}
	if preSHA == postSHA {
		t.Fatalf("canonical sha unchanged after Linux rename-over: %s (the swap did nothing)", postSHA)
	}
	wantSHA := SHA256Hex([]byte(newBinary))
	if postSHA != wantSHA {
		t.Fatalf("canonical sha %s, want the staged binary's %s", postSHA, wantSHA)
	}
	if res.AfterSHA != wantSHA || res.BeforeSHA != preSHA {
		t.Fatalf("result before=%s after=%s, want before=%s after=%s",
			ShortDigest(res.BeforeSHA), ShortDigest(res.AfterSHA), ShortDigest(preSHA), ShortDigest(wantSHA))
	}
	if got, _ := os.ReadFile(canonical); string(got) != newBinary {
		t.Fatalf("canonical bytes = %q, want the new binary", got)
	}
	// The pre-swap image is the rollback source and must still be on disk.
	if got, _ := os.ReadFile(canonical + ".old"); string(got) != oldBinary {
		t.Fatalf(".old bytes = %q, want the pre-swap binary", got)
	}
	t.Logf("linux rename-over: %s -> %s", ShortDigest(preSHA), ShortDigest(postSHA))
}

// TestSwapWindowsStagedAndVerifiedOnFakeFS proves the Windows ORDER on a fake
// filesystem that enforces the Windows rules: park the running image to .old,
// promote .next onto the freed canonical name, then read size+sha back. It also
// asserts no copy was ever used to place the binary.
//
// The Windows RUNTIME behaviour is UNPROVEN-ON-HOST: no Windows node is
// reachable from this build, so this test proves the ordering and the
// verification contract against a model of the platform, not the platform
// itself.
func TestSwapWindowsStagedAndVerifiedOnFakeFS(t *testing.T) {
	const canonical = `C:\Probe\probe-client.exe`
	const staged = canonical + `.next`
	const backup = canonical + `.old`
	const oldBinary = "windows-old-binary-v1.19.1"
	const newBinary = "windows-new-binary-v1.19.2-longer"

	fs := newFakeFS(true, canonical, map[string]string{
		canonical: oldBinary,
		staged:    newBinary,
	})
	preSHA := SHA256Hex([]byte(oldBinary))
	newSHA := SHA256Hex([]byte(newBinary))

	res, err := SwapRunningBinary(SwapRequest{
		Canonical: canonical,
		Staged:    staged,
		Platform:  PlatformWindows,
		FS:        fs,
	})
	if err != nil {
		t.Fatalf("SwapRunningBinary(windows): %v", err)
	}

	if res.Backup != backup || res.Staged != staged {
		t.Fatalf("backup=%q staged=%q, want %q / %q", res.Backup, res.Staged, backup, staged)
	}
	if got := fs.body(backup); got != oldBinary {
		t.Fatalf(".old holds %q, want the parked running image", got)
	}
	if got := fs.body(canonical); got != newBinary {
		t.Fatalf("canonical holds %q, want the promoted .next", got)
	}
	if res.BeforeSHA != preSHA || res.BeforeSize != int64(len(oldBinary)) {
		t.Fatalf("before=%s/%d, want %s/%d", ShortDigest(res.BeforeSHA), res.BeforeSize, ShortDigest(preSHA), len(oldBinary))
	}
	if res.AfterSHA != newSHA || res.AfterSize != int64(len(newBinary)) {
		t.Fatalf("after=%s/%d, want %s/%d", ShortDigest(res.AfterSHA), res.AfterSize, ShortDigest(newSHA), len(newBinary))
	}
	if res.BackupSHA != preSHA || res.BackupSize != int64(len(oldBinary)) {
		t.Fatalf("backup proof %s/%d, want %s/%d", ShortDigest(res.BackupSHA), res.BackupSize, ShortDigest(preSHA), len(oldBinary))
	}
	// Park then promote, both renames: never a copy onto the running image.
	if fs.renameCalls != 2 {
		t.Fatalf("rename calls = %d, want 2 (park + promote)", fs.renameCalls)
	}
	if fs.copyCalls != 0 {
		t.Fatalf("copy calls = %d, want 0 (a copy over a running exe silently does nothing)", fs.copyCalls)
	}

	if runtime.GOOS != "windows" {
		t.Log("UNPROVEN-ON-HOST: Windows runtime swap not executed — no Windows node reachable; " +
			"this test proves the park/promote order and the read-back verification against a Windows-semantics fake")
	}
}

// TestSwapWindowsRefusesWhenParkLeavesCanonicalOccupied encodes the trap: a
// park that reports success but frees nothing leaves the canonical name
// occupied. On Windows the new binary cannot be placed there without
// overwriting a running image, so the swap must refuse loudly instead of
// claiming success — the defect that makes a "successful" update leave the old
// binary in place.
func TestSwapWindowsRefusesWhenParkLeavesCanonicalOccupied(t *testing.T) {
	const canonical = `C:\Probe\probe-client.exe`
	const staged = canonical + `.next`
	const oldBinary = "windows-old-binary"
	const newBinary = "windows-new-binary"

	fs := newFakeFS(true, canonical, map[string]string{
		canonical: oldBinary,
		staged:    newBinary,
	})
	fs.silentPark = canonical

	_, err := SwapRunningBinary(SwapRequest{
		Canonical: canonical,
		Staged:    staged,
		Platform:  PlatformWindows,
		FS:        fs,
	})
	if err == nil {
		t.Fatal("swap reported success while the running image was never moved aside")
	}
	for _, want := range []string{"still occupied after park", "silently does nothing on Windows"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
	if got := fs.body(canonical); got != oldBinary {
		t.Fatalf("canonical holds %q after refusal, want the untouched pre-swap binary", got)
	}
}

// TestSwapRollbackProvesSizeAndSHA reads the rollback result the way the
// acceptance requires: size and sha256 of the restored path AFTER the restore,
// not the restore's return code. The op log proves the read happened after the
// restore rename.
func TestSwapRollbackProvesSizeAndSHA(t *testing.T) {
	const canonical = `C:\Probe\probe-client.exe`
	const staged = canonical + `.next`
	const oldBinary = "pre-swap-binary-that-must-survive"
	const newBinary = "staged-binary-that-will-fail-to-promote"

	fs := newFakeFS(false, "", map[string]string{
		canonical: oldBinary,
		staged:    newBinary,
	})
	fs.failRenameOn = 2 // 1 = park, 2 = promote

	preSHA := SHA256Hex([]byte(oldBinary))
	_, err := SwapRunningBinary(SwapRequest{
		Canonical: canonical,
		Staged:    staged,
		Platform:  PlatformLinux,
		FS:        fs,
	})
	if err == nil {
		t.Fatal("swap reported success although the promotion rename failed")
	}
	if !strings.Contains(err.Error(), "rolled back") || !strings.Contains(err.Error(), preSHA) {
		t.Fatalf("error does not report a verified rollback with the pre-swap sha: %v", err)
	}
	// Snapshot the swap's own operation order before this test reads anything,
	// so the assertion below is about the swap's verification, not ours.
	swapOps := append([]string(nil), fs.ops...)

	// The restored image: read from disk, not inferred from the rename result.
	st, statErr := fs.Stat(canonical)
	if statErr != nil || !st.Exists {
		t.Fatalf("restored canonical missing (err=%v)", statErr)
	}
	restoredSHA, hashErr := fs.Hash(canonical)
	if hashErr != nil {
		t.Fatalf("hash restored: %v", hashErr)
	}
	if st.Size != int64(len(oldBinary)) {
		t.Fatalf("restored size = %d, want pre-swap %d", st.Size, len(oldBinary))
	}
	if restoredSHA != preSHA {
		t.Fatalf("restored sha = %s, want pre-swap %s", restoredSHA, preSHA)
	}
	if got := fs.body(canonical); got != oldBinary {
		t.Fatalf("restored bytes = %q, want %q", got, oldBinary)
	}

	// Ordering: the last two reads the swap performed are of the restored
	// canonical, i.e. after the restore rename — the check is made on the
	// bytes, never on the code.
	if len(swapOps) < 3 || swapOps[len(swapOps)-2] != "stat:"+canonical || swapOps[len(swapOps)-1] != "hash:"+canonical {
		t.Fatalf("verification reads did not follow the restore; tail ops = %v", swapOps)
	}
	if !strings.HasPrefix(swapOps[len(swapOps)-3], "rename:") {
		t.Fatalf("no restore rename immediately before the verification reads; tail ops = %v", swapOps)
	}
	t.Logf("rollback: restored size=%d sha=%s (read after restore)", st.Size, ShortDigest(restoredSHA))
}

// TestVerifyPlacedImageCatchesSilentCopy is the anti-defect test: a forced copy
// over the running image returns nil and moves no bytes. The verify step must
// catch that, which is only possible because it reads size and sha256 instead of
// trusting the copy's return value.
func TestVerifyPlacedImageCatchesSilentCopy(t *testing.T) {
	const canonical = `C:\Probe\probe-client.exe`
	const oldBinary = "old-running-binary"
	const newBinary = "new-binary-of-a-different-length"

	fs := newFakeFS(true, canonical, map[string]string{canonical: oldBinary})
	if err := fs.CopyOver(canonical, newBinary); err != nil {
		t.Fatalf("CopyOver: %v", err)
	}
	// The trap: the copy "succeeded" (nil error) and changed nothing.
	if got := fs.body(canonical); got != oldBinary {
		t.Fatalf("fake copy unexpectedly replaced the image: %q", got)
	}
	err := VerifyPlacedImage(fs, canonical, int64(len(newBinary)), SHA256Hex([]byte(newBinary)))
	if err == nil {
		t.Fatal("VerifyPlacedImage accepted a copy that moved no bytes")
	}
	if !strings.Contains(err.Error(), "size is") {
		t.Fatalf("error does not name the size mismatch: %v", err)
	}
}

// TestSwapRejectsWrongStagingName pins the staged naming contract: the promoted
// file must be <canonical>.next, so a stray path can never be promoted onto the
// executable.
func TestSwapRejectsWrongStagingName(t *testing.T) {
	const canonical = `C:\Probe\probe-client.exe`
	fs := newFakeFS(true, canonical, map[string]string{
		canonical:                      "old",
		`C:\Probe\somewhere-else.exe`: "new",
	})
	_, err := SwapRunningBinary(SwapRequest{
		Canonical: canonical,
		Staged:    `C:\Probe\somewhere-else.exe`,
		Platform:  PlatformWindows,
		FS:        fs,
	})
	if err == nil || !strings.Contains(err.Error(), "is not") {
		t.Fatalf("swap accepted a non-.next staged path: %v", err)
	}
}
