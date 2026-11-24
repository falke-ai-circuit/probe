package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// This file is the ONE implementation of the local "replace the running binary"
// step. The canary (internal/agent) and any future in-band swap call it; there
// is deliberately no second copy of this logic, because two implementations of
// one swap leg have already cost this programme twice.
//
// The per-OS rule is the whole point of the file:
//
//	Linux/other POSIX: rename the staged .next over the canonical path. A POSIX
//	  rename replaces the directory entry while the old image is still open
//	  (the running process keeps its inode), so the replacement is atomic and
//	  the canonical path never disappears.
//
//	Windows: a running image cannot be replaced in place. The running exe must
//	  first be renamed aside to <canonical>.old (allowed: the loader opens it
//	  with FILE_SHARE_DELETE), and only then can the staged .next be promoted
//	  onto the now-free canonical name. A copy over the live image is the trap —
//	  "Copy-Item -Force" over a running exe reports success and leaves the old
//	  bytes in place — so promotion is a rename and the parked-then-promoted
//	  order is enforced, not assumed.
//
// Both platforms: the staged name is <canonical>.next and the backup name is
// <canonical>.old; every claim about the result is made by reading size and
// sha256 from disk AFTER the operation. A command's exit status is never
// accepted as evidence that bytes moved.

// Platform names the set of swap rules that apply to a host.
type Platform string

const (
	// PlatformLinux is POSIX rename-over semantics (Linux, BSD, Darwin).
	PlatformLinux Platform = "linux"
	// PlatformWindows is park-then-promote semantics.
	PlatformWindows Platform = "windows"
	// PlatformOther is any other POSIX-like host: same rules as PlatformLinux.
	PlatformOther Platform = "other"
)

// RuntimePlatform maps the build host to the swap rules it obeys. A binary
// cross-compiled for Windows still reports windows at run time, which is what
// the swap needs: the rule belongs to the host doing the swap, not the builder.
func RuntimePlatform() Platform {
	switch runtime.GOOS {
	case "windows":
		return PlatformWindows
	case "linux":
		return PlatformLinux
	default:
		return PlatformOther
	}
}

// SwapFile is the filesystem fact the swap needs. A missing file is
// Exists=false with a nil error: "not there" is an answer, not a failure.
type SwapFile struct {
	Exists bool
	IsDir  bool
	Size   int64
}

// SwapFS is the minimal filesystem surface the swap touches. Production uses
// OSSwapFS; tests substitute a fake so the Windows refusal rules and the
// silent-copy trap can be reproduced on a Linux host (there is no Windows node
// reachable from this build).
type SwapFS interface {
	Stat(path string) (SwapFile, error)
	Hash(path string) (string, error)
	Rename(from, to string) error
}

// OSSwapFS is SwapFS on the host's real filesystem.
type OSSwapFS struct{}

// Stat reports existence, directory-ness and size.
func (OSSwapFS) Stat(path string) (SwapFile, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return SwapFile{}, nil
		}
		return SwapFile{}, err
	}
	return SwapFile{Exists: true, IsDir: fi.IsDir(), Size: fi.Size()}, nil
}

// Hash returns the lowercase hex sha256 of the file's bytes.
func (OSSwapFS) Hash(path string) (string, error) { return FileSHA256(path) }

// Rename is the host's atomic same-volume rename.
func (OSSwapFS) Rename(from, to string) error { return os.Rename(from, to) }

// FileSHA256 returns the lowercase hex sha256 of path. It is the digest
// authority for every post-operation check in this file: a size comparison
// alone cannot distinguish two binaries of equal length.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// StagedPath is the staged-next-to path for canonical. Both the updater that
// writes the payload and the swap that promotes it use this one function, so
// they cannot disagree about the name.
func StagedPath(canonical string) string { return canonical + StagedSuffix }

// SwapRequest is one swap of Staged over Canonical.
type SwapRequest struct {
	// Canonical is the path the running binary was started from.
	Canonical string
	// Staged is the verified new binary, named <canonical>.next.
	Staged string
	// Platform selects the rules. Zero means POSIX.
	Platform Platform
	// FS is the filesystem; nil means OSSwapFS.
	FS SwapFS
}

// SwapResult is what the swap proved, by reading back from disk.
type SwapResult struct {
	Canonical string
	Staged    string
	// Backup holds the pre-swap binary.
	Backup string
	// Archived is the sha-named older generation, when one was set aside.
	Archived string
	// BeforeSize/BeforeSHA describe the canonical image before the swap.
	BeforeSize int64
	BeforeSHA  string
	// AfterSize/AfterSHA describe the canonical image after promotion; both
	// are read from the file AFTER it was placed.
	AfterSize int64
	AfterSHA  string
	// BackupSize/BackupSHA describe the parked pre-swap image, read after the
	// park.
	BackupSize int64
	BackupSHA  string
}

