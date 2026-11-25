package relay

// P21-09 — the relay walks an ordered upstream candidate list.
//
// These tests drive the real relay process: a real listener for the upstream
// candidates, the real dial path, and the status document read back over HTTP
// from the relay's own listen port. Nothing here touches the host, the Radxa or
// OVH; every address is loopback.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// p2109Status mirrors the parts of the status document this unit asserts on.
type p2109Status struct {
	Upstream struct {
		URL        string `json:"url"`
		Port       int    `json:"port"`
		Transport  string `json:"transport"`
		State      string `json:"state"`
		LastError  string `json:"last_error"`
		Candidates []struct {
			URL       string `json:"url"`
			Port      int    `json:"port"`
			State     string `json:"state"`
			Attempts  int    `json:"attempts"`
			LastError string `json:"last_error"`
		} `json:"candidates"`
	} `json:"upstream"`
}

func p2109ReadStatus(t *testing.T, listen string) (p2109Status, string, error) {
	t.Helper()
	resp, err := http.Get("http://" + listen + "/api/v1/relay/status")
	if err != nil {
		return p2109Status{}, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return p2109Status{}, string(body), nil
	}
	var st p2109Status
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("decode status %q: %v", string(body), err)
	}
	return st, string(body), nil
}

// p2109WaitStatus polls until cond holds or the deadline passes, and returns
// the body it read last so the test can quote the real status block.
func p2109WaitStatus(t *testing.T, listen string, deadline time.Duration, cond func(p2109Status) bool) (p2109Status, string) {
	t.Helper()
	end := time.Now().Add(deadline)
	var last p2109Status
	var body string
	for time.Now().Before(end) {
		st, b, err := p2109ReadStatus(t, listen)
		if err == nil {
			last, body = st, b
			if cond(st) {
				return st, b
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("status never satisfied condition within %v; last body: %s", deadline, body)
	return last, body
}

// p2109ClosedPort returns a loopback address nothing is listening on: the
// "unreachable candidate" of the acceptance.
func p2109ClosedPort(t *testing.T) (string, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return addr, port
}

// p2109Candidate is a controllable upstream: it completes the upgrade, answers
// the relay registration with peer_hello, and holds the socket until it is
// killed. kill() closes the listener and every accepted socket (a dead
// upstream), revive() re-listens on the same address (the candidate is back).
type p2109Candidate struct {
	addr    string
	port    int
	mu      sync.Mutex
	ln      net.Listener
	conns   []*websocket.Conn
	accepts int32
	done    chan struct{}
}

func p2109StartCandidate(t *testing.T, addr string) *p2109Candidate {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	bound := ln.Addr().String()
	c := &p2109Candidate{addr: bound, port: ln.Addr().(*net.TCPAddr).Port, done: make(chan struct{})}
	c.serve(ln)
	return c
}

// serve runs one accept loop over ln until it is closed.
func (c *p2109Candidate) serve(ln net.Listener) {
	c.mu.Lock()
	c.ln = ln
	c.mu.Unlock()

	go func(l net.Listener) {
		srv := &http.Server{Handler: http.HandlerFunc(c.handle)}
		_ = srv.Serve(l) // returns when the listener is closed
	}(ln)
}

func (c *p2109Candidate) handle(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&c.accepts, 1)
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.conns = append(c.conns, conn)
	c.mu.Unlock()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		magic, chanID, payload, err := ParseFrame(data)
		if err != nil || chanID != 0 {
			continue
		}
		var ctrl ControlMessage
		if json.Unmarshal(payload, &ctrl) != nil || ctrl.Type != "relay_register" {
			continue
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, MakeFrame(magic, 0, []byte(p2103PeerHello)))
	}
}

func (c *p2109Candidate) kill(t *testing.T) {
	t.Helper()
	c.mu.Lock()
	ln := c.ln
	c.ln = nil
	conns := c.conns
	c.conns = nil
	c.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, conn := range conns {
		_ = conn.Close()
	}
	// Give the relay's reader the chance to observe the drop.
	time.Sleep(100 * time.Millisecond)
}

func (c *p2109Candidate) revive(t *testing.T) {
	t.Helper()
	var ln net.Listener
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for {
		ln, err = net.Listen("tcp", c.addr)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("re-listen %s: %v", c.addr, err)
	}
	c.serve(ln)
}

func (c *p2109Candidate) accepted() int { return int(atomic.LoadInt32(&c.accepts)) }

// p2109Relay starts a relay on a fresh loopback listener with the given
// candidates and waits for its own HTTP surface to answer.
func p2109Relay(t *testing.T, cands []string, dialTimeout time.Duration) (*Relay, string) {
	t.Helper()
	listen := p2103FreePort(t)
	rl := New(Config{
		UpstreamURLs: cands,
		ListenAddr:   listen,
		Token:        "relay-token",
		MaxAgents:    10,
		DialTimeout:  dialTimeout,
	})
	go func() { _ = rl.Run() }()
	t.Cleanup(rl.Stop)
	return rl, listen
}

// TestP2109_SecondCandidateWins is the acceptance's literal case: the first
// candidate is unreachable, the second accepts. The relay must connect on the
// second, name the second's port as the transport, and retain the first's
// error.
func TestP2109_SecondCandidateWins(t *testing.T) {
	badAddr, badPort := p2109ClosedPort(t)
	good := p2109StartCandidate(t, "127.0.0.1:0")
	defer good.kill(t)

	goodURL := "ws://" + good.addr + "/ws"
	badURL := "ws://" + badAddr + "/ws"
	_, listen := p2109Relay(t, []string{badURL, goodURL}, 2*time.Second)

	st, body := p2109WaitStatus(t, listen, 6*time.Second, func(s p2109Status) bool {
		return s.Upstream.State == "up"
	})
	t.Logf("status after failover: %s", body)

	if st.Upstream.URL != goodURL {
		t.Errorf("upstream.url: got %q, want the winning candidate %q", st.Upstream.URL, goodURL)
	}
	if st.Upstream.Port != good.port {
		t.Errorf("upstream.port: got %d, want %d", st.Upstream.Port, good.port)
	}
	if st.Upstream.Transport != strconv.Itoa(good.port) {
		t.Errorf("upstream.transport: got %q, want %q (the port actually used, not the first in the list)",
			st.Upstream.Transport, strconv.Itoa(good.port))
	}
	if st.Upstream.Transport == strconv.Itoa(badPort) {
		t.Errorf("upstream.transport names the unreachable candidate's port %d", badPort)
	}
	if len(st.Upstream.Candidates) != 2 {
		t.Fatalf("candidates: got %d entries, want 2", len(st.Upstream.Candidates))
	}
	if c0 := st.Upstream.Candidates[0]; c0.State != "down" || c0.LastError == "" || c0.Attempts < 1 {
		t.Errorf("first candidate not retained as a named failure: %+v", c0)
	}
	if c1 := st.Upstream.Candidates[1]; c1.State != "up" || c1.Attempts < 1 {
		t.Errorf("second candidate is not the recorded winner: %+v", c1)
	}
	if good.accepted() != 1 {
		t.Errorf("winning candidate accepted %d connections, want 1", good.accepted())
	}
}

// TestP2109_TimeoutMovesToNext pins the second failure mode: a candidate that
// accepts TCP and never completes the upgrade must bound that attempt and move
// on, not stall the walk on the dialer's default handshake timeout.
func TestP2109_TimeoutMovesToNext(t *testing.T) {
	// A listener that accepts and never speaks WebSocket.
	hole, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen blackhole: %v", err)
	}
	defer hole.Close()
	var held []net.Conn
	var hmu sync.Mutex
	go func() {
		for {
			c, err := hole.Accept()
			if err != nil {
				return
			}
			hmu.Lock()
			held = append(held, c)
			hmu.Unlock()
		}
	}()
	holePort := hole.Addr().(*net.TCPAddr).Port

	good := p2109StartCandidate(t, "127.0.0.1:0")
	defer good.kill(t)

	start := time.Now()
	_, listen := p2109Relay(t, []string{"ws://" + hole.Addr().String() + "/ws", "ws://" + good.addr + "/ws"}, 400*time.Millisecond)
	st, body := p2109WaitStatus(t, listen, 8*time.Second, func(s p2109Status) bool {
		return s.Upstream.State == "up"
	})
	t.Logf("status after timeout-failover: %s", body)

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("walk took %v: the black-holed candidate was not bounded by DialTimeout", elapsed)
	}
	if st.Upstream.Transport != strconv.Itoa(good.port) {
		t.Errorf("transport: got %q, want %d", st.Upstream.Transport, good.port)
	}
	if c0 := st.Upstream.Candidates[0]; c0.State != "down" || c0.LastError == "" {
		t.Errorf("timed-out candidate not retained as a named failure: %+v", c0)
	}
	if c0 := st.Upstream.Candidates[0]; c0.Port != holePort {
		t.Errorf("candidate port: got %d, want %d", c0.Port, holePort)
	}
	hmu.Lock()
	for _, c := range held {
		c.Close()
	}
	hmu.Unlock()
}

