package server

// P21-05 — the control plane routes by relay, not only by direct connection.
//
// Shape under test (loopback only; the Radxa<->OVH leg named by the spec is
// UNPROVEN-ON-HOST and nothing here touches either host):
//
//	agent --ws--> S1 --P21-04 forward link (relay)--> S2 <-- operator API
//	agent --ws--> relay socket (hand-rolled) --------> S2
//
// The agent stub answers exec / fs-list / proc-list deterministically, so a
// response body proves which agent produced it.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)

// p2105AgentStub answers forwarded envelopes deterministically. It runs for
// both topologies: a direct client of S1 (which forwards the frames) and a
// channel on a hand-rolled relay socket.
func p2105AgentStub(t *testing.T, conn *websocket.Conn, tag string) {
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
			var result interface{}
			switch env.Type {
			case protocol.TypePing:
				out, _ := json.Marshal(protocol.NewPong(env.ID))
				conn.WriteMessage(websocket.TextMessage, out)
				continue
			case protocol.TypeExec:
				var p protocol.ExecParams
				json.Unmarshal(env.Params, &p)
				result = protocol.ExecResult{Stdout: tag + "-echo:" + p.Command, ExitCode: 0}
			case protocol.TypeFSList:
				result = protocol.FSListResult{
					Entries:    []protocol.FSEntry{{Name: "alpha.txt", Size: 11, Mode: "-rw-r--r--", IsDir: false}, {Name: "beta.log", Size: 7, Mode: "-rw-r--r--"}},
					Count:      2,
					EntryCount: 2,
				}
			case protocol.TypeProcList:
				result = map[string]interface{}{
					"processes": []map[string]interface{}{
						{"pid": 4242, "name": tag + "-proc"},
					},
				}
			default:
				result = map[string]interface{}{"tag": tag}
			}
			res, _ := json.Marshal(result)
			out, _ := json.Marshal(protocol.Envelope{ID: env.ID, Type: env.Type + "_result", Result: res})
			conn.WriteMessage(websocket.TextMessage, out)
		}
	}()
}

