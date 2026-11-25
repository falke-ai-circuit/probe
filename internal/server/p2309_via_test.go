package server

// P23-09 — a relayed agent's row must name the relay that carries it, and a
// directly connected agent must not pretend to have one.
//
// Measured at b47227f: GET /api/v1/agents returned
//   {"agent_id":"relay/relay-ds/relayed-01", ... "liveness_source":"relay"}
// with no `via` key at all, and GET /api/v1/topology returned the node
// {"id":"relay/relay-ds/relayed-01","relayed":true} with no `via` either.
// The relay was live and carrying the agent; the operator could not see which
// one. These tests pin the two directions in the HTTP response shape a client
// actually receives, plus the topology node's own field.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// p2309RouteState builds the routing state a relay registration writes: the
// registry row under the prefixed id, the relay session holding the channel,
// and the route entry that names the carrier. It is the same state relay_ws.go
// creates at registration, constructed directly so the test pins the read path
// without standing up a socket.
func p2309RouteState(srv *Server, relayID, publishedName string) string {
	agentID := "relay/" + relayID + "/" + publishedName
	sess := &relaySession{relayID: relayID, channels: map[uint32]*virtualConn{}}
	vc := &virtualConn{channelID: 11, session: sess, agentID: agentID}
	sess.channels[11] = vc

	srv.relayMu.Lock()
	if srv.relays == nil {
		srv.relays = map[string]*relaySession{}
	}
	srv.relays[relayID] = sess
	srv.relayMu.Unlock()

	srv.registry.Register(agentID, publishedName, "1.19.1", "linux", "amd64", "connect", nil)
	srv.setRelayRoute(agentID, publishedName, relayID, 11, vc)

	srv.mu.Lock()
	srv.conns[agentID] = vc
	srv.mu.Unlock()
	return agentID
}

// TestP2309RelayedRowNamesItsRelay is acceptance 2: the row carries
// via=<relay-id> and the value is the relay's identity, not the server's name
// and not the agent's own id.
func TestP2309RelayedRowNamesItsRelay(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	const relayID = "relay-zeta"
	agentID := p2309RouteState(srv, relayID, "relayed-01")

	row, ok := p2101Rows(t, srv)[agentID]
	if !ok {
		t.Fatalf("%s missing from /api/v1/agents", agentID)
	}
	via, present := row["via"].(string)
	if !present || via == "" {
		t.Fatalf("relayed row carries no via: %v", row)
	}
	if via != relayID {
		t.Fatalf("via = %q, want %q (the relay carrying the agent)", via, relayID)
	}
	if via == srv.addr || via == agentID {
		t.Fatalf("via = %q names the requesting side, not the carrier", via)
	}
	if src, _ := row["liveness_source"].(string); src != LivenessSourceRelay {
		t.Fatalf("precondition: liveness_source = %q, want relay", src)
	}
}

// TestP2309DirectRowHasNoVia is acceptance 3: a direct agent's row has no via
// key at all — not "", not its own id.
func TestP2309DirectRowHasNoVia(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("direct-01", "direct-01", "1.19.1", "linux", "amd64", "connect", nil)
	p2101AttachWS(srv, "direct-01")

	row, ok := p2101Rows(t, srv)["direct-01"]
	if !ok {
		t.Fatal("direct-01 missing from /api/v1/agents")
	}
	if _, present := row["via"]; present {
		t.Fatalf("direct row carries a via key: %v", row)
	}
	if src, _ := row["liveness_source"].(string); src != LivenessSourceWS {
		t.Fatalf("precondition: liveness_source = %q, want ws", src)
	}
}

// TestP2309BothDirectionsInOneResponse is acceptance 3's "side by side": one
// response, one relayed row naming its carrier and one direct row carrying
// nothing.
func TestP2309BothDirectionsInOneResponse(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	relayedID := p2309RouteState(srv, "relay-zeta", "relayed-01")
	srv.registry.Register("direct-01", "direct-01", "1.19.1", "linux", "amd64", "connect", nil)
	p2101AttachWS(srv, "direct-01")

	rows := p2101Rows(t, srv)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2: %v", len(rows), rows)
	}
	if via, _ := rows[relayedID]["via"].(string); via != "relay-zeta" {
		t.Fatalf("relayed row via = %q, want relay-zeta (%v)", via, rows[relayedID])
	}
	if _, present := rows["direct-01"]["via"]; present {
		t.Fatalf("direct row carries a via key: %v", rows["direct-01"])
	}
}

// TestP2309TopologyNodeCarriesVia is acceptance 4: the relayed node's
// provenance is derivable from its own field, and a direct node's is absent.
func TestP2309TopologyNodeCarriesVia(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	relayedID := p2309RouteState(srv, "relay-zeta", "relayed-01")
	srv.registry.Register("direct-01", "direct-01", "1.19.1", "linux", "amd64", "connect", nil)
	p2101AttachWS(srv, "direct-01")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/topology", nil)
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/topology: status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Nodes []map[string]interface{} `json:"nodes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode topology: %v", err)
	}

	var relayedNode, directNode map[string]interface{}
	for _, n := range body.Data.Nodes {
		id, _ := n["id"].(string)
		switch id {
		case relayedID:
			relayedNode = n
		case "direct-01":
			directNode = n
		}
		for _, c := range anyChildren(n) {
			if cid, _ := c["id"].(string); cid == relayedID {
				relayedNode = c
			}
		}
	}
	if relayedNode == nil {
		t.Fatalf("relayed node %s missing from topology: %v", relayedID, body.Data.Nodes)
	}
	if via, _ := relayedNode["via"].(string); via != "relay-zeta" {
		t.Fatalf("relayed topology node via = %q, want relay-zeta (%v)", via, relayedNode)
	}
	if relayed, _ := relayedNode["relayed"].(bool); !relayed {
		t.Fatalf("relayed node lost its relayed flag: %v", relayedNode)
	}
	if directNode == nil {
		t.Fatalf("direct node missing from topology: %v", body.Data.Nodes)
	}
	if _, present := directNode["via"]; present {
		t.Fatalf("direct topology node carries a via key: %v", directNode)
	}
}

// anyChildren returns a topology node's children as maps, whatever shape the
// decoder produced.
func anyChildren(n map[string]interface{}) []map[string]interface{} {
	raw, ok := n["children"].([]interface{})
	if !ok {
		return nil
	}
	out := make([]map[string]interface{}, 0, len(raw))
	for _, c := range raw {
		if m, ok := c.(map[string]interface{}); ok {
			out = append(out, m)
		}
	}
	return out
}
