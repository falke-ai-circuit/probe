package relay

// P22-03 (F3) — a max_agents refusal is a typed frame, not a bare 429.
//
// The relay's admission guard used to answer an over-limit agent with
// http.Error(...429) BEFORE the WebSocket upgrade. A WebSocket client cannot
// read that answer: gorilla reduces every non-101 handshake to
// ErrBadHandshake, so the agent logged "bad handshake" and the relay's reason
// never left the process. These tests pin the fix at the wire level:
//
//   - the limit+1 connection UPGRADES (no handshake failure),
//   - it then receives one error envelope naming FORWARD_MAX_AGENTS, the
//     configured limit and the live count,
//   - the refusal fires at limit+1 and not earlier, with the configured limit,
//   - a non-WebSocket caller still gets a 429, but one that names the code.
//
// Everything is loopback: the upstream is the in-package candidate harness, the
// relay is the real New/Run path, the clients are real WebSocket clients.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// p2203WaitListening blocks until the relay's downstream listener accepts a TCP
// connection. Run() starts listening only after the upstream registration, so a
// successful accept also means the relay is past its uplink step.
func p2203WaitListening(t *testing.T, listen string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", listen, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("relay listener %s never came up", listen)
}

// p2203Start runs the real relay against the package's upstream candidate
// harness with the given max_agents, and returns its downstream listen address.
func p2203Start(t *testing.T, maxAgents int) (string, *Relay) {
	t.Helper()
	up := p2109StartCandidate(t, "127.0.0.1:0")
	t.Cleanup(func() { up.kill(t) })

	listen := p2103FreePort(t)
	rl := New(Config{
		UpstreamURLs: []string{"ws://" + up.addr + "/ws"},
		ListenAddr:   listen,
		Token:        "relay-token",
		MaxAgents:    maxAgents,
		DialTimeout:  2 * time.Second,
	})
	go func() { _ = rl.Run() }()
	t.Cleanup(rl.Stop)
	p2203WaitListening(t, listen)
	return listen, rl
}

// p2203DialAgent completes the upgrade and sends the first frame the relay
// reads to allocate a channel — i.e. exactly what an agent does on connect.
func p2203DialAgent(t *testing.T, listen, name string) *websocket.Conn {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial("ws://"+listen+"/ws", nil)
	if err != nil {
		st := 0
		if resp != nil {
			st = resp.StatusCode
		}
		t.Fatalf("agent %q dial failed: %v (status=%d) — admission should complete the upgrade", name, err, st)
	}
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("agent %q: status=%v, want 101 Switching Protocols", name, resp)
	}
	info := fmt.Sprintf(`{"id":"agent-info","type":"agent_info","result":{"name":%q}}`, name)
	if err := c.WriteMessage(websocket.TextMessage, []byte(info)); err != nil {
		c.Close()
		t.Fatalf("agent %q first frame: %v", name, err)
	}
	return c
}

// p2203WaitChannels waits until the relay counts n downstream channels.
func p2203WaitChannels(t *testing.T, rl *Relay, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rl.channels.Count() == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("relay channel count: got %d, want %d", rl.channels.Count(), n)
}

// p2203ReadRefusal reads one frame and decodes it as the typed refusal.
func p2203ReadRefusal(t *testing.T, c *websocket.Conn) protocol.Envelope {
	t.Helper()
	if err := c.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	mt, data, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("no refusal frame arrived: %v", err)
	}
	if mt != websocket.TextMessage {
		t.Fatalf("refusal frame type=%d, want a text envelope", mt)
	}
	t.Logf("refusal frame as received: %s", data)
	var env protocol.Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("refusal frame is not a protocol envelope: %v (%s)", err, data)
	}
	return env
}

