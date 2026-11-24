//go:build !linux && !windows

package lifecycle

import (
	"os"
	"syscall"
	"time"
)

// ProcessAlive on the remaining platforms is a signal-0 probe: the kernel
// returns ESRCH for a PID that does not exist and EPERM for one it exists but
// will not let us signal — EPERM still means alive.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	return err == syscall.EPERM
}

// ProcessStartTime is unavailable here: these platforms expose no portable
// process creation time without a sysctl per-OS. Callers are told so and fall
// back to PID existence, which is reported as a weaker verdict rather than
// silently upgraded to certainty.
func ProcessStartTime(int) (time.Time, bool) { return time.Time{}, false }

// OSName is the platform label the status record carries.
func OSName() string { return "other" }

// ServiceMarker is empty: no service manager on these platforms sets a marker
// this binary can read.
func ServiceMarker() string { return os.Getenv("PROBE_RUN_AS_SERVICE") }
