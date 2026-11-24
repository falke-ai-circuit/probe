package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestP1205RefusedUpdateIsNotReportedAsInProgress is the F1 handler acceptance:
// a forward that was refused before it reached the wire must answer a typed
// non-2xx error. Before the fix every error — including this local capability
// refusal — became HTTP 200 {"status":"update_in_progress"}, which is the silent
// false success P12-04 measured on a stock agent.
func TestP1205RefusedUpdateIsNotReportedAsInProgress(t *testing.T) {
	const agentID = "p1205-stock-agent"
	srv, _ := newUpdateTestServer(t, agentID)

	// The measured pre-fix state of a stock agent: connected, no "update".
	srv.registry.Register(agentID, agentID, "1.19.1", "linux", "amd64", "silent",
		[]string{"exec", "filesystem", "process"})

	fixture := filepath.Join(srv.FilesDir(), "probe-agent-fixture")
	if err := os.WriteFile(fixture, []byte("fixture-binary-bytes"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	rec := postUpdate(srv, agentID, map[string]interface{}{
		"binary_path": "probe-agent-fixture",
		"version":     "1.19.2",
	})

	if rec.Code == http.StatusOK {
		t.Fatalf("refused forward answered 200: %s (F1: a refusal was mapped to update_in_progress)", rec.Body.String())
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (capability refusal)\nbody: %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "update_in_progress") {
		t.Fatalf("refusal body still claims an update is in progress: %s", rec.Body.String())
	}

	var body struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.OK {
		t.Fatalf("body ok=true for a refusal: %s", rec.Body.String())
	}
	if body.Error.Code != RefusalCapabilityMissing {
		t.Fatalf("error code = %q, want the typed %q\nbody: %s", body.Error.Code, RefusalCapabilityMissing, rec.Body.String())
	}
	if !strings.Contains(body.Error.Message, "update") {
		t.Fatalf("error message does not name the missing capability: %q", body.Error.Message)
	}
}

// TestP1205DisconnectedAgentIsNotReportedAsInProgress pins the other pre-send
// refusal: an agent with no live connection never received the command, so the
// caller must not be told the update is under way.
func TestP1205DisconnectedAgentIsNotReportedAsInProgress(t *testing.T) {
	const agentID = "p1205-offline-agent"
	srv, _ := newUpdateTestServer(t, agentID)

	srv.mu.Lock()
	delete(srv.conns, agentID)
	delete(srv.connWriteMu, agentID)
	srv.mu.Unlock()

	fixture := filepath.Join(srv.FilesDir(), "probe-agent-fixture")
	if err := os.WriteFile(fixture, []byte("fixture-binary-bytes"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	rec := postUpdate(srv, agentID, map[string]interface{}{
		"binary_path": "probe-agent-fixture",
		"version":     "1.19.2",
	})

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (agent not connected)\nbody: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "update_in_progress") {
		t.Fatalf("offline refusal body claims an update is in progress: %s", rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != RefusalAgentNotConnected {
		t.Fatalf("error code = %q, want %q", body.Error.Code, RefusalAgentNotConnected)
	}
}
