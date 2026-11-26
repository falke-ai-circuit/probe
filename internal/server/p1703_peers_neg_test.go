package server

// P17-03 — NEGATIVE battery for the peer backend (P17-02).
//
// Research leg R17-02-SPECBIND: binding spec P17-01_PEER_DESIGN.md @ 9425b65
// (§6 credentials, §4 state machine, §7 audit); implementation under test is
// P17-02 at HEAD 9c8d571. The four tests invert / extend the P17-02 positive
// battery (p1702_peers_test.go) along the axes P17-02 did not cover:
//
//   TestP1703TokenNeverInGetBodies  — §6 inversion across GET /api/v1/peers,
//       GET /api/v1/peers/{id} and the error paths (PEER_NOT_FOUND 404,
//       BAD_REQUEST 400) — captured RAW bodies, scanned for the token and its
//       fp-alike shapes, across three upstream phases (live/listen-refusing/
//       registering-then-refusing). The §7.1 boundary-redactor test already
//       lives in P17-02 (TestP1702AuditRedaction); the audit side is asserted
//       HERE against the on-disk JSONL so the whole §6 "never re-appears"
//       discipline is read back from DISK, not from process memory.
//
//   TestP1703AuthRejectedIsState    — §4: a registration refused (wrong
//       credential at the upstream) is a STATE, not a crash: auth-rejected,
//       no runtime left behind, no auto-retry, and every later status read
//       still answers 200.
//
//   TestP1703RetryOnUpstreamDeath   — §4: connected -> retrying on link
//       death, and the retry loop keeps DIALING (bounded backoff, 1s exponential
//       cap 30s), never parking in a terminal state.
//
//   TestP1703RestartSurvival        — §5: a peers config written through the
//       API survives a server restart (in-process restart pattern, as the
//       P17-02 battery uses), bootstrap unseals the CredentialStore-sealed
//       token (never a hash — a hash could not be presented upstream) and the
//       peer re-reaches connected against a live upstream.
//
// Simulated upstreams are used where the P17-02 helpers cannot express the
// negative case: a plain net/http handler that accepts the Bearer credential
// (like the real gate) and then answers the relay_register frame with the
// SAME typed forward_refused envelope refuseRelayRegistration emits, or with
// silence (hold-open), or with nothing at all (401 before upgrade).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Simulated upstreams
// ---------------------------------------------------------------------------

// p1703Upstreams bundles the three simulated upstream phases the battery
// wires one at a time. url is always re-derived from the CURRENT listener so
// a re-arrangement is a live inspection of its phase.
type p1703Upstreams struct {
	mu       sync.Mutex
	srv      *httptest.Server // current phase listener
	url      string           // ws:// URL of srv's /ws
	upgrades int              // socket upgrades observed across ALL phases
	refused  int              // registrations answered with a typed refusal
	rejects  int              // 401-before-upgrade rejections
	tok      string           // the credential phase handlers accept
}

func (u *p1703Upstreams) set(srv *httptest.Server) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.srv = srv
	u.url = "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
}

func (u *p1703Upstreams) wsURL() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.url
}

func (u *p1703Upstreams) count(kind string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch kind {
	case "upgrade":
		u.upgrades++
	case "refuse":
		u.refused++
	case "reject":
		u.rejects++
	}
}

func (u *p1703Upstreams) counts() (up, ref, rej int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.upgrades, u.refused, u.rejects
}

// p1703StartUpstreams returns the harness with the LISTEN-REFUSING phase
// wired (401 before upgrade — an upstream that holds a different shared
// credential than the peer presents). Cleanup closes whatever phase is live.
func p1703StartUpstreams(t *testing.T, cred string) *p1703Upstreams {
	t.Helper()
	u := &p1703Upstreams{tok: cred}
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		u.count("upgrade")
		u.count("reject")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	u.set(srv)
	return u
}

