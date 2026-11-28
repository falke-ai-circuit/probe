package server

// P23-13 A5 — the one-hop silent drop, reproduced in-test and shown gone.
//
// The title defect: with E2E ON, a LIVE agent behind a one-hop relay
// registered (via=relay-…) and then every server-initiated envelope was
// dead (pings unanswered, exec DEADLINE_EXCEEDED) — and the founding
// symptom class was the relay leg dropping the agent when sealed frames
// crossed the hop. This rig runs the production relay (relay.New/Run, the
// P2108 shape) between a real server (httptest) and a REAL agent process
// (agent.New, E2E ON), then drives the exact s4 controls that failed live:
// a server ping answered with a pong, and one exec returning real output.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/agent"
	"github.com/falke-ai-circuit/probe/internal/crypto"
	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/falke-ai-circuit/probe/internal/relay"
	"github.com/gorilla/websocket"
)

// p2313s4SendSealed seals env with the shared-token manager and writes it
// as a BinaryMessage — the byte shape the real server's encryptAgentFrame
// produces under an active posture (asserted against the wire, not a mock:
// the transport under test is the production agent + production relay).
func p2313s4SendSealed(t *testing.T, mgr *crypto.Manager, conn *websocket.Conn, env protocol.Envelope) {
	t.Helper()
	plain, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sealed, err := mgr.Encrypt(plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, sealed); err != nil {
		t.Fatalf("write sealed envelope: %v", err)
	}
}

