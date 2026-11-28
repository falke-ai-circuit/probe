package server

// P27-02R — spec P27-01 §2.4 (relay first-frame decrypt, per-channel sealed
// classification, fail-closed refusal) and §5.3 rule 4 (/api/v1/chain
// registration_state folded into deriveChainState).
//
// Test map:
//
//	TestP2702R_SealedRelayedAgentStampedSealed     A-1 + §2.2 (vc.sealed -> e2e.sealed)
//	TestP2702R_SealedRelayedAgentForwardsWireBytes A-1 + §2.4 rule 3 (mirror is a byte pipe)
//	TestP2702R_RelayedPlaintextRefused             A-2 (cleartext downgrade -> E2E_KEY_REQUIRED)
//	TestP2702R_RelayedUndecryptableRefused         A-2 (unopenable ciphertext -> E2E_DECRYPT_FAILED)
//	TestP2702R_ChainRegistrationState              A-3 (three values + failing-hop fold + handler JSON)

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/crypto"
	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)

// p2702rDialRelay connects a hand-rolled relay socket (the wire the real relay
// speaks: 5-byte header, channel 0 for control).
func p2702rDialRelay(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// p2702rSend writes one relay frame on the socket.
func p2702rSend(t *testing.T, conn *websocket.Conn, chanID uint32, payload []byte) {
	t.Helper()
	frame := make([]byte, 5+len(payload))
	frame[0] = 0x42
	binary.BigEndian.PutUint32(frame[1:5], chanID)
	copy(frame[5:], payload)
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("frame write on channel %d: %v", chanID, err)
	}
}

