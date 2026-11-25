package server

// P21-03 — peer_hello is pushed DOWN the uplink on registration success.
//
// The test speaks the relay wire format by hand: one binary registration
// frame on channel 0, then exactly one framed peer_hello back on the same
// socket. No second connection, no polling.

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestP2103_PeerHelloAtRelayRegistration(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.addr = "localhost:7700"
	srv.SetVersion("v1.19.1-test")

	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial %s: %v", wsURL, err)
	}
	defer conn.Close()

	reg := []byte(`{"type":"relay_register","relay_id":"relay-1","token":"test-token","metadata":{"listen_addr":"127.0.0.1:7701","max_agents":5,"upstream":"ws://127.0.0.1:7700/ws"}}`)
	frame := make([]byte, 5+len(reg))
	frame[0] = 0x42
	binary.BigEndian.PutUint32(frame[1:5], 0)
	copy(frame[5:], reg)
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("send relay_register: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no frame arrived after relay_register: %v", err)
	}
	magic, chanID, payload, err := parseRelayFrame(data)
	if err != nil {
		t.Fatalf("parse peer frame: %v", err)
	}
	if magic != 0x42 {
		t.Errorf("frame magic: got 0x%02X, want the relay's 0x42", magic)
	}
	if chanID != 0 {
		t.Errorf("peer_hello channel: got %d, want 0 (control channel, same socket)", chanID)
	}

	var hello struct {
		Type string `json:"type"`
		Peer struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Version    string `json:"version"`
			ListenAddr string `json:"listen_addr"`
		} `json:"peer"`
	}
	if err := json.Unmarshal(payload, &hello); err != nil {
		t.Fatalf("decode peer frame %q: %v", string(payload), err)
	}
	if hello.Type != "peer_hello" {
		t.Fatalf("type: got %q, want peer_hello (payload %q)", hello.Type, string(payload))
	}
	t.Logf("peer_hello payload observed on the wire: %s", string(payload))
	if hello.Peer.ID == "" || hello.Peer.Name == "" {
		t.Errorf("peer identity empty: %+v", hello.Peer)
	}
	if hello.Peer.Version != "v1.19.1-test" {
		t.Errorf("peer.version: got %q, want the server build version", hello.Peer.Version)
	}
	host, port, err := net.SplitHostPort(hello.Peer.ListenAddr)
	if err != nil {
		t.Fatalf("peer.listen_addr %q is not host:port: %v", hello.Peer.ListenAddr, err)
	}
	if port != "7700" {
		t.Errorf("peer.listen_addr port: got %q, want 7700", port)
	}
	if host == "localhost" || host == "127.0.0.1" || host == "" || host == "0.0.0.0" {
		t.Errorf("peer.listen_addr %q is the bind string, not an address a client can dial", hello.Peer.ListenAddr)
	}

	// Exactly one message: registration success pushes the hello once.
	conn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if _, extra, err := conn.ReadMessage(); err == nil {
		t.Errorf("second frame after peer_hello: %q", string(extra))
	}
}
