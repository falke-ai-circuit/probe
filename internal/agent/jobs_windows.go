//go:build windows

package agent

import (
	"os/exec"
	"strconv"
	"syscall"
)

const (
	createNewProcessGroup    = 0x00000200
	detachedProcess          = 0x00000008
	createBreakawayFromJob   = 0x01000000
	processQueryLimitedInfo  = 0x1000
)

// detachedSysProcAttr detaches the guardian from the agent's job object and
// console, so killing the agent does not cascade into the guardian.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachedProcess | createBreakawayFromJob,
	}
}

// jobSysProcAttr keeps the job in its own process group.
func jobSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// pidAlive reports whether the pid still exists.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInfo, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	return true
}

// agentReachable is the guardian's test for the AGENT pid. Windows has no
// signal-0 equivalent; a handle we can open is the strongest available check.
func agentReachable(pid int) bool { return pidAlive(pid) }

// killJobGroup stops the job and its children.
func killJobGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F").Run()
}

// shellCommand builds the job command with the platform shell.
func shellCommand(sp *jobSpec) *exec.Cmd {
	return exec.Command("cmd", "/c", sp.Command)
}

// waitStatusCode returns the exit code; Windows has no shell signal convention.
func waitStatusCode(cmd *exec.Cmd) (int, int) {
	if cmd == nil || cmd.ProcessState == nil {
		return -1, 0
	}
	return cmd.ProcessState.ExitCode(), 0
}
