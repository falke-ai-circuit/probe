//go:build windows

package platform

import (
	"bytes"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/falke-ai-circuit/probe/internal/obfs"
	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// ExecWithStdin is the Windows twin of the POSIX helper: cmd.exe /c with the
// payload on the child's stdin so a credential never lands in argv.
func ExecWithStdin(command string, timeout int, workDir string, env map[string]string, stdin []byte) (protocol.ExecResult, error) {
	shell := obfs.Str("\x39\x37\x3e")
	args, rawLine := shellArgv(command)
	cmd := exec.Command(shell, args...)
	if rawLine {
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: shellCmdLine(shell, args)}
	} else {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
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
			cmd.Process.Kill()
			<-done
		}
	} else {
		execErr = <-done
	}
	exitCode := 0
	if execErr != nil {
		if ee, ok := execErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return protocol.ExecResult{Stdout: string(output), ExitCode: exitCode, TimedOut: timedOut}, nil
}
