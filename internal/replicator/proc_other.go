//go:build !windows

package replicator

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// pidAlive reports whether pid is alive (best-effort: /proc/<pid>/stat exists).
func pidAlive(pid int) bool {
	_, err := os.Stat("/proc/" + strconv.Itoa(pid) + "/stat")
	return err == nil
}

func killByPid(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// keep strings import if unused in future edits
var _ = strings.TrimSpace
