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

// swapAndKillOld atomically swaps the canary binary into the canonical path and
// then stops the old process. It is only called after the canary has proven it
// is healthy. The swap is ordered so that there is never a half-updated state:
//  1. rename <canonical> -> <canonical>.old  (back up the old binary)
//  2. rename <canonical>.next -> <canonical> (new takes the canonical path)
//  3. stop the old process
//
// If step 2 fails, the old binary is restored so the canonical path still holds
// a runnable binary.
func (a *Agent) swapAndKillOld() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	canonical := strings.TrimSuffix(exe, ".next")
	if canonical == exe {
		return fmt.Errorf("canary executable %q does not end in .next", exe)
	}
	backup := canonical + ".old"

	// 1. Back up the current (old) binary by renaming it aside.
	//    Linux/Unix: atomic rename over a running binary (old process keeps its
	//    inode). Windows: renaming a running exe aside is allowed (the loader
	//    opens it with FILE_SHARE_DELETE); only overwrite/delete is blocked.
	if err := os.Rename(canonical, backup); err != nil {
		return fmt.Errorf("backup old binary: %w", err)
	}

	// 2. Move the new (canary) binary into the canonical path.
	if err := os.Rename(exe, canonical); err != nil {
		// Roll back: restore the old binary so we never end up half-updated.
		_ = os.Rename(backup, canonical)
		return fmt.Errorf("rename new into place: %w", err)
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
