package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// P15-06 — stale counter semantics.
//
// The live question: /api/v1/health reported total 26 / active 4 / stale 2 on
// image 70de7fa, then total 26 / active 4 / stale 22 on 62a3b8e from the same
// host. Is 22 the same population counted honestly, or a double-count?
//
// The counter is transport-first: decorateAgentLiveness (agent_visibility.go)
// stamps Stale=true, reason "no_transport" on every row the dispatch predicate
// resolveAgentTransport refuses — i.e. every agent with no live conn-map entry
// — and registry applyLiveness only adds more reasons (inactive / error /
// never_seen / heartbeat_age > 90s / ping_unanswered). handleV1Health counts
// the union. There is exactly one stale bucket per row and active+stale must
// partition total.
//
// This test builds a known population the way a restarted server inherits it:
// 26 rows persisted by a previous process (4 of them later re-register and get
// fake live transports; 22 do not), then asserts the exact health triple.

func p1506WriteRegistry(t *testing.T, path string, docs map[string]map[string]interface{}) {
	t.Helper()
	b, err := json.Marshal(docs)
	if err != nil {
		t.Fatalf("marshal registry rows: %v", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write registry file: %v", err)
	}
}

func p1506Health(t *testing.T, srv *Server) map[string]interface{} {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/health: status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, ok := apiResp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", apiResp.Data)
	}
	return data
}

func TestP1506_StaleCounterTwentySixPopulation(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")

	now := time.Now().UTC()
	oldStamp := now.Add(-1 * time.Hour).Format(time.RFC3339)

	// 26 rows in the persisted shape a previous server leaves behind.
	// status is persisted "active" on all of them — load() must not trust it.
	docs := make(map[string]map[string]interface{}, 26)
	for i := 1; i <= 26; i++ {
		id := fmt.Sprintf("agent-%02d", i)
		docs[id] = map[string]interface{}{
			"agent_id":       id,
			"name":           id,
			"version":        "test",
			"os":             "linux",
			"arch":           "amd64",
			"mode":           "test",
			"connected_at":   oldStamp,
			"last_heartbeat": oldStamp,
			"status":         "active",
			"uptime_seconds": 600,
			"health_score":   0.5,
			"reconnects":     0,
			"ping_timeouts":  0,
			"ping_misses":    0,
			"error_count":    0,
		}
	}
	p1506WriteRegistry(t, regPath, docs)

	srv := NewServer("", "test-token", regPath)
	srv.mux = http.NewServeMux()
	srv.registerRoutes() // same harness as newV1TestServer in api_v1_test.go
	defer srv.registry.Stop()

	// First probe, before anything re-registers: every row is registry-only
	// (no transport in this process), so the honest answer is 26 stale.
	data := p1506Health(t, srv)
	total := int(data["total_agents"].(float64))
	active := int(data["active_agents"].(float64))
	stale := int(data["stale_agents"].(float64))
	threshold := int64(data["stale_threshold_seconds"].(float64))

	if total != 26 {
		t.Errorf("total_agents = %d, want 26", total)
	}
	if active != 0 {
		t.Errorf("first probe with no transports: active = %d, want 0", active)
	}
	if stale != 26 {
		t.Errorf("first probe with no transports: stale = %d, want 26 (every row is not currently serving)", stale)
	}
	if active+stale != total {
		t.Errorf("active (%d) + stale (%d) != total (%d) — a third bucket or double-count exists", active, stale, total)
	}
	if threshold != int64(staleThreshold.Seconds()) {
		t.Errorf("stale_threshold_seconds = %d, want %d", threshold, int64(staleThreshold.Seconds()))
	}

	// The live observation reproduced in-tree: 4 of the 26 re-register and
	// hold live transports; the 22 carried-over rows have none. The triple
	// must come out (26, 4, 22) — exactly what image 62a3b8e reported.
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("agent-%02d", i)
		srv.registry.Register(id, id, "test", "linux", "amd64", "test", nil)
		p2101AttachWS(srv, id) // fake live direct websocket + write mutex
	}

	data = p1506Health(t, srv)
	total = int(data["total_agents"].(float64))
	active = int(data["active_agents"].(float64))
	stale = int(data["stale_agents"].(float64))

	if total != 26 || active != 4 || stale != 22 {
		t.Errorf("health triple = (%d, %d, %d), want (26, 4, 22) — the live after-deploy observation", total, active, stale)
	}
	if active+stale != total {
		t.Errorf("active (%d) + stale (%d) != total (%d)", active, stale, total)
	}

	// Per-row cross-check: stale flag agrees with the bucket, every stale row
	// carries a real reason, and the 22 carried-over rows read inactive
	// (persisted "active" was not trusted on load).
	rows := srv.registry.ListAgents()
	gotStale := 0
	inactiveKeep := 0
	for _, a := range rows {
		if a.Stale {
			gotStale++
			if a.StaleReason == "ok" {
				t.Errorf("row %s: stale=true with reason ok", a.AgentID)
			}
			if i := a.AgentID; i > "agent-04" && a.StaleReason == "inactive" {
				inactiveKeep++
			}
		}
	}
	if gotStale != stale {
		t.Errorf("per-row stale flag count %d != health bucket %d", gotStale, stale)
	}
	if inactiveKeep != 22 {
		t.Errorf("carried-over rows reading inactive = %d, want 22 (load() must not trust persisted active)", inactiveKeep)
	}
}

// Guard: the counter follows present truth, not a monotone accumulator. When
// all 22 carried-over rows re-register and hold live transports, stale drops
// to 0 on the same server, same registry file.
func TestP1506_RejoinFlipsStaleBackToActive(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")

	now := time.Now().UTC()
	oldStamp := now.Add(-1 * time.Hour).Format(time.RFC3339)
	docs := make(map[string]map[string]interface{}, 22)
	for i := 1; i <= 22; i++ {
		id := fmt.Sprintf("agent-%02d", i)
		docs[id] = map[string]interface{}{
			"agent_id":       id,
			"name":           id,
			"connected_at":   oldStamp,
			"last_heartbeat": oldStamp,
			"status":         "active",
			"health_score":   0.5,
		}
	}
	p1506WriteRegistry(t, regPath, docs)

	srv := NewServer("", "test-token", regPath)
	srv.mux = http.NewServeMux()
	srv.registerRoutes()
	defer srv.registry.Stop()

	data := p1506Health(t, srv)
	if got := int(data["stale_agents"].(float64)); got != 22 {
		t.Fatalf("before rejoin: stale = %d, want 22", got)
	}

	for i := 1; i <= 22; i++ {
		id := fmt.Sprintf("agent-%02d", i)
		srv.registry.Register(id, id, "test", "linux", "amd64", "test", nil)
		p2101AttachWS(srv, id)
	}

	data = p1506Health(t, srv)
	if got := int(data["stale_agents"].(float64)); got != 0 {
		t.Errorf("after all 22 rejoin with live transports: stale = %d, want 0", got)
	}
	if got := int(data["active_agents"].(float64)); got != 22 {
		t.Errorf("after all 22 rejoin: active = %d, want 22", got)
	}
	if got := int(data["total_agents"].(float64)); got != 22 {
		t.Errorf("total = %d, want 22 (no double-count on re-registration)", got)
	}
}
