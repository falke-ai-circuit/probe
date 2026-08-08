package agent

import (
	"fmt"
	"os"
	"syscall"
)

// hostnameFn returns the OS hostname. Wrapped so tests can mock it.
var hostnameFn = func() (string, error) { return os.Hostname() }

// workingDirFn returns the current working directory.
var workingDirFn = func() (string, error) { return os.Getwd() }

// executableFn returns the absolute path of the running executable.
var executableFn = func() (string, error) { return os.Executable() }

// pidFn returns the process ID.
var pidFn = func() int { return os.Getpid() }

// uidGidFn returns the user and group IDs. Wrapped because the syscall
// package returns different types on Windows vs Unix.
var uidGidFn = func() (int, int) {
	return os.Getuid(), os.Getgid()
}

// argsFn returns the command-line arguments.
var argsFn = func() []string { return os.Args }

// envFn returns the environment as KEY=VALUE strings.
var envFn = func() []string { return os.Environ() }

// diskUsageFn returns total and free bytes for the given path. Implemented
// per-platform in sensors_disk_unix.go and sensors_disk_windows.go.
var diskUsageFn func(path string) (total, free uint64, err error)

// fileStatFn returns a fileStatResult for the given path. Implemented
// per-platform in sensors_disk_unix.go and sensors_disk_windows.go.
var fileStatFn func(path string) (any, error)

// fmtSscan is a tiny fmt.Sscan shim so we don't import fmt in every
// sensor file.
var fmtSscan = func(s string, ptrs ...any) (int, error) {
	return fmt.Sscan(s, ptrs...)
}

// Compile-time check that syscall is imported (used by some sensors).
var _ = syscall.AF_UNIX
