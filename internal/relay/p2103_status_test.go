package relay

// P21-03 — the uplink learns the peer, and a dead link stays visible.
//
// These tests exercise the relay's own HTTP listener and the real upstream
// WebSocket path. They read the status endpoint over HTTP and never reach
// into relay internals, so a status implementation placed on the server
// binary instead of the relay fails here.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const p2103PeerHello = `{"type":"peer_hello","peer":{"id":"server-ovh","name":"ovh-1","version":"v1.19.1","listen_addr":"ovh.example:7700"}}`

// p2103FreePort reserves and releases a loopback port for the relay listener.
func p2103FreePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// p2103Upstream is a fake upstream server: it accepts exactly one relay
// registration, answers with the peer_hello frame on channel 0, and then
// either holds the socket open or closes it after hold.
func p2103Upstream(t *testing.T, hold time.Duration) (*httptest.Server, *int32) {
	t.Helper()
	var connections int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&connections, 1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		magic, chanID, payload, err := ParseFrame(data)
		if err != nil || chanID != 0 {
			return
		}
		var ctrl ControlMessage
		if err := json.Unmarshal(payload, &ctrl); err != nil || ctrl.Type != "relay_register" {
			return
		}
		frame, err := MakeControlFrame(magic, ControlMessage{Type: "peer_hello"})
		_ = frame
		if err != nil {
			return
		}
		// peer_hello body is fixed JSON so this test does not depend on the
		// relay-side type existing yet; the wire contract is the assertion.
		if err := conn.WriteMessage(websocket.BinaryMessage, MakeFrame(magic, 0, []byte(p2103PeerHello))); err != nil {
			return
		}
		time.Sleep(hold)
	}))
	return srv, &connections
}

type p2103Status struct {
	RelayID       string `json:"relay_id"`
	MaxAgents     int    `json:"max_agents"`
	ForwardPolicy string `json:"forward_policy"`
	Upstream      struct {
		URL         string `json:"url"`
		Port        int    `json:"port"`
		State       string `json:"state"`
		ConnectedAt string `json:"connected_at"`
		Peer        *struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Version    string `json:"version"`
			ListenAddr string `json:"listen_addr"`
		} `json:"peer"`
		LastError string `json:"last_error"`
	} `json:"upstream"`
	Downstream struct {
		Listen   string                   `json:"listen"`
		Agents   []map[string]interface{} `json:"agents"`
		Channels int                      `json:"channels"`
	} `json:"downstream"`
}

func p2103ReadStatus(t *testing.T, listen string) (p2103Status, int, error) {
	t.Helper()
	resp, err := http.Get("http://" + listen + "/api/v1/relay/status")
	if err != nil {
		return p2103Status{}, 0, err
	}
	defer resp.Body.Close()
	var st p2103Status
	if resp.StatusCode == http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Logf("GET /api/v1/relay/status on %s: %s", listen, string(body))
		if err := json.Unmarshal(body, &st); err != nil {
			t.Fatalf("decode status %q: %v", string(body), err)
		}
	}
	return st, resp.StatusCode, nil
}

func p2103WaitStatus(t *testing.T, listen string, cond func(p2103Status) bool) p2103Status {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	var last p2103Status
	lastCode := 0
	for time.Now().Before(deadline) {
		st, code, err := p2103ReadStatus(t, listen)
		if err == nil {
			last, lastCode = st, code
			if code == http.StatusOK && cond(st) {
				return st
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status never satisfied condition; last http=%d last=%+v", lastCode, last)
	return last
}

// TestP2103_RelayStatusServesPeerHello drives the real relay process against
// a fake upstream and reads the status from the relay's own listen port.
func TestP2103_RelayStatusServesPeerHello(t *testing.T) {
	upstream, connections := p2103Upstream(t, 5*time.Second)
	defer upstream.Close()

	listen := p2103FreePort(t)
	rl := New(Config{
		UpstreamURL: "ws" + strings.TrimPrefix(upstream.URL, "http"),
		ListenAddr:  listen,
		Token:       "relay-token",
		MaxAgents:   12,

		AllowAnyDownstream: true, // P19-01: status pin, not an admission pin
	})
	go func() { _ = rl.Run() }()
	defer rl.Stop()

	st := p2103WaitStatus(t, listen, func(s p2103Status) bool {
		return s.Upstream.State == "up" && s.Upstream.Peer != nil
	})

	if st.RelayID == "" {
		t.Errorf("relay_id is empty; the relay must name itself")
	}
	if st.MaxAgents != 12 {
		t.Errorf("max_agents: got %d, want 12", st.MaxAgents)
	}
	if st.Upstream.URL == "" || st.Upstream.Port == 0 {
		t.Errorf("upstream url/port not populated: %q/%d", st.Upstream.URL, st.Upstream.Port)
	}
	if st.Upstream.Peer.ID != "server-ovh" || st.Upstream.Peer.ListenAddr != "ovh.example:7700" {
		t.Errorf("peer_hello not stored: %+v", st.Upstream.Peer)
	}
	if _, err := time.Parse(time.RFC3339Nano, st.Upstream.ConnectedAt); err != nil {
		t.Errorf("connected_at %q is not an RFC3339 stamp: %v", st.Upstream.ConnectedAt, err)
	}
	if st.Upstream.LastError != "" {
		t.Errorf("last_error on a healthy link: %q", st.Upstream.LastError)
	}
	if st.Downstream.Listen != listen {
		t.Errorf("downstream.listen: got %q, want %q", st.Downstream.Listen, listen)
	}
	if got := atomic.LoadInt32(connections); got != 1 {
		t.Errorf("upstream connections: got %d, want 1 (peer_hello must not open a second socket)", got)
	}
}

// TestP2103_RelayStatusSurvivesUpstreamLoss pins the P14B/P21-02 lesson: a
// dead link is degraded and named, not silently absent.
func TestP2103_RelayStatusSurvivesUpstreamLoss(t *testing.T) {
	upstream, _ := p2103Upstream(t, 150*time.Millisecond)
	defer upstream.Close()

	listen := p2103FreePort(t)
	rl := New(Config{
		UpstreamURL: "ws" + strings.TrimPrefix(upstream.URL, "http"),
		ListenAddr:  listen,
		Token:       "relay-token",

		AllowAnyDownstream: true, // P19-01: status pin, not an admission pin
	})
	go func() { _ = rl.Run() }()
	defer rl.Stop()

	up := p2103WaitStatus(t, listen, func(s p2103Status) bool {
		return s.Upstream.State == "up" && s.Upstream.Peer != nil
	})
	connectedAt := up.Upstream.ConnectedAt

	down := p2103WaitStatus(t, listen, func(s p2103Status) bool {
		return s.Upstream.State == "down"
	})
	if down.Upstream.Peer == nil || down.Upstream.Peer.ID != "server-ovh" {
		t.Errorf("last known peer dropped on link loss: %+v", down.Upstream.Peer)
	}
	if down.Upstream.ConnectedAt != connectedAt {
		t.Errorf("connected_at changed on link loss: got %q, want last known %q", down.Upstream.ConnectedAt, connectedAt)
	}
	if down.Upstream.LastError == "" {
		t.Errorf("state=down with empty last_error: the failure is not named")
	}
}
