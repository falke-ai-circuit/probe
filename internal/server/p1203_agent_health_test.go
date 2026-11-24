package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)

// P12-03 — agent-state visibility.
//
// The defect under test: /api/v1/health answered while exec/fs-write
// answered 503 for the same agent. These tests pin the visibility contract
// (stale is explicit, derived from last_seen, with a named threshold) and
// the counters that make a dead agent detectable without reading logs.

// TestP1203_FreshAgentIsNotStale pins the healthy row: every visibility
// field is present and populated, and stale is false.
func TestP1203_FreshAgentIsNotStale(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "agent-1", "1.19.1", "linux", "amd64", "outbound", nil)

	rec, err := r.GetHealth("agent-1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if rec.Stale {
		t.Errorf("fresh agent: stale=true (reason %q), want false", rec.StaleReason)
	}
	if rec.StaleReason != "ok" {
		t.Errorf("stale_reason: got %q, want ok", rec.StaleReason)
	}
	if rec.StaleThresholdSeconds != 90 {
		t.Errorf("stale_threshold_seconds: got %d, want 90 (staleThreshold)", rec.StaleThresholdSeconds)
	}
	if rec.LastSeen == "" || rec.LastSeenAgeSeconds < 0 || rec.LastSeenAgeSeconds > 5 {
		t.Errorf("last_seen=%q age=%d: want a fresh RFC3339 stamp and age ~0", rec.LastSeen, rec.LastSeenAgeSeconds)
	}
	if rec.Version != "1.19.1" {
		t.Errorf("version: got %q", rec.Version)
	}
	if rec.ExecutionContext != ExecutionContextAgentOwn {
		t.Errorf("execution_context: got %q, want %q", rec.ExecutionContext, ExecutionContextAgentOwn)
	}
	if rec.Reconnects != 0 {
		t.Errorf("reconnects: got %d, want 0 on first registration", rec.Reconnects)
	}
	if rec.ConnectionStartedAt == "" || rec.ConnectionUptimeSeconds < 0 {
		t.Errorf("connection uptime: started_at=%q uptime=%d", rec.ConnectionStartedAt, rec.ConnectionUptimeSeconds)
	}
	if rec.PingTimeouts != 0 || rec.PingMisses != 0 {
		t.Errorf("ping counters: timeouts=%d misses=%d, want 0", rec.PingTimeouts, rec.PingMisses)
	}
}

// TestP1203_StaleDerivedFromLastSeen proves the flag is derived from the
// timestamp against the named threshold, not from the background detector
// (which only runs every 30s and only touches "active" rows).
func TestP1203_StaleDerivedFromLastSeen(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "agent-1", "1.19.1", "linux", "amd64", "outbound", nil)

	r.mu.Lock()
	r.agents["agent-1"].lastHeartbeat = time.Now().Add(-2 * time.Minute)
	r.mu.Unlock()

	rec, err := r.GetHealth("agent-1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if !rec.Stale {
		t.Fatalf("2-minute-old heartbeat: stale=false, want true (threshold %ds)", rec.StaleThresholdSeconds)
	}
	if rec.StaleReason != "heartbeat_age" {
		t.Errorf("stale_reason: got %q, want heartbeat_age", rec.StaleReason)
	}
	if rec.LastSeenAgeSeconds < 119 {
		t.Errorf("last_seen_age_seconds: got %d, want >= 119", rec.LastSeenAgeSeconds)
	}
	// The flag must not be indistinguishable from a healthy row.
	healthy := AgentRecord{Status: "active", StaleReason: "ok"}
	if healthy.StaleReason == rec.StaleReason {
		t.Error("stale row shares stale_reason with the healthy row")
	}
}

// TestP1203_UnregisterKeepsLastSeen proves a disconnect does not forge a
// fresh last_seen: the agent stopped talking, and the row must say so.
func TestP1203_UnregisterKeepsLastSeen(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "agent-1", "1.19.1", "linux", "amd64", "outbound", nil)
	before, _ := r.GetHealth("agent-1")

	r.Unregister("agent-1")

	after, err := r.GetHealth("agent-1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if after.LastSeen != before.LastSeen {
		t.Errorf("last_seen moved on disconnect: %q -> %q", before.LastSeen, after.LastSeen)
	}
	if !after.Stale || after.StaleReason != "inactive" {
		t.Errorf("disconnected agent: stale=%v reason=%q, want true/inactive", after.Stale, after.StaleReason)
	}
}

// TestP1203_ReconnectsMonotonic pins the reconnect counter and the
// connection-scoped uptime: a reconnect is a new connection, the count only
// grows, and uptime restarts.
func TestP1203_ReconnectsMonotonic(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "agent-1", "1.19.1", "linux", "amd64", "outbound", nil)

	r.mu.Lock()
	r.agents["agent-1"].connectionStartedAt = time.Now().Add(-10 * time.Minute)
	r.mu.Unlock()
	rec, _ := r.GetHealth("agent-1")
	if rec.ConnectionUptimeSeconds < 590 {
		t.Fatalf("connection_uptime_seconds: got %d, want ~600", rec.ConnectionUptimeSeconds)
	}

	r.Unregister("agent-1")
	r.Register("agent-1", "agent-1", "1.19.1", "linux", "amd64", "outbound", nil)
	r.Register("agent-1", "agent-1", "1.19.1", "linux", "amd64", "outbound", nil)

	rec, _ = r.GetHealth("agent-1")
	if rec.Reconnects != 2 {
		t.Errorf("reconnects: got %d, want 2", rec.Reconnects)
	}
	if rec.ConnectionUptimeSeconds > 5 {
		t.Errorf("connection_uptime_seconds: got %d, want ~0 after reconnect", rec.ConnectionUptimeSeconds)
	}
	if rec.Stale {
		t.Errorf("reconnected agent: stale=true (reason %q), want false", rec.StaleReason)
	}
}