// TestP2109_DisconnectRewalksFromTop kills the accepted upstream and asserts the
// relay re-enters the list at the top — the dead candidate is dialled again, not
// skipped — and re-forms on the candidate that came back.
func TestP2109_DisconnectRewalksFromTop(t *testing.T) {
	badAddr, _ := p2109ClosedPort(t)
	good := p2109StartCandidate(t, "127.0.0.1:0")
	defer good.kill(t)
	goodAddr := good.addr

	_, listen := p2109Relay(t, []string{"ws://" + badAddr + "/ws", "ws://" + goodAddr + "/ws"}, 2*time.Second)

	first, _ := p2109WaitStatus(t, listen, 6*time.Second, func(s p2109Status) bool {
		return s.Upstream.State == "up"
	})
	if c0, c1 := first.Upstream.Candidates[0], first.Upstream.Candidates[1]; c0.Attempts != 1 || c1.Attempts != 1 {
		t.Fatalf("attempts before the drop: %d/%d, want 1/1", c0.Attempts, c1.Attempts)
	}

	// Kill the accepted upstream, wait for the relay to see the drop, then bring
	// the same candidate back on the same address.
	good.kill(t)
	down, _ := p2109WaitStatus(t, listen, 5*time.Second, func(s p2109Status) bool {
		return s.Upstream.State == "down"
	})
	if down.Upstream.Candidates[1].State != "down" || down.Upstream.Candidates[1].LastError == "" {
		t.Errorf("dead candidate not named on the drop: %+v", down.Upstream.Candidates[1])
	}
	good.revive(t)

	st, body := p2109WaitStatus(t, listen, 15*time.Second, func(s p2109Status) bool {
		return s.Upstream.State == "up"
	})
	t.Logf("status after re-walk: %s", body)

	if len(st.Upstream.Candidates) != 2 {
		t.Fatalf("candidates: got %d entries, want 2", len(st.Upstream.Candidates))
	}
	if c0 := st.Upstream.Candidates[0]; c0.Attempts < 2 {
		t.Errorf("first candidate attempts: got %d after the drop, want >= 2 — the walk did not restart at the top", c0.Attempts)
	}
	if c1 := st.Upstream.Candidates[1]; c1.State != "up" || c1.Attempts < 2 {
		t.Errorf("second candidate after re-form: %+v (want state=up, attempts>=2)", c1)
	}
	if st.Upstream.Transport != strconv.Itoa(good.port) {
		t.Errorf("transport after re-form: got %q, want %d", st.Upstream.Transport, good.port)
	}
	if !strings.Contains(st.Upstream.URL, goodAddr) {
		t.Errorf("upstream.url after re-form: got %q, want %q", st.Upstream.URL, goodAddr)
	}
	if got := good.accepted(); got < 2 {
		t.Errorf("revived candidate accepted %d connections, want >= 2", got)
	}
}

