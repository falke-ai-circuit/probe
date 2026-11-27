package relay

// P22-05 (F5) — a policy refusal survives the hop, and is not retried.
//
// The measurement (P23-00 §T5.3) pinned the defect: the upstream server
// sends a typed forward_refused frame naming FORWARD_ALLOWLIST_DENIED, but
// the relay ignored it, logged `websocket: close 1006 (abnormal closure)`,
// and retried forever. A refusal that can never succeed was retried forever.
// These tests pin the fix at the wire level:
//
//   - the refused relay READS the forward_refused frame, and its log and
//     status document name the code the upstream sent (the P22-03 class);
//   - no reconnect attempts follow a policy refusal inside a fixed window;
//   - a transport kill (server gone mid-session) still retries — proving
//     the terminal classification did not swallow transport failures.
//
// Everything is loopback: the upstream is a real HTTP+WebSocket server here,
// the relay is the real New/Run path.

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var p2205Upgrader = &websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

// p2205Upstream serves one upstream: reads relay_register frames, counts them,
// and answers each via respond (payload to send, keep the socket open).
type p2205Upstream struct {
	regs int32
	srv  *httptest.Server
}

func p2205StartUpstream(t *testing.T, respond func(magic byte, reg ControlMessage) ([]byte, bool)) *p2205Upstream {
	t.Helper()
	up := &p2205Upstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := p2205Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			magic, chanID, payload, perr := ParseFrame(data)
			if perr != nil || chanID != 0 {
				continue
			}
			var ctrl ControlMessage
			if json.Unmarshal(payload, &ctrl) != nil || ctrl.Type != "relay_register" {
				continue
			}
			atomic.AddInt32(&up.regs, 1)
			answ, keep := respond(magic, ctrl)
			if answ != nil {
				if err := conn.WriteMessage(websocket.BinaryMessage, MakeFrame(magic, 0, answ)); err != nil {
					return
				}
			}
			if !keep {
				// Hold the socket briefly so the refusal frame is readable,
				// then drop it — the close that used to be the peer's
				// entire answer (defect shape: close 1006).
				conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				_, _, _ = conn.ReadMessage()
				return
			}
		}
	})
	up.srv = httptest.NewServer(mux)
	t.Cleanup(up.srv.Close)
	return up
}

// p2205Refuse answers every registration with one forward_refused.
func p2205Refuse(code string) func(byte, ControlMessage) ([]byte, bool) {
	return func(magic byte, reg ControlMessage) ([]byte, bool) {
		reason := fmt.Sprintf("%s: relay %s refused by test upstream", code, reg.RelayID)
		answ, _ := json.Marshal(ControlMessage{
			Type:          "forward_refused",
			RefusalCode:   code,
			RefusalReason: reason,
		})
		return answ, false
	}
}

// p2205Accept answers the registration with peer_hello and keeps the socket.
func p2205Accept(magic byte, reg ControlMessage) ([]byte, bool) {
	hello, _ := json.Marshal(ControlMessage{
		Type: "peer_hello",
		Peer: &PeerHello{ID: "srv-p2205", Name: "test upstream", Version: "test"},
	})
	return hello, true
}

// p2205CaptureLog points the std logger at a temp file and returns its path.
func p2205CaptureLog(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "relaylog-*.txt")
	if err != nil {
		t.Fatalf("temp log file: %v", err)
	}
	log.SetOutput(f)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		f.Close()
	})
	return f.Name()
}

