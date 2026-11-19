package agent

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// This file is the single source of truth for the update launch contract: the
// argv the updater hands to the freshly staged binary, the log file that
// captures that binary's output, and the startup recovery that undo()s a
// half-finished swap. cmd/probe-client parses exactly this argv (see
// parseClientArgs in cmd/probe-client/main.go); cmd/probe's connect subcommand
// accepts the same flags (cmd/probe/connect.go).

// CanaryArgs returns the argv passed to the staged binary. The first element is
// the optional "connect" subcommand (both entrypoints accept it), followed by
// the config path and the canary flags that wire the new process into
// CanaryMode/CanaryOldPID in internal/agent.
func CanaryArgs(configPath string, oldPID int) []string {
	return []string{
		"connect",
		"--config", configPath,
		"--canary",
		"--canary-old-pid", strconv.Itoa(oldPID),
	}
}

// CanaryLogPath returns the bounded log file that receives the canary's stdout
// and stderr, beside the canonical binary.
func CanaryLogPath(canonicalPath string) string {
	return canonicalPath + ".canary.log"
}

// StartCanary launches newExePath as a canary, capturing its stdout/stderr into
// a log file truncated at the start of every update so a failed update always
// leaves diagnostics. The caller owns the returned process.
func StartCanary(newExePath, canonicalPath, configPath string, oldPID int) (newPID int, logPath string, err error) {
	logPath = CanaryLogPath(canonicalPath)
	var out *os.File
	out, err = os.Create(logPath) // truncates any previous update's diagnostics
	if err != nil {
		log.Printf("[update] WARNING: cannot open canary log %s: %v (canary output will be discarded)", logPath, err)
		out = nil
		err = nil
	}
	log.Printf("[update] canary log: %s", logPath)

	newCmd := exec.Command(newExePath, CanaryArgs(configPath, oldPID)...)
	newCmd.Stdin = nil
	if out != nil {
		newCmd.Stdout = out
		newCmd.Stderr = out
	} else {
		newCmd.Stdout = nil
		newCmd.Stderr = nil
	}
	newCmd.SysProcAttr = getSysProcAttr()

	startErr := newCmd.Start()
	if out != nil {
		_ = out.Close() // the child holds its own duplicate of the fd
	}
	if startErr != nil {
		return 0, logPath, startErr
	}
	return newCmd.Process.Pid, logPath, nil
}

// RecoverCanonicalBinary restores <exe>.old over <exe> when the canonical path
// is missing at process start. It covers the crash window between
// rename(canonical -> .old) and rename(.next -> canonical), where a host would
// otherwise be left with no agent binary at the canonical path.
//
// It is a no-op (nil error, restored=false) when the canonical binary exists or
// when there is no .old backup to restore.
func RecoverCanonicalBinary(exePath string) (restored bool, err error) {
	if exePath == "" {
		return false, fmt.Errorf("empty executable path")
	}
	if _, statErr := os.Stat(exePath); statErr == nil {
		return false, nil
	}
	backup := exePath + ".old"
	if _, statErr := os.Stat(backup); statErr != nil {
		return false, nil
	}
	if renameErr := os.Rename(backup, exePath); renameErr != nil {
		return false, fmt.Errorf("restore %s from %s: %w", exePath, backup, renameErr)
	}
	if chmodErr := os.Chmod(exePath, 0755); chmodErr != nil {
		log.Printf("[recovery] chmod warning on %s: %v", exePath, chmodErr)
	}
	log.Printf("[recovery] canonical binary %s was missing; restored from %s (interrupted update swap)", exePath, backup)
	return true, nil
}

// recoveredExecutable returns the executable path to use at startup, restoring
// a .old backup when the canonical path vanished mid-swap. The returned path is
// always the canonical (non-.next) path.
func recoveredExecutable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	canonical := trimNextSuffix(exe)
	if _, restoreErr := RecoverCanonicalBinary(canonical); restoreErr != nil {
		log.Printf("[recovery] FAILED to restore canonical binary: %v", restoreErr)
	}
	return canonical
}

// trimNextSuffix strips a stale ".next" suffix, which Windows reports as the
// load-time path after a self-promotion swap renames the running exe.
func trimNextSuffix(p string) string {
	const suffix = ".next"
	if len(p) > len(suffix) && p[len(p)-len(suffix):] == suffix {
		return p[:len(p)-len(suffix)]
	}
	return p
}

// canaryStartError builds the protocol error returned when the canary cannot be
// started, including the log path so the server operator knows where to look.
func canaryStartError(envID string, err error, logPath string) protocol.Envelope {
	return protocol.NewError(envID, protocol.ErrInternal, fmt.Sprintf("start new process failed: %v (canary log: %s)", err, logPath))
}
