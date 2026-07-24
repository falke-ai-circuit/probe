//go:build windows

package agent

import "syscall"

// getSysProcAttr returns Windows-specific process attributes to fully detach
// the new process from the current one, ensuring it survives when the parent
// is killed by the server's proc_kill command.
//
// CREATE_NEW_PROCESS_GROUP (0x00000200) — new process group, no CTRL+C signals
// DETACHED_PROCESS       (0x00000008) — no console inheritance
// CREATE_BREAKAWAY_FROM_JOB (0x01000000) — break away from parent's job object
//
// Without CREATE_BREAKAWAY_FROM_JOB, killing the parent process can cascade
// and kill the child process too (Windows job object behavior).
func getSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: 0x00000208 | 0x01000000,
	}
}