// TestP1203_PingTimeoutsCountedAndStale proves the ping-timeout counter is
// monotonic and that repeated misses flip the agent to stale without waiting
// for the 90s heartbeat window.
func TestP1203_PingTimeoutsCountedAndStale(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "agent-1", "1.19.1", "linux", "amd64", "outbound", nil)

	for i := 1; i < pingMissThreshold; i++ {
		r.RecordPingTimeout("agent-1")
		rec, _ := r.GetHealth("agent-1")
		if rec.PingTimeouts != i {
			t.Fatalf("ping_timeouts after %d misses: got %d", i, rec.PingTimeouts)
		}
		if rec.Stale {
			t.Fatalf("stale at %d misses, want false below threshold %d", i, pingMissThreshold)
		}
	}

	r.RecordPingTimeout("agent-1")
	rec, _ := r.GetHealth("agent-1")
	if rec.PingTimeouts != pingMissThreshold || rec.PingMisses != pingMissThreshold {
		t.Errorf("counters: timeouts=%d misses=%d, want %d", rec.PingTimeouts, rec.PingMisses, pingMissThreshold)
	}
	if !rec.Stale || rec.StaleReason != "ping_unanswered" {
		t.Errorf("after %d unanswered pings: stale=%v reason=%q, want true/ping_unanswered", pingMissThreshold, rec.Stale, rec.StaleReason)
	}
	if rec.LastPingTimeoutAt == "" {
		t.Error("last_ping_timeout_at is empty after a timeout")
	}

	// A pong proves life again and clears the run, but the counter stays
	// monotonic — it counts events, not current state.
	r.RecordPong("agent-1")
	rec, _ = r.GetHealth("agent-1")
	if rec.PingMisses != 0 {
		t.Errorf("ping_misses after pong: got %d, want 0", rec.PingMisses)
	}
	if rec.PingTimeouts != pingMissThreshold {
		t.Errorf("ping_timeouts after pong: got %d, want %d (monotonic)", rec.PingTimeouts, pingMissThreshold)
	}
	if rec.Stale {
		t.Errorf("after pong: stale=true (reason %q), want false", rec.StaleReason)
	}
}

// TestP1203_HealthSurfaceCarriesFields proves the HTTP surface — not just
// the struct — carries every required field for a dead agent.
func TestP1203_HealthSurfaceCarriesFields(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.registry.Register("agent-1", "agent-1", "1.19.1", "linux", "amd64", "outbound", []string{"exec"})
	srv.registry.RecordPingTimeout("agent-1")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents/agent-1/health", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("GET agent health: status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("data is %T, want object", resp.Data)
	}
	for _, key := range []string{
		"last_seen", "last_seen_age_seconds", "stale", "stale_reason",
		"stale_threshold_seconds", "reconnects", "connection_started_at",
		"connection_uptime_seconds", "execution_context", "version",
		"ping_timeouts", "ping_misses",
	} {
		if _, ok := data[key]; !ok {
			t.Errorf("health response missing %q", key)
		}
	}
	// No transport exists on this server, so the row must not claim health
	// even though the registry row is fresh.
	if data["stale"] != true {
		t.Errorf("stale: got %v, want true (no transport)", data["stale"])
	}
	if data["stale_reason"] != "no_transport" {
		t.Errorf("stale_reason: got %v, want no_transport", data["stale_reason"])
	}
}

// TestP1203_PingProbeCountsRealTimeout drives the probe against a real
// WebSocket agent that never answers: the probe must count each timeout and
// flip the agent to stale, through the production path only.
func TestP1203_PingProbeCountsRealTimeout(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.pingProbeTimeout = 300 * time.Millisecond

	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer conn.Close()

	info, _ := json.Marshal(protocol.AgentInfo{
		Name: "silent-agent", Version: "1.19.1", OS: "linux",
		Arch: "amd64", Mode: "outbound", ProtocolVersion: "2",
	})
	if err := conn.WriteJSON(protocol.Envelope{ID: "agent-info", Type: "agent_info", Result: info}); err != nil {
		t.Fatalf("send agent_info: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := srv.registry.GetHealth("silent-agent"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	for i := 0; i < pingMissThreshold; i++ {
		srv.probeConnectedAgents()
		time.Sleep(srv.pingProbeTimeout + 200*time.Millisecond)
	}

	rec, err := srv.registry.GetHealth("silent-agent")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if rec.PingTimeouts < pingMissThreshold {
		t.Errorf("ping_timeouts: got %d, want >= %d (probe path)", rec.PingTimeouts, pingMissThreshold)
	}
	if !rec.Stale {
		t.Errorf("agent that answers nothing: stale=false, want true")
	}
	if rec.StaleReason != "ping_unanswered" && rec.StaleReason != "heartbeat_age" {
		t.Errorf("stale_reason: got %q, want ping_unanswered", rec.StaleReason)
	}
}