// SwapRunningBinary replaces the running binary at req.Canonical with the
// verified staged binary at req.Staged.
//
// Order: prove both images by size+sha → archive any previous .old → park the
// canonical to .old → (Windows) prove the canonical name is free → promote
// .next onto canonical → prove the placed bytes. Any failure after the park is
// rolled back and the rollback is proved by size+sha; an unprovable rollback is
// reported as ROLLBACK FAILED rather than a silent success.
func SwapRunningBinary(req SwapRequest) (SwapResult, error) {
	var res SwapResult
	fs := req.FS
	if fs == nil {
		fs = OSSwapFS{}
	}
	platform := req.Platform
	if platform == "" {
		platform = PlatformLinux
	}

	canonical := strings.TrimSpace(req.Canonical)
	staged := strings.TrimSpace(req.Staged)
	if canonical == "" || staged == "" {
		return res, stepErr("SW-20", CodeRollbackFailed,
			fmt.Errorf("swap needs a canonical and a staged path"))
	}
	if !isStagedNameFor(staged, canonical) {
		return res, stepErr("SW-20", CodeRollbackFailed,
			fmt.Errorf("staged path %q is not %q", staged, StagedPath(canonical)))
	}
	res.Canonical, res.Staged = canonical, staged

	// Prove the staged payload before anything touches the canonical image.
	stagedStat, err := fs.Stat(staged)
	if err != nil {
		return res, stepErr("SW-20", CodeWriteFailed, fmt.Errorf("stat staged %s: %w", staged, err))
	}
	if !stagedStat.Exists || stagedStat.IsDir {
		return res, stepErr("SW-20", CodeWriteFailed, fmt.Errorf("staged payload %s is missing", staged))
	}
	newSHA, err := fs.Hash(staged)
	if err != nil {
		return res, stepErr("SW-20", CodeWriteFailed, fmt.Errorf("hash staged %s: %w", staged, err))
	}
	newSize := stagedStat.Size

	// The pre-swap image: its size and sha are the rollback contract.
	preStat, err := fs.Stat(canonical)
	if err != nil {
		return res, stepErr("SW-20", CodeWriteFailed, fmt.Errorf("stat canonical %s: %w", canonical, err))
	}
	if !preStat.Exists || preStat.IsDir {
		return res, stepErr("SW-20", CodeRollbackFailed, fmt.Errorf("canonical %s is missing; nothing to swap", canonical))
	}
	preSHA, err := fs.Hash(canonical)
	if err != nil {
		return res, stepErr("SW-20", CodeRollbackFailed, fmt.Errorf("hash canonical %s: %w", canonical, err))
	}
	res.BeforeSize, res.BeforeSHA = preStat.Size, preSHA

	backup := BackupPath(canonical)
	res.Backup = backup

	// A second update must not overwrite the last-known-good pre-update image:
	// the previous .old is moved to a sha-named generation first.
	archived, err := archiveBackup(fs, backup)
	if err != nil {
		return res, stepErr("SW-21", CodeRollbackFailed, err)
	}
	res.Archived = archived

	// Park: rename — never copy — the canonical aside. On Windows this is the
	// only way to free the name of a running image; on POSIX it keeps the
	// pre-swap image available for rollback.
	if err := fs.Rename(canonical, backup); err != nil {
		return res, stepErr("SW-22", CodeRollbackFailed,
			fmt.Errorf("backup old binary %s -> %s: %w", canonical, backup, err))
	}

	// The parked image is the rollback source: prove its bytes now, not after
	// a failure, so a broken backup cannot masquerade as a safe rollback.
	backupStat, err := fs.Stat(backup)
	if err != nil {
		return res, stepErr("SW-22", CodeRollbackFailed, fmt.Errorf("stat backup %s: %w", backup, err))
	}
	if !backupStat.Exists || backupStat.IsDir {
		// The park reported success but produced nothing. On Windows that is
		// the forced-copy signature: the copy "worked", the running image was
		// never moved aside, and the canonical name is still occupied.
		if platform == PlatformWindows {
			return res, stepErr("SW-22", CodeRollbackFailed,
				fmt.Errorf("canonical %s is still occupied after park: the running image was not moved aside (a forced copy over a running executable reports success and silently does nothing on Windows); refusing to overwrite it",
					canonical))
		}
		return res, stepErr("SW-22", CodeRollbackFailed,
			fmt.Errorf("park left no backup at %s; nothing was promoted", backup))
	}
	backupSHA, hashErr := fs.Hash(backup)
	if hashErr != nil {
		return res, stepErr("SW-22", CodeRollbackFailed, fmt.Errorf("hash backup %s: %w", backup, hashErr))
	}
	res.BackupSize, res.BackupSHA = backupStat.Size, backupSHA
	if !backupStat.Exists || backupStat.IsDir || backupStat.Size != preStat.Size ||
		!strings.EqualFold(backupSHA, preSHA) {
		// Nothing was promoted: put the canonical back and prove it.
		if rbErr := rollbackSwap(fs, canonical, backup, preSHA, preStat.Size); rbErr != nil {
			return res, stepErr("SW-22", CodeRollbackFailed,
				fmt.Errorf("parked backup %s does not match pre-swap size=%d sha=%s; ROLLBACK FAILED: %v",
					backup, preStat.Size, preSHA, rbErr))
		}
		return res, stepErr("SW-22", CodeRollbackFailed,
			fmt.Errorf("parked backup %s does not match pre-swap size=%d sha=%s (rolled back, sha %s size %d verified)",
				backup, preStat.Size, preSHA, preSHA, preStat.Size))
	}

	// Windows trap guard: a park that reports success but does nothing (the
	// "Copy-Item -Force over a running exe" signature) leaves the canonical
	// name occupied. Shipping the new binary there would amount to an in-place
	// overwrite; refuse instead of pretending the swap happened.
	if platform == PlatformWindows {
		if st, statErr := fs.Stat(canonical); statErr != nil {
			return res, stepErr("SW-23", CodeRollbackFailed,
				fmt.Errorf("stat canonical after park: %w", statErr))
		} else if st.Exists {
			if rbErr := rollbackSwap(fs, canonical, backup, preSHA, preStat.Size); rbErr != nil {
				return res, stepErr("SW-23", CodeRollbackFailed,
					fmt.Errorf("canonical %s is still occupied after park (running image not moved aside); ROLLBACK FAILED: %v", canonical, rbErr))
			}
			return res, stepErr("SW-23", CodeRollbackFailed,
				fmt.Errorf("canonical %s is still occupied after park: a rename that replaces a running image silently does nothing on Windows; refusing to overwrite it (rolled back, sha %s size %d verified)",
					canonical, preSHA, preStat.Size))
		}
	}

	// Promote. POSIX replaces the path atomically even while the old image is
	// open; Windows reaches this point with the name already free.
	if err := fs.Rename(staged, canonical); err != nil {
		if rbErr := rollbackSwap(fs, canonical, backup, preSHA, preStat.Size); rbErr != nil {
			return res, stepErr("SW-24", CodeRollbackFailed,
				fmt.Errorf("rename new into place: %w; ROLLBACK FAILED: %v (expected pre-update sha %s size %d)",
					err, rbErr, preSHA, preStat.Size))
		}
		return res, stepErr("SW-24", CodeRollbackFailed,
			fmt.Errorf("rename new into place: %w (rolled back, sha %s size %d verified)", err, preSHA, preStat.Size))
	}

	// The placed image must be the verified bytes. This read — not the rename's
	// nil return — is what makes the swap a fact.
	if err := VerifyPlacedImage(fs, canonical, newSize, newSHA); err != nil {
		if rbErr := rollbackSwap(fs, canonical, backup, preSHA, preStat.Size); rbErr != nil {
			return res, stepErr("SW-25", CodeDigestMismatch,
				fmt.Errorf("%v; ROLLBACK FAILED: %v (expected pre-update sha %s size %d)", err, rbErr, preSHA, preStat.Size))
		}
		return res, stepErr("SW-25", CodeDigestMismatch,
			fmt.Errorf("%v (rolled back, sha %s size %d verified)", err, preSHA, preStat.Size))
	}

	placedStat, err := fs.Stat(canonical)
	if err != nil {
		return res, stepErr("SW-25", CodeWriteFailed, fmt.Errorf("stat placed %s: %w", canonical, err))
	}
	placedSHA, err := fs.Hash(canonical)
	if err != nil {
		return res, stepErr("SW-25", CodeWriteFailed, fmt.Errorf("hash placed %s: %w", canonical, err))
	}
	res.AfterSize, res.AfterSHA = placedStat.Size, placedSHA
	return res, nil
}