// TestP2203_MaxAgentsRefusalArrivesAsTypedFrame is the acceptance's core: the
// limit+1 agent completes the handshake and is told, in the protocol's own
// shape, which limit it hit and how many agents the relay is carrying.
func TestP2203_MaxAgentsRefusalArrivesAsTypedFrame(t *testing.T) {
	listen, rl := p2203Start(t, 1)

	holder := p2203DialAgent(t, listen, "holder")
	defer holder.Close()
	p2203WaitChannels(t, rl, 1)

	// limit+1 — the upgrade must succeed. A dial error here is the defect.
	over, resp, err := websocket.DefaultDialer.Dial("ws://"+listen+"/ws", nil)
	if err != nil {
		st := 0
		if resp != nil {
			st = resp.StatusCode
		}
		t.Fatalf("limit+1 dial failed: %v (status=%d) — the refusal is still a handshake failure, not a frame", err, st)
	}
	defer over.Close()
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("limit+1 status=%v, want 101", resp)
	}

	env := p2203ReadRefusal(t, over)
	if env.Type != "error" || env.Error == nil {
		t.Fatalf("refusal envelope: got type=%q error=%v, want a typed error frame", env.Type, env.Error)
	}
	if env.Error.Code != protocol.CodeForwardMaxAgents {
		t.Errorf("refusal code: got %q, want %q", env.Error.Code, protocol.CodeForwardMaxAgents)
	}
	limit, ok := env.Error.DetailInt("limit")
	if !ok || limit != 1 {
		t.Errorf("refusal limit: got %d (present=%v), want the configured 1", limit, ok)
	}
	current, ok := env.Error.DetailInt("current")
	if !ok || current != 1 {
		t.Errorf("refusal current: got %d (present=%v), want the live count 1", current, ok)
	}
	if !strings.Contains(env.Error.Message, "max_agents limit 1") {
		t.Errorf("refusal message %q does not quote the limit", env.Error.Message)
	}
	if !strings.Contains(env.Error.Message, rl.relayID()) {
		t.Errorf("refusal message %q does not name the link", env.Error.Message)
	}

	// The frame is the answer; the socket closes after it.
	if _, _, err := over.ReadMessage(); err == nil {
		t.Errorf("refused socket stayed open after the refusal frame")
	}

	// The admitted agent is untouched by the refusal.
	if err := holder.WriteMessage(websocket.TextMessage, []byte(`{"id":"ping-1","type":"ping"}`)); err != nil {
		t.Errorf("admitted agent's socket broke when the next agent was refused: %v", err)
	}
}

// TestP2203_RefusalFiresAtLimitPlusOne pins the cap: with max_agents=2 the
// third arrival is refused and neither of the first two is.
func TestP2203_RefusalFiresAtLimitPlusOne(t *testing.T) {
	listen, rl := p2203Start(t, 2)

	a := p2203DialAgent(t, listen, "a")
	defer a.Close()
	b := p2203DialAgent(t, listen, "b")
	defer b.Close()
	p2203WaitChannels(t, rl, 2)

	// Under the limit nothing is refused: a third dial while the count is 2
	// must be the one that carries the frame.
	c := p2203DialAgent(t, listen, "c") // dial helper fails the test on a handshake error
	defer c.Close()
	if n := rl.channels.Count(); n != 2 {
		t.Errorf("channel count after the refused arrival: got %d, want the admitted 2 — the refusal must not consume a slot", n)
	}

	env := p2203ReadRefusal(t, c)
	if env.Error == nil || env.Error.Code != protocol.CodeForwardMaxAgents {
		t.Fatalf("third arrival: got %+v, want %s", env.Error, protocol.CodeForwardMaxAgents)
	}
	if limit, _ := env.Error.DetailInt("limit"); limit != 2 {
		t.Errorf("refusal limit: got %d, want the configured 2", limit)
	}
	if current, _ := env.Error.DetailInt("current"); current != 2 {
		t.Errorf("refusal current: got %d, want 2 (limit reached count, not limit+1)", current)
	}
}

// TestP2203_NonUpgradeCallerGetsNamed429 keeps the HTTP answer usable for a
// probe or a curl: it is still a 429, but it names the code instead of
// "too many agents".
func TestP2203_NonUpgradeCallerGetsNamed429(t *testing.T) {
	listen, rl := p2203Start(t, 1)
	holder := p2203DialAgent(t, listen, "holder")
	defer holder.Close()
	p2203WaitChannels(t, rl, 1)

	resp, err := http.Get("http://" + listen + "/ws")
	if err != nil {
		t.Fatalf("GET /ws: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status: got %d, want 429", resp.StatusCode)
	}
	buf := make([]byte, 512)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	t.Logf("non-upgrade refusal body: %s", strings.TrimSpace(body))
	if !strings.Contains(body, protocol.CodeForwardMaxAgents) {
		t.Errorf("body %q does not name %s", body, protocol.CodeForwardMaxAgents)
	}
	if !strings.Contains(body, "max_agents limit 1") {
		t.Errorf("body %q does not quote the configured limit", body)
	}
}
