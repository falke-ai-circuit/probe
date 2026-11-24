//go:build linux

package lifecycle

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// userHZ is the kernel's USER_HZ. On every Linux architecture Go supports it
// is 100: the value is part of the kernel ABI, not a tunable, so it is a
// constant here rather than a syscall. /proc/<pid>/stat reports process start
// time in these ticks since boot.
const userHZ = 100

// ProcessAlive reports whether pid names a live (non-zombie) process. A zombie
// has already exited and only its parent has not reaped it, so for lifecycle
// purposes it is dead: nothing is running the agent loop behind it.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// Field 3 (state) lives just after the parenthesised comm, which may itself
	// contain spaces and parentheses — split at the LAST ')'.
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 || i+2 >= len(b) {
		return false
	}
	state := b[i+2]
	return state != 'Z' && state != 'X' && state != 'x'
}

// ProcessStartTime returns the wall-clock creation time of pid, derived from
// /proc/<pid>/stat starttime (ticks since boot) plus the boot time in
// /proc/stat. It exists so a recycled PID cannot be mistaken for the agent.
func ProcessStartTime(pid int) (time.Time, bool) {
	if pid <= 0 {
		return time.Time{}, false
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, false
	}
	line := string(raw)
	i := strings.LastIndexByte(line, ')')
	if i < 0 || i+2 >= len(line) {
		return time.Time{}, false
	}
	// Fields after comm: state ppid pgrp session tty_nr tpgid flags minflt
	// cminflt majflt cmajflt utime stime cutime cstime priority nice
	// num_threads itrealvalue starttime → starttime is field 22 overall, i.e.
	// index 19 of this tail (state is index 0).
	fields := strings.Fields(line[i+2:])
	const starttimeIdx = 19
	if len(fields) <= starttimeIdx {
		return time.Time{}, false
	}
	ticks, err := strconv.ParseInt(fields[starttimeIdx], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	btime, ok := bootTime()
	if !ok {
		return time.Time{}, false
	}
	return btime.Add(time.Duration(ticks) * time.Second / userHZ), true
}

// bootTime reads the system boot time in seconds since the epoch.
func bootTime() (time.Time, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, false
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(ln, "btime ") {
			continue
		}
		secs, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(ln, "btime ")), 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(secs, 0), true
	}
	return time.Time{}, false
}

// OSName is the platform label the status record carries.
func OSName() string { return "linux" }

// ServiceMarker reports how this privileged process believes it was started:
// systemd exports INVOCATION_ID for every unit it starts, which is the only
// reliable in-process signal (a shell-started process does not have it). The
// explicit marker is set by the Windows service entrypoint and is honoured
// here too so the field means the same thing on both platforms.
func ServiceMarker() string {
	if v := os.Getenv("PROBE_RUN_AS_SERVICE"); v != "" {
		return v
	}
	if os.Getenv("INVOCATION_ID") != "" {
		return "systemd"
	}
	return ""
}

// SystemdUnitActive asks systemd for its own view of unit without assuming
// systemd exists on this host. The second return value is false when systemd is
// not PID 1 or systemctl is absent, so the caller prints "unavailable" instead
// of inventing a state. A non-zero exit with output is still an answer:
// systemctl is-active prints "inactive" and exits 3.
func SystemdUnitActive(unit string) (string, bool) {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return "", false // containers and build hosts: no systemd as PID 1
	}
	out, _ := execCommand("systemctl", "is-active", unit)
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", false
	}
	return s, true
}
