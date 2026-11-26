package server

// P21-06 — a tunnel response must not publish a port until something proves it.
//
// The defect this file pins: the create handler used to return listen_port the
// instant the server's own listener existed, so a caller could dial a port whose
// agent side had never been checked. The contract now is:
//
//	create = agent dials the target (tunnel_probe) → server dials the port it is
//	about to publish → only then does listen_port travel, with verified:true.
//
// Every failure shape answers with an error envelope that has NO listen_port
// key at all (absence, not null and not zero), and the listener is torn down.
//
// The success and bind-failure cases run a REAL agent (internal/agent) against
// the test server over a real WebSocket, and the target is a real TCP socket, so
// the round-trip assertion is bytes on the wire, not a stub's bookkeeping.
// Everything is loopback: no host-level claim is made here.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/agent"
	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)

// p2106EchoTarget starts a real TCP echo server. Whatever a client writes comes
// back byte-for-byte, so a tunnel round-trip that returns the payload is proof
// the bytes crossed server listener → agent → target and came back.
func p2106EchoTarget(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo target listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// p2106FreePort returns a port that was just bound and released. Used when a
// test must name the port a refused create would have used — the refusal itself
// never names it, which is the point.
func p2106FreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// p2106StartRealAgent runs the real agent binary code in-process against ts and
// waits until the server considers it dispatchable. This is what makes the
// acceptance "a real agent behind it" rather than a protocol-level stub.
func p2106StartRealAgent(t *testing.T, srv *Server, ts *httptest.Server, name string) *agent.Agent {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	a := agent.New(agent.Config{
		Mode:        "outbound",
		URL:         wsURL,
		Token:       "test-token",
		Name:        name,
		Permissions: "full",
		StatusPath:  filepath.Join(t.TempDir(), "status.json"),
		BackoffMin:  10 * time.Millisecond,
		BackoffMax:  50 * time.Millisecond,
	})
	go func() { _ = a.Run() }()
	t.Cleanup(a.Stop)

	p2104Wait(t, "real agent registration", func() bool {
		ok, _, _ := srv.AgentDispatchability(name)
		return ok
	})
	return a
}

// p2106WaitConn waits until the arrival path has stored the agent's live
// connection. That entry is the fact the create's dispatch predicate reads, so
// a create issued before it exists is refused as an absent agent — a different
// contract than the one under test.
func p2106WaitConn(t *testing.T, srv *Server, agentID string) {
	t.Helper()
	p2104Wait(t, "connection for "+agentID, func() bool {
		srv.mu.RLock()
		_, ok := srv.conns[agentID]
		srv.mu.RUnlock()
		return ok
	})
}

// p2106Create performs the create call and returns status + raw body.
func p2106Create(t *testing.T, ts *httptest.Server, agentID string, targetPort, listenPort int) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"target_host":"127.0.0.1","target_port":%d,"listen_port":%d}`, targetPort, listenPort)
	return p2105Post(t, ts, "/api/v1/agents/"+agentID+"/tunnel", body)
}

// p2106CreateOK parses a success body or fails the test with the raw answer.
func p2106CreateOK(t *testing.T, body string) protocol.TunnelOpenResult {
	t.Helper()
	var env struct {
		OK   bool                      `json:"ok"`
		Data protocol.TunnelOpenResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("create body is not JSON: %s", body)
	}
	if !env.OK {
		t.Fatalf("create answered ok=false: %s", body)
	}
	return env.Data
}

// p2106AssertNoPort is the absence contract: the key must not appear anywhere in
// the body, not as a value, not as null, not as zero.
func p2106AssertNoPort(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, "listen_port") {
		t.Fatalf("failure body publishes a listen_port key: %s", body)
	}
}

// p2106AssertClosed asserts the listener is gone by dialling it. A closed
// listener refuses (or resets) immediately; a leaked one accepts.
func p2106AssertClosed(t *testing.T, port int) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatalf("port %d still accepts connections after the create was refused", port)
	}
}

// p2106ProbeAgentStub answers the readiness probe with ready=true and treats any
// other forwarded command as a harmless success. Used for the relayed-agent and
// loopback-failure cases, where the agent's own dial is not what is under test.
func p2106ProbeAgentStub(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env protocol.Envelope
			if json.Unmarshal(data, &env) != nil {
				continue
			}
			switch env.Type {
			case protocol.TypePing:
				out, _ := json.Marshal(protocol.NewPong(env.ID))
				conn.WriteMessage(websocket.TextMessage, out)
				continue
			case protocol.TypeTunnelProbe:
				var p protocol.TunnelProbeParams
				json.Unmarshal(env.Params, &p)
				res, _ := json.Marshal(protocol.TunnelProbeResult{TunnelID: p.TunnelID, Target: "stub-target", Ready: true})
				out, _ := json.Marshal(protocol.Envelope{ID: env.ID, Type: protocol.TypeTunnelProbeResult, Result: res})
				conn.WriteMessage(websocket.TextMessage, out)
				continue
			}
			res, _ := json.Marshal(map[string]interface{}{"ok": true})
			out, _ := json.Marshal(protocol.Envelope{ID: env.ID, Type: env.Type + "_result", Result: res})
			conn.WriteMessage(websocket.TextMessage, out)
		}
	}()
}

// p2106MuteAgentStub registers an agent that answers pings and nothing else, so
// a tunnel_probe gets no answer at all — the timeout case.
func p2106MuteAgentStub(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env protocol.Envelope
			if json.Unmarshal(data, &env) != nil {
				continue
			}
			if env.Type == protocol.TypePing {
				out, _ := json.Marshal(protocol.NewPong(env.ID))
				conn.WriteMessage(websocket.TextMessage, out)
			}
			// Every other command is deliberately ignored.
		}
	}()
}

// TestP2106_CreatePublishesVerifiedPortAndRoundTrips is the success acceptance:
// a real agent behind the server, verified:true + listen_port in the response,
// then a connection to that port from the test that round-trips real bytes to
// the target and back.
func TestP2106_CreatePublishesVerifiedPortAndRoundTrips(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	echoPort := p2106EchoTarget(t)
	const name = "p2106-live"
	p2106StartRealAgent(t, srv, ts, name)

	start := time.Now()
	status, body := p2106Create(t, ts, name, echoPort, 0)
	elapsed := time.Since(start)
	if status != http.StatusOK {
		t.Fatalf("create: status %d body %s", status, body)
	}
	got := p2106CreateOK(t, body)
	if !got.Verified {
		t.Fatalf("success body is not verified: %s", body)
	}
	if got.ListenPort <= 0 || got.TunnelID == "" {
		t.Fatalf("success body lost the pre-existing contract: %s", body)
	}
	if got.Loopback == nil || !got.Loopback.OK {
		t.Fatalf("success body carries no successful loopback proof: %s", body)
	}
	if got.Loopback.LatencyMS < 0 {
		t.Fatalf("negative loopback latency: %s", body)
	}
	if elapsed < time.Millisecond {
		t.Fatalf("create returned in %v — the agent confirmation cannot have happened", elapsed)
	}

	// The caller's side of the contract: dial the published port and exchange
	// bytes with the target through the agent.
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", got.ListenPort), 3*time.Second)
	if err != nil {
		t.Fatalf("dial published listen_port %d: %v", got.ListenPort, err)
	}
	defer conn.Close()
	payload := []byte("p21-06-echo")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write to tunnel: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo through tunnel: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("round-trip: got %q want %q", buf, payload)
	}
	t.Logf("create body: %s", body)
	t.Logf("round-trip on 127.0.0.1:%d wrote %q and read back %q (create took %v)",
		got.ListenPort, payload, buf, elapsed)
}

// TestP2106_AgentBindFailureIs502WithoutPort is the bind-failure acceptance: the
// agent's own dial error travels as the 502 message, listen_port is absent from
// the JSON, and the listener the create had opened is gone.
func TestP2106_AgentBindFailureIs502WithoutPort(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	const name = "p2106-bindfail"
	p2106StartRealAgent(t, srv, ts, name)

	deadPort := p2106FreePort(t)     // nothing listens here
	wouldBePort := p2106FreePort(t) // the create is asked to use this one

	status, body := p2106Create(t, ts, name, deadPort, wouldBePort)
	if status != http.StatusBadGateway {
		t.Fatalf("bind failure: status %d body %s", status, body)
	}
	if !strings.Contains(body, "agent bind check failed") {
		t.Fatalf("502 does not carry the agent's own error text: %s", body)
	}
	if !strings.Contains(body, fmt.Sprintf("127.0.0.1:%d", deadPort)) {
		t.Fatalf("502 does not name the target the agent failed to reach: %s", body)
	}
	if !strings.Contains(body, "TUNNEL_BIND_FAILED") {
		t.Fatalf("502 does not carry the bind-failure code: %s", body)
	}
	p2106AssertNoPort(t, body)
	p2106AssertClosed(t, wouldBePort)
	t.Logf("bind-failure body: %s", body)
}

// TestP2106_TimeoutIs502AndListenerClosed is the timeout acceptance: an agent
// that never answers the probe produces a distinct 502, no port, and a torn-down
// listener. The wait is shortened here; production waits tunnelReadyTimeout (5s).
func TestP2106_TimeoutIs502AndListenerClosed(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	conn := p2104DialAgent(t, ts, "p2106-mute")
	defer conn.Close()
	p2106MuteAgentStub(t, conn)
	// The create's dispatch predicate reads the conn-map; a create issued
	// before the arrival path has stored the connection is refused as an
	// absent agent, which is a different contract than the timeout under
	// test. Wait for the route the predicate reads — no assertion changes.
	p2106WaitConn(t, srv, "p2106-mute")

	old := tunnelReadyTimeout
	tunnelReadyTimeout = 300 * time.Millisecond
	defer func() { tunnelReadyTimeout = old }()

	wouldBePort := p2106FreePort(t)
	targetPort := p2106FreePort(t)
	status, body := p2106Create(t, ts, "p2106-mute", targetPort, wouldBePort)
	if status != http.StatusBadGateway {
		t.Fatalf("timeout: status %d body %s", status, body)
	}
	if !strings.Contains(body, "TUNNEL_READY_TIMEOUT") || !strings.Contains(body, "did not confirm") {
		t.Fatalf("timeout 502 does not carry its own reason: %s", body)
	}
	p2106AssertNoPort(t, body)
	p2106AssertClosed(t, wouldBePort)
	t.Logf("timeout body: %s", body)
}

// TestP2106_LoopbackFailureIsRefused pins the second proof: if the server cannot
// dial the port it opened, that is a failure response, not a success with a
// caveat. The dial is forced to fail so the branch is deterministic.
func TestP2106_LoopbackFailureIsRefused(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	conn := p2104DialAgent(t, ts, "p2106-loopback")
	defer conn.Close()
	p2106ProbeAgentStub(t, conn)
	p2106WaitConn(t, srv, "p2106-loopback")

	old := tunnelLoopbackProbe
	tunnelLoopbackProbe = func(int) (bool, int64, string) {
		return false, 0, "simulated: connection refused"
	}
	defer func() { tunnelLoopbackProbe = old }()

	wouldBePort := p2106FreePort(t)
	status, body := p2106Create(t, ts, "p2106-loopback", p2106FreePort(t), wouldBePort)
	if status != http.StatusBadGateway {
		t.Fatalf("loopback failure: status %d body %s", status, body)
	}
	if !strings.Contains(body, "TUNNEL_LOOPBACK_FAILED") || !strings.Contains(body, "simulated: connection refused") {
		t.Fatalf("loopback 502 does not carry the dial result: %s", body)
	}
	p2106AssertNoPort(t, body)
	p2106AssertClosed(t, wouldBePort)
}

// TestP2106_RelayedAgentTunnelVerifiesThroughResolver keeps P21-05's truth: the
// readiness probe goes through the one resolver, so a relayed agent's tunnel is
// verified exactly like a direct one — the create path has no second lookup.
func TestP2106_RelayedAgentTunnelVerifiesThroughResolver(t *testing.T) {
	up, upTS, upCleanup := p2104Server(t)
	defer upCleanup()
	down, downTS, downCleanup := p2104Server(t)
	defer downCleanup()

	down.SetUplinkForwardTarget(upTS.URL, "test-token")
	down.SetForwarderID("relay-p2106")
	down.SetForwardPolicy("p2106-relay", PolicyRelay)

	conn := p2104DialAgent(t, downTS, "p2106-relay")
	defer conn.Close()
	p2106ProbeAgentStub(t, conn)

	p2104Wait(t, "upstream registration", func() bool { return p2104RegistryHas(up, "relay/relay-p2106/p2106-relay") })
	p2105WaitRoute(up, "p2106-relay", true)

	status, body := p2106Create(t, upTS, "p2106-relay", p2106FreePort(t), 0)
	if status != http.StatusOK {
		t.Fatalf("create through relay: status %d body %s", status, body)
	}
	got := p2106CreateOK(t, body)
	if !got.Verified || got.Loopback == nil || !got.Loopback.OK {
		t.Fatalf("relayed create is not verified: %s", body)
	}
	if got.ListenPort <= 0 {
		t.Fatalf("relayed create published no port: %s", body)
	}
	t.Logf("relayed create body: %s", body)
}
