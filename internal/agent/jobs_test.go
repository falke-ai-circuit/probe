package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// writeTestJob lays down the spec+state pair an agent would have written
// before spawning the guardian.
func writeTestJob(t *testing.T, root, command, onDeath string) string {
	t.Helper()
	jobID := "job-test-" + filepath.Base(t.Name())
	dir := filepath.Join(root, "jobs", jobID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sp := jobSpec{
		JobID:        jobID,
		Command:      command,
		OnDeath:      onDeath,
		AgentPID:     os.Getpid(),
		CreatedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	b, _ := json.Marshal(sp)
	if err := os.WriteFile(filepath.Join(dir, "spec.json"), b, 0600); err != nil {
		t.Fatalf("spec: %v", err)
	}
	now := time.Now().UTC()
	st := &jobState{
		JobID:        jobID,
		Phase:        jobPhaseStarting,
		AgentPID:     os.Getpid(),
		Command:      command,
		OnDeath:      onDeath,
		StartedAtUTC: now.Format(time.RFC3339Nano),
	}
	if err := writeStateAtomic(filepath.Join(dir, "state.json"), st); err != nil {
		t.Fatalf("state: %v", err)
	}
	return dir
}

// TestGuardianRunsOnDeathExactlyOnce proves the one-shot marker: a second
// guardian on the same job directory must not execute cleanup again.
func TestGuardianRunsOnDeathExactlyOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh job commands")
	}
	root := t.TempDir()
	counter := filepath.Join(root, "cleanup.count")
	dir := writeTestJob(t, root, "exit 3", "printf x >> "+counter)

	if code := RunGuardian(dir); code != 0 {
		t.Fatalf("first guardian exit = %d, want 0", code)
	}
	if code := RunGuardian(dir); code != 0 {
		t.Fatalf("second guardian exit = %d, want 0", code)
	}

	b, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("cleanup did not run: %v", err)
	}
	if len(b) != 1 {
		t.Fatalf("on_death ran %d times, want exactly 1", len(b))
	}

	st, err := readState(dir)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.Phase != jobPhaseClosed {
		t.Fatalf("phase = %q, want closed", st.Phase)
	}
	if st.ExitCode == nil || *st.ExitCode != 3 {
		t.Fatalf("exit_code = %v, want 3", st.ExitCode)
	}
	if !st.OnDeathRan || st.OnDeathAtUTC == "" {
		t.Fatal("on_death ran but was not recorded in the spool")
	}
	if _, err := os.Stat(filepath.Join(dir, "on_death.done")); err != nil {
		t.Fatalf("missing one-shot marker: %v", err)
	}
}

// TestGuardianSkipsCleanupOnExitZero is the negative: a job that succeeds on
// its own changed nothing that needs undoing.
func TestGuardianSkipsCleanupOnExitZero(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh job commands")
	}
	root := t.TempDir()
	counter := filepath.Join(root, "cleanup.count")
	dir := writeTestJob(t, root, "exit 0", "printf x >> "+counter)

	if code := RunGuardian(dir); code != 0 {
		t.Fatalf("guardian exit = %d, want 0", code)
	}
	if _, err := os.Stat(counter); err == nil {
		t.Fatal("on_death ran for a job that exited 0")
	}
	st, err := readState(dir)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.OnDeathRan {
		t.Fatal("on_death_ran = true for a clean exit")
	}
	if st.ExitCode == nil || *st.ExitCode != 0 {
		t.Fatalf("exit_code = %v, want 0", st.ExitCode)
	}
}

// TestJobStatusServedFromSpool proves job_status answers from disk, not from
// process memory: the state was written by a guardian that already exited.
func TestJobStatusServedFromSpool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh job commands")
	}
	root := t.TempDir()
	dir := writeTestJob(t, root, "echo hello; exit 4", "")
	if code := RunGuardian(dir); code != 0 {
		t.Fatalf("guardian exit = %d, want 0", code)
	}

	a := &Agent{cfg: Config{StateDir: root}}
	params, _ := json.Marshal(protocol.JobStatusParams{JobID: "job-test-" + t.Name()})
	resp := a.handleJobStatus(protocol.Envelope{ID: "q1", Type: protocol.TypeJobStatus, Params: params})
	if resp.Type != protocol.TypeJobStatusResult {
		t.Fatalf("type = %q (%v)", resp.Type, resp.Error)
	}
	var got protocol.JobStatusResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Phase != jobPhaseClosed || got.Alive {
		t.Fatalf("phase=%q alive=%v, want closed/not alive", got.Phase, got.Alive)
	}
	if got.ExitCode == nil || *got.ExitCode != 4 {
		t.Fatalf("exit_code = %v, want 4", got.ExitCode)
	}
	if got.LastLine != "hello" {
		t.Fatalf("last_line = %q, want hello", got.LastLine)
	}

	logParams, _ := json.Marshal(protocol.JobLogParams{JobID: "job-test-" + t.Name(), SinceOffset: 0})
	logResp := a.handleJobLog(protocol.Envelope{ID: "q2", Type: protocol.TypeJobLog, Params: logParams})
	var tail protocol.JobLogResult
	if err := json.Unmarshal(logResp.Result, &tail); err != nil {
		t.Fatalf("log unmarshal: %v", err)
	}
	if tail.Data != "hello\n" || !tail.Closed {
		t.Fatalf("log = %q closed=%v, want \"hello\\n\"/closed", tail.Data, tail.Closed)
	}
}

// TestJobIDCannotEscapeSpool keeps path traversal out of the query surface.
func TestJobIDCannotEscapeSpool(t *testing.T) {
	a := &Agent{cfg: Config{StateDir: t.TempDir()}}
	for _, id := range []string{"", "../etc", "a/b", "..", "job with space"} {
		if _, ok := a.resolveJobDir(id); ok {
			t.Fatalf("resolveJobDir(%q) accepted an unsafe id", id)
		}
	}
}