// p2105Post performs an authenticated API call and returns status + body.
func p2105Post(t *testing.T, ts *httptest.Server, path string, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// p2105WaitRoute polls the route map, so a lifecycle assertion never rests on
// a sleep.
func p2105WaitRoute(srv *Server, agentID string, want bool) {
	srv.routeStore.mu.RLock()
	_, ok := srv.routeStore.routes[agentID]
	srv.routeStore.mu.RUnlock()
	if ok != want {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			srv.routeStore.mu.RLock()
			_, ok = srv.routeStore.routes[agentID]
			srv.routeStore.mu.RUnlock()
			if ok == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// TestP2105_ExecThroughRelayReturnsAgentOutput is the spec's shape: two
// servers, a relay link between them, an agent behind it. The operator asks
// the UPSTREAM server for the agent's own id and gets the agent's real output.
func TestP2105_ExecThroughRelayReturnsAgentOutput(t *testing.T) {
	up, upTS, upCleanup := p2104Server(t)
	defer upCleanup()
	down, downTS, downCleanup := p2104Server(t)
	defer downCleanup()

	down.SetUplinkForwardTarget(upTS.URL, "test-token")
	down.SetForwarderID("relay-ds")
	down.SetForwardPolicy("eas1", PolicyRelay)

	conn := p2104DialAgent(t, downTS, "eas1")
	defer conn.Close()
	p2105AgentStub(t, conn, "eas1")

	p2104Wait(t, "upstream registration", func() bool {
		return p2104RegistryHas(up, "relay/relay-ds/eas1")
	})
	p2105WaitRoute(up, "eas1", true)

	// The predicate must agree with the router before anything is sent.
	dispatchable, source, reason := up.AgentDispatchability("eas1")
	if !dispatchable || source != LivenessSourceRelay || reason != "" {
		t.Fatalf("predicate for relayed agent: dispatchable=%v source=%q reason=%q", dispatchable, source, reason)
	}

	status, body := p2105Post(t, upTS, "/api/v1/agents/eas1/exec", `{"command":"hostname"}`)
	if status != http.StatusOK {
		t.Fatalf("exec through relay: status %d body %s", status, body)
	}
	var got struct {
		Data struct {
			Stdout string `json:"stdout"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("exec body is not JSON: %s", body)
	}
	if got.Data.Stdout != "eas1-echo:hostname" {
		t.Fatalf("exec stdout: got %q, want the agent's own output %q (body %s)", got.Data.Stdout, "eas1-echo:hostname", body)
	}
	t.Logf("exec response body through relay: %s", body)
}

// TestP2105_FSListAndProcListShareResolver proves the resolver is shared: two
// more control types reach the same relayed agent without their own lookup.
func TestP2105_FSListAndProcListShareResolver(t *testing.T) {
	up, upTS, upCleanup := p2104Server(t)
	defer upCleanup()
	down, downTS, downCleanup := p2104Server(t)
	defer downCleanup()

	down.SetUplinkForwardTarget(upTS.URL, "test-token")
	down.SetForwarderID("relay-ds")
	down.SetForwardPolicy("eas2", PolicyRelay)

	conn := p2104DialAgent(t, downTS, "eas2")
	defer conn.Close()
	p2105AgentStub(t, conn, "eas2")

	p2104Wait(t, "upstream registration", func() bool { return p2104RegistryHas(up, "relay/relay-ds/eas2") })
	p2105WaitRoute(up, "eas2", true)

	status, body := p2105Post(t, upTS, "/api/v1/agents/eas2/fs-list", `{"path":"/tmp"}`)
	if status != http.StatusOK {
		t.Fatalf("fs-list through relay: status %d body %s", status, body)
	}
	if !strings.Contains(body, "alpha.txt") || !strings.Contains(body, "beta.log") {
		t.Fatalf("fs-list body is not the agent's listing: %s", body)
	}

	status, body = p2105Post(t, upTS, "/api/v1/agents/eas2/proc-list", `{}`)
	if status != http.StatusOK {
		t.Fatalf("proc-list through relay: status %d body %s", status, body)
	}
	if !strings.Contains(body, "eas2-proc") {
		t.Fatalf("proc-list body is not the agent's process table: %s", body)
	}
	t.Logf("fs-list body: %s", body)
}

// p2105RelaySocket opens a relay socket to srv, opens one channel, and
// registers an agent on it. It returns the socket and the channel id.
func p2105RelaySocket(t *testing.T, ts *httptest.Server, relayID, magic, agentName string, chanID uint32) *websocket.Conn {
	t.Helper()
	conn := p2104RelayRegistration(t, ts, []byte(magic)[0], map[string]interface{}{
		"type": "relay_register", "relay_id": relayID, "token": "test-token",
	})
	open, _ := json.Marshal(map[string]interface{}{"type": "channel_open", "channel_id": chanID, "relay_id": relayID})
	conn.WriteMessage(websocket.BinaryMessage, p2104Frame([]byte(magic)[0], 0, open))
	res, _ := json.Marshal(protocol.AgentInfo{Name: agentName, Version: "v-test", OS: "linux", Arch: "amd64"})
	env, _ := json.Marshal(protocol.Envelope{ID: "reg-1", Type: "agent_info", Result: res})
	conn.WriteMessage(websocket.BinaryMessage, p2104Frame([]byte(magic)[0], chanID, env))
	return conn
}

// p2105RelayResponder answers framed envelopes on one relay channel.
// P22-07: writes are serialized through p2105WriteMu — this responder goroutine
// and the test body write control frames (channel_close) on the SAME conn, and
// the two unsynchronized WriteMessage calls were the fixture-internal data race
// the package -race run flagged (pre-existing at the ea28a49/60c5950 baseline).
func p2105RelayResponder(t *testing.T, conn *websocket.Conn, magic string, chanID uint32, tag string, answered chan<- string) {
	t.Helper()
	writeMu := &sync.Mutex{}
	p2105WriteMu.Store(conn, writeMu)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				p2105WriteMu.Delete(conn)
				return
			}
			m, ch, payload, perr := parseRelayFrame(data)
			if perr != nil || ch != chanID {
				continue
			}
			var env protocol.Envelope
			if json.Unmarshal(payload, &env) != nil {
				continue
			}
			var result interface{}
			switch env.Type {
			case protocol.TypeExec:
				var p protocol.ExecParams
				json.Unmarshal(env.Params, &p)
				result = protocol.ExecResult{Stdout: tag + "-echo:" + p.Command}
			case protocol.TypeFSList:
				result = protocol.FSListResult{Entries: []protocol.FSEntry{{Name: "relay-alpha.txt"}}, Count: 1, EntryCount: 1}
			default:
				result = map[string]interface{}{"tag": tag}
			}
			res, _ := json.Marshal(result)
			out, _ := json.Marshal(protocol.Envelope{ID: env.ID, Type: env.Type + "_result", Result: res})
			_ = m
			writeMu.Lock()
			werr := conn.WriteMessage(websocket.BinaryMessage, p2104Frame(magic[0], chanID, out))
			writeMu.Unlock()
			if werr != nil {
				p2105WriteMu.Delete(conn)
				return
			}
			if answered != nil {
				select {
				case answered <- env.Type:
				default:
				}
			}
		}
	}()
}

// p2105WriteMu maps a fixture relay conn to the mutex its writers must take,
// so the test body can send control frames on the same socket without racing
// the responder goroutine.
var p2105WriteMu sync.Map

// TestP2105_RouteMapLifecycleNamedError proves the map is written at
// registration and removed with the channel — and that a command after the
// close fails by NAME, not by timeout and not as AGENT_UNAVAILABLE.
func TestP2105_RouteMapLifecycleNamedError(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	relayConn := p2105RelaySocket(t, ts, "relay-lc", "Q", "eas3", 7)
	p2105RelayResponder(t, relayConn, "Q", 7, "eas3", nil)

	p2105WaitRoute(srv, "relay/relay-lc/eas3", true)
	p2105WaitRoute(srv, "eas3", true)

	status, body := p2105Post(t, ts, "/api/v1/agents/eas3/exec", `{"command":"id"}`)
	if status != http.StatusOK || !strings.Contains(body, "eas3-echo:id") {
		t.Fatalf("exec before close: status %d body %s", status, body)
	}

	done, _ := json.Marshal(map[string]interface{}{"type": "channel_close", "channel_id": 7, "relay_id": "relay-lc"})
	// The channel_close control frame shares the socket with the responder
	// goroutine; take the fixture's write mutex the way the responder does.
	if mu, ok := p2105WriteMu.Load(relayConn); ok {
		mu.(*sync.Mutex).Lock()
		relayConn.WriteMessage(websocket.BinaryMessage, p2104Frame('Q', 0, done))
		mu.(*sync.Mutex).Unlock()
	} else {
		relayConn.WriteMessage(websocket.BinaryMessage, p2104Frame('Q', 0, done))
	}
	p2105WaitRoute(srv, "eas3", false)
	p2105WaitRoute(srv, "relay/relay-lc/eas3", false)

	start := time.Now()
	status, body = p2105Post(t, ts, "/api/v1/agents/eas3/exec", `{"command":"id"}`)
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("exec on a dead route took %v: it hung instead of failing by name", elapsed)
	}
	if status != http.StatusServiceUnavailable {
		t.Fatalf("exec after channel close: status %d body %s", status, body)
	}
	if !strings.Contains(body, RefusalRouteGone) {
		t.Fatalf("exec after channel close: body %s does not name %s", body, RefusalRouteGone)
	}
	if strings.Contains(body, "AGENT_UNAVAILABLE") {
		t.Fatalf("exec after channel close answered the generic absence code: %s", body)
	}
	t.Logf("dead-route refusal in %v: %s", elapsed, body)
}

// TestP2105_DirectBeatsRelayRoute proves the preference order by the path
// taken: an agent that has BOTH a direct connection and a stale route entry is
// served directly, and the relay socket sees nothing.
func TestP2105_DirectBeatsRelayRoute(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	relayConn := p2105RelaySocket(t, ts, "relay-pref", "R", "eas4", 9)
	relayAnswered := make(chan string, 4)
	p2105RelayResponder(t, relayConn, "R", 9, "eas4-relay", relayAnswered)
	p2105WaitRoute(srv, "eas4", true)

	// A direct connection for the SAME id arrives after the route exists.
	direct := p2104DialAgent(t, ts, "eas4")
	defer direct.Close()
	p2105AgentStub(t, direct, "eas4-direct")

	p2104Wait(t, "direct registration", func() bool {
		srv.mu.RLock()
		_, ok := srv.conns["eas4"]
		srv.mu.RUnlock()
		return ok
	})

	tr, err := srv.resolveAgentTransport("eas4")
	if err != nil {
		t.Fatalf("resolve with both paths: %v", err)
	}
	if tr.Source != LivenessSourceWS {
		t.Fatalf("resolver chose %q for an agent with a direct connection: the relay route won", tr.Source)
	}

	status, body := p2105Post(t, ts, "/api/v1/agents/eas4/exec", `{"command":"whoami"}`)
	if status != http.StatusOK {
		t.Fatalf("exec with both paths: status %d body %s", status, body)
	}
	if !strings.Contains(body, "eas4-direct-echo:whoami") {
		t.Fatalf("exec was not served by the direct connection: %s", body)
	}
	select {
	case seen := <-relayAnswered:
		t.Fatalf("relay received %q for an agent with a direct connection: double-send", seen)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestP2105_DeadRouteMidCallFailsByName drops the relay socket while a command
// is in flight. The caller must get the named route error promptly — not a
// timeout, not a generic failure.
func TestP2105_DeadRouteMidCallFailsByName(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	relayConn := p2105RelaySocket(t, ts, "relay-mid", "S", "eas5", 11)
	p2105WaitRoute(srv, "eas5", true)
	// No responder: the command is in flight when the socket dies.

	errCh := make(chan error, 1)
	go func() {
		_, err := srv.forwardToAgentWithTimeout("eas5", protocol.TypeExec,
			protocol.ExecParams{Command: "sleep 30"}, 20*time.Second, "")
		errCh <- err
	}()
	time.Sleep(120 * time.Millisecond)
	relayConn.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("in-flight command on a dropped route returned success")
		}
		if !strings.Contains(err.Error(), RefusalRouteGone) {
			t.Fatalf("mid-call drop error %q does not name %s", err.Error(), RefusalRouteGone)
		}
		t.Logf("mid-call route loss: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight command on a dropped route hung: no answer in 3s")
	}
}

// TestP2105_PredicateNamesTheMissingRoute keeps P21-01 honest when neither
// path exists: the dispatch error names the missing route.
func TestP2105_PredicateNamesTheMissingRoute(t *testing.T) {
	srv, _, cleanup := p2104Server(t)
	defer cleanup()

	dispatchable, _, reason := srv.AgentDispatchability("nobody")
	if dispatchable {
		t.Fatal("predicate called an agent with no route dispatchable")
	}
	if !strings.Contains(reason, "no route") && !strings.Contains(reason, "neither") {
		t.Fatalf("dispatch error does not name the missing route: %q", reason)
	}
	t.Logf("missing-route dispatch error: %s", reason)
}

// p2105NoDummies: every name in this file is used by a real assertion.
