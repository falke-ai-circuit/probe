package server

// P23-12 — C6 handshake falsification (input: P23-07 CHECK 6).
//
// Fault history live (probe-v119-test, 09:25-09:45Z): 21 of 22 agent dials
// close 1006 ~1s after connect and the server logs
// "expected relay registration on channel 0, got channel <random>"
// (internal/server/relay_ws.go:193). One dial reached agent-connected.
//
// Falsified trigger, reproduced here as a source-backed unit:
// server_ws.go handleWebSocket routes ANY first-frame BinaryMessage to
// handleRelayConnection (server_ws.go:105-108). An E2E-active agent's first
// frame is binary AES-GCM ciphertext of its agent-info Envelope
// (agent.go writeMessage: e2eMgr.IsActive -> WriteMessage(BinaryMessage,
// ciphertext); agent.go handleConnection sends agent-info FIRST).
// The relay parser reads bytes [1:5] of the ciphertext as a channel ID —
// random because those bytes are random nonce/ciphertext — and the
// chanID != 0 guard closes the socket. The server's E2E decrypt branch
// exists only inside handleMessages (server_ws.go:289-295), after
// registration, so the hello can never reach it.
//
// Version skew is NOT the trigger: the E2E default-on posture and the
// writeMessage encrypt branch are identical agent- and server-side at this
// HEAD. The 1/22 that DID register is one dial whose server-side posture had
// E2E off (or the restart boundary), where the first frame is TextMessage
// JSON and registration always lands. The prediction this file pins:
//
//	e2e-on agent hello  -> relay-registration channel error, socket closed.
//	e2e-off agent hello -> "agent connected", no relay parser hit.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"bytes"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/crypto"
	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)

// p2312StartWSServer boots a real HTTP server on /ws with the posture the T1
// rig runs: E2E ON, token "test-token". Returns the server and the base URL.
func p2312StartWSServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv := NewServer("", "test-token", t.TempDir()+"/registry.json")
	srv.mux = http.NewServeMux()
	srv.mux.HandleFunc("/ws", srv.handleWebSocket)
	srv.SetE2E("test-token", true)
	ts := httptest.NewServer(srv.mux)
	t.Cleanup(func() {
		ts.Close()
		srv.registry.Stop()
		// Drain async registry/session writes (atomic .tmp renames) before
		// the framework RemoveAll's the TempDir — P23-12: full-suite mode
		// surfaced the race as "TempDir RemoveAll cleanup: directory not
		// empty" / "rename ... no such file or directory".
		time.Sleep(250 * time.Millisecond)
	})
	return srv, strings.Replace(ts.URL, "http", "ws", 1) + "/ws"
}

// p2312WaitForLog polls a log buffer for a substring with a deadline.
func p2312WaitForLog(t *testing.T, buf *bytes.Buffer, needle string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s := buf.String(); strings.Contains(s, needle) {
			return s
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("log line %q never appeared; log=%q", needle, buf.String())
	return ""
}

func p2312Dial(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	h := http.Header{}
	h.Set("Authorization", "Bearer test-token")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, h)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func p2312AgentInfoEnvelope(t *testing.T, name string) protocol.Envelope {
	t.Helper()
	info := protocol.AgentInfo{
		Name:            name,
		Version:         "1.19.1",
		OS:              "linux",
		Arch:            "amd64",
		Mode:            "outbound",
		ProtocolVersion: "2",
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Envelope{ID: "agent-info", Type: "agent_info", Result: raw}
}

// TestP2312_C6_HelloBinaryCipherRegisters — A2 pinned in-suite (pre-patch run
// of this same test FAILed with `expected relay registration on channel 0, got
// channel 2993237412` — that failure IS the repro; see P23-12 artefact): an
// E2E-active agent's first frame (binary ciphertext of agent-info) must now
// decrypt via the first-frame GCM classification and register the agent.
func TestP2312_C6_HelloBinaryCipherRegisters(t *testing.T) {
	_, wsURL := p2312StartWSServer(t)

	var logBuf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prev)

	conn := p2312Dial(t, wsURL)

	mgr := crypto.NewManager("test-token", true)
	if !mgr.IsActive() {
		t.Fatal("e2e manager inactive in repro setup")
	}
	plain, err := json.Marshal(p2312AgentInfoEnvelope(t, "p2312-e2e-agent"))
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := mgr.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, cipher); err != nil {
		t.Fatalf("first-frame write: %v", err)
	}

	p2312WaitForLog(t, &logBuf, "agent connected: p2312-e2e-agent")
	if s := logBuf.String(); strings.Contains(s, "expected relay registration") {
		t.Fatalf("E2E hello misrouted to the relay parser: %s", s)
	}
}

// TestP2312_C6_HelloPlaintextRegisters — the 1/22 control, UPDATED BY P23-14
// (b): with the E2E posture on, a plaintext TEXT hello is no longer
// registered — it is refused with the typed E2E_KEY_REQUIRED code and the
// socket closes. That refusal IS the fix: "not encrypted" must not read as
// "probably plaintext". (At the pre-P23-14 HEAD this test asserted a
// registration; that behaviour was the silent-downgrade defect this unit
// closes, and the assertion flips with it.)
func TestP2312_C6_HelloPlaintextRegisters(t *testing.T) {
	_, wsURL := p2312StartWSServer(t)

	var logBuf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prev)

	conn := p2312Dial(t, wsURL)

	if err := conn.WriteJSON(p2312AgentInfoEnvelope(t, "p2312-plain-agent")); err != nil {
		t.Fatalf("first-frame write: %v", err)
	}

	p2312WaitForLog(t, &logBuf, "E2E_KEY_REQUIRED")
	if s := logBuf.String(); strings.Contains(s, "agent connected: p2312-plain-agent") {
		t.Fatalf("plaintext agent registered on an e2e-active server — silent downgrade: %s", s)
	}
}

// TestP2312_C6_RelayFrameStillRegisters — the patch must not move the relay
// path: a genuine BinaryMessage relay_register frame (channel 0, plaintext
// JSON — relay framing is NOT E2E-sealed) fails GCM auth and falls through to
// handleRelayConnection, which registers the relay as before.
func TestP2312_C6_RelayFrameStillRegisters(t *testing.T) {
	_, wsURL := p2312StartWSServer(t)

	var logBuf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prev)

	conn := p2312Dial(t, wsURL)

	reg := struct {
		Type    string `json:"type"`
		RelayID string `json:"relay_id"`
		Token   string `json:"token"`
	}{
		Type:    "relay_register",
		RelayID: "p2312-relay-1",
		Token:   "test-token",
	}
	frame := make([]byte, 5)
	frame[0] = 0x7B // magic byte (parseRelayFrame only reads [1:5] for the id)
	// channel id 0 is big-endian zero in frame[1:5]
	payload, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	frame = append(frame, payload...)
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatalf("relay register write: %v", err)
	}

	p2312WaitForLog(t, &logBuf, "relay connected: id=p2312-relay-1")
	if s := logBuf.String(); strings.Contains(s, "expected relay registration") {
		t.Fatalf("genuine relay register must not hit the channel-0 guard: %s", s)
	}
}

// lineContaining returns the first line of s containing needle ("" if none).
func lineContaining(s, needle string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}