// TestP2109_SingleStringIsOneCandidate proves the legacy config form is the same
// code path: one string resolves to a one-element list, and a one-element list
// still connects and reports its transport.
func TestP2109_SingleStringIsOneCandidate(t *testing.T) {
	good := p2109StartCandidate(t, "127.0.0.1:0")
	defer good.kill(t)

	listen := p2103FreePort(t)
	rl := New(Config{
		UpstreamURL: "ws://" + good.addr + "/ws", // legacy single-string form
		ListenAddr:  listen,
		Token:       "relay-token",
	})
	if got := len(rl.candidates); got != 1 {
		t.Fatalf("legacy UpstreamURL resolved to %d candidates, want 1", got)
	}
	go func() { _ = rl.Run() }()
	defer rl.Stop()

	st, body := p2109WaitStatus(t, listen, 6*time.Second, func(s p2109Status) bool {
		return s.Upstream.State == "up"
	})
	t.Logf("status from single-string config: %s", body)

	if len(st.Upstream.Candidates) != 1 {
		t.Fatalf("candidates: got %d, want 1", len(st.Upstream.Candidates))
	}
	if st.Upstream.Transport != strconv.Itoa(good.port) {
		t.Errorf("transport: got %q, want %d", st.Upstream.Transport, good.port)
	}
}
