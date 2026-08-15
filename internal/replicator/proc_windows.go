//go:build windows

package replicator

import (
	"os"
)

// pidAlive reports whether pid is alive (best-effort on Windows).
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// os.FindProcess on Windows always succeeds for any pid; the real liveness
	// check is done at kill time. Treat as alive unless the pid is 0/negative.
	return pid > 0 && p != nil
}

func killByPid(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