// p2702rRegisterRelay registers the socket as a relay and waits until the
// server holds the session — the channel_open is only legal afterwards.
func p2702rRegisterRelay(t *testing.T, srv *Server, conn *websocket.Conn, relayID string) {
	t.Helper()
	reg, _ := json.Marshal(map[string]string{"type": "relay_register", "relay_id": relayID, "token": "test-token"})
	p2702rSend(t, conn, 0, reg)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		srv.relayMu.RLock()
		_, up := srv.relays[relayID]
		srv.relayMu.RUnlock()
		if up {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("relay %s never registered", relayID)
}

// p2702rOpenChannel opens a channel and waits for the server-side slot.
func p2702rOpenChannel(t *testing.T, srv *Server, conn *websocket.Conn, relayID string, chanID uint32) {
	t.Helper()
	open, _ := json.Marshal(map[string]interface{}{"type": "channel_open", "channel_id": chanID, "relay_id": relayID})
	p2702rSend(t, conn, 0, open)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		srv.relayMu.RLock()
		rs := srv.relays[relayID]
		srv.relayMu.RUnlock()
		if rs != nil {
			rs.channelsMu.RLock()
			_, ok := rs.channels[chanID]
			rs.channelsMu.RUnlock()
			if ok {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("channel %d never opened on relay %s", chanID, relayID)
}

// p2702rSealedHello builds an agent-info envelope sealed under the shared
// token — the frame an E2E-active agent actually sends.
func p2702rSealedHello(t *testing.T, name string) []byte {
	t.Helper()
	info, _ := json.Marshal(protocol.AgentInfo{Name: name, Version: "1", OS: "linux", Arch: "amd64", Mode: "outbound"})
	hello, _ := json.Marshal(protocol.Envelope{ID: "agent-info", Type: "agent_info", Result: info})
	sealed, err := crypto.NewManager("test-token", true).Encrypt(hello)
	if err != nil {
		t.Fatalf("seal hello: %v", err)
	}
	return sealed
}

// p2702rAwaitControl reads control frames until one of wantType arrives, and
// returns its decoded body. Anything else on channel 0 is skipped (peer_hello
// is written at registration success).
func p2702rAwaitControl(t *testing.T, conn *websocket.Conn, wantType string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	_ = conn.SetReadDeadline(deadline)
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("no %s control frame before the deadline: %v", wantType, err)
		}
		_, chanID, payload, perr := parseRelayFrame(data)
		if perr != nil || chanID != 0 {
			continue
		}
		var body map[string]interface{}
		if json.Unmarshal(payload, &body) != nil {
			continue
		}
		if body["type"] == wantType {
			_ = conn.SetReadDeadline(time.Time{})
			return body
		}
	}
}

// p2702rSealedFlag reads a channel's classification under channelsMu.
func p2702rSealedFlag(srv *Server, relayID string, chanID uint32) (sealed, found bool) {
	srv.relayMu.RLock()
	rs := srv.relays[relayID]
	srv.relayMu.RUnlock()
	if rs == nil {
		return false, false
	}
	rs.channelsMu.RLock()
	defer rs.channelsMu.RUnlock()
	vc, ok := rs.channels[chanID]
	if !ok {
		return false, false
	}
	return vc.sealed, true
}

// TestP2702R_SealedRelayedAgentStampedSealed — A-1. A sealed agent behind a
// relay registers, and the relayed row carries the wire-proved e2e.sealed
// stamp plus the per-channel classification (§2.4 rule 2). Before this leg the
// channel decrypt existed (P23-14 c) but nothing recorded WHICH channels were
// sealed, so the relayed row's e2e stayed null.
func TestP2702R_SealedRelayedAgentStampedSealed(t *testing.T) {
	srv, wsURL := p2312StartWSServer(t)
	conn := p2702rDialRelay(t, wsURL)
	p2702rRegisterRelay(t, srv, conn, "p2702r-relay")
	p2702rOpenChannel(t, srv, conn, "p2702r-relay", 7)

	p2702rSend(t, conn, 7, p2702rSealedHello(t, "p2702r-sealed"))

	agentID := "relay/p2702r-relay/p2702r-sealed"
	deadline := time.Now().Add(3 * time.Second)
	for !registered(srv, agentID) {
		if time.Now().After(deadline) {
			t.Fatal("sealed relayed agent never registered — first-frame decrypt missing")
		}
		time.Sleep(20 * time.Millisecond)
	}

	sealed, found := p2702rSealedFlag(srv, "p2702r-relay", 7)
	if !found {
		t.Fatal("channel 7 vanished after registration")
	}
	if !sealed {
		t.Error("vc.sealed is false for a channel whose first frame authenticated under the e2e key")
	}

	// §2.2 stamp ordering: Register publishes the row, MarkE2E stamps it
	// immediately after — the same shape the direct path already uses
	// (server_ws.go:270/286). A row seen inside that window carries e2e:null,
	// which is the honest "not measured yet", so poll for the classification
	// instead of pretending the insert and the stamp are one atomic step.
	var stamp *agentE2E
	stampDeadline := time.Now().Add(2 * time.Second)
	for {
		stamp = nil
		for _, rec := range srv.registry.ListAgents() {
			if rec.AgentID == agentID {
				stamp = rec.E2E
			}
		}
		if stamp != nil || time.Now().After(stampDeadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stamp == nil {
		t.Fatal("relayed row carries e2e:null — the wire classification was never persisted (§2.2)")
	}
	if !stamp.Sealed {
		t.Errorf("relayed row stamped sealed=false for a sealed hello: %+v", stamp)
	}
	if stamp.ObservedAt == "" {
		t.Error("e2e.observed_at empty — a claim with no timestamp cannot be aged out")
	}
}

// TestP2702R_SealedRelayedAgentForwardsWireBytes — A-1 + §2.4 rule 3. A sealed
// relayed agent re-forwarded under relay_all must reach the upstream as the
// SAME bytes the agent sent: the upstream (E2E-active) can only register it by
// decrypting ciphertext. If the mirror carried the decrypted copy, the
// upstream would refuse it E2E_KEY_REQUIRED and never register.
func TestP2702R_SealedRelayedAgentForwardsWireBytes(t *testing.T) {
	up, upWS := p2312StartWSServer(t)
	down, downWS := p2312StartWSServer(t)

	down.SetUplinkForwardTarget(upWS, "test-token")
	down.SetForwarderID("p2702r-fwd")
	down.SetForwardPolicy("p2702r-upstream-sealed", PolicyRelayAll)

	conn := p2702rDialRelay(t, downWS)
	p2702rRegisterRelay(t, down, conn, "p2702r-hop")
	p2702rOpenChannel(t, down, conn, "p2702r-hop", 9)
	p2702rSend(t, conn, 9, p2702rSealedHello(t, "p2702r-upstream-sealed"))

	upstreamID := "relay/p2702r-fwd/p2702r-upstream-sealed"
	deadline := time.Now().Add(5 * time.Second)
	for !registered(up, upstreamID) {
		if time.Now().After(deadline) {
			t.Fatal("sealed relayed agent never registered upstream — the mirror did not carry the sealed bytes (§2.4 rule 3)")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if sealed, found := p2702rSealedFlag(down, "p2702r-hop", 9); !found || !sealed {
		t.Errorf("downstream channel not classified sealed: found=%v sealed=%v", found, sealed)
	}
}

// TestP2702R_RelayedPlaintextRefused — A-2. A plaintext agent hello on a
// relayed channel against an E2E-active server is the downgrade the posture
// forbids on every entry path: a typed refusal, no registration, and no
// silent drop. The prior code logged a decrypt error and continued, which left
// the relay waiting forever with nothing on the wire.
func TestP2702R_RelayedPlaintextRefused(t *testing.T) {
	srv, wsURL := p2312StartWSServer(t)
	conn := p2702rDialRelay(t, wsURL)
	p2702rRegisterRelay(t, srv, conn, "p2702r-plain-relay")
	p2702rOpenChannel(t, srv, conn, "p2702r-plain-relay", 11)

	info, _ := json.Marshal(protocol.AgentInfo{Name: "p2702r-plain", Version: "1", OS: "linux", Arch: "amd64"})
	hello, _ := json.Marshal(protocol.Envelope{ID: "reg-1", Type: "agent_info", Result: info})
	p2702rSend(t, conn, 11, hello)

	body := p2702rAwaitControl(t, conn, "forward_refused")
	if got, _ := body["code"].(string); got != refusalE2EKeyRequired {
		t.Fatalf("refusal code: got %q, want %q (%v)", got, refusalE2EKeyRequired, body)
	}
	if ch, _ := body["channel_id"].(float64); uint32(ch) != 11 {
		t.Errorf("refusal names channel %v, want 11", body["channel_id"])
	}
	if registered(srv, "relay/p2702r-plain-relay/p2702r-plain") {
		t.Fatal("plaintext relayed agent registered on an e2e-active server — silent downgrade")
	}
}

// TestP2702R_RelayedUndecryptableRefused — A-2 negative. Bytes that neither
// authenticate under the posture key nor parse as a plaintext envelope are
// refused with their own typed code: a broken key must never read as
// cleartext, and the frame must never be dropped in silence.
func TestP2702R_RelayedUndecryptableRefused(t *testing.T) {
	srv, wsURL := p2312StartWSServer(t)
	conn := p2702rDialRelay(t, wsURL)
	p2702rRegisterRelay(t, srv, conn, "p2702r-garbage-relay")
	p2702rOpenChannel(t, srv, conn, "p2702r-garbage-relay", 12)

	garbage := make([]byte, 64)
	for i := range garbage {
		garbage[i] = 0xAB
	}
	p2702rSend(t, conn, 12, garbage)

	body := p2702rAwaitControl(t, conn, "forward_refused")
	if got, _ := body["code"].(string); got != refusalE2EDecryptFailed {
		t.Fatalf("refusal code: got %q, want %q (%v)", got, refusalE2EDecryptFailed, body)
	}
	reason, _ := body["reason"].(string)
	if !strings.Contains(reason, "never parsed as plaintext") {
		t.Errorf("refusal reason does not state the fail-closed rule: %q", reason)
	}
	for _, rec := range srv.registry.ListAgents() {
		if strings.Contains(rec.AgentID, "p2702r-garbage") {
			t.Fatalf("undecryptable frame produced a registry row: %q", rec.AgentID)
		}
	}
}

// TestP2702R_ChainRegistrationState — A-3. The three registration_state values
// are distinguished from the carrying counters, refused is folded into
// deriveChainState as a failing hop (never-green), and the value reaches the
// /api/v1/chain server hop as JSON.
func TestP2702R_ChainRegistrationState(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.SetUplinkForwardTarget("ws://127.0.0.1:1/ws", "test-token")

	f := srv.uplinkForward()
	if f == nil {
		t.Fatal("forwarder was not wired — the test cannot derive carrying truth")
	}
	if state, _ := srv.uplinkRegistrationState(); state != "" {
		t.Fatalf("fresh forwarder must report no state (nothing asked), got %q", state)
	}

	f.mu.Lock()
	f.links[1] = &uplinkChannel{agentID: "p2702r-pending"}
	f.mu.Unlock()
	if state, _ := srv.uplinkRegistrationState(); state != registrationUnanswered {
		t.Fatalf("open unconfirmed channel: got %q, want %q", state, registrationUnanswered)
	}

	f.mu.Lock()
	f.links[1].confirmed = true
	f.mu.Unlock()
	if state, _ := srv.uplinkRegistrationState(); state != registrationRegistered {
		t.Fatalf("confirmed channel: got %q, want %q", state, registrationRegistered)
	}

	// A refusal with nothing confirmed is the state the never-green rule needs.
	f.mu.Lock()
	f.links[1].confirmed = false
	f.mu.Unlock()
	f.noteRefusal(RefusalPolicyDenied, "policy denies forwarding for agent p2702r-pending")
	state, detail := srv.uplinkRegistrationState()
	if state != registrationRefused {
		t.Fatalf("refusal on record: got %q, want %q", state, registrationRefused)
	}
	if !strings.Contains(detail, RefusalPolicyDenied) {
		t.Errorf("refused detail does not name the refusal code: %q", detail)
	}

	// A confirmed registration after the refusal wins: a recovered lane must
	// stop reading refused.
	f.mu.Lock()
	delete(f.links, 1)
	f.links[2] = &uplinkChannel{agentID: "p2702r-recovered", confirmed: true}
	f.mu.Unlock()
	if state, _ := srv.uplinkRegistrationState(); state != registrationRegistered {
		t.Fatalf("recovered lane: got %q, want %q", state, registrationRegistered)
	}

	// §5.3 rule 4 fold: refused is treated exactly like a down hop, and named.
	hops := []chainHop{
		{ID: "server", Role: "server", State: chainStateUp, Observed: true,
			RegistrationState: registrationRefused, Error: detail},
		{ID: "p2702r-relay", Role: "relay", State: chainStateUp, Observed: true},
		{ID: "p2702r-agent", Role: "agent", State: chainStateUp, Observed: true},
	}
	cv := newChainView("p2702r-agent", []string{"server", "p2702r-relay", "p2702r-agent"}, hops)
	if cv.State != chainStateDegraded {
		t.Errorf("chain state with a refused server hop: got %q, want %q", cv.State, chainStateDegraded)
	}
	if cv.Failing == nil || cv.Failing.ID != "server" {
		t.Fatalf("failing_hop must name the refused server hop, got %+v", cv.Failing)
	}
	if cv.Hops[0].State != chainStateDown {
		t.Errorf("refused server hop state: got %q, want %q", cv.Hops[0].State, chainStateDown)
	}
	if cv.Hops[1].State != chainStateDegraded || cv.Hops[1].Observed {
		t.Errorf("hop behind the refused server must be unobservable, got %+v", cv.Hops[1])
	}

	// On the wire: a live relay session + a refused lane => the server hop in
	// GET /api/v1/chain carries registration_state:"refused".
	// Put the lane back into the refused state the handler must report: an
	// open channel with nothing confirmed, the earlier refusal still on record.
	f.mu.Lock()
	delete(f.links, 2)
	f.links[3] = &uplinkChannel{agentID: "p2702r-refused"}
	f.mu.Unlock()
	if state, _ := srv.uplinkRegistrationState(); state != registrationRefused {
		t.Fatalf("precondition for the wire assertion: got %q, want %q", state, registrationRefused)
	}
	// (registerRoutes already mounts /ws — server.go:566 — so the relay socket
	// speaks to the same mux the API call hits.)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn := p2702rDialRelay(t, wsURL)
	p2702rRegisterRelay(t, srv, conn, "p2702r-chain-relay")

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/chain", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/chain: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			Chains []struct {
				State string `json:"state"`
				Hops  []struct {
					ID                string `json:"id"`
					Role              string `json:"role"`
					RegistrationState string `json:"registration_state"`
				} `json:"hops"`
			} `json:"chains"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("chain JSON: %v (%s)", err, rec.Body.String())
	}
	if len(resp.Data.Chains) == 0 {
		t.Fatalf("no chains served for the live relay: %s", rec.Body.String())
	}
	var seen bool
	for _, ch := range resp.Data.Chains {
		for _, hop := range ch.Hops {
			if hop.Role != "server" {
				continue
			}
			seen = true
			if hop.RegistrationState != registrationRefused {
				t.Errorf("server hop registration_state: got %q, want %q", hop.RegistrationState, registrationRefused)
			}
		}
	}
	if !seen {
		t.Fatalf("no server-role hop in the chain response: %s", rec.Body.String())
	}
}
