package server

// P21-01 — one liveness truth, asserted in the API's terms.
//
// The defect: /api/v1/agents reported an agent as active with health ~0.95
// while exec answered AGENT_UNAVAILABLE for the same id. Registry and conn-map
// disagreed and nothing reconciled them at read time.
//
// These tests read the HTTP response, not the struct, so the invariant is
// pinned in the shape a client actually receives: every row carries
// dispatchable / liveness_source / dispatch_error, active implies dispatchable
// or a stated reason, and a dispatchable agent is never refused as absent.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// p2101Rows fetches GET /api/v1/agents and returns the rows keyed by agent_id.
func p2101Rows(t *testing.T, srv *Server) map[string]map[string]interface{} {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/agents: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		OK   bool                     `json:"ok"`
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode agent list %q: %v", rec.Body.String(), err)
	}
	out := make(map[string]map[string]interface{}, len(body.Data))
	for _, row := range body.Data {
		id, _ := row["agent_id"].(string)
		out[id] = row
	}
	return out
}

// p2101AttachWS gives an agent a live direct WebSocket-class connection and the
// per-agent write mutex, i.e. exactly the state a dispatched command needs.
func p2101AttachWS(srv *Server, id string) {
	srv.mu.Lock()
	srv.conns[id] = noopConn{}
	srv.connWriteMu[id] = &sync.Mutex{}
	srv.mu.Unlock()
}

// noopConn accepts every frame and never answers: enough for the liveness
// question, which is about whether a channel exists, not what it says.
type noopConn struct{}

func (noopConn) WriteJSON(v interface{}) error     { return nil }
func (noopConn) WriteMessage(int, []byte) error    { return nil }
func (noopConn) Close() error                      { return nil }

// TestP2101RegistryOnlyRowIsNotGreen is acceptance 4(a): an agent that exists
// in the registry with no conn-map entry must read active but not dispatchable,
// with a reason. No live agent is involved — the registry/conn-map state is
// constructed directly, which is the honest smallest level for this case.
func TestP2101RegistryOnlyRowIsNotGreen(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("p2101-ghost", "ghost", "1.19.1", "linux", "amd64", "serve", nil)

	row, ok := p2101Rows(t, srv)["p2101-ghost"]
	if !ok {
		t.Fatal("p2101-ghost missing from /api/v1/agents")
	}
	if status, _ := row["status"].(string); status != "active" {
		t.Fatalf("status = %q, want active (the registry says so)", status)
	}
	dispatchable, present := row["dispatchable"].(bool)
	if !present {
		t.Fatalf("row carries no dispatchable field: %v", row)
	}
	if dispatchable {
		t.Fatalf("registry-only agent reported dispatchable=true: %v", row)
	}
	if src, _ := row["liveness_source"].(string); src != "registry-only" {
		t.Fatalf("liveness_source = %q, want registry-only", src)
	}
	reason, _ := row["dispatch_error"].(string)
	if strings.TrimSpace(reason) == "" {
		t.Fatalf("active but not dispatchable with no dispatch_error: %v", row)
	}
	if !strings.Contains(reason, "no live ws connection") {
		t.Fatalf("dispatch_error does not name the absence: %q", reason)
	}
}

// TestP2101LiveWSRowIsDispatchable is acceptance 4(b): a conn-map entry makes
// the row dispatchable and names the source as ws.
func TestP2101LiveWSRowIsDispatchable(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("p2101-live", "live", "1.19.1", "linux", "amd64", "serve", nil)
	p2101AttachWS(srv, "p2101-live")

	row := p2101Rows(t, srv)["p2101-live"]
	if row == nil {
		t.Fatal("p2101-live missing from /api/v1/agents")
	}
	if dispatchable, _ := row["dispatchable"].(bool); !dispatchable {
		t.Fatalf("live ws agent reported dispatchable=false: %v", row)
	}
	if src, _ := row["liveness_source"].(string); src != "ws" {
		t.Fatalf("liveness_source = %q, want ws", src)
	}
	if reason, _ := row["dispatch_error"].(string); reason != "" {
		t.Fatalf("dispatchable row carries a dispatch_error: %q", reason)
	}
}

// TestP2101RelayChannelIsNamedNotIgnored proves the middle step of the
// predicate: a relay channel that holds an agent which never reached the
// conn-map is reported as relay, not as a registry-only green row. Reporting
// it dispatchable would be the old lie one layer down — the dispatch path
// reads the conn-map, so the predicate must not promise more than it can send.
func TestP2101RelayChannelIsNamedNotIgnored(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("p2101-relayed", "relayed", "1.19.1", "linux", "amd64", "serve", nil)

	sess := &relaySession{
		relayID:  "relay-a",
		channels: map[uint32]*virtualConn{},
	}
	sess.channels[7] = &virtualConn{channelID: 7, session: sess, agentID: "p2101-relayed"}
	srv.relayMu.Lock()
	srv.relays["relay-a"] = sess
	srv.relayMu.Unlock()

	row := p2101Rows(t, srv)["p2101-relayed"]
	if row == nil {
		t.Fatal("p2101-relayed missing from /api/v1/agents")
	}
	if src, _ := row["liveness_source"].(string); src != "relay" {
		t.Fatalf("liveness_source = %q, want relay (a relay channel exists)", src)
	}
	if dispatchable, _ := row["dispatchable"].(bool); dispatchable {
		t.Fatalf("agent with a relay channel but no conn-map entry reported dispatchable: %v", row)
	}
	reason, _ := row["dispatch_error"].(string)
	if !strings.Contains(reason, "relay-a") {
		t.Fatalf("dispatch_error does not name the relay holding the channel: %q", reason)
	}
}

