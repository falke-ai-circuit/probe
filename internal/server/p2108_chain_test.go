package server

// P21-08 — a two-hop loopback harness: one chain object, counters measured at
// the copy points, and a dead hop that stays named.
//
// Topology driven for real, on the loopback only:
//
//	server ← relay-A ← relay-B ← agent
//
// relay-A and relay-B are the production relay binary's own code
// (relay.New/Run), not a mock. The agent is a raw WebSocket speaking the same
// AgentInfo envelope a real one sends. Nothing touches the Radxa or OVH: every
// host-level claim in this unit is UNPROVEN-ON-HOST.

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/falke-ai-circuit/probe/internal/relay"
	"github.com/gorilla/websocket"
)

type p2108Link struct {
	AgentsCarried     int    `json:"agents_carried"`
	AgentsCurrent     int    `json:"agents_current"`
	BytesRelayed      int64  `json:"bytes_relayed"`
	BytesFromRelay    int64  `json:"bytes_from_relay"`
	BytesToRelay      int64  `json:"bytes_to_relay"`
	Reconnects        int    `json:"reconnects"`
	LastError         string `json:"last_error"`
	UpstreamLatencyMs int64  `json:"upstream_latency_ms"`
	ProbeLatencyMs    int64  `json:"probe_latency_ms"`
}

type p2108Hop struct {
	ID       string     `json:"id"`
	Role     string     `json:"role"`
	State    string     `json:"state"`
	Error    string     `json:"error"`
	Channels int        `json:"channels"`
	Link     *p2108Link `json:"link"`
	Observed bool       `json:"observed"`
}

type p2108Chain struct {
	ID      string     `json:"id"`
	State   string     `json:"state"`
	Path    []string   `json:"path"`
	Hops    []p2108Hop `json:"hops"`
	Failing *struct {
		ID    string `json:"id"`
		Error string `json:"error"`
	} `json:"failing_hop"`
	Agent          string `json:"agent"`
	DegradedReason string `json:"degraded_reason"`
}

func (c p2108Chain) hop(id string) *p2108Hop {
	for i := range c.Hops {
		if c.Hops[i].ID == id {
			return &c.Hops[i]
		}
	}
	return nil
}

