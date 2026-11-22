package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// RunGuardian is the `probe guardian --job-dir <dir>` entry point.
//
// It is the job's parent and the executor of on_death. It is deliberately the
// only component that decides a job is over: the agent may die at any moment
// and cannot run its own cleanup, so cleanup cannot live in the agent.
//
// Idempotency: on_death runs at most once per job directory. The marker file
// is created with O_EXCL *before* the cleanup command starts, so a guardian
// restarted after a host reboot (or after being killed mid-cleanup) will not
// run it a second time.
func RunGuardian(jobDir string) int {
	sp, err := readSpec(jobDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "guardian: spec: %v\n", err)
		return 2
	}
	st, err := readState(jobDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "guardian: state: %v\n", err)
		return 2
	}
	st.OnDeath = sp.OnDeath
	st.JobTimeoutS = sp.TimeoutS
	st.AgentPID = sp.AgentPID

	if st.Phase == jobPhaseClosed {
		return 0
	}

	logf, err := os.OpenFile(filepath.Join(jobDir, "log.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "guardian: log: %v\n", err)
		return 2
	}
	defer logf.Close()

	now := time.Now().UTC()
	st.GuardianPID = os.Getpid()
	st.GuardianVersion = 1
	agentLost := st.AgentLost || !pidAlive(sp.AgentPID)

	// Guardian restart with a published pid: the job is either still running
	// or it died with its previous guardian. Reconcile before doing anything.
	if st.PID > 0 && !pidAlive(st.PID) {
		code := -1
		st.ExitCode = &code
		st.Phase = jobPhaseClosed
		st.EndedAtUTC = now.Format(time.RFC3339Nano)
		st.LastLine = jobLastLine(jobDir)
		st.AgentLost = agentLost
		if agentLost || st.TimedOut {
			runOnDeathOnce(jobDir, st, logf)
		}
		_ = writeStateAtomic(filepath.Join(jobDir, "state.json"), st)
		return 0
	}

	var waitCh chan error
	var jobCmd *exec.Cmd
	if st.PID == 0 {
		cmd := shellCommand(sp)
		jobCmd = cmd
		cmd.Dir = sp.WorkDir
		cmd.Env = buildJobEnv(sp.Env)
		cmd.Stdout = logf
		cmd.Stderr = logf
		cmd.SysProcAttr = jobSysProcAttr()
		if err := cmd.Start(); err != nil {
			code := -1
			st.Phase = jobPhaseClosed
			st.ExitCode = &code
			st.EndedAtUTC = now.Format(time.RFC3339Nano)
			st.LastLine = "job start failed: " + err.Error()
			_ = writeStateAtomic(filepath.Join(jobDir, "state.json"), st)
			return 2
		}
		st.PID = cmd.Process.Pid
		st.Phase = jobPhaseRunning
		st.StartedAtUTC = now.Format(time.RFC3339Nano)
		st.StartedAtLocal = now.Local().Format(time.RFC3339)
		st.TZ = tzName(now)
		waitCh = make(chan error, 1)
		go func() { waitCh <- cmd.Wait() }()
		// Expose the pid to the launching agent as the first observable fact.
		if err := writeStateAtomic(filepath.Join(jobDir, "state.json"), st); err != nil {
			fmt.Fprintf(os.Stderr, "guardian: state write: %v\n", err)
		}

		// Re-check agent liveness right after start: the agent may already be
		// gone between spawn and here.
		if !agentLost && !pidAlive(sp.AgentPID) {
			agentLost = true
		}
		_ = writeStateAtomic(filepath.Join(jobDir, "state.json"), st)
	} else {
		st.Phase = jobPhaseRunning
		_ = writeStateAtomic(filepath.Join(jobDir, "state.json"), st)
	}

	startedAt := now
	if t, err := time.Parse(time.RFC3339Nano, st.StartedAtUTC); err == nil {
		startedAt = t
	}

	ticker := time.NewTicker(guardianPollInterval)
	defer ticker.Stop()

	for {
		if waitCh != nil {
			select {
			case <-waitCh:
				return finalizeJob(jobDir, st, jobCmd, agentLost, logf)
			case <-ticker.C:
			}
		} else {
			<-ticker.C
			if !pidAlive(st.PID) {
				// Restarted guardian, job already gone: its exit status died
				// with the previous guardian. Unknown is reported as -1 and
				// treated as failure, so cleanup still runs (at most once).
				return finalizeJob(jobDir, st, nil, agentLost, logf)
			}
		}

		if !agentLost && !pidAlive(sp.AgentPID) {
			// The agent is gone and the job is still open. Its per-call
			// deadline died with it; the guardian is now the only executor
			// left, so it stops the job and runs the cleanup.
			agentLost = true
			st.AgentLost = true
			st.AgentLostAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
			_ = writeStateAtomic(filepath.Join(jobDir, "state.json"), st)
			killJobGroup(st.PID)
			continue
		}

		if st.JobTimeoutS > 0 && time.Since(startedAt) > time.Duration(st.JobTimeoutS)*time.Second {
			if !st.TimedOut {
				st.TimedOut = true
				_ = writeStateAtomic(filepath.Join(jobDir, "state.json"), st)
				killJobGroup(st.PID)
			}
		}
	}
}

