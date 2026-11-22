package agent

import (
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// spawnGuardian starts the same binary in `guardian` mode, in its own session
// so that killing the agent (or the agent's process group) cannot take it
// down. stdin/stdout/stderr go to the job's guardian.log: the guardian must
// not hold a pipe that dies with its parent.
func (a *Agent) spawnGuardian(jobDir string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(jobDir, "guardian.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, "guardian", "--job-dir", jobDir)
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.SysProcAttr = detachedSysProcAttr()
	applyJobEnv(cmd)
	return cmd.Start()
}

// readRand is a thin seam so job id generation stays testable.
func readRand(b []byte) (int, error) { return rand.Read(b) }

// applyJobEnv detaches the guardian from the agent's environment changes that
// matter for liveness (no-op placeholder kept explicit for readability).
func applyJobEnv(cmd *exec.Cmd) {
	cmd.Env = append(os.Environ(), "PROBE_GUARDIAN=1")
}

// guardianPollInterval is how often the guardian re-checks the job and the
// agent. 250ms keeps the post-agent-death cleanup well inside the 15s bar.
const guardianPollInterval = 250 * time.Millisecond