// p2108FreeAddr reserves a loopback port and releases it for the relay to bind.
func p2108FreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// p2108WaitFor polls cond until it holds or the timeout expires.
func p2108WaitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// p2108StartRelay runs a real relay process-equivalent on a loopback port.
func p2108StartRelay(t *testing.T, upstreamURL, relayID string) (*relay.Relay, string) {
	t.Helper()
	addr := p2108FreeAddr(t)
	r := relay.New(relay.Config{
		UpstreamURL: upstreamURL,
		ListenAddr:  addr,
		Token:       "test-token",
		RelayID:     relayID,
	})
	go func() {
		if err := r.Run(); err != nil && !strings.Contains(err.Error(), "Server closed") {
			t.Logf("relay %s exited: %v", relayID, err)
		}
	}()
	t.Cleanup(r.Stop)
	p2108WaitFor(t, 5*time.Second, "relay "+relayID+" listening on "+addr, func() bool {
		resp, err := http.Get("http://" + addr + "/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	return r, addr
}

// p2108Fetch reads /api/v1/chain through the production mux and returns the
// chains plus the raw body (quoted in the artefact).
func p2108Fetch(t *testing.T, srv *Server) ([]p2108Chain, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/chain", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/chain: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK   bool `json:"ok"`
		Data struct {
			Chains []p2108Chain `json:"chains"`
			Count  int          `json:"count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode chain response %q: %v", rec.Body.String(), err)
	}
	if !out.OK {
		t.Fatalf("chain response not ok: %s", rec.Body.String())
	}
	if out.Data.Count != len(out.Data.Chains) {
		t.Fatalf("chain count %d disagrees with %d objects", out.Data.Count, len(out.Data.Chains))
	}
	return out.Data.Chains, rec.Body.String()
}

func p2108ChainFor(t *testing.T, chains []p2108Chain, agentID string) p2108Chain {
	t.Helper()
	for _, c := range chains {
		if c.Agent == agentID {
			return c
		}
	}
	raw, _ := json.Marshal(chains)
	t.Fatalf("no chain for agent %q in %s", agentID, string(raw))
	return p2108Chain{}
}

// p2108DialAgent connects an agent to a relay and sends its AgentInfo.
func p2108DialAgent(t *testing.T, relayAddr, name string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+relayAddr+"/ws", nil)
	if err != nil {
		t.Fatalf("dial relay %s: %v", relayAddr, err)
	}
	res, _ := json.Marshal(protocol.AgentInfo{Name: name, Version: "v-p2108", OS: "linux", Arch: "amd64"})
	env, _ := json.Marshal(protocol.Envelope{ID: "reg-p2108", Type: "agent_info", Result: res})
	if err := conn.WriteMessage(websocket.TextMessage, env); err != nil {
		t.Fatalf("send agent_info: %v", err)
	}
	return conn
}

// TestP2108_TwoHopChain is the acceptance: one object, one path, counters that
// move only when bytes move.
func TestP2108_TwoHopChain(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.SetVersion("v1.19.1-p2108")
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	serverWS := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	relayA, addrA := p2108StartRelay(t, serverWS, "relay-A")
	relayB, addrB := p2108StartRelay(t, "ws://"+addrA+"/ws", "relay-B")

	// relay-B's registration must have reached relay-A: that is the hop the
	// server cannot see and reads through relay-A's own status document.
	p2108WaitFor(t, 5*time.Second, "relay-B nested under relay-A", func() bool {
		resp, err := http.Get("http://" + addrA + "/api/v1/relay/status")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var doc struct {
			Downstream struct {
				Nested []struct {
					ID string `json:"id"`
				} `json:"nested"`
			} `json:"downstream"`
		}
		if json.NewDecoder(resp.Body).Decode(&doc) != nil {
			return false
		}
		for _, n := range doc.Downstream.Nested {
			if n.ID == "relay-B" {
				return true
			}
		}
		return false
	})

	agentID := "relay/relay-A/p2108-agent"
	agent := p2108DialAgent(t, addrB, "p2108-agent")
	defer agent.Close()

	p2108WaitFor(t, 5*time.Second, "agent admitted on relay-A's link", func() bool {
		if _, err := srv.registry.GetHealth(agentID); err != nil {
			return false
		}
		chains, _ := p2108Fetch(t, srv)
		return len(chains) == 1 && chains[0].Agent == agentID
	})

	chains, raw := p2108Fetch(t, srv)
	if len(chains) != 1 {
		t.Fatalf("two-hop path must be ONE chain object, got %d: %s", len(chains), raw)
	}
	chain := chains[0]
	wantPath := []string{"server", "relay-A", "relay-B", agentID}
	if strings.Join(chain.Path, "->") != strings.Join(wantPath, "->") {
		t.Fatalf("path: got %v, want %v", chain.Path, wantPath)
	}
	if chain.State != chainStateUp {
		t.Fatalf("chain state: got %q, want up (%s)", chain.State, raw)
	}
	if chain.Failing != nil {
		t.Fatalf("up chain must not name a failing hop: %+v", chain.Failing)
	}
	t.Logf("CHAIN BEFORE: %s", raw)

	hopA := chain.hop("relay-A")
	if hopA == nil || hopA.Link == nil {
		t.Fatalf("hop relay-A carries no link counters: %s", raw)
	}
	if hopA.Link.AgentsCarried != 1 {
		t.Errorf("agents_carried on relay-A link: got %d, want 1", hopA.Link.AgentsCarried)
	}
	if hopA.Link.UpstreamLatencyMs != latencyUnmeasured {
		t.Errorf("upstream_latency_ms must be stated absent (-1), got %d", hopA.Link.UpstreamLatencyMs)
	}
	if hopA.Link.ProbeLatencyMs < 0 {
		t.Errorf("probe_latency_ms was not measured: %d", hopA.Link.ProbeLatencyMs)
	}
	hopB := chain.hop("relay-B")
	if hopB == nil || hopB.Link == nil {
		t.Fatalf("hop relay-B carries no link counters: %s", raw)
	}

	// A payload of known size, sent once. Both links must report exactly its
	// length: bytes are counted where they move, not estimated.
	payload := []byte(`{"id":"p2108-payload","type":"heartbeat"}`)
	beforeA, beforeB := hopA.Link.BytesFromRelay, hopB.Link.BytesFromRelay
	if err := agent.WriteMessage(websocket.TextMessage, payload); err != nil {
		t.Fatalf("send payload: %v", err)
	}

	p2108WaitFor(t, 3*time.Second, "bytes_relayed to reflect the payload", func() bool {
		chains, _ := p2108Fetch(t, srv)
		c := p2108ChainFor(t, chains, agentID)
		a, b := c.hop("relay-A"), c.hop("relay-B")
		if a == nil || b == nil || a.Link == nil || b.Link == nil {
			return false
		}
		return a.Link.BytesFromRelay-beforeA == int64(len(payload)) &&
			b.Link.BytesFromRelay-beforeB == int64(len(payload))
	})
	chains, raw = p2108Fetch(t, srv)
	chain = p2108ChainFor(t, chains, agentID)
	hopA, hopB = chain.hop("relay-A"), chain.hop("relay-B")
	if got := hopA.Link.BytesFromRelay - beforeA; got != int64(len(payload)) {
		t.Errorf("server-side link bytes delta: got %d, want %d", got, len(payload))
	}
	if got := hopB.Link.BytesFromRelay - beforeB; got != int64(len(payload)) {
		t.Errorf("nested link bytes delta: got %d, want %d", got, len(payload))
	}
	if hopA.Link.BytesRelayed < int64(len(payload)) {
		t.Errorf("bytes_relayed did not accumulate: %d", hopA.Link.BytesRelayed)
	}
	bytesBeforeReconnect := hopA.Link.BytesRelayed
	carriedBeforeReconnect := hopA.Link.AgentsCarried

	// Forced reconnect: the agent's socket drops and it re-registers on the
	// same link. reconnects is the registry field extended to the link.
	agent.Close()
	agent = p2108DialAgent(t, addrB, "p2108-agent")
	defer agent.Close()

	p2108WaitFor(t, 5*time.Second, "link reconnects to follow the registry field", func() bool {
		chains, _ := p2108Fetch(t, srv)
		for _, c := range chains {
			if c.Agent != agentID {
				continue
			}
			h := c.hop("relay-A")
			if h != nil && h.Link != nil && h.Link.Reconnects >= 1 {
				return true
			}
		}
		return false
	})
	chains, raw = p2108Fetch(t, srv)
	chain = p2108ChainFor(t, chains, agentID)
	hopA = chain.hop("relay-A")
	rec, err := srv.registry.GetHealth(agentID)
	if err != nil {
		t.Fatalf("registry record for %s: %v", agentID, err)
	}
	if hopA.Link.Reconnects != rec.Reconnects {
		t.Errorf("link reconnects %d disagrees with registry reconnects %d", hopA.Link.Reconnects, rec.Reconnects)
	}
	if hopA.Link.AgentsCarried != carriedBeforeReconnect {
		t.Errorf("agents_carried inflated by a reconnect: %d → %d", carriedBeforeReconnect, hopA.Link.AgentsCarried)
	}
	if hopA.Link.BytesRelayed < bytesBeforeReconnect {
		t.Errorf("bytes_relayed reset across a reconnect: %d → %d", bytesBeforeReconnect, hopA.Link.BytesRelayed)
	}
	t.Logf("CHAIN AFTER RECONNECT: %s", raw)

	_ = relayA
	_ = relayB
}

// TestP2108_DeadHopStaysNamed kills the middle hop and asserts the same chain
// object survives, degraded, naming that hop.
func TestP2108_DeadHopStaysNamed(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	serverWS := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	relayA, addrA := p2108StartRelay(t, serverWS, "relay-A")
	_, addrB := p2108StartRelay(t, "ws://"+addrA+"/ws", "relay-B")

	agentID := "relay/relay-A/p2108-dead"
	agent := p2108DialAgent(t, addrB, "p2108-dead")
	defer agent.Close()

	p2108WaitFor(t, 5*time.Second, "agent admitted before the kill", func() bool {
		chains, _ := p2108Fetch(t, srv)
		return len(chains) == 1 && chains[0].Agent == agentID
	})
	before, rawBefore := p2108Fetch(t, srv)
	t.Logf("CHAIN BEFORE KILL: %s", rawBefore)
	pathBefore := strings.Join(before[0].Path, "->")

	// Kill the middle hop: relay-A's listener and its uplink socket.
	relayA.Stop()

	// Deterministic synchronisation, not a sleep and not a timing guess: the
	// server removes the relay from its registry under relayMu in
	// cleanupRelaySession. Waiting on that condition pins the read below to the
	// retained-snapshot branch, whose output is fully determined by the
	// snapshot the last successful read stored.
	p2108WaitFor(t, 5*time.Second, "server to drop relay-A's session", func() bool {
		srv.relayMu.RLock()
		_, live := srv.relays["relay-A"]
		srv.relayMu.RUnlock()
		return !live
	})

	chains, rawAfter := p2108Fetch(t, srv)
	t.Logf("CHAIN AFTER KILL: %s", rawAfter)
	if len(chains) != 1 {
		t.Fatalf("dead hop must leave one chain object, got %d: %s", len(chains), rawAfter)
	}
	after := chains[0]

	if strings.Join(after.Path, "->") != pathBefore {
		t.Errorf("path changed when a hop died: %q → %q", pathBefore, strings.Join(after.Path, "->"))
	}
	hopA := after.hop("relay-A")
	if hopA == nil {
		t.Fatalf("dead hop relay-A vanished from the chain: %s", rawAfter)
	}
	if hopA.State != chainStateDown {
		t.Errorf("dead hop state: got %q, want down", hopA.State)
	}
	if hopA.Error == "" {
		t.Errorf("dead hop carries no error text: %s", rawAfter)
	}
	if after.Failing == nil || after.Failing.ID != "relay-A" {
		t.Fatalf("failing_hop must name the dead hop relay-A, got %+v (%s)", after.Failing, rawAfter)
	}
	if after.Failing.Error == "" {
		t.Errorf("failing_hop names %q with no error text", after.Failing.ID)
	}
	// The hop behind the dead one keeps its identity in the path, last-known.
	if hopB := after.hop("relay-B"); hopB == nil {
		t.Errorf("hop behind the dead middle hop vanished from the chain: %s", rawAfter)
	} else if hopB.Observed || hopB.State == chainStateUp {
		t.Errorf("hop behind a dead hop must be last-known, not up: %+v", *hopB)
	}
	// The endpoint did not wait on the dead hop: no status read was made, so
	// probe_latency_ms is exactly the value measured before the kill. This is
	// the deterministic form of "must not block on a dead hop" — no clock.
	if beforeA := before[0].hop("relay-A"); beforeA != nil && beforeA.Link != nil && hopA.Link != nil {
		if got := hopA.Link.ProbeLatencyMs; got != beforeA.Link.ProbeLatencyMs {
			t.Errorf("dead hop was probed again after it died: probe_latency_ms %d → %d",
				beforeA.Link.ProbeLatencyMs, got)
		}
	}
	if hopA.Observed {
		t.Errorf("dead hop reported observed=true: %s", rawAfter)
	}
	t.Logf("degraded chain: failing_hop=%s (%s)", after.Failing.ID, after.Failing.Error)
}

// TestP2108_UnreadableHopIsDownNotDegraded pins the other ordering of the same
// defect: the relay is still in the server's registry while its status endpoint
// no longer answers. Making that window permanent instead of racing it is what
// makes the assertion deterministic — the read below never depends on how
// quickly the server's read loop notices the socket.
func TestP2108_UnreadableHopIsDownNotDegraded(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// A port reserved and released: every status read is refused immediately,
	// with no scheduling dependency.
	deadAddr := p2108FreeAddr(t)

	agentID := "relay/relay-A/p2108-unreadable"
	sess := &relaySession{
		relayID:  "relay-A",
		channels: map[uint32]*virtualConn{},
		metadata: &relayMetadata{ListenAddr: deadAddr},
		link:     srv.linkStats.forRelay("relay-A"),
	}
	sess.channels[2] = &virtualConn{channelID: 2, session: sess, agentID: agentID}
	srv.relayMu.Lock()
	srv.relays["relay-A"] = sess
	srv.relayMu.Unlock()

	// The tail the last successful read stored: relay-B was behind relay-A.
	sess.link.setNested([]chainHop{{
		ID:       "relay-B",
		Role:     "relay",
		State:    chainStateUp,
		Channels: 1,
		Link: &LinkCounters{
			AgentsCarried:     1,
			AgentsCurrent:     1,
			UpstreamLatencyMs: latencyUnmeasured,
			ProbeLatencyMs:    latencyUnmeasured,
		},
		Observed: true,
	}})

	chains, raw := p2108Fetch(t, srv)
	if len(chains) != 1 {
		t.Fatalf("one live link must render one chain, got %d: %s", len(chains), raw)
	}
	chain := chains[0]

	wantPath := "server->relay-A->relay-B->" + agentID
	if got := strings.Join(chain.Path, "->"); got != wantPath {
		t.Errorf("path: got %q, want %q (%s)", got, wantPath, raw)
	}
	if chain.State != chainStateDegraded {
		t.Errorf("chain state: got %q, want degraded", chain.State)
	}
	if chain.Failing == nil || chain.Failing.ID != "relay-A" {
		t.Fatalf("failing_hop must name the unreadable hop relay-A, got %+v (%s)", chain.Failing, raw)
	}
	hopA := chain.hop("relay-A")
	if hopA == nil {
		t.Fatalf("unreadable hop missing from the chain: %s", raw)
	}
	if hopA.State != chainStateDown {
		t.Errorf("hop that answers nothing must be down, got %q", hopA.State)
	}
	if !strings.Contains(hopA.Error, "unreadable") {
		t.Errorf("unreadable hop carries no read failure: %q", hopA.Error)
	}
	hopB := chain.hop("relay-B")
	if hopB == nil {
		t.Fatalf("hop behind the unreadable hop dropped from the path: %s", raw)
	}
	if hopB.State != chainStateDegraded || hopB.Observed {
		t.Errorf("hop behind the unreadable hop must be last-known degraded: %+v", *hopB)
	}
	t.Logf("UNREADABLE HOP: %s", raw)
}

// TestP2108_BlackHoledHopBounded proves the endpoint answers while a hop
// accepts the connection and never replies.
func TestP2108_BlackHoledHopBounded(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	serverWS := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	// A hop whose status endpoint accepts and never answers.
	blackhole, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("blackhole listener: %v", err)
	}
	defer blackhole.Close()
	held := make(chan net.Conn, 8)
	go func() {
		for {
			c, err := blackhole.Accept()
			if err != nil {
				return
			}
			held <- c // held open, never written to
		}
	}()
	defer func() {
		close(held)
		for c := range held {
			c.Close()
		}
	}()

	// A relay registration by hand: the socket is live, the hop it declares
	// answers nothing.
	conn, _, err := websocket.DefaultDialer.Dial(serverWS, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial server: %v", err)
	}
	defer conn.Close()
	reg, _ := json.Marshal(map[string]interface{}{
		"type":     "relay_register",
		"relay_id": "relay-blackhole",
		"token":    "test-token",
		"metadata": map[string]interface{}{
			"listen_addr": blackhole.Addr().String(),
			"max_agents":  5,
		},
	})
	frame := make([]byte, 5+len(reg))
	frame[0] = 0x42
	binary.BigEndian.PutUint32(frame[1:5], 0)
	copy(frame[5:], reg)
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("send relay_register: %v", err)
	}

	p2108WaitFor(t, 3*time.Second, "black-holed relay admitted", func() bool {
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/relays", nil))
		return rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), "relay-blackhole")
	})

	start := time.Now()
	chains, raw := p2108Fetch(t, srv)
	elapsed := time.Since(start)
	t.Logf("CHAIN WITH BLACK-HOLED HOP: %s", raw)
	if len(chains) != 1 {
		t.Fatalf("chain count with a black-holed hop: got %d, want 1 (%s)", len(chains), raw)
	}
	chain := chains[0]
	if chain.State != chainStateDegraded {
		t.Errorf("chain state: got %q, want degraded", chain.State)
	}
	if chain.Failing == nil || chain.Failing.ID != "relay-blackhole" {
		t.Fatalf("failing hop not named: %s", raw)
	}
	if chain.Failing.Error == "" {
		t.Errorf("failing hop has no error text: %s", raw)
	}
	if elapsed > time.Second {
		t.Errorf("chain endpoint took %v with a black-holed hop; bound is %v", elapsed, chainObserveTimeout)
	}
	t.Logf("bounded read answered in %v (per-hop bound %v)", elapsed, chainObserveTimeout)
}
