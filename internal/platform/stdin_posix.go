//go:build !windows

package platform

import (
	"bytes"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// ExecWithStdin runs command through the platform shell with payload on the
// child's stdin. It exists for credential consumers (P8-07): a secret handed to
// a child over stdin never appears in the process command line, which argv
// would expose to every process on the host.
func ExecWithStdin(command string, timeout int, workDir string, env map[string]string, stdin []byte) (protocol.ExecResult, error) {
	cmd := exec.Command("sh", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	done := make(chan error, 1)
	var output []byte
	go func() {
		out, err := cmd.CombinedOutput()
		output = out
		done <- err
	}()
	timedOut := false
	var execErr error
	if timeout > 0 {
		timer := time.NewTimer(time.Duration(timeout) * time.Second)
		defer timer.Stop()
		select {
		case execErr = <-done:
		case <-timer.C:
			timedOut = true
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	} else {
		execErr = <-done
	}
	exitCode := 0
	if execErr != nil {
		if ee, ok := execErr.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
				exitCode = ws.ExitStatus()
			} else {
				exitCode = -1
			}
		} else {
			exitCode = -1
		}
	}
	return protocol.ExecResult{Stdout: string(output), ExitCode: exitCode, TimedOut: timedOut}, nil
}
