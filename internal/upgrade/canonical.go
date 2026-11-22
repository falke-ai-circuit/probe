package upgrade

import (
	"fmt"
	"strings"
)

// Code strings used by the caller side of the sequence and by the journal.
const (
	CodeCanonicalUnresolved = "CANONICAL_UNRESOLVED"
	CodeCanonicalMissing    = "CANONICAL_MISSING"
	CodeSandboxRefused      = "SANDBOX_REFUSED"
	CodeWriteFailed         = "WRITE_FAILED"
	CodeDigestMismatch      = "DIGEST_MISMATCH"
	CodeSizeMismatch        = "SIZE_MISMATCH"
	CodeManifestInvalid     = "MANIFEST_INVALID"
	CodePreflightRefused    = "PREFLIGHT_REFUSED"
	CodeLaunchFailed        = "LAUNCH_FAILED"
	CodeJournalUnreadable   = "JOURNAL_UNREADABLE"
	// CodeVersionMismatch: the artifact's own --version does not match the
	// manifest. The sequence refuses to swap (P8-11 gap 2).
	CodeVersionMismatch = "VERSION_MISMATCH"
	CodeRollbackFailed  = "ROLLBACK_FAILED"
)

// CanonicalSource records which input the canonical path came from.
type CanonicalSource string

const (
	// SourceManifest means the caller supplied manifest.canonical.
	SourceManifest CanonicalSource = "manifest"
	// SourceDiscovered means the path came from A4's ancestor walk.
	SourceDiscovered CanonicalSource = "discovered"
)

// CanonicalResolution is the outcome of SW-00.
type CanonicalResolution struct {
	Canonical  string
	Source     CanonicalSource
	Discovered string
	Supplied   string
}

// StatFunc probes a path through the agent (fs-stat).
type StatFunc func(path string) (StatResult, error)

// StripStagedSuffix removes one ".next" and then one ".old" suffix, in that
// order, from a Windows path. This is C11's anchor applied from outside the
// process: a binary started from a staging path still reports the staging name
// from os.Executable().
func StripStagedSuffix(p string) string {
	s := CleanWinPath(p)
	lower := strings.ToLower(s)
	if strings.HasSuffix(lower, StagedSuffix) {
		s = s[:len(s)-len(StagedSuffix)]
		lower = strings.ToLower(s)
	}
	if strings.HasSuffix(lower, BackupSuffix) {
		s = s[:len(s)-len(BackupSuffix)]
	}
	return CleanWinPath(s)
}

// ResolveCanonical implements SW-00: one path, two sources, disagreement is
// fatal. Precedence is the caller-supplied manifest path, but a disagreement
// between the two sources is refused rather than silently resolved.
func ResolveCanonical(discoveredExe, supplied string, stat StatFunc) (CanonicalResolution, error) {
	res := CanonicalResolution{
		Discovered: CleanWinPath(discoveredExe),
		Supplied:   CleanWinPath(supplied),
	}

	if res.Supplied != "" && !IsAbsWinPath(res.Supplied) {
		return res, stepErr("SW-00", CodeCanonicalUnresolved,
			fmt.Errorf("manifest canonical %q is not an absolute path", res.Supplied))
	}
	if res.Discovered != "" && !IsAbsWinPath(res.Discovered) {
		return res, stepErr("SW-00", CodeCanonicalUnresolved,
			fmt.Errorf("discovered exe %q is not an absolute path", res.Discovered))
	}

	switch {
	case res.Supplied != "":
		res.Canonical = StripStagedSuffix(res.Supplied)
		res.Source = SourceManifest
	case res.Discovered != "":
		res.Canonical = StripStagedSuffix(res.Discovered)
		res.Source = SourceDiscovered
	default:
		return res, stepErr("SW-00", CodeCanonicalUnresolved,
			fmt.Errorf("neither manifest.canonical nor a discovered exe path was supplied"))
	}

	if res.Supplied != "" && res.Discovered != "" {
		fromDiscovered := StripStagedSuffix(res.Discovered)
		if !EqualWinPath(fromDiscovered, res.Canonical) {
			return res, stepErr("SW-00", CodeCanonicalUnresolved,
				fmt.Errorf("exe=%s manifest=%s", res.Discovered, res.Canonical))
		}
	}

	if stat == nil {
		return res, nil
	}
	st, err := stat(res.Canonical)
	if err != nil {
		return res, stepErr("SW-00", CodeCanonicalUnresolved, err)
	}
	if !st.Exists {
		return res, stepErr("SW-00", CodeCanonicalMissing,
			fmt.Errorf("exe=%s canonical=%s", res.Discovered, res.Canonical))
	}
	if st.IsDir {
		return res, stepErr("SW-00", CodeCanonicalUnresolved,
			fmt.Errorf("canonical %q is a directory", res.Canonical))
	}
	return res, nil
}

// SandboxGate implements G-SANDBOX: every fs-* operation on the staged path is
// path-checked against sandbox_dir in sandboxed, standard and read-only tiers,
// so the unit may only run when sandbox_dir is empty or contains the exe
// directory. exec/workdir is not path-checked; the staged path is.
func SandboxGate(sandboxDir, exeDir string) error {
	sandbox := CleanWinPath(sandboxDir)
	if sandbox == "" {
		return nil
	}
	if !WithinWinPath(sandbox, exeDir) {
		return stepErr("SW-02", CodeSandboxRefused,
			fmt.Errorf("sandbox_dir=%s does not contain %s", sandbox, CleanWinPath(exeDir)))
	}
	return nil
}