// p1703WireRegisteringThenRefusingSW answers every registration with the
// typed forward_refused envelope forward_policy.go's refuseRelayRegistration
// writes (same channel-0 framing, same code family). The Bearer gate passes —
// this is a refusal AT registration, not an upgrade rejection, which is what
// the §4 auth-rejected state is DEFINED on (spec P17-01 §4 table).
func (u *p1703Upstreams) wireRegisteringThenRefusing(t *testing.T, code, reason string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+u.tok {
			u.count("reject")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		up := &websocket.Upgrader{ReadBufferSize: 65536, WriteBufferSize: 65536,
			CheckOrigin: func(_ *http.Request) bool { return true }}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		u.count("upgrade")
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if len(data) < 5 {
			return
		}
		magic := data[0]
		payload, _ := json.Marshal(map[string]string{
			"type":   "forward_refused",
			"code":   code,
			"reason": reason,
		})
		frame := append([]byte{magic, 0, 0, 0, 0}, payload...)
		_ = conn.WriteMessage(websocket.BinaryMessage, frame)
		u.count("refuse")
		time.Sleep(100 * time.Millisecond)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	u.rewire(t, mux, "registering-then-refusing")
}

// p1703WireRegisteringThenHold accepts the socket, reads the relay_register
// and answers NOTHING (the relay's registration deadline expires). Every
// expiry is one completed ATTEMPT — the observable for "keeps attempting".
func (u *p1703Upstreams) wireRegisteringThenHold(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+u.tok {
			u.count("reject")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		up := &websocket.Upgrader{ReadBufferSize: 65536, WriteBufferSize: 65536,
			CheckOrigin: func(_ *http.Request) bool { return true }}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		u.count("upgrade")
		_, _, _ = conn.ReadMessage() // relay_register arrives; we never answer
		time.Sleep(60 * time.Second)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	u.rewire(t, mux, "registering-then-hold")
}

// p1703WireDead listens and answers nothing — the dial itself fails (connection refused after close semantics, or accept-then-nothing).
func (u *p1703Upstreams) wireDead(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, _ *http.Request) {
		u.count("reject")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	u.rewire(t, mux, "dead")
}

func (u *p1703Upstreams) rewire(t *testing.T, mux *http.ServeMux, phase string) {
	t.Helper()
	srv := httptest.NewServer(mux)
	u.set(srv)
	t.Logf("P1703-PHASE upstream -> %s at %s", phase, srv.URL)
}

// ---------------------------------------------------------------------------
// Peer-server helper (dialing side)
// ---------------------------------------------------------------------------

func p1703PeersServer(t *testing.T, up *p1703Upstreams, id string) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	t.Setenv("PROBE_SERVER_NAME", "p1703-dialer")
	srv, cleanup := newV1TestServer(t)
	srv.addr = "localhost:7700"
	srv.SetVersion("v1.20.0-p1702")
	body := fmt.Sprintf(`{"id":%q,"upstream":%q,"token":%q,"enabled":true,"max_agents":8}`,
		id, up.wsURL(), up.tok)
	req := httptest.NewRequest("POST", "/api/v1/peers", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/peers: status %d body %s", rec.Code, rec.Body.String())
	}
	return srv, cleanup
}

func p1703AwaitState(t *testing.T, srv *Server, id, want string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest("GET", "/api/v1/peers/"+id, nil)
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			var env struct {
				OK   bool           `json:"ok"`
				Data map[string]any `json:"data"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &env) == nil {
				if st, _ := env.Data["link_state"].(string); st == want {
					return env.Data
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Acceptance 1 — GET-body token inversion + on-disk audit discipline (§6/§7)
// ---------------------------------------------------------------------------

// TestP1703TokenNeverInGetBodies drives the peer across three upstream phases
// (healthy scenery: live, listen-refusing, registering-then-refusing), raw-
// captures every GET peers / GET peers/{id} / status-error body plus the
// peer.* audit lines from DISK, and demands the token never appears — the
// plaintext AND the fp-shaped digests of it (a leak hides in digests too).
func TestP1703TokenNeverInGetBodies(t *testing.T) {
	const tok = "p1703-sup3r-secret-token-9f2e6d4c"
	const tok2 = "p1703-second-secret-f00df00d"
	up := p1703StartUpstreams(t, "test-token")
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	t.Setenv("PROBE_SERVER_NAME", "p1703-dialer")

	// The audit sink is attached BEFORE the first POST so peer.created rides
	// the same JSONL the §7 boundary redactor owns.
	auditPath := filepath.Join(dir, "audit.jsonl")

	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.addr = "localhost:7700"
	srv.SetVersion("v1.20.0-p1702")
	srv.SetAuditPath(auditPath)

	// Main peer (the one whose credential this test hunts for) + a disabled
	// sidecar so the LIST view is never a single-element degenerate case.
	post := func(id, token string, enabled bool) {
		t.Helper()
		body := fmt.Sprintf(`{"id":%q,"upstream":%q,"token":%q,"enabled":%t}`,
			id, up.wsURL(), token, enabled)
		req := httptest.NewRequest("POST", "/api/v1/peers", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST peer %s: %d %s", id, rec.Code, rec.Body.String())
		}
	}
	post("p1703-sec-peer", tok, true)
	post("p1703-sidecar", "test-token", false)

	if p1703AwaitState(t, srv, "p1703-sec-peer", peerStateRetrying, 10*time.Second) == nil {
		t.Fatalf("peer never entered the runtime (list/detail below would be config-only)")
	}

	// p1703Body: one capture sink for every responding GET/status shape.
	bodies := map[string]string{}
	add := func(label, method, path, body string) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		bodies[label] = fmt.Sprintf("HTTP %d | %s", rec.Code, rec.Body.String())
	}

	detail := func(label string) { add(label, "GET", "/api/v1/peers/p1703-sec-peer", "") }

	retry := func() bool {
		return p1703AwaitState(t, srv, "p1703-sec-peer", peerStateRetrying, 5*time.Second) != nil
	}

	// PHASE A — upstream live (connection accepted, registration refused).
	if !retry() {
		t.Fatalf("phase A: peer not in runtime")
	}
	add("A list", "GET", "/api/v1/peers", "")
	detail("A detail")
	add("A detail absent", "GET", "/api/v1/peers/p1703-ghost", "")
	add("A bad action", "POST", "/api/v1/peers/p1703-sec-peer/action", `{"action":"p1703-nope"}`)
	add("A bad create", "POST", "/api/v1/peers", `{"id":"p1703 bad id!","upstream":"`+up.wsURL()+`","token":"`+tok+`"}`)

	// PHASE B — upstream dead (dial refused): error-path capture on a dead
	// transport, where an error-echoing handler would leak the credential.
	up.srv.CloseClientConnections()
	up.srv.Close()
	time.Sleep(300 * time.Millisecond)
	add("B list dead", "GET", "/api/v1/peers", "")
	detail("B detail dead")

	// Rotation through the API mid-test: the fp moves; the plaintexts must
	// still never surface in either GET family.
	putReq := httptest.NewRequest("PUT", "/api/v1/peers/p1703-sec-peer",
		strings.NewReader(`{"upstream":"`+up.wsURL()+`","token":"`+tok2+`"}`))
	putRec := httptest.NewRecorder()
	srv.mux.ServeHTTP(putRec, putReq) // httptest.NewRequest/Recorder call order kept explicit: ServeHTTP(req, rec)
	if putRec.Code != http.StatusOK {
		t.Fatalf("rotation PUT: %d %s", putRec.Code, putRec.Body.String())
	}
	if !retry() {
		t.Fatalf("phase B: lost runtime after rotation")
	}
	add("B list rotated", "GET", "/api/v1/peers", "")
	detail("B detail rotated")

	// PHASE C — a REGISTRING upstream that refuses the credential on the
	// wire (typed path): bodies captured while the machine moves states.
	up.wireRegisteringThenRefusing(t, "RELAY_CREDENTIAL_DENIED",
		"credential presented for relay p1703-sec-peer does not match its bound identity (token_fp mismatch)")
	if !retry() {
		t.Fatalf("phase C: peer not in runtime after rewire")
	}
	add("C list", "GET", "/api/v1/peers", "")
	detail("C detail")

	// ---- the inversion, over every captured body ----
	fps := map[string]bool{
		tokenFingerprint(tok):  true,
		tokenFingerprint(tok2): true,
	}
	var hits []string
	for label, b := range bodies {
		if strings.Contains(b, tok) || strings.Contains(b, tok2) {
			hits = append(hits, fmt.Sprintf("%s carries a plaintext", label))
		}
		for fp := range fps {
			// token_fp is the PUBLISHED fingerprint (§6) — its PRESENCE is
			// the contract. What must never appear: any EIGHT-HEX-shaped
			// secret digest in a place token_fp does not belong… which is
			// every other field. Assert: an fp-alike only ever rides the
			// "token_fp" key.
			for _, m := range regexp.MustCompile(`"([^"]*)"\s*:\s*"[0-9a-f]{8}"`).FindAllStringSubmatch(b, -1) {
				if m[1] != "token_fp" && fp == m[2] {
					hits = append(hits, fmt.Sprintf("%s carries secret digest under key %q", label, m[1]))
				}
			}
		}
	}
	if len(hits) > 0 {
		t.Fatalf("token material surfaced in GET bodies (§6 violation): %v\n\nRAW CAPTURED BODIES:\n%s",
			hits, p1703DumpBodies(bodies))
	}
	t.Logf("P1703-BODIES-OK %d captured bodies, zero token/fp-alike occurrences:\n%s",
		len(bodies), p1703DumpBodies(bodies))

	// ---- audit side (§7), read back from DISK ----
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("read audit jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var peerLines []string
	for _, ln := range lines {
		if strings.Contains(ln, `"action":"peer.`) || strings.Contains(ln, `peer.`) {
			// unescape the JSONL: the boundary writes params with the token
			// already redacted; an UNREDACTED token would arrive quoted.
			unq, _ := regexp.Compile(`\\u[0-9a-fA-F]{4}`)
			_ = unq // (JSONL writes plain UTF-8; the quoted-form sweep below
			//       additionally covers any escaping a future boundary adds)
			if strings.Contains(ln, `"action":"peer.`) {
				peerLines = append(peerLines, ln)
				if strings.Contains(ln, tok) || strings.Contains(ln, tok2) ||
					strings.Contains(ln, strings.ReplaceAll(tok, `"`, `\"`)) {
					t.Fatalf("audit peer.* line carries a plaintext token (§7.1): %s", ln)
				}
			}
		}
	}
	if len(peerLines) == 0 {
		t.Fatalf("audit jsonl carried no peer.* entries — the §7 family never fired")
	}
	t.Logf("P1703-AUDIT-OK %d peer.* lines scanned from disk, zero token occurrences", len(peerLines))
}

