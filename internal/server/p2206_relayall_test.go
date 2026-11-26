package server

// P22-06 — relay_all end-to-end acceptance, on loopback (method copied from
// p2104_forwardpolicy_test.go: real servers, real WebSockets, polled
// conditions, no fixed sleeps where a condition can be waited on).
//
// The five acceptance cells:
//   1. local/relay/relay_all each set without error; the persisted policy
//      reads back (GET).
//   2. relay_all: 2+ simultaneous clients ALL appear upstream as
//      relay/<link>/<name>.
//   3. relay: only the tagged client appears upstream; the untagged
//      neighbour does not (the untagged default is local).
//   4. Toggle one client off mid-run: it leaves the upstream list, others
//      stay; toggle back on restores it. No relay restart anywhere.
//   5. POST forward-policy {"action":"relay_all"} does NOT answer 400.

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

// p2206PostPolicy drives the HTTP setter exactly as the operator does.
func p2206PostPolicy(t *testing.T, ts *httptest.Server, agentID, action string) (int, map[string]interface{}) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/v1/agents/"+agentID+"/forward-policy",
		"application/json", strings.NewReader(`{"action":"`+action+`"}`))
	if err != nil {
		t.Fatalf("POST forward-policy %s/%s: %v", agentID, action, err)
	}
	defer resp.Body.Close()
	var body map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// p2206GetPolicy reads the persisted policy row back.
