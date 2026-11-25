package server

// P21-04 — forward_policy is enforced, and the guards are proved on loopback.
//
// Every test drives real servers over real WebSockets on ephemeral ports. The
// Radxa<->OVH leg named by the spec is UNPROVEN-ON-HOST: owner authorisation is
// still pending, so nothing here touches either host.

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)

// p2104Server starts a server with routes and an httptest listener.
func p2104Server(t *testing.T) (*Server, *httptest.Server, func()) {
	t.Helper()
	srv, cleanup := newV1TestServer(t)
	ts := httptest.NewServer(srv.mux)
	return srv, ts, func() {
		ts.Close()
		cleanup()
	}
}

// p2104DialAgent connects a direct agent and sends its AgentInfo, which is the
// registration message the server's arrival path reads.
func p2104DialAgent(t *testing.T, ts *httptest.Server, name string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	res, _ := json.Marshal(protocol.AgentInfo{Name: name, Version: "v-test", OS: "linux", Arch: "amd64"})
	env, _ := json.Marshal(protocol.Envelope{ID: "reg-1", Type: "agent_info", Result: res})
	if err := conn.WriteMessage(websocket.TextMessage, env); err != nil {
		t.Fatalf("send agent_info for %s: %v", name, err)
	}
	return conn
}

// p2104RegistryHas reports whether a registry record exists.
func p2104RegistryHas(srv *Server, id string) bool {
	for _, rec := range srv.registry.ListAgents() {
		if rec.AgentID == id {
			return true
		}
	}
	return false
}

// p2104Wait polls until cond holds or the deadline passes. Registration happens
// in a goroutine on the far side of a socket; a fixed sleep would be a guess.
func p2104Wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestP2104_LocalPolicyForwardsNothing proves `local` keeps the client on the
// server it reached even when an uplink exists.
func TestP2104_LocalPolicyForwardsNothing(t *testing.T) {
	up, upTS, upCleanup := p2104Server(t)
	defer upCleanup()
	down, downTS, downCleanup := p2104Server(t)
	defer downCleanup()

	down.SetUplinkForwardTarget(upTS.URL, "test-token")
	down.SetForwarderID("relay-ds")
	down.SetForwardPolicy("agent-local", PolicyLocal)

	conn := p2104DialAgent(t, downTS, "agent-local")
	defer conn.Close()

	p2104Wait(t, "local registration", func() bool { return p2104RegistryHas(down, "agent-local") })

	// Give a wrong implementation time to forward, then prove it did not.
	time.Sleep(150 * time.Millisecond)
	for _, rec := range up.registry.ListAgents() {
		if strings.Contains(rec.AgentID, "agent-local") {
			t.Fatalf("local policy forwarded agent upstream: %q appeared on the upstream server", rec.AgentID)
		}
	}
	if got := down.MirroredAgents(); len(got) != 0 {
		t.Fatalf("local policy left a mirror record: %+v", got)
	}
}

// TestP2104_RelayPolicyRegistersUpstreamWithMirror proves `relay` registers the
// client upstream and leaves a read-only mirror marked relayed, with the link.
func TestP2104_RelayPolicyRegistersUpstreamWithMirror(t *testing.T) {
	up, upTS, upCleanup := p2104Server(t)
	defer upCleanup()
	down, downTS, downCleanup := p2104Server(t)
	defer downCleanup()

	down.SetUplinkForwardTarget(upTS.URL, "test-token")
	down.SetForwarderID("relay-ds")
	down.SetForwardPolicy("agent-relay", PolicyRelay)

	conn := p2104DialAgent(t, downTS, "agent-relay")
	defer conn.Close()

	wantUpstream := "relay/relay-ds/agent-relay"
	p2104Wait(t, "upstream registration "+wantUpstream, func() bool {
		return p2104RegistryHas(up, wantUpstream)
	})

	p2104Wait(t, "mirror record", func() bool {
		_, ok := down.mirroredAgent("agent-relay")
		return ok
	})
	rec, _ := down.mirroredAgent("agent-relay")
	if !rec.Relayed || rec.Via != "relay-ds" {
		t.Fatalf("mirror record: got %+v, want relayed=true via=relay-ds", rec)
	}

	// Read-only by construction: a live mirror still refuses the write, because
	// the write belongs upstream, not to the local copy.
	if err := down.MirrorWriteGuard("agent-relay"); err == nil {
		t.Fatal("write against a mirrored record was accepted: the mirror is not read-only")
	} else if !strings.Contains(err.Error(), "mirror is read-only") {
		t.Fatalf("mirror guard reason: %q does not say the mirror is read-only", err.Error())
	}

	// When the uplink dies the mirror stays visible but reports no live
	// upstream, and the refusal says so.
	down.uplinkForward().Close()
	p2104Wait(t, "mirror marked stale", func() bool {
		r, _ := down.mirroredAgent("agent-relay")
		return !r.UpstreamLive
	})
	err := down.MirrorWriteGuard("agent-relay")
	if err == nil || !strings.Contains(err.Error(), RefusalMirrorStale) {
		t.Fatalf("stale mirror refusal: got %v, want code %s", err, RefusalMirrorStale)
	}
	if !strings.Contains(err.Error(), "no live upstream") {
		t.Fatalf("stale mirror refusal: %q does not name the missing upstream", err.Error())
	}
}

