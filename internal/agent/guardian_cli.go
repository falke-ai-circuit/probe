package agent

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// guardianSelfTestMarker is what `guardian --selftest` prints. The agent runs
// it against its own executable before launching a detached job, so a binary
// without the guardian entry point fails loudly instead of spawning nothing.
const guardianSelfTestMarker = "guardian-ok"

// ErrGuardianUnsupported marks a launch refused because the running binary has
// no guardian subcommand. Callers surface this as GUARDIAN_UNAVAILABLE.
var ErrGuardianUnsupported = errors.New("binary has no guardian entry point")

// RunGuardianCLI is the shared implementation of the `guardian` subcommand for
// every client build (cmd/probe and cmd/probe-client). Keeping it here means
// the two entrypoints cannot drift: both dispatch to this one function.
//
//	guardian --selftest           print guardian-ok and exit 0
//	guardian --job-dir <dir>      supervise one detached job
func RunGuardianCLI(args []string) int {
	fs := flag.NewFlagSet("guardian", flag.ContinueOnError)
	selftest := fs.Bool("selftest", false, "verify this binary can act as a guardian")
	jobDir := fs.String("job-dir", "", "detached job spool directory")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "guardian: %v\n", err)
		return 2
	}
	if *selftest {
		fmt.Println(guardianSelfTestMarker)
		return 0
	}
	if *jobDir == "" {
		fmt.Fprintln(os.Stderr, "guardian: --job-dir is required")
		return 2
	}
	return RunGuardian(*jobDir)
}

// checkGuardianSupport proves the executable that will outlive this agent
// actually implements the guardian subcommand. A binary without it cannot run
// cleanup, and that must be an error at launch time — not a job that silently
// has no executor.
func checkGuardianSupport(exe string) error {
	cmd := exec.Command(exe, "guardian", "--selftest")
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		return fmt.Errorf("guardian selftest timed out")
	}
	if err != nil {
		return fmt.Errorf("guardian selftest failed: %v", err)
	}
	if !strings.Contains(string(out), guardianSelfTestMarker) {
		return fmt.Errorf("guardian selftest produced %q, want %q", strings.TrimSpace(string(out)), guardianSelfTestMarker)
	}
	return nil
}