// VerifyPlacedImage reads path back and asserts it holds exactly wantSize bytes
// with digest wantSHA. Callers use it after any placement (rename or copy):
// the operation's return value is not evidence that the bytes moved, because a
// forced copy over a running image reports success and changes nothing.
func VerifyPlacedImage(fs SwapFS, path string, wantSize int64, wantSHA string) error {
	if fs == nil {
		fs = OSSwapFS{}
	}
	st, err := fs.Stat(path)
	if err != nil {
		return fmt.Errorf("verify %s: stat: %w", path, err)
	}
	if !st.Exists || st.IsDir {
		return fmt.Errorf("verify %s: file is missing after placement", path)
	}
	if st.Size != wantSize {
		return fmt.Errorf("verify %s: size is %d, expected %d", path, st.Size, wantSize)
	}
	got, err := fs.Hash(path)
	if err != nil {
		return fmt.Errorf("verify %s: hash: %w", path, err)
	}
	if !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("verify %s: sha256 is %s, expected %s", path, ShortDigest(got), ShortDigest(wantSHA))
	}
	return nil
}

// archiveBackup moves a pre-existing <canonical>.old to
// "<canonical>.old-<sha8 of its contents>" so the fixed slot can be reused
// without destroying the previous generation. It returns "" when no backup
// exists.
func archiveBackup(fs SwapFS, backup string) (string, error) {
	st, err := fs.Stat(backup)
	if err != nil {
		return "", fmt.Errorf("stat existing backup %s: %w", backup, err)
	}
	if !st.Exists {
		return "", nil
	}
	if st.IsDir {
		return "", fmt.Errorf("existing backup %s is a directory", backup)
	}
	slotHash, err := fs.Hash(backup)
	if err != nil {
		return "", fmt.Errorf("hash existing backup %s: %w", backup, err)
	}
	if len(slotHash) < 8 {
		return "", fmt.Errorf("existing backup %s has an unreadable digest", backup)
	}
	archived := backup + "-" + slotHash[:8]
	if prev, prevErr := fs.Hash(archived); prevErr == nil {
		if !strings.EqualFold(prev, slotHash) {
			return "", fmt.Errorf("refusing to overwrite %s: holds sha %s, slot holds sha %s",
				archived, ShortDigest(prev), ShortDigest(slotHash))
		}
		// Same bytes already archived: replacing it loses nothing.
	} else if st, statErr := fs.Stat(archived); statErr != nil {
		return "", fmt.Errorf("hash archived backup %s: %w", archived, statErr)
	} else if st.Exists {
		return "", fmt.Errorf("archived backup %s is unreadable", archived)
	}
	if err := fs.Rename(backup, archived); err != nil {
		return "", fmt.Errorf("archive existing backup %s -> %s: %w", backup, archived, err)
	}
	return archived, nil
}