// TestP2104_RelayAllAppearsUpstreamWithVia is the acceptance shape: with
// relay_all a client arriving downstream appears upstream, named by the link.
func TestP2104_RelayAllAppearsUpstreamWithVia(t *testing.T) {
	up, upTS, upCleanup := p2104Server(t)
	defer upCleanup()
	down, downTS, downCleanup := p2104Server(t)
	defer downCleanup()

	down.SetUplinkForwardTarget(upTS.URL, "test-token")
	down.SetForwarderID("relay-ds")
	down.SetForwardPolicy("agent-all", PolicyRelayAll)

	conn := p2104DialAgent(t, downTS, "agent-all")
	defer conn.Close()

	wantUpstream := "relay/relay-ds/agent-all"
	p2104Wait(t, "relay_all upstream registration", func() bool {
		return p2104RegistryHas(up, wantUpstream)
	})
	rec, ok := down.mirroredAgent("agent-all")
	if !ok || rec.Via != "relay-ds" || !rec.Relayed {
		t.Fatalf("mirror record for relay_all: %+v (ok=%v)", rec, ok)
	}
}

// TestP2104_LoopIsRefusedAndChainTerminates wires two servers into each other:
// S1 -> S2 -> S1. The second hop carries a chain that names S1's own link, so
// S1 refuses the registration and the terminal client is told why.
func TestP2104_LoopIsRefusedAndChainTerminates(t *testing.T) {
	s1, s1TS, s1Cleanup := p2104Server(t)
	defer s1Cleanup()
	s2, s2TS, s2Cleanup := p2104Server(t)
	defer s2Cleanup()

	s1.SetServerIdentity("s1", "s1")
	s2.SetServerIdentity("s2", "s2")
	s1.SetForwarderID("relay-ds")
	s2.SetForwarderID("relay-up")
	s1.SetUplinkForwardTarget(s2TS.URL, "test-token")
	s2.SetUplinkForwardTarget(s1TS.URL, "test-token") // the cycle
	s1.SetForwardPolicy("agent-loop", PolicyRelayAll)
	s2.SetForwardPolicy("agent-loop", PolicyRelayAll)

	conn := p2104DialAgent(t, s1TS, "agent-loop")
	defer conn.Close()

	// The refusal travels back down the chain to the client that started it.
	conn.SetReadDeadline(time.Now().Add(4 * time.Second))
	var got map[string]string
	for i := 0; i < 4; i++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("no loop refusal reached the client: %v", err)
		}
		if json.Unmarshal(data, &got) == nil && got["code"] == RefusalLoop {
			break
		}
	}
	if got["code"] != RefusalLoop {
		t.Fatalf("client refusal code: got %q, want %s", got["code"], RefusalLoop)
	}
	if !strings.Contains(got["error"], "loop refused") {
		t.Fatalf("loop refusal reason: %q does not say the chain was refused", got["error"])
	}
	if !strings.Contains(got["error"], "uplink chain") {
		t.Fatalf("loop refusal reason: %q does not name the uplink chain", got["error"])
	}
	t.Logf("loop refusal quoted: %s", got["error"])

	// The chain terminated: the cycling agent registered nowhere.
	for _, srv := range []*Server{s1, s2} {
		if p2104RegistryHas(srv, "agent-loop") || p2104RegistryHas(srv, "relay/relay-ds/agent-loop") {
			t.Fatalf("loop chain registered an agent: the cycle was not terminated")
		}
	}
}

// p2104RelayRegistration opens a hand-rolled relay socket: one relay_register
// frame on channel 0, then the peer_hello the server answers with.
func p2104RelayRegistration(t *testing.T, ts *httptest.Server, magic byte, reg map[string]interface{}) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial relay %s: %v", wsURL, err)
	}
	payload, _ := json.Marshal(reg)
	if err := conn.WriteMessage(websocket.BinaryMessage, p2104Frame(magic, 0, payload)); err != nil {
		t.Fatalf("send relay_register: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no answer to relay_register: %v", err)
	}
	conn.SetReadDeadline(time.Time{})
	_, chanID, body, perr := parseRelayFrame(data)
	if perr != nil || chanID != 0 {
		t.Fatalf("registration answer is not a control frame")
	}
	var ctrl struct {
		Type string `json:"type"`
	}
	json.Unmarshal(body, &ctrl)
	if ctrl.Type != "peer_hello" {
		t.Fatalf("registration answer: got type %q, want peer_hello", ctrl.Type)
	}
	return conn
}