// finalizeJob records the terminal state and, when the job did not succeed on
// its own or the agent was lost, runs the cleanup exactly once.
func finalizeJob(jobDir string, st *jobState, cmd *exec.Cmd, agentLost bool, logf *os.File) int {
	code, sig := waitStatusCode(cmd)
	st.ExitCode = &code
	st.Signal = sig
	st.Phase = jobPhaseClosed
	st.EndedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
	st.AgentLost = agentLost
	st.LastLine = jobLastLine(jobDir)

	// A job that exited 0 on its own changed nothing that needs undoing.
	if code != 0 || sig != 0 || agentLost || st.TimedOut {
		runOnDeathOnce(jobDir, st, logf)
	}
	if err := writeStateAtomic(filepath.Join(jobDir, "state.json"), st); err != nil {
		fmt.Fprintf(os.Stderr, "guardian: final state: %v\n", err)
		return 1
	}
	return 0
}

// runOnDeathOnce executes st.OnDeath at most once for this job directory.
// The O_EXCL marker is written before the command starts: a crash between the
// two leaves a job marked cleaned-up rather than cleaned-up twice, which is
// the safe direction for a switch port that is already enabled.
func runOnDeathOnce(jobDir string, st *jobState, logf *os.File) {
	if st.OnDeath == "" {
		return
	}
	marker := filepath.Join(jobDir, "on_death.done")
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		// Marker already exists: cleanup already started in an earlier life.
		st.OnDeathRan = true
		if st.OnDeathAtUTC == "" {
			if b, rerr := os.ReadFile(marker); rerr == nil {
				var m struct {
					StartedAtUTC string `json:"started_at_utc"`
				}
				if json.Unmarshal(b, &m) == nil && m.StartedAtUTC != "" {
					st.OnDeathAtUTC = m.StartedAtUTC
				}
			}
		}
		return
	}
	startedAt := time.Now().UTC()
	mb, _ := json.Marshal(map[string]string{
		"job_id":         st.JobID,
		"started_at_utc": startedAt.Format(time.RFC3339Nano),
		"pid":            fmt.Sprintf("%d", os.Getpid()),
	})
	_, _ = f.Write(append(mb, '\n'))
	_ = f.Close()

	cmd := shellCommand(&jobSpec{Command: st.OnDeath})
	cmd.Stdout = logf
	cmd.Stderr = logf
	err = cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	st.OnDeathRan = true
	st.OnDeathExit = &code
	st.OnDeathAtUTC = startedAt.Format(time.RFC3339Nano)
	_ = writeStateAtomic(filepath.Join(jobDir, "state.json"), st)
}

// buildJobEnv gives the job the guardian's environment plus the caller's
// overrides, matching the foreground exec path.
func buildJobEnv(over map[string]string) []string {
	env := os.Environ()
	for k, v := range over {
		env = append(env, k+"="+v)
	}
	return env
}
