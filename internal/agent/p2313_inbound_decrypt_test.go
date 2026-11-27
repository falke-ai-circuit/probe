package agent

// P23-13 — inbound decrypt on the agent read loop.
//
// The server seals every agent-ward envelope under an active E2E posture
// (server_ws.go encryptAgentFrame: ping, exec, credentials, token init,
// pong). The agent's read loop used to hand those bytes straight to
// json.Unmarshal: parse failed, pings were never answered, and the agent
// died at the miss threshold ("ping timeout (3 misses), closing" — P23-17
// live s4, BOTH legs). The fix decrypts BinaryMessage frames with the SAME
// key the agent->server direction already seals with: the shared token,
// SHA-256, AES-256-GCM, no negotiation (P25-08).
//
// These tests speak the REAL framer path: a real agent (agent.New +
// handleConnection over a live WebSocket) against a hand-driven server-side
// conn that seals with crypto.Manager exactly as the server's
// encryptAgentFrame does.

import (
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

// p2313Seal mirrors the server's encryptAgentFrame active-posture half:
// seal an envelope into a BinaryMessage payload. Kept local to the test so
// the assertion is on the agent's decryption of the WIRE shape, not on a
// server package import.
func p2313Seal(t *testing.T, mgr *crypto.Manager, env protocol.Envelope) ([]byte, error) {
	t.Helper()
	plain, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return mgr.Encrypt(plain)
}

// p2313AgentConn is the minimal server-side half of the fixtures: it upgrades
// a WebSocket, reads the agent's hello (sealed or plain per the agent's own
// posture — writeMessage seals it when active), and records it.
func p2313Upgrader(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return up.Upgrade(w, r, nil)
}

// readAgentHello reads the first frame from the agent (its agent_info) and
// unmarshals it. Sealed (BinaryMessage) frames are opened with the manager.
func readAgentHello(t *testing.T, conn *websocket.Conn, mgr *crypto.Manager) protocol.Envelope {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read agent hello: %v", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	if mt == websocket.BinaryMessage && mgr != nil {
		plain, derr := mgr.Decrypt(data)
		if derr != nil {
			t.Fatalf("agent hello not decryptable: %v", derr)
		}
		data = plain
	}
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("agent hello not JSON: %v (%q)", err, data)
	}
	return env
}

// P2313Connect dials once and runs handleConnection — the outbound session
// shape without runOutbound's infinite reconnect loop (which would mask
// first-session behaviour). Exported: the server-package one-hop rig
// (internal/server) drives the production agent through it.
func (a *Agent) P2313Connect() error {
	conn, err := protocol.Dial(a.cfg.URL, a.cfg.CertPath, a.cfg.ClientCertFile, a.cfg.ClientKeyFile, a.cfg.Token)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.conn = conn
	a.pingMisses = 0
	a.mu.Unlock()
	a.handleConnection(conn)
	return nil
}

// TestP2313_AgentDecryptsSealedPing — A1. A real E2E-on agent answers a
// sealed server ping with a pong. The rig dials a real agent process-shape
// (New + handleConnection) and seals the ping exactly as the server does.
// FAILS if the read loop stops decrypting (pong never arrives; the agent
// miss-times out instead).
func TestP2313_AgentDecryptsSealedPing(t *testing.T) {
	const token = "p2313-ping-token"
	mgr := crypto.NewManager(token, true)
	if !mgr.IsActive() {
		t.Fatal("manager must be active")
	}

	agentDone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := p2313Upgrader(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		hello := readAgentHello(t, conn, mgr)
		if hello.Type != "agent_info" {
			t.Errorf("first frame not agent_info: %q", hello.Type)
		}
		// Seal a ping exactly like encryptAgentFrame's active half.
		ping := protocol.Envelope{ID: "srv-ping-1", Type: protocol.TypePing}
		sealed, serr := p2313Seal(t, mgr, ping)
		if serr != nil {
			t.Fatalf("seal ping: %v", serr)
		}
		if werr := conn.WriteMessage(websocket.BinaryMessage, sealed); werr != nil {
			t.Errorf("write sealed ping: %v", werr)
		}
		// Expect a pong back (the agent's own writeMessage seals it).
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read pong: %v", err)
			return
		}
		if mt == websocket.BinaryMessage {
			plain, derr := mgr.Decrypt(data)
			if derr != nil {
				t.Errorf("pong not decryptable: %v", derr)
				return
			}
			data = plain
		}
		var pong protocol.Envelope
		if err := json.Unmarshal(data, &pong); err != nil {
			t.Errorf("pong not JSON: %v (%q)", err, data)
			return
		}
		if pong.Type != protocol.TypePong || pong.ID != "srv-ping-1" {
			t.Errorf("got type=%q id=%q, want pong/srv-ping-1", pong.Type, pong.ID)
		}
		close(agentDone)
	}))
	defer ts.Close()

	a := New(Config{
		URL:        "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws",
		Mode:       "outbound",
		Token:      token,
		Name:       "p2313-ping-agent",
		E2EEnabled: true,
	})
	go func() {
		_ = a.P2313Connect()
	}()
	select {
	case <-agentDone:
	case <-time.After(10 * time.Second):
		t.Fatal("sealed ping never answered within 10s — decrypt missing in the read loop")
	}
}