// p2104Frame builds the relay wire frame.
func p2104Frame(magic byte, chanID uint32, payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = magic
	binary.BigEndian.PutUint32(frame[1:5], chanID)
	copy(frame[5:], payload)
	return frame
}

// p2104ReadControl reads frames until a control refusal arrives.
func p2104ReadControl(t *testing.T, conn *websocket.Conn) map[string]interface{} {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	for i := 0; i < 4; i++ {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("no control refusal arrived: %v", err)
		}
		_, chanID, payload, perr := parseRelayFrame(data)
		if perr != nil {
			continue
		}
		var ctrl map[string]interface{}
		if json.Unmarshal(payload, &ctrl) != nil || chanID != 0 {
			continue
		}
		if ctrl["type"] == "forward_refused" {
			return ctrl
		}
	}
	t.Fatal("no forward_refused control frame arrived")
	return nil
}

// TestP2104_MaxAgentsPerLink proves the per-link limit: at the limit the next
// channel is refused with the limit named; below it, it is accepted.
func TestP2104_MaxAgentsPerLink(t *testing.T) {
	t.Run("at_limit_refused", func(t *testing.T) {
		srv, ts, cleanup := p2104Server(t)
		defer cleanup()

		conn := p2104RelayRegistration(t, ts, 0x42, map[string]interface{}{
			"type": "relay_register", "relay_id": "relay-full",
			"metadata": map[string]interface{}{"max_agents": 1},
		})
		defer conn.Close()
		_ = srv

		conn.WriteMessage(websocket.BinaryMessage, p2104Frame(0x42, 0,
			[]byte(`{"type":"channel_open","channel_id":1,"relay_id":"relay-full"}`)))
		time.Sleep(50 * time.Millisecond) // channel 1 occupies the only slot
		conn.WriteMessage(websocket.BinaryMessage, p2104Frame(0x42, 0,
			[]byte(`{"type":"channel_open","channel_id":2,"relay_id":"relay-full"}`)))

		ctrl := p2104ReadControl(t, conn)
		if ctrl["code"] != RefusalLinkMaxAgents {
			t.Fatalf("refusal code: got %v, want %s", ctrl["code"], RefusalLinkMaxAgents)
		}
		reason, _ := ctrl["reason"].(string)
		if !strings.Contains(reason, "max_agents limit 1") {
			t.Fatalf("refusal reason %q does not name the limit (1)", reason)
		}
		if !strings.Contains(reason, "relay-full") {
			t.Fatalf("refusal reason %q does not name the link", reason)
		}
		t.Logf("max_agents refusal quoted: %s", reason)
	})

	t.Run("below_limit_accepted", func(t *testing.T) {
		_, ts, cleanup := p2104Server(t)
		defer cleanup()

		conn := p2104RelayRegistration(t, ts, 0x43, map[string]interface{}{
			"type": "relay_register", "relay_id": "relay-roomy",
			"metadata": map[string]interface{}{"max_agents": 2},
		})
		defer conn.Close()

		for _, ch := range []uint32{1, 2} {
			open, _ := json.Marshal(map[string]interface{}{
				"type": "channel_open", "channel_id": ch, "relay_id": "relay-roomy",
			})
			conn.WriteMessage(websocket.BinaryMessage, p2104Frame(0x43, 0, open))
			time.Sleep(30 * time.Millisecond)
		}
		// No refusal may arrive for two channels under a limit of two.
		conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, data, err := conn.ReadMessage()
		conn.SetReadDeadline(time.Time{})
		if err == nil {
			_, _, payload, _ := parseRelayFrame(data)
			var ctrl map[string]interface{}
			if json.Unmarshal(payload, &ctrl) == nil && ctrl["type"] == "forward_refused" {
				t.Fatalf("two channels under a limit of 2 were refused: %v", ctrl)
			}
		}
	})
}