// TestP2313_OneHopRelayedAgentAlive — A5 + relayed half of A2. Production
// relay, production agent (E2E ON), real server. The agent's sealed hello
// must survive the hop (register), a sealed server ping must be answered
// within one probe interval, and a sealed exec must return real output.
// Before the inbound-decrypt fix this rig goes silent: registration
// succeeds, then the pong/exec never flow — the live P23-17 s4 signature.
func TestP2313_OneHopRelayedAgentAlive(t *testing.T) {
	srv := NewServer("", "test-token", t.TempDir()+"/registry.json")
	srv.mux = http.NewServeMux()
	srv.mux.HandleFunc("/ws", srv.handleWebSocket)
	srv.SetE2E("test-token", true)
	ts := httptest.NewServer(srv.mux)
	t.Cleanup(func() {
		ts.Close()
		srv.registry.Stop()
		time.Sleep(250 * time.Millisecond)
	})
	serverWS := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	// Production relay between the server and the agent (P2108 rig shape).
	addr := p2108FreeAddr(t)
	r := relay.New(relay.Config{
		UpstreamURL:        serverWS,
		ListenAddr:         addr,
		Token:              "test-token",
		RelayID:            "p2313-relay",
		AllowAnyDownstream: true,
	})
	go func() { _ = r.Run() }()
	t.Cleanup(r.Stop)
	p2108WaitFor(t, 5*time.Second, "relay listening", func() bool {
		resp, err := http.Get("http://" + addr + "/health")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	// REAL agent, E2E ON, dialling through the relay. Runs one session in
	// the background (p2108-style single-connect; runOutbound's reconnect
	// loop would retry under us). Dial succeeded = sealed hello crossed the
	// hop; the read loop (the fix) decrypts the server's sealed frames.
	a := agent.New(agent.Config{
		URL:        "ws://" + addr + "/ws",
		Mode:       "outbound",
		Token:      "test-token",
		Name:       "p2313-hopped-agent",
		E2EEnabled: true,
	})
	// Production Run loop (outbound mode): the exact live-canary shape —
	// dial, sealed hello, read loop. Stopped at teardown via a.Stop().
	agentDead := make(chan error, 1)
	go func() { agentDead <- a.Run() }()
	t.Cleanup(a.Stop)

	agentID := "relay/p2313-relay/p2313-hopped-agent"
	p2108WaitFor(t, 10*time.Second, "relayed agent registered (sealed hello through the hop)", func() bool {
		return registered(srv, agentID)
	})

	// s4 control 1: server ping answered with a pong, within one probe
	// interval (5s timeout, well under the live 60s DEADLINE_EXCEEDED).
	pingID := "s4-ping-1"
	pongDone := make(chan protocol.Envelope, 1)
	srv.pendingMu.Lock()
	srv.pendingReqs[pingID] = pongDone
	srv.pendingMu.Unlock()
	defer func() {
		srv.pendingMu.Lock()
		delete(srv.pendingReqs, pingID)
		srv.pendingMu.Unlock()
	}()
	mgr := crypto.NewManager("test-token", true)
	p2313s4SealedWrite(t, mgr, srvRelayedConn(t, srv, agentID), protocol.Envelope{ID: pingID, Type: protocol.TypePing})
	select {
	case pong := <-pongDone:
		if pong.Type != protocol.TypePong {
			t.Fatalf("got type=%q, want pong", pong.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("relayed agent did not answer the sealed ping within 5s — the one-hop silence lives")
	}

	// s4 control 2: one exec returns REAL output through the hop.
	mgr2 := crypto.NewManager("test-token", true)
	params, _ := json.Marshal(protocol.ExecParams{Command: "echo s4-through-relay", Timeout: 20})
	execDone := make(chan protocol.Envelope, 1)
	srv.pendingMu.Lock()
	srv.pendingReqs["s4-exec-1"] = execDone
	srv.pendingMu.Unlock()
	defer func() {
		srv.pendingMu.Lock()
		delete(srv.pendingReqs, "s4-exec-1")
		srv.pendingMu.Unlock()
	}()
	p2313s4SealedWrite(t, mgr2, srvRelayedConn(t, srv, agentID), protocol.Envelope{ID: "s4-exec-1", Type: protocol.TypeExec, Params: params})
	select {
	case resp := <-execDone:
		if resp.Type != protocol.TypeExecResult {
			t.Fatalf("got type=%q, want exec_result", resp.Type)
		}
		var res protocol.ExecResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatalf("result body: %v", err)
		}
		if res.ExitCode != 0 || !strings.Contains(res.Stdout, "s4-through-relay") {
			t.Fatalf("exec not real: exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("relayed exec never returned real output within 25s")
	}

	select {
	case err := <-agentDead:
		if err != nil && !strings.Contains(err.Error(), "websocket: close") {
			t.Fatalf("agent died mid-test: %v", err)
		}
	default:
	}
}

// p2313s4SealedWrite seals env with the shared-token manager and writes it
// through the virtualConn — the SAME seat the server's writeEncryptedJSON
// uses for relayed agents (vc.WriteMessage frames the sealed payload on the
// relay wire). Raw-writes onto the session conn bypass the relay framing and
// prove nothing about the hop.
// p2313s4SealedWrite seals env with the shared-token manager and writes it
// through the virtualConn — the SAME seat the server's writeEncryptedJSON
// uses for relayed agents (vc.WriteMessage frames the sealed payload on the
// relay wire). Raw-writes onto the session conn bypass the relay framing and
// prove nothing about the hop.
func p2313s4SealedWrite(t *testing.T, mgr *crypto.Manager, vc *virtualConn, env protocol.Envelope) {
	t.Helper()
	plain, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sealed, err := mgr.Encrypt(plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := vc.WriteMessage(websocket.BinaryMessage, sealed); err != nil {
		t.Fatalf("sealed virtualConn write: %v", err)
	}
}

func srvRelayedConn(t *testing.T, srv *Server, agentID string) *virtualConn {
	t.Helper()
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	c := srv.conns[agentID]
	vc, ok := c.(*virtualConn)
	if !ok {
		t.Fatalf("agent %s conn is not a virtualConn (%T)", agentID, c)
	}
	return vc
}

// The WebSocket and binary imports serve the sealed-frame wire helpers
// above (p2313s4SendSealed's framing contract on the A2/A5 path).
