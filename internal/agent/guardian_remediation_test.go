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

// P8-12 F1: a guardian that starts when the agent is ALREADY dead must behave
// exactly like a mid-run loss — kill the group, mark agent_lost, run cleanup
// once, phase closed. Before this fix the job ran forever and job_status kept
// answering running/agent_lost=false.
func TestGuardianClosesJobWhenAgentAlreadyDead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh job commands")
	}
	root := t.TempDir()
	counter := filepath.Join(root, "cleanup.count")
	jobDir := writeTestJob(t, root, "sleep 60", "printf x >> "+counter)

	// Overwrite the spec with an agent pid that cannot exist, so the guardian
	// sees the spawn-then-die window on its first poll.
	sp, err := readSpec(jobDir)
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	sp.AgentPID = 999999
	b, _ := json.Marshal(sp)
	if err := os.WriteFile(filepath.Join(jobDir, "spec.json"), b, 0600); err != nil {
		t.Fatalf("spec write: %v", err)
	}

	done := make(chan int, 1)
	go func() { done <- RunGuardian(jobDir) }()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("guardian exit = %d, want 0", code)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("guardian did not finish within 1s: the job was left orphaned")
	}

	st, err := readState(jobDir)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if st.Phase != jobPhaseClosed {
		t.Fatalf("phase = %q, want closed", st.Phase)
	}
	if !st.AgentLost || st.AgentLostAtUTC == "" {
		t.Fatalf("agent_lost not recorded: %+v", st)
	}
	if !st.OnDeathRan {
		t.Fatal("on_death did not run for a job whose agent was already dead")
	}
	if st.ExitCode == nil {
		t.Fatal("exit status not recorded")
	}
	if _, err := os.Stat(counter); err != nil {
		t.Fatalf("cleanup command did not execute: %v", err)
	}
	if !pidAlive(st.PID) {
		return // killed, as required
	}
}

// P8-12 F2: a guardian restart on a phase=running spool whose job died with the
// previous guardian has an UNKNOWN exit status. The old guard skipped cleanup
// for that case even though the comment promised it would run.
func TestGuardianRestartCleansUpUnknownExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh job commands")
	}
	root := t.TempDir()
	counter := filepath.Join(root, "cleanup.count")
	jobDir := writeTestJob(t, root, "exit 0", "printf x >> "+counter)

	st, err := readState(jobDir)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	st.Phase = jobPhaseRunning
	st.PID = 999998 // died with the previous guardian
	if err := writeStateAtomic(filepath.Join(jobDir, "state.json"), st); err != nil {
		t.Fatalf("state write: %v", err)
	}

	if code := RunGuardian(jobDir); code != 0 {
		t.Fatalf("guardian exit = %d, want 0", code)
	}
	got, err := readState(jobDir)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if got.Phase != jobPhaseClosed || got.ExitCode == nil || *got.ExitCode != -1 {
		t.Fatalf("state = %+v, want closed with unknown exit -1", got)
	}
	if !got.OnDeathRan {
		t.Fatal("cleanup did not run for an unknown exit status")
	}
}

// P8-12 F3: a second guardian must not rewrite a terminal record — the spool is
// the only truth after the job is gone, and a late guardian used to overwrite a
// real exit code with -1 and drop on_death_exit.
func TestSecondGuardianKeepsTerminalRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sh job commands")
	}
	root := t.TempDir()
	jobDir := writeTestJob(t, root, "exit 0", "")
	code := 3
	clean := 0
	st := &jobState{
		JobID:        "job-test-" + t.Name(),
		Phase:        jobPhaseClosed,
		PID:          999997,
		AgentPID:     os.Getpid(),
		StartedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
		ExitCode:     &code,
		OnDeathRan:   true,
		OnDeathExit:  &clean,
		OnDeathAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeStateAtomic(filepath.Join(jobDir, "state.json"), st); err != nil {
		t.Fatalf("state write: %v", err)
	}

	if rc := RunGuardian(jobDir); rc != 0 {
		t.Fatalf("second guardian exit = %d, want 0", rc)
	}
	got, err := readState(jobDir)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if got.ExitCode == nil || *got.ExitCode != 3 {
		t.Fatalf("exit_code = %v, want the recorded 3 (terminal record was rewritten)", got.ExitCode)
	}
	if !got.OnDeathRan || got.OnDeathExit == nil || *got.OnDeathExit != 0 {
		t.Fatalf("on_death record was downgraded: %+v", got)
	}
}

// P8-12 F5: the post-write digest must run under the same deadline as the rest
// of fs-write. A volume that stalls on read-back has to surface TIMEOUT.
func TestFSWriteVerificationIsBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sparse-file timing probe")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(128 << 20); err != nil {
		f.Close()
		t.Skip("filesystem does not support sparse files")
	}
	f.Close()

	a := &Agent{cfg: Config{Permissions: "full"}}
	params, _ := json.Marshal(protocol.FSParams{
		Path:       path,
		Data:       "AQ==", // one byte at offset 0, no truncate
		Offset:     0,
		Mode:       "0644",
		DeadlineMs: 1,
	})
	resp := a.handleFSWrite(protocol.Envelope{ID: "f5", Type: protocol.TypeFileSave, Params: params})
	if resp.Error == nil {
		t.Fatalf("expected TIMEOUT from an unbounded-cost digest, got result %s", resp.Result)
	}
	if resp.Error.Code != protocol.ErrTimeout {
		t.Fatalf("code = %q, want %q", resp.Error.Code, protocol.ErrTimeout)
	}
}
