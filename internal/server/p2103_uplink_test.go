package server

// P21-03 — the local server merges its uplink into /api/v1/topology.
//
// A fake relay serves the status body over HTTP at a loopback address; the
// server is pointed at it and the topology is read through the production
// mux. Both states are asserted: up carries the peer identity, down stays
// present with a named reason and the last known peer. The slow-relay test
// pins the non-blocking bound.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const p2103UplinkBody = `{
  "relay_id": "radxa-relay",
  "max_agents": 100,
  "forward_policy": "",
  "upstream": {
    "url": "wss://ovh.example:443/ws",
    "port": 443,
    "state": "up",
    "connected_at": "2026-11-25T10:00:00Z",
    "peer": {"id": "server-ovh", "name": "ovh-1", "version": "v1.19.1", "listen_addr": "ovh.example:7700"},
    "last_error": ""
  },
  "downstream": {"listen": "127.0.0.1:7701", "agents": [], "channels": 0}
}`

func p2103TopologyEdges(t *testing.T, srv *Server) []map[string]interface{} {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/topology", nil)
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET topology: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			Edges []map[string]interface{} `json:"edges"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode topology: %v", err)
	}
	return resp.Data.Edges
}

func p2103UplinkEdge(t *testing.T, edges []map[string]interface{}) map[string]interface{} {
	t.Helper()
	for _, e := range edges {
		if e["type"] == "uplink" {
			return e
		}
	}
	t.Fatalf("no uplink edge in topology edges: %v", edges)
	return nil
}

func TestP2103_TopologyUplinkUp(t *testing.T) {
	relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/relay/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(p2103UplinkBody))
	}))
	defer relaySrv.Close()

	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.SetUplinkRelayStatus(strings.TrimPrefix(relaySrv.URL, "http://"))

	edge := p2103UplinkEdge(t, p2103TopologyEdges(t, srv))
	if edge["from"] != "server" || edge["to"] != "radxa-relay" {
		t.Errorf("uplink edge endpoints: got %v -> %v, want server -> radxa-relay", edge["from"], edge["to"])
	}
	if edge["state"] != "up" {
		t.Errorf("uplink state: got %v, want up", edge["state"])
	}
	peer, ok := edge["peer"].(map[string]interface{})
	if !ok {
		t.Fatalf("uplink edge carries no peer object: %v", edge)
	}
	if peer["id"] != "server-ovh" || peer["listen_addr"] != "ovh.example:7700" {
		t.Errorf("peer fields: %v", peer)
	}
	if edge["connected_at"] != "2026-11-25T10:00:00Z" {
		t.Errorf("connected_at: got %v", edge["connected_at"])
	}
}

func TestP2103_TopologyUplinkDownRetainsLastKnownPeer(t *testing.T) {
	relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(p2103UplinkBody))
	}))
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.SetUplinkRelayStatus(strings.TrimPrefix(relaySrv.URL, "http://"))

	up := p2103UplinkEdge(t, p2103TopologyEdges(t, srv))
	if up["state"] != "up" {
		t.Fatalf("precondition: uplink state %v, want up", up["state"])
	}

	relaySrv.Close() // relay gone: connection refused, not a 200

	edge := p2103UplinkEdge(t, p2103TopologyEdges(t, srv))
	if edge["state"] != "down" {
		t.Errorf("uplink state after relay loss: got %v, want down", edge["state"])
	}
	if reason, _ := edge["state_reason"].(string); reason == "" {
		t.Errorf("state=down with empty state_reason: the failure is not named")
	}
	peer, ok := edge["peer"].(map[string]interface{})
	if !ok || peer["id"] != "server-ovh" {
		t.Errorf("last known peer not retained on the down edge: %v", edge["peer"])
	}
	if edge["to"] != "radxa-relay" {
		t.Errorf("down edge lost the relay id: got %v", edge["to"])
	}
}

func TestP2103_TopologyAnswersWithUnreachableRelay(t *testing.T) {
	// A relay that accepts the request and then stalls for far longer than
	// the server's status timeout.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		_, _ = w.Write([]byte(p2103UplinkBody))
	}))
	defer slow.Close()

	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.SetUplinkRelayStatus(strings.TrimPrefix(slow.URL, "http://"))

	start := time.Now()
	edges := p2103TopologyEdges(t, srv)
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Errorf("topology took %v with a stalled relay; the status read must be bounded", elapsed)
	}
	edge := p2103UplinkEdge(t, edges)
	if edge["state"] != "down" {
		t.Errorf("stalled relay: state %v, want down", edge["state"])
	}
	if reason, _ := edge["state_reason"].(string); reason == "" {
		t.Errorf("stalled relay: empty state_reason")
	}
	t.Logf("topology answered in %v with the relay stalling 2s (probe timeout %v)", elapsed, uplinkStatusTimeout)
}

func TestP2103_TopologyNoUplinkConfiguredIsAbsent(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	for _, e := range p2103TopologyEdges(t, srv) {
		if e["type"] == "uplink" {
			t.Fatalf("uplink edge present with no uplink configured: %v", e)
		}
	}
}
