//go:build !windows

package agent

import (
	"os"
	"os/exec"
	"syscall"
)

// detachedSysProcAttr starts the guardian in its own session: killing the
// agent (even its whole process group) leaves the guardian running.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// jobSysProcAttr gives the job its own process group so the guardian can stop
// the whole pipeline — `sh -c 'a | b'` must not leave half of it running.
func jobSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// pidAlive reports whether pid exists and is signalable by us.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// killJobGroup kills the job's process group.
func killJobGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// shellCommand builds the job command. POSIX sh only: no Python, no helpers.
func shellCommand(sp *jobSpec) *exec.Cmd {
	return exec.Command("sh", "-c", sp.Command)
}

// waitStatusCode turns a finished process into (exit code, signal). A job
// killed by a signal is reported as 128+signal, the shell convention.
func waitStatusCode(cmd *exec.Cmd) (int, int) {
	if cmd == nil || cmd.ProcessState == nil {
		return -1, 0
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		sig := int(ws.Signal())
		return 128 + sig, sig
	}
	return cmd.ProcessState.ExitCode(), 0
}

var _ = os.Getpid