func p1703DumpBodies(bodies map[string]string) string {
	keys := make([]string, 0, len(bodies))
	for k := range bodies {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for _, k := range keys {
		out += fmt.Sprintf("  [%s] %s\n", k, bodies[k])
	}
	return out
}

// ---------------------------------------------------------------------------
// Acceptance 2 — auth-rejected is a STATE, not a crash (§4)
// ---------------------------------------------------------------------------

// TestP1703AuthRejectedIsState drives a peer against an upstream that refuses
// the registration with the typed credential refusal. Demands: the peer row
// reads auth-rejected (terminal per §4), the lifecycle goroutine EXITED (no
// retry ever), the runtime holds no live uplink, and later status reads stay
// healthy (200, machine-shaped — no 500, no crash).
func TestP1703AuthRejectedIsState(t *testing.T) {
	up := p1703StartUpstreams(t, "test-token")
	const id = "p1703-auth-peer"
	up.wireRegisteringThenRefusing(t, "RELAY_CREDENTIAL_DENIED",
		"credential presented for relay "+id+" does not match its bound identity (token_fp mismatch)")
	srv, cleanup := p1703PeersServer(t, up, "p1703-auth-peer")
	defer cleanup()

	// Terminal per §4: entered from retrying after one attempt, never a
	// crash, never a bare disconnect-loops-forever.
	if p1703AwaitState(t, srv, id, peerStateAuthRejected, 10*time.Second) == nil {
		req := httptest.NewRequest("GET", "/api/v1/peers/"+id, nil)
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		t.Fatalf("peer never reached auth-rejected. last row: HTTP %d %s", rec.Code, rec.Body.String())
	}

	// The machine's detail field carries the refusal (§4 operator contract).
	req := httptest.NewRequest("GET", "/api/v1/peers/"+id, nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	t.Logf("P1703-AUTH peer row: HTTP %d %s", rec.Code, rec.Body.String())
	var env struct {
		Data struct {
			LinkState   string          `json:"link_state"`
			StateDetail linkStateDetail `json:"state_detail"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status read after terminal state: %d %v", rec.Code, err)
	}
	if env.Data.StateDetail.RefusalCode == "" {
		t.Fatalf("auth-rejected without a refusal code in state_detail: %s", rec.Body.String())
	}

	// NO auto-retry (§4: terminal means hopeless): the lifecycle loop has
	// RETURNED — the runtime record survives (single-owner map), but there
	// is no live uplink and none comes back after a quiet window.
	pm := srv.peerManagerFor()
	pm.mu.Lock()
	rt := pm.peers[id]
	rel := rt.relay
	pm.mu.Unlock()
	if rel == nil {
		// A relay object is attached before the first dial; its absence
		// would mean the register/spawn path never ran at all.
		t.Fatalf("runtime never built its relay (state machine bypassed)")
	}
	if rel.UplinkConnected() {
		t.Fatalf("terminal auth-rejected but the uplink reports connected (§4 violation)")
	}
	time.Sleep(3500 * time.Millisecond)
	if p1703AwaitState(t, srv, id, peerStateAuthRejected, 1500*time.Millisecond) == nil {
		t.Fatalf("state moved OFF auth-rejected within the quiet window — a retry survived (§4 violation)")
	}
	pm.mu.Lock()
	rt = pm.peers[id]
	relNow := rt.relay
	pm.mu.Unlock()
	if relNow == nil {
		t.Fatalf("runtime record lost after terminal state (map ownership violation)")
	}
	if relNow.UplinkConnected() {
		t.Fatalf("a retry re-established the uplink after auth-rejected (§4 no-retry violation)")
	}
	_, ref, _ := up.counts()
	if ref != 1 {
		t.Fatalf("upstream observed %d refusal rounds — the peer dialed again (§4 no-retry violation)", ref)
	}

	// And every later status read stays healthy: 200, machine-shaped, no 500.
	for i := 0; i < 3; i++ {
		// one round of list+detail, per §4's "status read does not 500".
		rec2 := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec2, httptest.NewRequest("GET", "/api/v1/peers", nil))
		rec3 := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec3, httptest.NewRequest("GET", "/api/v1/peers/"+id, nil))
		if rec2.Code != http.StatusOK {
			t.Fatalf("GET list after terminal: %d %s", rec2.Code, rec2.Body.String())
		}
		if rec3.Code != http.StatusOK {
			t.Fatalf("GET detail after terminal: %d %s (%v rounds)", rec3.Code, rec3.Body.String(), i)
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("P1703-AUTH-OK auth-rejected held; %d refusal rounds; all status reads 200", ref)
}

// ---------------------------------------------------------------------------
// Acceptance 3 — upstream death -> retrying, bounded, keeps attempting (§4)
// ---------------------------------------------------------------------------

// TestP1703RetryOnUpstreamDeath connects for real (the P17-02 REAL-upstream
// helper — the link-death kill relies on the upstream's relayMu registry,
// which a simulated upstream has no equivalent of), kills the link (the real
// socket death), asserts connected -> retrying, and proves the loop KEEPS
// DIALING: the config is re-aimed via PUT (FM-1.3 live apply) at a
// registering-then-hold upstream whose every accepted socket is one counted
// ATTEMPT (the relay's 5s registration deadline expiry completes it); >=2
// attempts inside the window is the keep-attempting proof. Throughout, the
// state must stay inside §4's retrying/connected readings — never terminal.
func TestP1703RetryOnUpstreamDeath(t *testing.T) {
	up := p1702StartUpstream(t)
	srv, cleanup := p1702PeersServer(t, up, "p1703-retry-peer")
	defer cleanup()

	if p1703AwaitState(t, srv, "p1703-retry-peer", peerStateConnected, 10*time.Second) == nil {
		t.Fatalf("peer never connected against the live upstream (§4 precondition)")
	}

	// Kill the link the way the real death looks: close the hijacked
	// upstream session (the P17-02 disconnect test's finding — http.Server
	// Close never closes hijacked conns) and then the listener.
	connClosed := false
	for i := 0; i < 40 && !connClosed; i++ {
		up.s.relayMu.RLock()
		for _, sess := range up.s.relays {
			if sess.conn != nil {
				_ = sess.conn.Close()
				connClosed = true
			}
		}
		up.s.relayMu.RUnlock()
		time.Sleep(50 * time.Millisecond)
	}
	up.srv.CloseClientConnections()
	up.srv.Close()

	if p1703AwaitState(t, srv, "p1703-retry-peer", peerStateRetrying, 15*time.Second) == nil {
		t.Fatalf("peer stayed connected after upstream death (§4 non-forgery violation)")
	}
	t.Logf("P1703-RETRY connected -> retrying after real link death")

	// Keep-attempting proof against a DEAD upstream for a window long enough
	// to cover >=2 backoff doublings of §4's floor (1s, 2s): every state
	// read healthy, every reading inside the closed §4 vocabulary, NEVER
	// terminal (transport failure is retryable by definition).
	deadline := time.Now().Add(9 * time.Second)
	saw200 := 0
	for time.Now().Before(deadline) {
		req2 := httptest.NewRequest("GET", "/api/v1/peers/p1703-retry-peer", nil)
		rec2 := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Fatalf("state read during retry storm: HTTP %d %s", rec2.Code, rec2.Body.String())
		}
		saw200++
		var env struct {
			Data struct {
				LinkState string `json:"link_state"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec2.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode during retry: %v", err)
		}
		switch env.Data.LinkState {
		case peerStateRetrying, peerStateConnected:
			// both are §4-legal mid-retry readings (a dial is in flight)
		case peerStateAuthRejected, peerStateVersionMismatch:
			t.Fatalf("transport death drifted the peer into terminal %q (§4: transport failure is retryable)", env.Data.LinkState)
		default:
			t.Fatalf("link_state %q outside the §4 retry vocabulary mid-death", env.Data.LinkState)
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("P1703-RETRY-OK %d healthy state reads across the dead-upstream window, never terminal", saw200)

	// Positive keep-attempting proof: re-aim the peer (PUT = live apply) at
	// a registering-then-hold upstream. Every socket the hold upstream
	// accepts and leaves unanswered is one ATTEMPT the relay completed
	// (registration deadline 5s + §4 backoff); >=2 upgrades is the
	// at-least-two-attempts proof.
	putReq := httptest.NewRequest("PUT", "/api/v1/peers/p1703-retry-peer",
		strings.NewReader(`{"upstream":"`+p1703HoldURL(t, "test-token")+`","token":"test-token"}`))
	putRec := httptest.NewRecorder()
	srv.mux.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("re-aim PUT: %d %s", putRec.Code, putRec.Body.String())
	}
	time.Sleep(16 * time.Second)
	hu, _, _ := p1703HoldCounter()
	if hu < 2 {
		t.Fatalf("hold upstream saw %d upgrades in the attempt window — the peer is NOT re-dialing (§4 retry violation)", hu)
	}
	// …and still NEVER terminal (no refusal arrived; transport death only).
	req3 := httptest.NewRequest("GET", "/api/v1/peers/p1703-retry-peer", nil)
	rec3 := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec3, req3)
	var env2 struct {
		Data struct {
			LinkState string `json:"link_state"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec3.Body.Bytes(), &env2)
	if env2.Data.LinkState == peerStateAuthRejected || env2.Data.LinkState == peerStateVersionMismatch {
		t.Fatalf("peer parked in terminal %q without a refusal (§4 violation)", env2.Data.LinkState)
	}
	t.Logf("P1703-ATTEMPTS hold-upstream accepted %d registration attempts; state=%q (not terminal)", hu, env2.Data.LinkState)
}

// p1703HoldUp tracks the one-shot registering-then-hold upstream the retry
// test aims a config PUT at (its upgrade counter gives the attempt count).
var p1703HoldUp struct {
	mu  sync.Mutex
	srv *httptest.Server
	url string
	n   int
}

func p1703HoldURL(t *testing.T, cred string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+cred {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		up := &websocket.Upgrader{ReadBufferSize: 65536, WriteBufferSize: 65536,
			CheckOrigin: func(_ *http.Request) bool { return true }}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		p1703HoldUp.mu.Lock()
		p1703HoldUp.n++
		p1703HoldUp.mu.Unlock()
		_, _, _ = conn.ReadMessage()
		time.Sleep(120 * time.Second)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	p1703HoldUp.mu.Lock()
	p1703HoldUp.srv = srv
	p1703HoldUp.url = "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	p1703HoldUp.mu.Unlock()
	return p1703HoldUp.url
}

func p1703HoldCounter() (int, int, int) {
	p1703HoldUp.mu.Lock()
	defer p1703HoldUp.mu.Unlock()
	return p1703HoldUp.n, 0, 0
}

// ---------------------------------------------------------------------------
// Acceptance 4 — restart survival: unseal + reconnect (§5/§6)
// ---------------------------------------------------------------------------

// TestP1703RestartSurvival writes the peers config through the API (live
// POST against a REAL relay upstream from the P17-02 helpers), restarts the
// server in-process (the existing battery's pattern: the config document and
// the credential store live in the SAME temp dir across the restart), and
// demands: bootstrap re-spawns the configured peer, unseals the AES-sealed
// token from the CredentialStore (a HASH could not be presented upstream),
// and the peer reaches connected again — with the SAME token fingerprint. A
// restart that lost the plaintext (or stored only a hash) would show up here
// as auth-rejected, not connected.
func TestP1703RestartSurvival(t *testing.T) {
	up := p1702StartUpstream(t)
	const id = "p1703-survivor"
	// The peer token IS the upstream's shared auth token (P17-02 topology:
	// the relay presents it as the WS Bearer credential AND inside
	// relay_register — a custom value would be rejected at the Bearer gate
	// before registration, which is acceptance-2's scenario, not this one).
	const tok = "test-token"
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	t.Setenv("PROBE_SERVER_NAME", "p1703-dialer-before")

	// ---- SERVER PROCESS 1: config through the API ----
	srv1, cleanup1 := newV1TestServer(t)
	srv1.addr = "localhost:7700"
	srv1.SetVersion("v1.20.0-p1702")
	createBody := fmt.Sprintf(`{"id":%q,"upstream":%q,"token":%q,"enabled":true,"max_agents":8}`,
		id, up.url, tok)
	req := httptest.NewRequest("POST", "/api/v1/peers", strings.NewReader(createBody))
	rec := httptest.NewRecorder()
	srv1.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/peers: %d %s", rec.Code, rec.Body.String())
	}
	if p1703AwaitState(t, srv1, id, peerStateConnected, 10*time.Second) == nil {
		t.Fatalf("pre-restart: peer never connected (§5 precondition)")
	}

	// The fp before restart, read from the API (§6 shape).
	preFP := p1703TokenFP(t, srv1, id)
	if preFP == "" || preFP != tokenFingerprint(tok) {
		t.Fatalf("pre-restart token_fp %q is not the published fingerprint shape", preFP)
	}

	// On disk: the config survives as a SEALED blob (never plaintext, never
	// a hash — the hash family is rejected by design, §6).
	docRaw, err := os.ReadFile(filepath.Join(dir, "servers.json"))
	if err != nil {
		t.Fatalf("read servers.json: %v", err)
	}
	if !strings.Contains(string(docRaw), `"token_sealed"`) {
		t.Fatalf("servers.json carries no token_sealed field: %s", string(docRaw))
	}
	if strings.Contains(string(docRaw), tok) {
		t.Fatalf("servers.json carries the PLAINTEXT token (§6 violation): %s", string(docRaw))
	}
	t.Logf("P1703-DISK sealed at rest, plaintext absent (%d bytes)", len(docRaw))

	// ---- RESTART: stop process-1's runtime, leave the DOC + credential
	// store on disk, construct process-2 from the same environment ----
	up.s.relayMu.RLock()
	for _, sess := range up.s.relays {
		_ = sess.conn.Close()
	}
	up.s.relayMu.RUnlock()
	pm1 := srv1.peerManagerFor()
	pm1.mu.Lock()
	ids := make([]string, 0, len(pm1.peers))
	for pid := range pm1.peers {
		ids = append(ids, pid)
	}
	pm1.mu.Unlock()
	for _, pid := range ids {
		pm1.stopRuntime(pid)
	}
	cleanup1()
	t.Logf("P1703-RESTART process-1 stopped; doc + sealed credential persist")

	// ---- SERVER PROCESS 2: bootstrap only — NO re-POST, the doc is the truth ----
	t.Setenv("PROBE_SERVER_NAME", "p1703-dialer-after")
	srv2, cleanup2 := newV1TestServer(t)
	defer cleanup2()
	srv2.addr = "localhost:7711"
	srv2.SetVersion("v1.20.0-p1702")
	srv2.bootstrapPeers() // §5: load doc, staggered spawns, seal-check

	// The peer row must BOOTSTRAP back from the doc (no API write in
	// process 2 — persistence inversion, not a re-create).
	listRec := httptest.NewRecorder()
	srv2.mux.ServeHTTP(listRec, httptest.NewRequest("GET", "/api/v1/peers", nil))
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), id) {
		t.Fatalf("post-restart bootstrap lost the peer: %d %s", listRec.Code, listRec.Body.String())
	}

	// §5.2: restart ALWAYS spawns as retrying, never as connected (the row
	// must not claim a link it has not formed). Read it before the link
	// forms — a strictly-connected-on-read would itself be the defect.
	spawnState := ""
	{
		req2 := httptest.NewRequest("GET", "/api/v1/peers/"+id, nil)
		rec2 := httptest.NewRecorder()
		srv2.mux.ServeHTTP(rec2, req2)
		var env struct {
			Data map[string]any `json:"data"`
		}
		if json.Unmarshal(rec2.Body.Bytes(), &env) == nil {
			spawnState, _ = env.Data["link_state"].(string)
		}
	}
	t.Logf("P1703-SPAWN post-bootstrap link_state=%q (§5.2: spawn is retrying/empty, never connected)", spawnState)
	if spawnState == peerStateConnected {
		t.Fatalf("post-restart bootstrap presented the peer as connected before peer_hello (§4 non-forgery)")
	}

	// Connected again — which PROVES the unseal: the upstream validates the
	// RAW credential (bindRelayIdentity). A hash or sealed blob presented
	// upstream would be refused (identity adopted on the shared token in
	// process 1; a mismatching presentation lands RELAY_CREDENTIAL_DENIED
	// -> auth-rejected). connected after restart == unseal succeeded.
	if p1703AwaitState(t, srv2, id, peerStateConnected, 15*time.Second) == nil {
		req3 := httptest.NewRequest("GET", "/api/v1/peers/"+id, nil)
		rec3 := httptest.NewRecorder()
		srv2.mux.ServeHTTP(rec3, req3)
		t.Fatalf("post-restart peer never re-connected (unseal/re-dial defect). last row: HTTP %d %s",
			rec3.Code, rec3.Body.String())
	}
	postFP := p1703TokenFP(t, srv2, id)
	if postFP != preFP {
		t.Fatalf("token_fp drifted across restart: %q -> %q (credential store key derivation is not stable)", preFP, postFP)
	}

	// §6: the same inversion on process 2's responses — no plaintext.
	listRec2 := httptest.NewRecorder()
	srv2.mux.ServeHTTP(listRec2, httptest.NewRequest("GET", "/api/v1/peers", nil))
	if strings.Contains(listRec2.Body.String(), tok) {
		t.Fatalf("post-restart GET list carries the plaintext token (§6 violation)")
	}
	t.Logf("P1703-SURVIVE-OK bootstrap unsealed + reconnected; token_fp stable across restart (%s)", postFP)
}

func p1703TokenFP(t *testing.T, srv *Server, id string) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/peers/"+id, nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return ""
	}
	var env struct {
		Data struct {
			TokenFP string `json:"token_fp"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		return ""
	}
	return env.Data.TokenFP
}
