package server

// P14A-01 (P12-06 residual R1) — a write that never reached the agent must not
// answer "update in progress". The pre-send refusals (no connection, missing
// capability) were made typed in P12-05; the write itself was not. A
// conn.WriteJSON failure returned a bare error (server_api.go:885-888), the
// update route could not tell it from "sent, the answer did not arrive", and
// answered HTTP 200 {"status":"update_in_progress"} for a command the agent
// never received. These tests drive that exact branch through the route.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// p14a01UpdateResponse is the refusal shape writeErrorDetail emits.
type p14a01UpdateResponse struct {
	OK    bool `json:"ok"`
	Error struct {
		Code    string                 `json:"code"`
		Message string                 `json:"message"`
		Detail  map[string]interface{} `json:"detail"`
	} `json:"error"`
}

// TestP14A01UndeliveredUpdateIsRefused replaces the agent's connection with one
// whose WriteJSON always fails — a live entry in s.conns, a capability-bearing
// registry row, and a transport that refuses the frame. That is precisely the
// state the old code turned into 200 update_in_progress.
func TestP14A01UndeliveredUpdateIsRefused(t *testing.T) {
	const agentID = "p14a01-write-fail"
	srv, _ := newUpdateTestServer(t, agentID) // registers conn + "update" capability

	// Persist audit rows so the "no success record" half is measurable.
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	srv.audit = NewAuditLogger(auditPath)

	srv.mu.Lock()
	srv.conns[agentID] = failingWriteConn{}
	srv.mu.Unlock()

	fixture := filepath.Join(srv.FilesDir(), "probe-agent-fixture")
	if err := os.WriteFile(fixture, []byte("fixture-binary-bytes"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	rec := postUpdate(srv, agentID, map[string]interface{}{
		"binary_path": "probe-agent-fixture",
		"version":     "1.19.2",
	})

	// 1. The answer is a refusal, not an in-progress claim.
	if rec.Code == http.StatusOK {
		t.Fatalf("undelivered write answered 200: %s (R1: the false success this unit closes)", rec.Body.String())
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d (delivery failed)\nbody: %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "update_in_progress") {
		t.Fatalf("body of an undelivered command still claims an update is in progress: %s", rec.Body.String())
	}

	var body p14a01UpdateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.OK {
		t.Fatalf("body ok=true for an undelivered command: %s", rec.Body.String())
	}
	if body.Error.Code != RefusalDeliveryFailed {
		t.Fatalf("error code = %q, want the typed %q\nbody: %s", body.Error.Code, RefusalDeliveryFailed, rec.Body.String())
	}
	if delivered, ok := body.Error.Detail["delivered"].(bool); !ok || delivered {
		t.Fatalf("detail.delivered = %v (present=%v), want false: nothing reached the agent", body.Error.Detail["delivered"], ok)
	}

	// 2. No in-progress status was recorded anywhere on the server side.
	srv.pendingMu.Lock()
	pending, inProgress := srv.pendingUpdates[agentID]
	srv.pendingMu.Unlock()
	if inProgress {
		t.Fatalf("undelivered command recorded an in-progress update: %+v", pending)
	}

	entries := srv.audit.Query(AuditFilter{AgentID: agentID})
	if len(entries) == 0 {
		t.Fatalf("no audit row for the refused delivery (expected an error row)")
	}
	for _, e := range entries {
		if e.Result == "success" {
			t.Fatalf("audit recorded success for an undelivered command: %+v", e)
		}
	}
	last := entries[len(entries)-1]
	if last.Result != "error" {
		t.Fatalf("audit result = %q, want \"error\" for a failed write", last.Result)
	}
	if !strings.Contains(last.ErrorMsg, "send to agent failed") {
		t.Fatalf("audit error message does not name the write failure: %q", last.ErrorMsg)
	}
}

// TestP14A01DeliveredUpdateAnswersAsBefore is the happy-path guard: a write that
// does reach the agent must still answer the agent's payload with 200 and leave
// a success row, so the fix above cannot be satisfied by refusing everything.
func TestP14A01DeliveredUpdateAnswersAsBefore(t *testing.T) {
	const agentID = "p14a01-write-ok"
	srv, agent := newUpdateTestServer(t, agentID)

	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	srv.audit = NewAuditLogger(auditPath)

	fixture := filepath.Join(srv.FilesDir(), "probe-agent-fixture")
	if err := os.WriteFile(fixture, []byte("fixture-binary-bytes"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	rec := postUpdate(srv, agentID, map[string]interface{}{
		"binary_path": "probe-agent-fixture",
		"version":     "1.19.2",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("delivered update status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if _, ok := agent.last(); !ok {
		t.Fatalf("agent never saw the agent_update envelope")
	}
	if !strings.Contains(rec.Body.String(), "old_pid") {
		t.Fatalf("delivered update did not return the agent's payload: %s", rec.Body.String())
	}

	entries := srv.audit.Query(AuditFilter{AgentID: agentID})
	var success int
	for _, e := range entries {
		if e.Result == "success" && e.Action == protocol.TypeAgentUpdate {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("audit success rows for a delivered agent_update = %d, want 1 (rows: %+v)", success, entries)
	}
}