func p2206GetPolicy(t *testing.T, ts *httptest.Server, agentID string) string {
	t.Helper()
	resp, err := http.Get(ts.URL + "/api/v1/agents/" + agentID + "/forward-policy")
	if err != nil {
		t.Fatalf("GET forward-policy %s: %v", agentID, err)
	}
	defer resp.Body.Close()
	var body struct {
		Policy string `json:"policy"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	return body.Policy
}

// p2206UpstreamHas reports whether the upstream registry carries id AS A
// LIVE ROW: a channel_close marks the row inactive without removing it, and
// an inactive row is not "appearing upstream".
func p2206UpstreamHas(up *Server, id string) bool {
	for _, rec := range up.registry.ListAgents() {
		if rec.AgentID == id && rec.Status == "active" {
			return true
		}
	}
	return false
}

// TestP2206_SetterAcceptsAllThreePolicies is acceptance cell 1 + 5: the
// relay_all POST is 200 (the measured defect answered 400), and every
// persisted row reads back through GET.
func TestP2206_SetterAcceptsAllThreePolicies(t *testing.T) {
	_, ts, cleanup := p2104Server(t)
	defer cleanup()

	for _, action := range []string{PolicyLocal, PolicyRelay, PolicyRelayAll} {
		code, body := p2206PostPolicy(t, ts, "agent-"+action, action)
		if code != http.StatusOK {
			t.Fatalf("POST %s: got HTTP %d (%v), want 200 — relay_all must be settable", action, code, body)
		}
		if err, has := body["error"]; has && err != nil {
			t.Fatalf("POST %s returned an error: %v", action, err)
		}
	}
	for _, action := range []string{PolicyLocal, PolicyRelay, PolicyRelayAll} {
		p2104Wait(t, "policy readback "+action, func() bool {
			return p2206GetPolicy(t, ts, "agent-"+action) == action
		})
	}
}

// TestP2206_RelayAllBatchAllAppearUpstream is acceptance cell 2: TWO
// simultaneous clients under relay_all both appear upstream, named by the
// carrying link.
func TestP2206_RelayAllBatchAllAppearUpstream(t *testing.T) {
	up, upTS, upCleanup := p2104Server(t)
	defer upCleanup()
	down, downTS, downCleanup := p2104Server(t)
	defer downCleanup()
	_ = upTS

	down.SetUplinkForwardTarget(upTS.URL, "test-token")
	down.SetForwarderID("relay-ds")
	code, _ := p2206PostPolicy(t, downTS, "batch-a", PolicyRelayAll)
	if code != http.StatusOK {
		t.Fatalf("setter refused relay_all: HTTP %d", code)
	}
	p2206PostPolicy(t, downTS, "batch-b", PolicyRelayAll)

	connA := p2104DialAgent(t, downTS, "batch-a")
	defer connA.Close()
	connB := p2104DialAgent(t, downTS, "batch-b")
	defer connB.Close()

	p2104Wait(t, "batch-a upstream", func() bool { return p2206UpstreamHas(up, "relay/relay-ds/batch-a") })
	p2104Wait(t, "batch-b upstream", func() bool { return p2206UpstreamHas(up, "relay/relay-ds/batch-b") })

	recA, ok := down.mirroredAgent("batch-a")
	if !ok || recA.Via != "relay-ds" || !recA.Relayed || recA.UpstreamLive != true {
		t.Fatalf("mirror for batch-a: %+v (ok=%v)", recA, ok)
	}
	recB, ok := down.mirroredAgent("batch-b")
	if !ok || recB.Via != "relay-ds" {
		t.Fatalf("mirror for batch-b: %+v (ok=%v)", recB, ok)
	}
}

// TestP2206_RelayForwardsOnlyTheTaggedClient is acceptance cell 3: under
// `relay`, the tagged client appears upstream and the untagged neighbour does
// not — relay never aliases to "forward everything".
func TestP2206_RelayForwardsOnlyTheTaggedClient(t *testing.T) {
	up, upTS, upCleanup := p2104Server(t)
	defer upCleanup()
	down, downTS, downCleanup := p2104Server(t)
	defer downCleanup()

	down.SetUplinkForwardTarget(upTS.URL, "test-token")
	down.SetForwarderID("relay-ds")
	p2206PostPolicy(t, downTS, "tagged", PolicyRelay)
	// neighbour stays untagged: the untagged default is local.

	tagged := p2104DialAgent(t, downTS, "tagged")
	defer tagged.Close()
	neighbour := p2104DialAgent(t, downTS, "neighbour")
	defer neighbour.Close()

	p2104Wait(t, "tagged upstream", func() bool { return p2206UpstreamHas(up, "relay/relay-ds/tagged") })

	p2104Wait(t, "untagged neighbour registered locally", func() bool {
		return p2104RegistryHas(down, "neighbour")
	})
	time.Sleep(300 * time.Millisecond) // an implementation that forwards the neighbour would have done so by now
	if p2206UpstreamHas(up, "relay/relay-ds/neighbour") {
		t.Fatal("untagged neighbour was forwarded under a relay-tagged policy")
	}
	if _, ok := down.mirroredAgent("neighbour"); ok {
		t.Fatal("untagged neighbour left a mirror record")
	}
}

// TestP2206_ToggleOffThenOnMidRun is acceptance cell 4: toggle ONE client's
// policy to local mid-run — it leaves the upstream list while its neighbour
// stays; toggling relay_all back restores it. The client's own socket never
// reconnects, and both updates need no relay restart.
func TestP2206_ToggleOffThenOnMidRun(t *testing.T) {
	up, upTS, upCleanup := p2104Server(t)
	defer upCleanup()
	down, downTS, downCleanup := p2104Server(t)
	defer downCleanup()

	down.SetUplinkForwardTarget(upTS.URL, "test-token")
	down.SetForwarderID("relay-ds")
	for _, name := range []string{"victim", "neighbour"} {
		p2206PostPolicy(t, downTS, name, PolicyRelayAll)
	}

	victim := p2104DialAgent(t, downTS, "victim")
	defer victim.Close()
	neighbour := p2104DialAgent(t, downTS, "neighbour")
	defer neighbour.Close()
	p2104Wait(t, "both upstream", func() bool {
		return p2206UpstreamHas(up, "relay/relay-ds/victim") && p2206UpstreamHas(up, "relay/relay-ds/neighbour")
	})

	// The victim keeps sending on its live socket; nothing reconnects.
	p2206PostPolicy(t, downTS, "victim", PolicyLocal)

	p2104Wait(t, "victim OFF upstream", func() bool { return !p2206UpstreamHas(up, "relay/relay-ds/victim") })
	if _, ok := down.mirroredAgent("victim"); ok {
		t.Fatal("victim kept a mirror record after toggle-off")
	}
	if !p2206UpstreamHas(up, "relay/relay-ds/neighbour") {
		t.Fatal("toggle-off took the neighbour's upstream channel down")
	}
	if p2206GetPolicy(t, downTS, "victim") != PolicyLocal {
		t.Fatalf("victim policy readback: got %s, want local", p2206GetPolicy(t, downTS, "victim"))
	}

	p2206PostPolicy(t, downTS, "victim", PolicyRelayAll)
	p2104Wait(t, "victim ON upstream", func() bool { return p2206UpstreamHas(up, "relay/relay-ds/victim") })
	if rec, ok := down.mirroredAgent("victim"); !ok || rec.Via != "relay-ds" {
		t.Fatalf("victim mirror after toggle-on: %+v (ok=%v)", rec, ok)
	}
	if !p2206UpstreamHas(up, "relay/relay-ds/neighbour") {
		t.Fatal("toggle-on disturbed the neighbour's upstream channel")
	}
}

// TestP2206_ToggleAppliesWithoutClientReconnect proves the mid-run toggle
// does not depend on the client's socket restart: after toggle-on, frames the
// ALREADY-CONNECTED client sends are mirrored upstream (the socket records a
// reconnect only via the registry's Reconnects counter — asserted unchanged).
func TestP2206_ToggleAppliesWithoutClientReconnect(t *testing.T) {
	up, upTS, upCleanup := p2104Server(t)
	defer upCleanup()
	down, downTS, downCleanup := p2104Server(t)
	defer downCleanup()

	down.SetUplinkForwardTarget(upTS.URL, "test-token")
	down.SetForwarderID("relay-ds")

	conn := p2104DialAgent(t, downTS, "mirrored")
	defer conn.Close()
	p2104Wait(t, "local registration", func() bool { return p2104RegistryHas(down, "mirrored") })

	// Reach in and read the reconnect count on the row the first arrival
	// created: the toggle must not change it.
	connAtArrival := 0
	if rec, err := down.registry.GetHealth("mirrored"); err == nil {
		connAtArrival = rec.Reconnects
	}

	p2206PostPolicy(t, downTS, "mirrored", PolicyRelayAll)
	p2104Wait(t, "upstream registration", func() bool { return p2206UpstreamHas(up, "relay/relay-ds/mirrored") })

	// The link carries frames the connected client sends from now on.
	ping, _ := json.Marshal(protocol.Envelope{ID: "p2206-1", Type: protocol.TypePing})
	if err := conn.WriteMessage(websocket.TextMessage, ping); err != nil {
		t.Fatalf("send ping: %v", err)
	}
	// The upstream server answers the ping — proof the frame crossed the
	// toggle-created link end to end.
	conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	var sawPong bool
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && !sawPong {
		_, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		var env protocol.Envelope
		if json.Unmarshal(data, &env) == nil && env.Type == protocol.TypePong && env.ID == "p2206-1" {
			sawPong = true
		}
	}
	if !sawPong {
		t.Fatal("frame from the already-connected client never came back through the toggled link")
	}

	if rec, err := down.registry.GetHealth("mirrored"); err != nil || rec.Reconnects != connAtArrival {
		t.Fatalf("toggle-on forced a client reconnect: reconnects %d (was %d), err=%v",
			func() int { if rec, err := down.registry.GetHealth("mirrored"); err == nil { return rec.Reconnects }; return -2 }(),
			connAtArrival, err)
	}
}
