package server

// P23-14 — E2E silent downgrade + relay decrypt-before-parse.
//
// Three defects, three in-tree tests, each asserted to FAIL at the pre-fix
// HEAD:
//
//	a) Agent E2E posture config-deaf: agent.Config.E2EEnabled carried no json
//	   tag, so "e2e_enabled": true in a config file never reached
//	   cryptomanager.NewManager and writeMessage never took the encrypt
//	   branch. Covered here by TestP2314_ManagerPosture (posture semantics)
//	   and the agent-side wiring is exercised live (artefact §1).
//	b) Server admits plaintext silently: with the posture on, a plaintext
//	   TEXT hello used to register with a 200-shape success. Now it is
//	   refused with the typed E2E_KEY_REQUIRED code (docs/SECURITY.md §2).
//	c) Relay path cannot decrypt: relay_ws.go json.Unmarshalled channel
//	   payloads with no decrypt step, so an encrypted agent behind a relay
//	   never registered. Both loops now share one decrypt-before-parse
//	   helper (s.decryptAgentPayload).

import (
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/crypto"
	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)

// p2314Dial dials the test server with the shared token.
func p2314Dial(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// TestP2314_ManagerPosture pins the crypto.Manager semantics the fix rests
// on: enabled+token = active cipher; enabled+no-token = LOUD broken state
// (Enabled() true, IsActive() false, Encrypt/Decrypt fail closed); disabled =
// the historic pass-through. Before the fix, enabled+no-token silently
// behaved as disabled — the exact confusion the unit closes.
func TestP2314_ManagerPosture(t *testing.T) {
	m := crypto.NewManager("test-token", true)
	if !m.IsActive() || !m.Enabled() || m.CipherError() != nil {
		t.Fatalf("enabled manager with token must be active: active=%v enabled=%v err=%v", m.IsActive(), m.Enabled(), m.CipherError())
	}
	broken := crypto.NewManager("", true)
	if !broken.Enabled() {
		t.Fatal("enabled+no-token must report Enabled()=true — it asked for encryption")
	}
	if broken.IsActive() {
		t.Fatal("enabled+no-token must NOT be active (no cipher)")
	}
	if broken.CipherError() == nil {
		t.Fatal("enabled+no-token must carry a CipherError — loud, not silent")
	}
	if _, err := broken.Encrypt([]byte("x")); err == nil {
		t.Fatal("broken manager Encrypt must fail closed, never pass plaintext")
	}
	if _, err := broken.Decrypt([]byte("x")); err == nil {
		t.Fatal("broken manager Decrypt must fail closed")
	}
	off := crypto.NewManager("test-token", false)
	if off.IsActive() || off.Enabled() {
		t.Fatal("disabled manager must be inactive and report Enabled()=false")
	}
	if _, err := off.Encrypt([]byte("x")); err != nil {
		t.Fatalf("disabled manager Encrypt must pass through: %v", err)
	}
}

// TestP2314_PlaintextHelloRefused — (b). On an e2e-active server a plaintext
// TEXT hello is refused with E2E_KEY_REQUIRED; the agent is NOT registered.
func TestP2314_PlaintextHelloRefused(t *testing.T) {
	srv, wsURL := p2312StartWSServer(t) // same rig: e2e ON, token "test-token"
	_ = srv

	conn := p2314Dial(t, wsURL)
	env := protocol.Envelope{ID: "agent-info", Type: "agent_info"}
	raw, _ := json.Marshal(protocol.AgentInfo{Name: "p2314-plain", Version: "1", OS: "linux", Arch: "amd64", Mode: "outbound"})
	env.Result = raw
	if err := conn.WriteJSON(env); err != nil {
		t.Fatalf("plaintext hello write: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected a refusal frame, got read error: %v", err)
	}
	var refusal map[string]string
	if err := json.Unmarshal(data, &refusal); err != nil {
		t.Fatalf("refusal frame not JSON: %v (%q)", err, data)
	}
	if refusal["code"] != "E2E_KEY_REQUIRED" {
		t.Fatalf("refusal code: got %q, want E2E_KEY_REQUIRED (%q)", refusal["code"], data)
	}
	if _, err := srv.registry.GetHealth("p2314-plain"); err == nil {
		t.Fatal("plaintext agent was registered on an e2e-active server — silent downgrade")
	}
}

// TestP2314_RelayedEncryptedAgentRegisters — (c). An e2e-sealed agent behind
// a relay registers upstream: its channel payloads are decrypted before the
// envelope parse. The test speaks the relay wire format by hand (same shape
// as P2103/P2312): a relay_register frame, a channel_open control, then the
// agent's agent-info envelope AES-GCM-sealed with the shared token as the
// first channel frame.
func TestP2314_RelayedEncryptedAgentRegisters(t *testing.T) {
	srv, wsURL := p2312StartWSServer(t)

	conn := p2314Dial(t, wsURL)

	sendFrame := func(chanID uint32, payload []byte) {
		t.Helper()
		frame := make([]byte, 5+len(payload))
		frame[0] = 0x42
		binary.BigEndian.PutUint32(frame[1:5], chanID)
		copy(frame[5:], payload)
		if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			t.Fatalf("frame write: %v", err)
		}
	}

	reg := []byte(`{"type":"relay_register","relay_id":"p2314-relay","token":"test-token"}`)
	sendFrame(0, reg)

	// Wait for the relay to register before opening the channel.
	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.relayMu.RLock()
		_, up := srv.relays["p2314-relay"]
		srv.relayMu.RUnlock()
		if up {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("relay never registered")
		}
		time.Sleep(20 * time.Millisecond)
	}

	open, _ := json.Marshal(map[string]interface{}{"type": "channel_open", "channel_id": 7, "relay_id": "p2314-relay"})
	sendFrame(0, open)
	time.Sleep(100 * time.Millisecond)

	mgr := crypto.NewManager("test-token", true)
	info, _ := json.Marshal(protocol.AgentInfo{Name: "p2314-rel-agent", Version: "1", OS: "linux", Arch: "amd64", Mode: "outbound"})
	hello, _ := json.Marshal(protocol.Envelope{ID: "agent-info", Type: "agent_info", Result: info})
	sealed, err := mgr.Encrypt(hello)
	if err != nil {
		t.Fatal(err)
	}
	sendFrame(7, sealed)

	deadline = time.Now().Add(3 * time.Second)
	for {
		if registered(srv, "relay/p2314-relay/p2314-rel-agent") {
			return // registered through the hop — defect (c) closed
		}
		if time.Now().After(deadline) {
			t.Fatal("encrypted relayed agent never registered — decrypt-before-parse missing")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestP2314_HelperFailsClosedOnGarbage — the shared helper must never pass a
// failed decrypt through as plaintext (the "broken key = cleartext" bug).
func TestP2314_HelperFailsClosedOnGarbage(t *testing.T) {
	srv := NewServer("", "test-token", t.TempDir()+"/registry.json")
	srv.SetE2E("test-token", true)
	_, active, err := srv.decryptAgentPayload([]byte("this is not ciphertext"))
	if err == nil {
		t.Fatal("decrypt of garbage must error under an active posture")
	}
	if !active {
		t.Fatal("posture must report active")
	}
	off := NewServer("", "test-token", t.TempDir()+"/registry.json")
	off.SetE2E("test-token", false)
	plain, offActive, err := off.decryptAgentPayload([]byte(`{"id":"x","type":"ping"}`))
	if err != nil || offActive {
		t.Fatalf("inactive posture must pass through: active=%v err=%v", offActive, err)
	}
	if string(plain) != `{"id":"x","type":"ping"}` {
		t.Fatalf("passthrough changed the payload: %q", plain)
	}
}

// TestP2314_PlaintextFrameAfterRegistrationRefused — (b) mid-session: a
// registered agent cannot slide back to cleartext. Registered via a sealed
// hello (the only path that registers now), then a plaintext TEXT frame must
// draw the refusal, not a parse.
func TestP2314_PlaintextFrameAfterRegistrationRefused(t *testing.T) {
	srv, wsURL := p2312StartWSServer(t)

	conn := p2314Dial(t, wsURL)

	mgr := crypto.NewManager("test-token", true)
	info, _ := json.Marshal(protocol.AgentInfo{Name: "p2314-direct", Version: "1", OS: "linux", Arch: "amd64", Mode: "outbound"})
	hello, _ := json.Marshal(protocol.Envelope{ID: "agent-info", Type: "agent_info", Result: info})
	sealed, err := mgr.Encrypt(hello)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, sealed); err != nil {
		t.Fatalf("sealed hello write: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if registered(srv, "p2314-direct") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sealed hello never registered")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"id":"ping-1","type":"ping"}`)); err != nil {
		t.Fatalf("plaintext frame write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected refusal frame, got: %v", err)
	}
	var refusal map[string]string
	if err := json.Unmarshal(data, &refusal); err != nil {
		t.Fatalf("refusal not JSON: %v (%q)", err, data)
	}
	if !strings.Contains(refusal["code"], "E2E_KEY_REQUIRED") {
		t.Fatalf("refusal code: got %q, want E2E_KEY_REQUIRED", refusal["code"])
	}
}

// registered reports whether the agent row exists in the registry.
func registered(srv *Server, agentID string) bool {
	_, err := srv.registry.GetHealth(agentID)
	return err == nil
}
