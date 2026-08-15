//go:build windows

package replicator

import (
	"os/exec"
	"syscall"
)

// CREATE_NEW_PROCESS_GROUP (0x200) + DETACHED_PROCESS (0x08) + BREAKAWAY_FROM_JOB (0x01000000)
func setProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000208 | 0x01000000,
	}
}