// p2205WaitLog returns the log's content once it contains want, or its
// current content at the deadline (so the failure message shows what the
// relay actually logged).
func p2205WaitLog(t *testing.T, path, want string, wait time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(wait)
	var last []byte
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			last = b
			if strings.Contains(string(last), want) {
				return string(last)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return string(last)
}

// p2205StartRelay runs the real relay against one upstream server.
func p2205StartRelay(t *testing.T, up *p2205Upstream) *Relay {
	t.Helper()
	rl := p2205StartRelayAt(t, up.srv.URL)
	return rl
}

// p2205StartRelayAt runs the real relay against one upstream URL
// (http://host:port form from an httptest.Server or a raw listener).
func p2205StartRelayAt(t *testing.T, url string) *Relay {
	t.Helper()
	listen := p2103FreePort(t)
	rl := New(Config{
		UpstreamURLs: []string{"ws://" + strings.TrimPrefix(url, "http://") + "/ws"},
		ListenAddr:   listen,
		Token:        "relay-token",
		DialTimeout:  2 * time.Second,

		AllowAnyDownstream: true, // P19-01: refusal pin, not an admission pin
	})
	go func() { _ = rl.Run() }()
	t.Cleanup(rl.Stop)
	return rl
}

// TestP2205_AllowlistRefusalSeenByRefusedRelay covers acceptance 1-3: the
// refused relay names the code (log AND status document), and no retry loop
// follows the policy refusal.
func TestP2205_AllowlistRefusalSeenByRefusedRelay(t *testing.T) {
	logPath := p2205CaptureLog(t)
	const code = "FORWARD_ALLOWLIST_DENIED"
	up := p2205StartUpstream(t, p2205Refuse(code))
	rl := p2205StartRelay(t, up)

	// The log must name the code the upstream sent — not a bare close 1006.
	tail := p2205WaitLog(t, logPath, code, 5*time.Second)
	if !strings.Contains(tail, code) {
		t.Fatalf("relay log does not name %s; log was:\n%s", code, tail)
	}
	if strings.Contains(tail, "attempting reconnect") {
		t.Errorf("relay logged reconnect attempts on a policy refusal:\n%s", tail)
	}

	// The status document carries the refusal as structured data.
	var st Status
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st = rl.Status()
		if st.Upstream.Refusal != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st.Upstream.Refusal == nil {
		t.Fatalf("status.upstream.refusal never appeared; upstream.last_error=%q", st.Upstream.LastError)
	}
	if st.Upstream.Refusal.Code != code {
		t.Errorf("refusal.code: got %q, want %q", st.Upstream.Refusal.Code, code)
	}
	if st.Upstream.Refusal.Retryable {
		t.Errorf("refusal.retryable=true — a policy refusal must be terminal")
	}
	if st.Upstream.State == "up" {
		t.Errorf("upstream.state=up after a refusal")
	}

	// Acceptance 3: no retry loop follows the policy refusal. The watcher
	// wakes every 5s; 12s without a second registration proves the terminal
	// gate, not just a slow retry.
	time.Sleep(12 * time.Second)
	if n := atomic.LoadInt32(&up.regs); n != 1 {
		t.Errorf("registration attempts after a %s refusal: got %d, want exactly 1 — the refusal was retried", code, n)
	}
}

// TestP2205_TransportLossStillRetries proves the terminal classification did
// not swallow transport drops: an upstream that accepts and then drops the
// link is re-dialled and a fresh registration lands once it returns.
func TestP2205_TransportLossStillRetries(t *testing.T) {
	logPath := p2205CaptureLog(t)
	up := p2205StartUpstream(t, p2205Accept)

	// Re-serve the same handler on a FIXED listener we own, so the upstream
	// can die and come back on the address the relay retries.
	var regs atomic.Int32
	var live atomic.Pointer[websocket.Conn]
	handler := http.NewServeMux()
	handler.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := p2205Upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		live.Store(conn)
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			magic, chanID, payload, perr := ParseFrame(data)
			if perr != nil || chanID != 0 {
				continue
			}
			var ctrl ControlMessage
			if json.Unmarshal(payload, &ctrl) != nil || ctrl.Type != "relay_register" {
				continue
			}
			regs.Add(1)
			answ, _ := json.Marshal(ControlMessage{
				Type: "peer_hello",
				Peer: &PeerHello{ID: "srv-p2205", Name: "test upstream", Version: "test"},
			})
			if err := conn.WriteMessage(websocket.BinaryMessage, MakeFrame(magic, 0, answ)); err != nil {
				return
			}
		}
	})
	up.srv.Close() // the httptest wrapper is replaced below by the fixed listener

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	hs := &http.Server{Handler: handler}
	go func() { _ = hs.Serve(ln) }()
	up.srv.URL = "http://" + addr
	t.Cleanup(func() { hs.Close() })

	rl := p2205StartRelayAt(t, up.srv.URL)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if rl.upstreamOK.Load() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !rl.upstreamOK.Load() {
		t.Fatalf("relay never connected to the accepting upstream")
	}
	before := regs.Load()

	// Kill the transport mid-session: close the live WebSocket and stop the
	// listener. http.Server.Close() does not touch hijacked (WebSocket)
	// connections, so the socket is killed explicitly — the relay's read
	// error must be a transport drop, which IS retried.
	hs.Close()
	if c := live.Load(); c != nil {
		c.Close()
	}

	// A transport drop must be retried: bring the upstream back on the same
	// port before the watcher's next walk. From the relay's side the link
	// died and was re-established — exactly the path a refusal must not take.
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("relisten %s: %v", addr, err)
	}
	hs2 := &http.Server{Handler: handler}
	go func() { _ = hs2.Serve(ln2) }()
	t.Cleanup(func() { hs2.Close() })

	deadline = time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if regs.Load() > before {
			return // second registration observed: the retry happened
		}
		time.Sleep(100 * time.Millisecond)
	}
	if b, err := os.ReadFile(logPath); err == nil {
		t.Logf("relay log at failure:\n%s", string(b))
	}
	t.Fatalf("no retry after a transport drop: registrations %d -> %d over 40s", before, regs.Load())
}

// TestP2205_RefusalClassification pins the code-level rule the watcher and
// the status document both consume: every FORWARD_* refusal is terminal, and
// the classification is by wire code, not by matching an error string.
func TestP2205_RefusalClassification(t *testing.T) {
	for _, code := range []string{
		"FORWARD_ALLOWLIST_DENIED", "FORWARD_LOOP", "FORWARD_POLICY_DENIED",
		"FORWARD_MAX_AGENTS", "FORWARD_MIRROR_HAS_NO_LIVE_UPSTREAM",
	} {
		ref := &ForwardRefusal{Code: code, Reason: code + ": test"}
		if ref.Retryable() {
			t.Errorf("%s: Retryable()=true, want false — policy refusals are terminal", code)
		}
		if !IsForwardRefusalCode(code) {
			t.Errorf("%s: IsForwardRefusalCode()=false, want true", code)
		}
		if !strings.HasPrefix(ref.Error(), code) {
			t.Errorf("Error() %q does not carry the wire code %q", ref.Error(), code)
		}
	}
	if IsForwardRefusalCode("") || IsForwardRefusalCode("ECONNREFUSED") {
		t.Errorf("non-FORWARD codes must not classify as forwarding refusals")
	}
	// A transport error is not a refusal: it keeps its retry semantics.
	var transport error = fmt.Errorf("no answer to registration: %w", &websocket.CloseError{Code: websocket.CloseAbnormalClosure})
	if _, ok := transport.(*ForwardRefusal); ok {
		t.Errorf("a transport failure must not be typed as a ForwardRefusal")
	}
}
