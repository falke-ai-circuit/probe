//go:build windows

package lifecycle

import (
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// ProcessAlive reports whether pid names a live process. OpenProcess fails with
// ERROR_INVALID_PARAMETER for a PID that no longer exists; a process that has
// exited but not been reaped by its parent still has an object, so the exit
// code is checked too.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	// STILL_ACTIVE (259) is the documented sentinel: a live process reports it,
	// an exited one reports its real exit code. A process whose object still
	// exists but has exited must not count as running.
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		// The handle opened, so the process object exists. Report alive rather
		// than turning an API gap into a false not_running.
		return true
	}
	return code == 259 // STILL_ACTIVE
}

// ProcessStartTime returns the wall-clock creation time of pid from the process
// object itself (not from a file), so a recycled PID cannot be mistaken for the
// agent: Windows exposes the true creation time.
func ProcessStartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, false
	}
	defer windows.CloseHandle(h)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, creation.Nanoseconds()), true
}

// OSName is the platform label the status record carries.
func OSName() string { return "windows" }

// ServiceMarker reports how the process was started. The SCM entrypoint
// (--service) sets PROBE_RUN_AS_SERVICE=windows-service before starting the
// agent loop; svc.IsWindowsService starts the process as a console-subsystem
// binary, so there is no other in-process evidence to read.
func ServiceMarker() string {
	return os.Getenv("PROBE_RUN_AS_SERVICE")
}
