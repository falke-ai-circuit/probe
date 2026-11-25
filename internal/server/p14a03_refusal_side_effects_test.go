package server

// P14A-03 (R2 residual of P12-06 / P16-03) — a refused update must not leave
// side effects the refusal denies.
//
// Measured state on the pre-R2 tree: handleAgentUpdate purged the
// "<agent>-canary" registry row *before* the frame was forwarded, so a delivery
// that the forwarder refused (no connection, no capability, or — since P14A-01 —
// a write that failed on the wire) came back with a body saying nothing was
// staged, while the canary row was already gone. The comment on the refusal
// branch asserted a side-effect-free path the code did not have.
//
// These tests drive the refusal through the route with failingWriteConn (the
// transport that P9-14 already uses, see p914_update_purge_test.go:22-31) and
// assert the two admissible shapes: either the row survives, or the refusal
// declares the purge. The purge is now commit-side, so the row must survive and
// detail.canary_row_purged must say so.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// p14a03Response is the refusal shape writeErrorDetail emits.
type p14a03Response struct {
	OK    bool `json:"ok"`
	Error struct {
		Code    string                 `json:"code"`
		Message string                 `json:"message"`
		Detail  map[string]interface{} `json:"detail"`
	} `json:"error"`
}

// TestP14A03RefusedUpdateKeepsCanaryRow is the discriminating test: an earlier
// attempt left a canary row, this request's write fails, and the start-path
// mutations must not have committed.
func TestP14A03RefusedUpdateKeepsCanaryRow(t *testing.T) {
	const agentID = "p14a03-refused"
	srv, _ := newUpdateTestServer(t, agentID) // live conn + "update" capability

	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	srv.audit = NewAuditLogger(auditPath)

	// The measured start state: a previous attempt left the suffixed row.
	srv.registry.Register(agentID+canaryRegistrySuffix, agentID+canaryRegistrySuffix,
		"1.18.6", "windows", "amd64", "silent", nil)

	// Live connection, capability present, transport refuses the frame.
	srv.mu.Lock()
	srv.conns[agentID] = failingWriteConn{}
	srv.mu.Unlock()

	fixture := filepath.Join(srv.FilesDir(), "p14a03-fixture")
	if err := os.WriteFile(fixture, []byte("fixture-binary-bytes"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	rec := postUpdate(srv, agentID, map[string]interface{}{
		"binary_path": "p14a03-fixture",
		"version":     "1.19.2",
	})

	// The status contract is P14A-01's: a failed write is 502 DELIVERY_FAILED.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d\nbody: %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	var body p14a03Response
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.Error.Code != RefusalDeliveryFailed {
		t.Fatalf("error code = %q, want %q\nbody: %s", body.Error.Code, RefusalDeliveryFailed, rec.Body.String())
	}

	// 1. The start-path purge did not happen: the canary row survives.
	if _, err := srv.registry.GetHealth(agentID + canaryRegistrySuffix); err != nil {
		t.Fatalf("a refused update removed the canary registry row (%v): the purge still runs before the frame is written", err)
	}
	// The canonical row was never the purge's target and still must not be.
	if _, err := srv.registry.GetHealth(agentID); err != nil {
		t.Fatalf("refusal removed the canonical registry row: %v", err)
	}

	// 2. The body states what it did instead of denying it.
	if delivered, ok := body.Error.Detail["delivered"].(bool); !ok || delivered {
		t.Fatalf("detail.delivered = %v (present=%v), want false", body.Error.Detail["delivered"], ok)
	}
	if purged, ok := body.Error.Detail["canary_row_purged"].(bool); !ok || purged {
		t.Fatalf("detail.canary_row_purged = %v (present=%v), want false: no purge ran", body.Error.Detail["canary_row_purged"], ok)
	}
	staged, ok := body.Error.Detail["staged_artifact"].(string)
	if !ok || staged == "" {
		t.Fatalf("detail.staged_artifact = %v (present=%v), want the managed path that was staged and registered", body.Error.Detail["staged_artifact"], ok)
	}
	if _, statErr := os.Stat(staged); statErr != nil {
		t.Fatalf("declared staged artifact %q does not exist: %v", staged, statErr)
	}

	// 3. Nothing that reads as success was logged for the refused delivery.
	entries := srv.audit.Query(AuditFilter{AgentID: agentID})
	if len(entries) == 0 {
		t.Fatal("no audit row for the refused delivery")
	}
	for _, e := range entries {
		if e.Result == "success" {
			t.Fatalf("audit recorded success for an undelivered update: %+v", e)
		}
	}
	if last := entries[len(entries)-1]; !strings.Contains(last.ErrorMsg, "send to agent failed") {
		t.Fatalf("audit error message does not name the write failure: %q", last.ErrorMsg)
	}

	// 4. The retention pin armed before the write is released again. It exists
	// to protect a download that never started; handleAgentUpdateResult, the
	// only other release path, never runs for a refusal.
	srv.pendingMu.Lock()
	_, pinned := srv.updateArtifacts[agentID]
	_, inProgress := srv.pendingUpdates[agentID]
	srv.pendingMu.Unlock()
	if pinned {
		t.Fatal("refused update left the update-artifact pin armed: completion never releases it")
	}
	srv.inflightMu.Lock()
	_, held := srv.inflight[filepath.Clean(staged)]
	srv.inflightMu.Unlock()
	if held {
		t.Fatalf("refused update left %q pinned in-flight", staged)
	}
	if inProgress {
		t.Fatal("refused update recorded an in-progress update")
	}
}

// TestP14A03DeliveredUpdateStillDropsCanaryRow guards the reorder: moving the
// purge after the write must not stop an accepted start from dropping the stale
// canary row (P9-08 defect B).
func TestP14A03DeliveredUpdateStillDropsCanaryRow(t *testing.T) {
	const agentID = "p14a03-delivered"
	srv, agent := newUpdateTestServer(t, agentID)
	srv.registry.Register(agentID+canaryRegistrySuffix, agentID+canaryRegistrySuffix,
		"1.18.6", "windows", "amd64", "silent", nil)

	fixture := filepath.Join(srv.FilesDir(), "p14a03-delivered-fixture")
	if err := os.WriteFile(fixture, []byte("delivered-binary-bytes"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	rec := postUpdate(srv, agentID, map[string]interface{}{
		"binary_path": "p14a03-delivered-fixture",
		"version":     "1.19.2",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("delivered update status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	if _, ok := agent.last(); !ok {
		t.Fatal("agent never saw the agent_update envelope")
	}
	if _, err := srv.registry.GetHealth(agentID + canaryRegistrySuffix); err == nil {
		t.Fatal("accepted update left the stale canary row behind: the purge moved past the write and never ran")
	}
	if _, err := srv.registry.GetHealth(agentID); err != nil {
		t.Fatalf("accepted update removed the canonical row: %v", err)
	}
}