// TestP2104_RelayedClientHitsTheSameLimiter proves rate/concurrency parity by
// driving both transports through the same limiter with the only slot held:
// neither path is admitted, and the counters show both were refused.
func TestP2104_RelayedClientHitsTheSameLimiter(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	srv.rateLimit = NewRateLimiter(10, 20, 1)
	if !srv.rateLimit.Allow("holder") {
		t.Fatal("could not take the only concurrency slot")
	}
	defer srv.rateLimit.Release()

	// Direct path: an agent's inbound message is refused.
	if srv.admitInbound("direct-agent", false) {
		t.Fatal("direct client admitted while the only slot was taken")
	}

	// Relayed path: a relay opens a channel and registers an agent. The
	// registration frame is refused admission, so no agent appears — proof the
	// relayed client met the same limiter, not an assumed inheritance.
	conn := p2104RelayRegistration(t, ts, 0x44, map[string]interface{}{
		"type": "relay_register", "relay_id": "relay-limited",
	})
	defer conn.Close()
	conn.WriteMessage(websocket.BinaryMessage, p2104Frame(0x44, 0,
		[]byte(`{"type":"channel_open","channel_id":1,"relay_id":"relay-limited"}`)))
	info, _ := json.Marshal(protocol.AgentInfo{Name: "relayed-limited", Version: "v-test", OS: "linux", Arch: "amd64"})
	env, _ := json.Marshal(protocol.Envelope{ID: "reg-1", Type: "agent_info", Result: info})
	conn.WriteMessage(websocket.BinaryMessage, p2104Frame(0x44, 1, env))

	time.Sleep(200 * time.Millisecond)
	if p2104RegistryHas(srv, "relay/relay-limited/relayed-limited") {
		t.Fatal("relayed agent was registered while the limiter refused admission")
	}

	if got := srv.forwardStats.deniedDirect.Load(); got != 1 {
		t.Fatalf("deniedDirect: got %d, want 1", got)
	}
	if got := srv.forwardStats.deniedRelayed.Load(); got < 1 {
		t.Fatalf("deniedRelayed: got %d, want >=1 (the relayed path never reached the limiter)", got)
	}

	// And when the slot frees, both paths are admitted again.
	srv.rateLimit.Release()
	if !srv.admitInbound("direct-agent", false) {
		t.Fatal("direct client refused after the slot was freed")
	}
	if !srv.admitInbound("relayed-agent", true) {
		t.Fatal("relayed client refused after the slot was freed")
	}
}

// TestP2104_AllowlistDeniesWithDistinctError proves the P7 allowlist call is
// made and a miss refuses the registration with its own code, never silently.
func TestP2104_AllowlistDeniesWithDistinctError(t *testing.T) {
	_, ts, cleanup := p2104Server(t)
	defer cleanup()

	srv, _, _ := p2104Server(t)
	_ = srv

	// Install an allowlist on the server the relay dials.
	s2, ts2, cleanup2 := p2104Server(t)
	defer cleanup2()
	s2.SetRelayAllowlist(p2104Allowlist{allowed: map[string]bool{"relay-known": true}})

	wsURL := "ws" + strings.TrimPrefix(ts2.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reg, _ := json.Marshal(map[string]interface{}{
		"type": "relay_register", "relay_id": "relay-unknown", "fingerprint": "fp-x",
	})
	conn.WriteMessage(websocket.BinaryMessage, p2104Frame(0x45, 0, reg))

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no refusal for a relay outside the allowlist: %v", err)
	}
	_, _, payload, _ := parseRelayFrame(data)
	var ctrl map[string]interface{}
	json.Unmarshal(payload, &ctrl)
	if ctrl["code"] != RefusalAllowlist {
		t.Fatalf("allowlist refusal code: got %v, want %s", ctrl["code"], RefusalAllowlist)
	}
	reason, _ := ctrl["reason"].(string)
	if !strings.Contains(reason, "not in the downstream allowlist") {
		t.Fatalf("allowlist refusal: %q does not say the allowlist denied it", reason)
	}
	_ = ts
}

// p2104Allowlist is a static allowlist for the test.
type p2104Allowlist struct{ allowed map[string]bool }

func (a p2104Allowlist) Allows(relayID, _ string) bool { return a.allowed[relayID] }

// TestP2104_DecideForwardTaxonomy pins the four named refusals as distinct
// strings — the defect this unit removes is one generic "forwarding failed".
func TestP2104_DecideForwardTaxonomy(t *testing.T) {
	msgs := map[string]string{
		"policy":    refPolicyDenied("a", "local").Error(),
		"maxagents": refLinkMaxAgents("link", 100).Error(),
		"loop":      refLoop("r1", Chain{Traversed: []string{"r1"}}).Error(),
		"allowlist": refAllowlist("r1").Error(),
	}
	seen := map[string]string{}
	for kind, msg := range msgs {
		for otherKind, other := range seen {
			if msg == other {
				t.Fatalf("%s and %s share one refusal string: %q", kind, otherKind, msg)
			}
		}
		seen[kind] = msg
	}
	if _, err := DecideForward("a", "nonsense", "s", "l", true, Chain{}); err == nil {
		t.Fatal("unknown policy was accepted")
	}
	// Chain root terminates: no uplink means register locally, not refuse.
	dec, err := DecideForward("a", PolicyRelayAll, "s", "l", false, Chain{})
	if err != nil || dec.Forward {
		t.Fatalf("chain root: got %+v err=%v, want local registration with no error", dec, err)
	}
}