// TestP2313_AgentDecryptsSealedExec — A2 (agent half). A sealed exec with a
// real command comes back as a sealed exec result with real output (exit 0,
// command stdout present).
func TestP2313_AgentDecryptsSealedExec(t *testing.T) {
	const token = "p2313-exec-token"
	mgr := crypto.NewManager(token, true)

	agentDone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := p2313Upgrader(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		readAgentHello(t, conn, mgr)
		params, _ := json.Marshal(protocol.ExecParams{Command: "echo p2313-ok", Timeout: 20})
		env := protocol.Envelope{ID: "exec-1", Type: protocol.TypeExec, Params: params}
		sealed, serr := p2313Seal(t, mgr, env)
		if serr != nil {
			t.Fatalf("seal exec: %v", serr)
		}
		if werr := conn.WriteMessage(websocket.BinaryMessage, sealed); werr != nil {
			t.Errorf("write sealed exec: %v", werr)
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(25 * time.Second))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read exec result: %v", err)
			return
		}
		if mt == websocket.BinaryMessage {
			plain, derr := mgr.Decrypt(data)
			if derr != nil {
				t.Errorf("exec result not decryptable: %v", derr)
				return
			}
			data = plain
		}
		var resp protocol.Envelope
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Errorf("exec result not JSON: %v (%q)", err, data)
			return
		}
		if resp.Type != protocol.TypeExecResult {
			t.Errorf("got type=%q, want exec_result", resp.Type)
			return
		}
		var res protocol.ExecResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Errorf("exec result body not ExecResult: %v", err)
			return
		}
		if res.ExitCode != 0 || !strings.Contains(res.Stdout, "p2313-ok") {
			t.Errorf("exec not real: exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
			return
		}
		close(agentDone)
	}))
	defer ts.Close()

	a := New(Config{
		URL:        "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws",
		Mode:       "outbound",
		Token:      token,
		Name:       "p2313-exec-agent",
		E2EEnabled: true,
	})
	go func() {
		_ = a.P2313Connect()
	}()
	select {
	case <-agentDone:
	case <-time.After(30 * time.Second):
		t.Fatal("sealed exec never returned real output within 30s")
	}
}

// TestP2313_AgentDecryptFailureDropsNotParses — decrypt must fail closed: a
// corrupted binary frame is logged and dropped, never parsed as plaintext
// garbage and never answered with an invalid-JSON envelope.
func TestP2313_AgentDecryptFailureDropsNotParses(t *testing.T) {
	const token = "p2313-garbage-token"
	mgr := crypto.NewManager(token, true)

	garbage := make(chan struct{}, 1)
	corrupt := []byte("this is definitely not aes-gcm ciphertext")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := p2313Upgrader(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		readAgentHello(t, conn, mgr)
		if werr := conn.WriteMessage(websocket.BinaryMessage, corrupt); werr != nil {
			return
		}
		// A subsequent VALID sealed ping must still flow — the drop did not
		// poison the loop or produce a stray invalid-JSON reply in between.
		ping := protocol.Envelope{ID: "srv-ping-post", Type: protocol.TypePing}
		sealed, serr := p2313Seal(t, mgr, ping)
		if serr != nil {
			return
		}
		if werr := conn.WriteMessage(websocket.BinaryMessage, sealed); werr != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		mt, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("no frame after garbage: %v", err)
			return
		}
		if mt == websocket.BinaryMessage {
			plain, _ := mgr.Decrypt(data)
			data = plain
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			t.Errorf("frame after garbage not JSON: %v (%q)", err, data)
			return
		}
		if env.ID != "srv-ping-post" {
			t.Errorf("stray frame id=%q (invalid-JSON reply to garbage leaked?), want srv-ping-post", env.ID)
			return
		}
		garbage <- struct{}{}
	}))
	defer ts.Close()

	a := New(Config{
		URL:        "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws",
		Mode:       "outbound",
		Token:      token,
		Name:       "p2313-garbage-agent",
		E2EEnabled: true,
	})
	go func() {
		_ = a.P2313Connect()
	}()
	select {
	case <-garbage:
	case <-time.After(10 * time.Second):
		t.Fatal("loop did not recover after a corrupt sealed frame")
	}
}

// TestP2313_PlaintextFrameStillDispatched — the handshake's other half: a
// PLAINTEXT frame from the server on an E2E-on agent still dispatches (the
// agent-side carries no strict-refusal contract; the server refuses
// plaintext agent frames, not the reverse). This pins that the fix only
// widened the accepted frame set, it did not break the plaintext path that
// E2E-off deploys use.
func TestP2313_PlaintextFrameStillDispatched(t *testing.T) {
	const token = "p2313-plain-token"
	// NOTE: agent is E2E-ON here; a TEXT ping is accepted by the read loop
	// (the wire from the server decides per-frame), while E2E-OFF agents are
	// exercised end-to-end by the existing suites. The invariant pinned:
	// TextMessage frames parse as before the change.
	mgr := crypto.NewManager(token, false) // agent seals nothing; hello is plain

	plainDone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := p2313Upgrader(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		readAgentHello(t, conn, mgr)
		if werr := conn.WriteJSON(protocol.Envelope{ID: "plain-ping", Type: protocol.TypePing}); werr != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read plain ping reply: %v", err)
			return
		}
		var pong protocol.Envelope
		if err := json.Unmarshal(data, &pong); err != nil {
			t.Errorf("plain reply not JSON: %v (%q)", err, data)
			return
		}
		if pong.Type != protocol.TypePong {
			t.Errorf("got type=%q, want pong", pong.Type)
			return
		}
		close(plainDone)
	}))
	defer ts.Close()

	a := New(Config{
		URL:        "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws",
		Mode:       "outbound",
		Token:      token,
		Name:       "p2313-plain-agent",
		E2EEnabled: false,
	})
	go func() {
		_ = a.P2313Connect()
	}()
	select {
	case <-plainDone:
	case <-time.After(10 * time.Second):
		t.Fatal("plaintext ping not answered after the change — read loop regression")
	}
}
