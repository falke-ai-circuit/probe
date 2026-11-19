package agent

import (
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// canaryProveInterval is how long the canary must stay connected (healthy)
// before it is allowed to swap itself into the canonical path and stop the old
// process. If the connection drops during this window, the canary aborts and
// the old process keeps running untouched — the update is abandoned safely.
const canaryProveInterval = 10 * time.Second

// canaryNameSuffix is appended to the agent name while it runs as a canary, so
// the canary does not displace the still-running old agent in the server's
// registry until the swap commits. After the swap, the canary strips the suffix
// and re-registers under the canonical name (kept in sync with cmd/probe's
// connect.go which appends the same suffix).
const canaryNameSuffix = "-canary"

// runCanaryCommit runs the "prove healthy → swap → stop old" sequence for a
// canary process. It is launched after the canary has connected and sent its
// agent info. The swap is performed FROM the canary (the new version) — which
// proves the new version is alive and functional — and only then is the old
// process stopped. If anything fails, the old process keeps running.
func (a *Agent) runCanaryCommit(conn *websocket.Conn) {
	log.Printf("[canary] proving health for %v before swap...", canaryProveInterval)

	timer := time.NewTimer(canaryProveInterval)
	defer timer.Stop()

	select {
	case <-a.stopped:
		log.Printf("[canary] stopped during prove — aborting")
		return
	case <-timer.C:
	}

	// Confirm we are still the active connection (it did not drop during prove).
	a.mu.Lock()
	stillConnected := a.conn == conn
	a.mu.Unlock()
	if !stillConnected {
		log.Printf("[canary] connection dropped during prove — aborting, old keeps running")
		return
	}

	if err := a.swapAndKillOld(); err != nil {
		log.Printf("[canary] swap failed: %v — old keeps running (rollback)", err)
		return
	}

	log.Printf("[canary] update committed — new version in place, old stopped. Becoming canonical agent.")

	// Self-promote to the canonical agent. Not all hosts run under a service
	// manager (systemd / Windows SCM / Android foreground service), so the
	// canary itself must become the canonical process instead of relying on a
	// service to restart it. Clear canary mode, restore the canonical name, and
	// drop this connection so the reconnect loop re-registers under the
	// canonical name and keeps running. The .old backup stays on disk for
	// manual revert; the old process was already stopped above.
	a.mu.Lock()
	a.cfg.CanaryMode = false
	a.cfg.Name = strings.TrimSuffix(a.cfg.Name, canaryNameSuffix)
	a.mu.Unlock()

	_ = conn.Close()
}

// swapRename is the rename primitive used by swapBinaryPaths. It is a variable
// so tests can force the commit rename to fail and exercise the rollback path.
var swapRename = os.Rename

// backupSlot returns the fixed backup path used for a canonical binary. The
// slot name is stable because startup recovery (RecoverCanonicalBinary) looks
// for exactly "<canonical>.old" after a crash mid-swap.
func backupSlot(canonical string) string {
	return canonical + ".old"
}

// archiveExistingBackup moves a pre-existing "<canonical>.old" aside to
// "<canonical>.old-<sha8 of its contents>" so the slot can be reused without
// destroying the previous generation.
//
// POSIX rename replaces the destination, so without this guard a second update
// would overwrite the genuine old-version backup with the current binary and
// the rollback path could only "restore" the already-new binary. After N
// successful updates this keeps a chain: .old (immediately previous) plus one
// .old-<sha8> per earlier generation, each identifiable by its own sha.
func archiveExistingBackup(canonical string) (string, error) {
	slot := backupSlot(canonical)
	slotHash, err := hashFile(slot)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("hash existing backup %s: %w", slot, err)
	}
	archived := slot + "-" + slotHash[:8]
	if prevHash, prevErr := hashFile(archived); prevErr == nil {
		if prevHash != slotHash {
			return "", fmt.Errorf("refusing to overwrite %s: holds sha %s, slot holds sha %s", archived, prevHash, slotHash)
		}
		// Same bytes already archived: replacing it loses nothing.
	} else if !os.IsNotExist(prevErr) {
		return "", fmt.Errorf("hash archived backup %s: %w", archived, prevErr)
	}
	if err := swapRename(slot, archived); err != nil {
		return "", fmt.Errorf("archive existing backup %s -> %s: %w", slot, archived, err)
	}
	log.Printf("[canary] archived previous backup: %s -> %s", slot, archived)
	return archived, nil
}