// rollbackSwap renames the parked pre-swap image back onto the canonical path
// and proves the restored bytes by size and sha256, read from disk AFTER the
// restore. A restore whose result cannot be proved is an error, never a
// success inferred from the rename's return value.
func rollbackSwap(fs SwapFS, canonical, backup, preSHA string, preSize int64) error {
	if err := fs.Rename(backup, canonical); err != nil {
		return fmt.Errorf("restore %s -> %s: %w", backup, canonical, err)
	}
	st, err := fs.Stat(canonical)
	if err != nil {
		return fmt.Errorf("verify restored %s: stat: %w", canonical, err)
	}
	if !st.Exists || st.IsDir {
		return fmt.Errorf("verify restored %s: file is missing after restore", canonical)
	}
	got, err := fs.Hash(canonical)
	if err != nil {
		return fmt.Errorf("verify restored %s: hash: %w", canonical, err)
	}
	if st.Size != preSize || !strings.EqualFold(got, preSHA) {
		return fmt.Errorf("restored %s is size=%d sha=%s, expected size=%d sha=%s",
			canonical, st.Size, ShortDigest(got), preSize, ShortDigest(preSHA))
	}
	return nil
}

// isStagedNameFor reports whether staged is canonical plus the .next suffix,
// comparing with the path rules of the path style in use (Windows paths are
// case-insensitive, POSIX paths are not).
func isStagedNameFor(staged, canonical string) bool {
	if !strings.HasSuffix(staged, StagedSuffix) {
		return false
	}
	base := strings.TrimSuffix(staged, StagedSuffix)
	if IsAbsWinPath(base) || IsAbsWinPath(canonical) {
		return EqualWinPath(base, canonical)
	}
	return filepath.Clean(base) == filepath.Clean(canonical)
}
