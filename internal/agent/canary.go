package agent

import (
	"fmt"
	"io"
	"log"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/falke-ai-circuit/probe/internal/upgrade"
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

	canonical, err := a.swapAndKillOld()
	if err != nil {
		a.abandonFailedCanary(conn, err)
		return
	}
	a.finishCommit(canonical, conn)
}

// finishCommit is everything that happens after a swap has been verified: the
// process becomes canonical and the connection is dropped so the reconnect loop
// re-registers under the canonical name.
func (a *Agent) finishCommit(canonical string, conn io.Closer) {
	log.Printf("[canary] update committed — new version in place, old stopped. Becoming canonical agent.")

	a.promoteToCanonical(canonical)

	// Drop this connection so the reconnect loop re-registers under the
	// canonical name and keeps running.
	if conn != nil {
		_ = conn.Close()
	}
}

// canaryExitCode is the exit status of a canary that abandoned its update.
const canaryExitCode = 3

// canaryExecutable resolves the executable this process is running from. It is
// a variable so the failed-swap test can point the swap at a scratch directory
// instead of requiring the test binary to be named "<canonical>.next".
var canaryExecutable = os.Executable

// canaryExit terminates the canary process. It is a variable so a test can
// observe that the exit path was taken without killing the test binary.
var canaryExit = os.Exit

// promoteToCanonical makes this process the canonical agent after a committed
// swap. Not all hosts run under a service manager (systemd / Windows SCM /
// Android foreground service), so the canary itself becomes the canonical
// process instead of relying on a service to restart it: canary mode is cleared,
// the -canary suffix is stripped, and the published run-state record is
// re-derived.
//
// canonicalExe is the path the swap just placed the verified bytes at. Re-deriving
// it from os.Executable() alone is not enough on every platform (a process can
// keep reporting its staging name), and the swap has already proven the canonical
// path holds this exact image — so the proven path is what the record carries.
// The heartbeat shares this record, so the corrected identity is not reverted on
// the next tick (P12-04 F2: status still said name=p1204-agent-canary,
// executable=…/probe-agent.next, a file that no longer existed).
func (a *Agent) promoteToCanonical(canonicalExe string) {
	a.mu.Lock()
	a.cfg.CanaryMode = false
	a.cfg.Name = strings.TrimSuffix(a.cfg.Name, canaryNameSuffix)
	name := a.cfg.Name
	a.mu.Unlock()

	if err := a.republishStatusIdentity(name, canonicalExe); err != nil {
		log.Printf("[status] cannot re-publish run state after promotion: %v (record still names %q / %s)", err, name, canonicalExe)
		return
	}
	log.Printf("[status] run state re-published after promotion: name=%s executable=%s", name, canonicalExe)
}

// abandonFailedCanary is the failed-swap exit path (P12-04 F4). The old process
// keeps running and the canonical bytes were rolled back, so this process has no
// job left — but it was connected and registered under "<name>-canary". Leaving
// it alive produced TWO active agents after one failed update, the second one
// running from a .next that no longer existed.
//
// CanaryMode is deliberately left true: if the exit is stubbed (tests) or the
// runtime refuses to exit, Connect() sees an aborted canary and does not
// reconnect — the old process stays the only agent.
func (a *Agent) abandonFailedCanary(conn io.Closer, cause error) {
	log.Printf("[canary] swap failed: %v — old keeps running (rollback); canary exiting so it cannot register as a second active agent", cause)
	if conn != nil {
		_ = conn.Close()
	}
	canaryExit(canaryExitCode)
}

// swapRename is the rename primitive the swap uses. It is a variable so tests
// can force the commit rename to fail and exercise the rollback path.
var swapRename = os.Rename

// agentSwapFS adapts the process's own filesystem to upgrade.SwapFS. Rename goes
// through swapRename so the rollback tests keep their failure seam; Stat and
// Hash are the same reads the verification contract is built on.
type agentSwapFS struct{}

func (agentSwapFS) Stat(path string) (upgrade.SwapFile, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return upgrade.SwapFile{}, nil
		}
		return upgrade.SwapFile{}, err
	}
	return upgrade.SwapFile{Exists: true, IsDir: fi.IsDir(), Size: fi.Size()}, nil
}

func (agentSwapFS) Hash(path string) (string, error) { return hashFile(path) }

func (agentSwapFS) Rename(from, to string) error { return swapRename(from, to) }

// swapBinaryPaths performs the two-rename swap of newExe into canonical and
// returns the backup path holding the pre-update binary.
//
// The implementation lives in internal/upgrade (SwapRunningBinary): this is the
// same leg the in-band update path uses, and there is deliberately only one of
// it. The per-OS rules, the staged .next/.old naming and the size+sha read-back
// verification are documented there; this wrapper only supplies the local
// filesystem and the platform.
func swapBinaryPaths(newExe, canonical string) (string, error) {
	res, err := upgrade.SwapRunningBinary(upgrade.SwapRequest{
		Canonical: canonical,
		Staged:    newExe,
		Platform:  upgrade.RuntimePlatform(),
		FS:        agentSwapFS{},
	})
	if err != nil {
		return res.Backup, err
	}
	log.Printf("[canary] swap verified: canonical size=%d sha=%s, backup %s size=%d sha=%s",
		res.AfterSize, upgrade.ShortDigest(res.AfterSHA),
		res.Backup, res.BackupSize, upgrade.ShortDigest(res.BackupSHA))
	if res.Archived != "" {
		log.Printf("[canary] archived previous backup: %s", res.Archived)
	}
	return res.Backup, nil
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
func (a *Agent) swapAndKillOld() (string, error) {
	exe, err := canaryExecutable()
	if err != nil {
		return "", fmt.Errorf("os.Executable: %w", err)
	}
	canonical := strings.TrimSuffix(exe, ".next")
	if canonical == exe {
		return "", fmt.Errorf("canary executable %q does not end in .next", exe)
	}

	backup, err := swapBinaryPaths(exe, canonical)
	if err != nil {
		return canonical, err
	}

	log.Printf("[canary] swapped binary: %s -> %s (backup %s)", exe, canonical, backup)

	// 3. Stop the old process now that the new version is proven and in place.
	if a.cfg.CanaryOldPID > 0 {
		stopProcess(a.cfg.CanaryOldPID)
	}

	return canonical, nil
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