// restoreBackup renames the backup over the canonical path and proves the
// restored bytes are the pre-update binary. A restore that cannot prove the
// expected sha is reported as an error — never a silent success — because a
// silently-wrong rollback leaves the host running an unverified binary.
func restoreBackup(backup, canonical, expectedSHA string) error {
	if err := swapRename(backup, canonical); err != nil {
		return fmt.Errorf("restore %s -> %s: %w", backup, canonical, err)
	}
	restored, err := hashFile(canonical)
	if err != nil {
		return fmt.Errorf("verify restored %s: %w", canonical, err)
	}
	if restored != expectedSHA {
		return fmt.Errorf("restored sha %s != expected pre-update sha %s at %s", restored, expectedSHA, canonical)
	}
	log.Printf("[canary] rollback restored %s: expected sha %s, restored sha %s (match)", canonical, expectedSHA, restored)
	return nil
}

// swapBinaryPaths performs the two-rename swap of newExe into canonical and
// returns the backup path holding the pre-update binary:
//  1. archive an existing <canonical>.old to a sha-named generation
//  2. rename <canonical> -> <canonical>.old  (back up the old binary)
//  3. rename <canonical>.next -> <canonical> (new takes the canonical path)
//
// If step 3 fails the pre-update binary is restored and its sha asserted; a
// failed restore is returned as a loud, combined error.
func swapBinaryPaths(newExe, canonical string) (string, error) {
	expectedOld, err := hashFile(canonical)
	if err != nil {
		return "", fmt.Errorf("hash pre-update binary %s: %w", canonical, err)
	}

	if _, err := archiveExistingBackup(canonical); err != nil {
		return "", err
	}
	backup := backupSlot(canonical)

	// Linux/Unix: atomic rename over a running binary (old process keeps its
	// inode). Windows: renaming a running exe aside is allowed (the loader
	// opens it with FILE_SHARE_DELETE); only overwrite/delete is blocked.
	if err := swapRename(canonical, backup); err != nil {
		return "", fmt.Errorf("backup old binary: %w", err)
	}

	if err := swapRename(newExe, canonical); err != nil {
		if restoreErr := restoreBackup(backup, canonical, expectedOld); restoreErr != nil {
			return backup, fmt.Errorf("rename new into place: %w; ROLLBACK FAILED: %v (expected pre-update sha %s)", err, restoreErr, expectedOld)
		}
		return backup, fmt.Errorf("rename new into place: %w (rolled back, sha %s verified)", err, expectedOld)
	}

	return backup, nil
}

// swapAndKillOld atomically swaps the canary binary into the canonical path and
// then stops the old process. It is only called after the canary has proven it
// is healthy. The swap is ordered so that there is never a half-updated state:
//  1. archive any existing backup, then rename <canonical> -> <canonical>.old
//  2. rename <canonical>.next -> <canonical> (new takes the canonical path)
//  3. stop the old process
//
// If step 2 fails, the old binary is restored (sha-verified) so the canonical
// path still holds the exact pre-update binary.
func (a *Agent) swapAndKillOld() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	canonical := strings.TrimSuffix(exe, ".next")
	if canonical == exe {
		return fmt.Errorf("canary executable %q does not end in .next", exe)
	}

	backup, err := swapBinaryPaths(exe, canonical)
	if err != nil {
		return err
	}

	log.Printf("[canary] swapped binary: %s -> %s (backup %s)", exe, canonical, backup)

	// 3. Stop the old process now that the new version is proven and in place.
	if a.cfg.CanaryOldPID > 0 {
		stopProcess(a.cfg.CanaryOldPID)
	}

	return nil
}

// stopProcess stops a process by PID. On Unix it sends SIGTERM (graceful) and
// escalates to SIGKILL after a grace period. On Windows it terminates the
// process directly (no SIGTERM equivalent).
func stopProcess(pid int) {
	if pid <= 0 {
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = proc.Kill()
		return
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		// Process may already be gone.
		_ = proc.Kill()
		return
	}
	go func() {
		time.Sleep(5 * time.Second)
		_ = proc.Kill()
	}()
}