// TestP2101InvariantHoldsOnEveryRow is acceptance 4 as a single statement over
// a response containing rows of every kind: active=true implies
// dispatchable=true OR a non-empty dispatch_error, and every row always
// carries the three fields.
func TestP2101InvariantHoldsOnEveryRow(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("p2101-active-live", "live", "1.19.1", "linux", "amd64", "serve", nil)
	p2101AttachWS(srv, "p2101-active-live")
	srv.registry.Register("p2101-active-ghost", "ghost", "1.19.1", "windows", "amd64", "serve", nil)
	srv.registry.Register("p2101-relayed", "relayed", "1.19.1", "linux", "arm64", "serve", nil)

	sess := &relaySession{relayID: "relay-b", channels: map[uint32]*virtualConn{}}
	sess.channels[3] = &virtualConn{channelID: 3, session: sess, agentID: "p2101-relayed"}
	srv.relayMu.Lock()
	srv.relays["relay-b"] = sess
	srv.relayMu.Unlock()

	srv.registry.Register("p2101-gone", "gone", "1.19.1", "linux", "amd64", "serve", nil)
	srv.registry.Unregister("p2101-gone")

	rows := p2101Rows(t, srv)
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4: %v", len(rows), rows)
	}
	for id, row := range rows {
		_, hasDispatchable := row["dispatchable"].(bool)
		src, hasSource := row["liveness_source"].(string)
		if !hasDispatchable || !hasSource || src == "" {
			t.Fatalf("%s: liveness fields missing (dispatchable=%v source=%q)", id, hasDispatchable, src)
		}
		switch src {
		case "ws", "relay", "registry-only":
		default:
			t.Fatalf("%s: liveness_source = %q, outside the stated set", id, src)
		}
		if status, _ := row["status"].(string); status == "active" {
			dispatchable, _ := row["dispatchable"].(bool)
			reason, _ := row["dispatch_error"].(string)
			if !dispatchable && strings.TrimSpace(reason) == "" {
				t.Fatalf("%s: active=true, dispatchable=false, no dispatch_error — a green row with a dead control", id)
			}
		}
	}
}

// TestP2101DispatchableAgentNeverAnsweredAgentUnavailable is acceptance 3, or
// the third bullet: the endpoint says dispatchable, so the refusal path may not
// blame absence. The frame here fails on the link; that is the agent's/link's
// error and must be passed through as such.
func TestP2101DispatchableAgentNeverAnsweredAgentUnavailable(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	const agentID = "p2101-broken-link"
	srv.registry.Register(agentID, "broken", "1.19.1", "linux", "amd64", "serve", nil)
	srv.mu.Lock()
	srv.conns[agentID] = failingWriteConn{}
	srv.connWriteMu[agentID] = &sync.Mutex{}
	srv.mu.Unlock()

	row := p2101Rows(t, srv)[agentID]
	if dispatchable, _ := row["dispatchable"].(bool); !dispatchable {
		t.Fatalf("precondition: row not dispatchable, cannot test the refusal path: %v", row)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/exec",
		strings.NewReader(`{"command":"echo hi","timeout":5}`))
	srv.mux.ServeHTTP(rec, req)

	var body struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode exec response %q: %v", rec.Body.String(), err)
	}
	if rec.Code == http.StatusServiceUnavailable && body.Error.Code == "AGENT_UNAVAILABLE" {
		t.Fatalf("dispatchable agent refused as AGENT_UNAVAILABLE: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if body.Error.Code == "AGENT_UNAVAILABLE" {
		t.Fatalf("dispatchable agent answered AGENT_UNAVAILABLE: %s", rec.Body.String())
	}
	if !strings.Contains(body.Error.Message, "simulated transport failure") {
		t.Fatalf("the link's own error was not passed through: %q", body.Error.Message)
	}
}

// TestP2101RefusalStatesTheReason is acceptance 3 for the not-dispatchable
// direction: exec must answer with the predicate's sentence, not the generic
// "agent X not connected".
func TestP2101RefusalStatesTheReason(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	const agentID = "p2101-absent"
	srv.registry.Register(agentID, "absent", "1.19.1", "linux", "amd64", "serve", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/exec",
		strings.NewReader(`{"command":"echo hi","timeout":5}`))
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refusal %q: %v", rec.Body.String(), err)
	}
	if body.Error.Code != "AGENT_UNAVAILABLE" {
		t.Fatalf("code = %q, want AGENT_UNAVAILABLE for an agent with no transport", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "no live ws connection") {
		t.Fatalf("refusal states no reason: %q", body.Error.Message)
	}
}
