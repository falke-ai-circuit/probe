package server

import (
	"testing"
)

// TestDetachedJobRecordLifecycle covers the witness record: open on launch,
// pending when the agent drops, resolved from a spool answer.
func TestDetachedJobRecordLifecycle(t *testing.T) {
	s := &Server{jobs: make(map[string]*ServerJob)}
	s.recordDetachedJob("a1", map[string]interface{}{
		"job_id":         "job-1",
		"pid":            float64(42),
		"started_at_utc": "2026-11-22T10:00:00Z",
	})
	s.jobMu.Lock()
	rec := s.jobs["job-1"]
	s.jobMu.Unlock()
	if rec == nil || rec.Status != "open" || rec.PID != 42 {
		t.Fatalf("record = %+v, want open pid 42", rec)
	}

	// A foreground exec response has no job_id and must not create a record.
	s.recordDetachedJob("a1", map[string]interface{}{"stdout": "x", "exit_code": float64(0)})
	s.jobMu.Lock()
	count := len(s.jobs)
	s.jobMu.Unlock()
	if count != 1 {
		t.Fatalf("job records = %d, want 1", count)
	}

	s.agentJobsDisconnected("a1")
	s.jobMu.Lock()
	status := s.jobs["job-1"].Status
	s.jobMu.Unlock()
	if status != "recovery_pending" {
		t.Fatalf("status after disconnect = %q, want recovery_pending", status)
	}

	// The guardian's spool answer resolves it — with the guardian's exit code.
	s.applyJobStatus("job-1", map[string]interface{}{
		"phase":        "closed",
		"exit_code":    float64(137),
		"on_death_ran": true,
	})
	s.jobMu.Lock()
	rec = s.jobs["job-1"]
	s.jobMu.Unlock()
	if rec.Status != "resolved" || rec.RecoveryRequired {
		t.Fatalf("resolved record = %+v", rec)
	}
	if rec.ExitCode == nil || *rec.ExitCode != 137 || !rec.OnDeathRan {
		t.Fatalf("guardian report not folded in: %+v", rec)
	}
}
