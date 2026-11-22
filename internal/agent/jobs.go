package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-04 — detached exec + guaranteed cleanup.
//
// A detached job is launched by a guardian process (same binary, `guardian`
// subcommand) that is the job's parent and therefore outlives the agent. The
// agent only writes the job spec, spawns the guardian and reads the spool back
// for the pid. Everything a caller can later ask about the job — status, tail,
// whether cleanup ran — is read from the spool, never from agent memory, so it
// answers the same before and after an agent restart.
//
// Spool layout under the agent's state dir:
//
//	jobs/<job_id>/spec.json       written by the agent (immutable)
//	jobs/<job_id>/state.json      written by the guardian, atomic rename
//	jobs/<job_id>/log.txt         job stdout+stderr, appended by the job
//	jobs/<job_id>/guardian.log    guardian diagnostics
//	jobs/<job_id>/on_death.done   one-shot marker, created before cleanup runs

const (
	jobPhaseStarting = "starting"
	jobPhaseRunning  = "running"
	jobPhaseClosed   = "closed"

	jobLogChunk = 64 * 1024
)

// jobSpec is the immutable launch description the guardian reads.
type jobSpec struct {
	JobID        string            `json:"job_id"`
	Command      string            `json:"command"`
	WorkDir      string            `json:"workdir,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	TimeoutS     int               `json:"timeout_s"`
	OnDeath      string            `json:"on_death,omitempty"`
	AgentPID     int               `json:"agent_pid"`
	CreatedAtUTC string            `json:"created_at_utc"`
}

// jobState is the spool record every query is answered from.
type jobState struct {
	JobID           string `json:"job_id"`
	Phase           string `json:"phase"`
	PID             int    `json:"pid"`
	GuardianPID     int    `json:"guardian_pid"`
	AgentPID        int    `json:"agent_pid"`
	Command         string `json:"command"`
	OnDeath         string `json:"on_death,omitempty"`
	StartedAtUTC    string `json:"started_at_utc"`
	StartedAtLocal  string `json:"started_at_local"`
	TZ              string `json:"tz"`
	EndedAtUTC      string `json:"ended_at_utc,omitempty"`
	ExitCode        *int   `json:"exit_code,omitempty"`
	Signal          int    `json:"signal,omitempty"`
	LastLine        string `json:"last_line"`
	AgentLost       bool   `json:"agent_lost"`
	AgentLostAtUTC  string `json:"agent_lost_at_utc,omitempty"`
	TimedOut        bool   `json:"timed_out,omitempty"`
	OnDeathRan      bool   `json:"on_death_ran"`
	OnDeathExit     *int   `json:"on_death_exit,omitempty"`
	OnDeathAtUTC    string `json:"on_death_at_utc,omitempty"`
	JobTimeoutS     int    `json:"job_timeout_s"`
	GuardianVersion int    `json:"guardian_version"`
}

// stateDir resolves where job spools live: explicit config, environment, the
// config file's directory, or a relative default. It never depends on the
// process working directory changing between runs.
func (a *Agent) stateDir() string {
	if a.cfg.StateDir != "" {
		return a.cfg.StateDir
	}
	if v := os.Getenv("PROBE_STATE_DIR"); v != "" {
		return v
	}
	if a.cfg.ConfigPath != "" {
		return filepath.Join(filepath.Dir(a.cfg.ConfigPath), "state")
	}
	return "probe-state"
}

func (a *Agent) jobDir(jobID string) string {
	return filepath.Join(a.stateDir(), "jobs", jobID)
}

// writeStateAtomic replaces the spool record in one rename so a reader never
// sees a half-written job state.
func writeStateAtomic(path string, st *jobState) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readState loads a spool record. Missing files are reported as not found so
// callers can answer with a typed error rather than inventing job state.
func readState(jobDir string) (*jobState, error) {
	b, err := os.ReadFile(filepath.Join(jobDir, "state.json"))
	if err != nil {
		return nil, err
	}
	var st jobState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func readSpec(jobDir string) (*jobSpec, error) {
	b, err := os.ReadFile(filepath.Join(jobDir, "spec.json"))
	if err != nil {
		return nil, err
	}
	var sp jobSpec
	if err := json.Unmarshal(b, &sp); err != nil {
		return nil, err
	}
	return &sp, nil
}

// jobLastLine returns the last non-empty line of the job output file.
func jobLastLine(jobDir string) string {
	f, err := os.Open(filepath.Join(jobDir, "log.txt"))
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	const tail = 4096
	off := fi.Size() - tail
	if off < 0 {
		off = 0
	}
	buf := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\r\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}

// handleExecDetached writes the spec, starts the guardian and returns the
// launch handle as soon as the guardian has published the job pid. No output
// is returned: the caller reads it with job_log.
func (a *Agent) handleExecDetached(env protocol.Envelope, params protocol.ExecParams) protocol.Envelope {
	jobID, err := newJobID()
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, "job id: "+err.Error())
	}
	dir := a.jobDir(jobID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, "job dir: "+err.Error())
	}
	now := time.Now().UTC()
	sp := jobSpec{
		JobID:        jobID,
		Command:      params.Command,
		WorkDir:      params.WorkDir,
		Env:          params.Env,
		TimeoutS:     params.JobTimeoutS,
		OnDeath:      params.OnDeath,
		AgentPID:     os.Getpid(),
		CreatedAtUTC: now.Format(time.RFC3339Nano),
	}
	if sp.TimeoutS < 0 {
		sp.TimeoutS = 0
	}
	specBytes, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), specBytes, 0600); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, "spec: "+err.Error())
	}

	st := &jobState{
		JobID:          jobID,
		Phase:          jobPhaseStarting,
		AgentPID:       sp.AgentPID,
		Command:        params.Command,
		OnDeath:        params.OnDeath,
		StartedAtUTC:   now.Format(time.RFC3339Nano),
		StartedAtLocal: now.Local().Format(time.RFC3339),
		TZ:             tzName(now),
		JobTimeoutS:    sp.TimeoutS,
	}
	if err := writeStateAtomic(filepath.Join(dir, "state.json"), st); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, "state: "+err.Error())
	}

	if err := a.spawnGuardian(dir); err != nil {
		if errors.Is(err, ErrGuardianUnsupported) {
			return protocol.NewError(env.ID, protocol.ErrGuardianUnavailable, err.Error())
		}
		return protocol.NewError(env.ID, protocol.ErrInternal, "guardian: "+err.Error())
	}

	// Wait (briefly) for the guardian to publish the pid. This is a spool
	// read, not a wait on the job, so it cannot turn into the very deadline
	// this design exists to escape.
	deadline := time.Now().Add(1500 * time.Millisecond)
	var launched *jobState
	for time.Now().Before(deadline) {
		if cur, err := readState(dir); err == nil && cur.PID > 0 {
			launched = cur
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if launched == nil {
		return protocol.NewError(env.ID, protocol.ErrInternal,
			"guardian did not publish a job pid within 1.5s")
	}
	return protocol.NewResult(env.ID, protocol.TypeExecResult, protocol.JobLaunch{
		JobID:          jobID,
		PID:            launched.PID,
		GuardianPID:    launched.GuardianPID,
		StartedAtUTC:   launched.StartedAtUTC,
		StartedAtLocal: launched.StartedAtLocal,
		TZ:             launched.TZ,
	})
}

// handleJobStatus answers from the spool file only.
func (a *Agent) handleJobStatus(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.JobStatusParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	dir, ok := a.resolveJobDir(params.JobID)
	if !ok {
		return protocol.NewError(env.ID, protocol.ErrNotFound, "unknown job: "+params.JobID)
	}
	st, err := readState(dir)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrNotFound, "job state unreadable: "+err.Error())
	}
	last := jobLastLine(dir)
	if last != "" {
		st.LastLine = last
	}
	age := int64(0)
	if t, err := time.Parse(time.RFC3339Nano, st.StartedAtUTC); err == nil {
		age = int64(time.Since(t).Seconds())
	}
	logBytes := int64(0)
	if fi, err := os.Stat(filepath.Join(dir, "log.txt")); err == nil {
		logBytes = fi.Size()
	}
	alive := st.Phase == jobPhaseRunning && pidAlive(st.PID)
	return protocol.NewResult(env.ID, protocol.TypeJobStatusResult, protocol.JobStatusResult{
		JobID:          st.JobID,
		Alive:          alive,
		Phase:          st.Phase,
		PID:            st.PID,
		ExitCode:       st.ExitCode,
		Signal:         st.Signal,
		LastLine:       st.LastLine,
		AgeS:           age,
		StartedAtUTC:   st.StartedAtUTC,
		StartedAtLocal: st.StartedAtLocal,
		TZ:             st.TZ,
		EndedAtUTC:     st.EndedAtUTC,
		AgentLost:      st.AgentLost,
		OnDeathRan:     st.OnDeathRan,
		OnDeathExit:    st.OnDeathExit,
		OnDeathAtUTC:   st.OnDeathAtUTC,
		LogBytes:       logBytes,
	})
}

// handleJobLog returns the bytes after since_offset, capped per call.
func (a *Agent) handleJobLog(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.JobLogParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	dir, ok := a.resolveJobDir(params.JobID)
	if !ok {
		return protocol.NewError(env.ID, protocol.ErrNotFound, "unknown job: "+params.JobID)
	}
	f, err := os.Open(filepath.Join(dir, "log.txt"))
	if err != nil {
		if os.IsNotExist(err) {
			return protocol.NewResult(env.ID, protocol.TypeJobLogResult, protocol.JobLogResult{
				JobID: params.JobID, Offset: params.SinceOffset, NextOffset: params.SinceOffset,
			})
		}
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}
	off := params.SinceOffset
	if off < 0 {
		off = 0
	}
	if off > fi.Size() {
		off = fi.Size()
	}
	n := fi.Size() - off
	if n > jobLogChunk {
		n = jobLogChunk
	}
	buf := make([]byte, n)
	if n > 0 {
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
			return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
		}
	}
	closed := false
	if st, err := readState(dir); err == nil && st.Phase == jobPhaseClosed {
		closed = true
	}
	return protocol.NewResult(env.ID, protocol.TypeJobLogResult, protocol.JobLogResult{
		JobID:      params.JobID,
		Offset:     off,
		NextOffset: off + n,
		Data:       string(buf),
		Closed:     closed,
	})
}

// resolveJobDir validates a job id so a caller cannot walk out of the spool.
func (a *Agent) resolveJobDir(jobID string) (string, bool) {
	if jobID == "" || strings.ContainsAny(jobID, "/\\. ") || len(jobID) > 64 {
		return "", false
	}
	dir := a.jobDir(jobID)
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		return "", false
	}
	return dir, true
}

func tzName(t time.Time) string {
	name, _ := t.Zone()
	return name
}

// newJobID returns a sortable, collision-free job id: timestamp + random hex.
func newJobID() (string, error) {
	var b [6]byte
	if _, err := readRand(b[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("job-%d-%x", time.Now().UnixNano(), b), nil
}
