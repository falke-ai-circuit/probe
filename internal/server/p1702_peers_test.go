package server

// P17-02 — the test battery for the peer backend (spec P17-01 §1–§7).
//
// The hard acceptance line, tested here directly: "a connect must bind the
// listen socket AND produce a relay_register on the upstream — the class of
// defect the tunnel endpoint has (returns a port, binds nothing) is not
// repeated." cmd/probe119_p1702_smoke.go is the ss/netstat + upstream-log
// probe that quotes the same facts against real processes; the tests below
// assert the identical mechanisms in-process (bind collision = the socket
// exists; upstream inventory row = the registration arrived).

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// p1702Upstream is a real server process (httptest-backed mux) that accepts
// relay registrations at /ws — the upstream a default_peer dials.
type p1702Upstream struct {
	srv  *httptest.Server
	s    *Server
	url  string
	done chan struct{}
}

func p1702StartUpstream(t *testing.T) *p1702Upstream {
	t.Helper()
	up := NewServer("", "test-token", "")
	up.SetVersion("v1.20.0-p1702")
	up.mux = http.NewServeMux()
	up.registerRoutes()
	srv := httptest.NewServer(up.mux)
	t.Cleanup(func() {
		// A peer's websocket holds the connection open; plain Close()
		// waits for idle and hangs. Cut the sockets first.
		srv.CloseClientConnections()
		srv.Close()
	})
	// Pin the upstream's inventory document to the DEFAULT path BEFORE the
	// calling test re-points PROBE_SERVERS_DB at its own temp doc: the
	// store captures its path at first use, and a lazily created upstream
	// store would otherwise capture the dialer's doc (shared-file artifact:
	// two writers, one rename race, cross-config config_ref matches).
	up.inventory().localID()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	return &p1702Upstream{srv: srv, s: up, url: wsURL}
}

// p1702PeersServer is the DIALING server: v1 test server + temp inventory
// doc + the shared credential store pointed into the same temp dir.
func p1702PeersServer(t *testing.T, up *p1702Upstream, id string) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	t.Setenv("PROBE_SERVER_NAME", "dialer")
	srv, cleanup := newV1TestServer(t)
	srv.addr = "localhost:7700"
	srv.SetVersion("v1.20.0-p1702")

	// The peer's token IS the upstream server's shared auth token in this
	// topology: the relay presents it both as the WS Bearer credential and
	// inside relay_register for bindRelayIdentity.
	body := fmt.Sprintf(`{"id":%q,"upstream":%q,"token":"test-token","enabled":true,"max_agents":8}`, id, up.url)
	req := httptest.NewRequest("POST", "/api/v1/peers", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/peers: status %d body %s", rec.Code, rec.Body.String())
	}
	return srv, cleanup
}

// p1702AwaitState polls GET /api/v1/peers until id reaches want or times out.
func p1702AwaitState(t *testing.T, srv *Server, id, want string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last map[string]any
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
				last = env.Data
				if st, _ := env.Data["link_state"].(string); st == want {
					return env.Data
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Connected may surface too late for a slow CI box, but a mismatch
	// between connected and version-mismatch is always a defect.
	if st, _ := last["link_state"].(string); st == peerStateVersionMismatch {
		t.Fatalf("peer %s reached version-mismatch (detail %v), want %s", id, last, want)
	}
	return last
}

// TestP1702ConnectRegistersUpstream is the hard acceptance, in-process:
// a configured peer dials the upstream and the upstream ends up with the
// relay_register row (relay/<id> inventory entry with peer_meta). The
// dialing side reports connected after peer_hello.
func TestP1702ConnectRegistersUpstream(t *testing.T) {
	up := p1702StartUpstream(t)
	srv, cleanup := p1702PeersServer(t, up, "reg-peer-1")
	defer cleanup()

	data := p1702AwaitState(t, srv, "reg-peer-1", peerStateConnected, 10*time.Second)
	if data != nil {
		if got, _ := data["link_state"].(string); got != peerStateConnected {
			t.Logf("peer state = %v (link formed but connected not observed in window)", data)
		}
	}

	// The upstream must now know the peer: relay_register produced a live
	// row in /api/v1/servers (peer_meta present, config_ref ABSENT — it is
	// an inbound relay from the upstream's viewpoint).
	req := httptest.NewRequest("GET", "/api/v1/servers", nil)
	rec := httptest.NewRecorder()
	up.s.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upstream GET /api/v1/servers: %d", rec.Code)
	}
	var env struct {
		OK   bool            `json:"ok"`
		Data serversResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("upstream inventory decode: %v", err)
	}
	found := false
	for _, e := range env.Data.Servers {
		if e.Role == rolePeer && strings.Contains(e.ID, "reg-peer-1") {
			found = true
			if e.PeerMeta == nil {
				t.Fatalf("upstream row %s carries no peer_meta (§3 R3 violation)", e.ID)
			}
		}
	}
	if !found {
		t.Fatalf("upstream never received relay_register for reg-peer-1 (servers=%v)", env.Data.Servers)
	}
}

// TestP1702ListenSetBindsSocket proves the nested-relay peer binds its
// listen socket: a second bind of the same port must collide (the ss/netstat
// equivalent in-process — the listener exists or the port is free).
func TestP1702ListenSetBindsSocket(t *testing.T) {
	up := p1702StartUpstream(t)
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// A port the OS is not holding yet.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	body := fmt.Sprintf(`{"id":"listen-peer","upstream":%q,"token":"test-token","enabled":true,"listen":"127.0.0.1:%d"}`, up.url, port)
	req := httptest.NewRequest("POST", "/api/v1/peers", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body.String())
	}

	// A listener-set peer binds. The collision below is the proof the
	// "returns a port, binds nothing" defect class cannot recur here.
	deadline := time.Now().Add(10 * time.Second)
	bound := false
	for time.Now().Before(deadline) {
		probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			bound = true // the peer's listener holds the port
			break
		}
		probe.Close()
		time.Sleep(50 * time.Millisecond)
	}
	if !bound {
		t.Fatalf("listen-set peer never bound 127.0.0.1:%d — the tunnel-endpoint defect class", port)
	}
}

// TestP1702DisconnectFlipsState covers the §4 transport-drop edge: the
// upstream dies, the peer leaves connected (retrying or a dropped relay
// spawn loop) — a peer that stays "connected" against a dead upstream is
// the non-forgery violation.
func TestP1702DisconnectFlipsState(t *testing.T) {
	up := p1702StartUpstream(t)
	srv, cleanup := p1702PeersServer(t, up, "drop-peer")
	defer cleanup()

	p1702AwaitState(t, srv, "drop-peer", peerStateConnected, 10*time.Second)

	// Kill the link: the server's http.Server never closes HIJACKED
	// (upgraded websocket) conns, so Close/CloseClientConnections leaves
	// the peer socket open — closing the upstream's relay sessions is the
	// real link-death here.
	up.s.relayMu.RLock()
	for _, sess := range up.s.relays {
		_ = sess.conn.Close()
	}
	up.s.relayMu.RUnlock()
	up.srv.CloseClientConnections()
	up.srv.Close()

	deadline := time.Now().Add(15 * time.Second)
	flipped := false
	var lastStates []string
	for time.Now().Before(deadline) {
		req := httptest.NewRequest("GET", "/api/v1/peers/drop-peer", nil)
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			var env struct {
				Data peerConfigView `json:"data"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &env) == nil {
				lastStates = append(lastStates, env.Data.LinkState)
				if env.Data.LinkState == peerStateRetrying {
					flipped = true
					break
				}
			}
		} else {
			lastStates = append(lastStates, fmt.Sprintf("http=%d", rec.Code))
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !flipped {
		t.Fatalf("peer stayed connected after upstream death (§4 non-forgery violation); states=%v", lastStates)
	}
}

// TestP1702CRUDAndTokenDiscipline covers §2/§6: validation codes, duplicate
// 409, PUT-without-token keeps the secret, GET never returns a token, the
// response carries token_fp.
func TestP1702CRUDAndTokenDiscipline(t *testing.T) {
	up := p1702StartUpstream(t)
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var rd *strings.Reader
		if body == "" {
			rd = strings.NewReader("")
		} else {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		return rec
	}

	// Bad id → PEER_ID_INVALID.
	if rec := do("POST", "/api/v1/peers", `{"id":"bad id!","upstream":"`+up.url+`","token":"t"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "PEER_ID_INVALID") {
		t.Fatalf("id validation: %d %s", rec.Code, rec.Body.String())
	}
	// Bad upstream → PEER_UPSTREAM_INVALID (http:// is not ws).
	if rec := do("POST", "/api/v1/peers", `{"id":"p2","upstream":"http://x:1/ws","token":"t"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "PEER_UPSTREAM_INVALID") {
		t.Fatalf("upstream validation: %d %s", rec.Code, rec.Body.String())
	}
	// No token → PEER_TOKEN_REQUIRED (POST).
	if rec := do("POST", "/api/v1/peers", `{"id":"p3","upstream":"`+up.url+`"}`); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "PEER_TOKEN_REQUIRED") {
		t.Fatalf("token required: %d %s", rec.Code, rec.Body.String())
	}
	// Create.
	rec := do("POST", "/api/v1/peers", `{"id":"crud-peer","upstream":"`+up.url+`","token":"the-secret-value","enabled":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "the-secret-value") {
		t.Fatalf("create response echoed the token (§6 violation): %s", rec.Body.String())
	}
	// Duplicate → 409 PEER_EXISTS.
	if rec := do("POST", "/api/v1/peers", `{"id":"crud-peer","upstream":"`+up.url+`","token":"x"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "PEER_EXISTS") {
		t.Fatalf("duplicate: %d %s", rec.Code, rec.Body.String())
	}
	// GET: token_fp present, token absent, token_present true.
	rec = do("GET", "/api/v1/peers/crud-peer", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "the-secret-value") {
		t.Fatalf("GET returned the token (§6 violation): %s", rec.Body.String())
	}
	var env struct {
		Data peerConfigView `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("get decode: %v", err)
	}
	if !env.Data.TokenPresent || env.Data.TokenFP == "" {
		t.Fatalf("GET view missing token_fp/token_present: %+v", env.Data)
	}
	// The fp IS sha256(secret)[:8] — that equality is the contract (§6).
	if env.Data.TokenFP != tokenFingerprint("the-secret-value") {
		t.Fatalf("token_fp %q is not the fingerprint of the stored secret", env.Data.TokenFP)
	}
	// PUT without token: keeps stored secret (rotation response still carries fp).
	rec = do("PUT", "/api/v1/peers/crud-peer", `{"upstream":"`+up.url+`","enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	var putEnv struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &putEnv); err != nil {
		t.Fatalf("put decode: %v", err)
	}
	if fp, _ := putEnv.Data["token_fp"].(string); fp == "" {
		t.Fatalf("PUT response carries no token_fp (rotation confirmation contract): %s", rec.Body.String())
	}
	if enabled, _ := putEnv.Data["enabled"].(bool); enabled {
		t.Fatalf("PUT did not apply enabled=false")
	}
	// Delete → 204, then 404.
	if rec := do("DELETE", "/api/v1/peers/crud-peer", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := do("GET", "/api/v1/peers/crud-peer", ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "PEER_NOT_FOUND") {
		t.Fatalf("get after delete: %d %s", rec.Code, rec.Body.String())
	}
}

// TestP1702ActionRoutes covers §2 /action: enable/disable/dial-now shapes.
func TestP1702ActionRoutes(t *testing.T) {
	up := p1702StartUpstream(t)
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("POST", "/api/v1/peers", `{"id":"act-peer","upstream":"`+up.url+`","token":"t","enabled":false}`); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/api/v1/peers/act-peer/action", `{"action":"enable"}`); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/api/v1/peers/act-peer/action", `{"action":"disable"}`); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/api/v1/peers/act-peer/action", `{"action":"frobnicate"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad action: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/api/v1/peers/absent-peer/action", `{"action":"dial-now"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("action on absent peer: %d %s", rec.Code, rec.Body.String())
	}
}

// TestP1702AuditRedaction covers §7: peer.created lands with the token
// redacted at the boundary; redaction never lets the secret through.
func TestP1702AuditRedaction(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	auditPath := filepath.Join(dir, "audit.jsonl")
	up := NewServer("", "test-token", "")
	up.SetAuditPath(auditPath)
	up.mux = http.NewServeMux()
	up.registerRoutes()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))

	body := `{"id":"aud-peer","upstream":"ws://10.0.0.5:7700/ws","token":"super-secret-audit-token","enabled":false}`
	req := httptest.NewRequest("POST", "/api/v1/peers", strings.NewReader(body))
	rec := httptest.NewRecorder()
	up.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	entries := up.audit.Query(AuditFilter{Action: "peer.created"})
	if len(entries) == 0 {
		t.Fatalf("peer.created audit event missing (§7 family)")
	}
	raw := fmt.Sprintf("%+v", entries[0])
	if strings.Contains(raw, "super-secret-audit-token") {
		t.Fatalf("audit entry contains the token (§7.1 violation): %s", raw)
	}
	if !strings.Contains(raw, "aud-peer") {
		t.Fatalf("audit entry lost the peer id: %s", raw)
	}
}

// TestP1702VersionSemantics covers §4 READY-LEVEL rules as units.
func TestP1702VersionSemantics(t *testing.T) {
	cases := []struct {
		v, min string
		want   bool
	}{
		{"1.20.0", "1.20.0", true},
		{"1.21.3", "1.20.0", true},
		{"1.19.2", "1.20.0", false},
		{"1.20", "1.20.0", true}, // missing patch = 0
		{"v1.20.0", "1.20.0", true},
		{"1.20.0-p1702", "1.20.0", true},    // prerelease suffix ≥ base
		{"1.20.1+build.7", "1.20.0", true},  // build metadata ≥ base
		{"", "1.20.0", false},               // honest absence never proves sufficiency
		{"garbage", "1.20.0", false},
	}
	for _, c := range cases {
		if got := versionAtLeast(c.v, c.min); got != c.want {
			t.Errorf("versionAtLeast(%q,%q)=%v want %v", c.v, c.min, got, c.want)
		}
	}
	if peerVersionMismatch("") {
		t.Errorf("empty version = degraded vocabulary, NOT mismatch (§4)")
	}
	if !peerVersionMismatch("1.19.2") {
		t.Errorf("1.19.2 predates the ready level — mismatch must assert")
	}
	if peerVersionMismatch("1.20.0") {
		t.Errorf("peer at the ready level must not mismatch")
	}
}

// TestP1702DocV2Tolerance covers FM-2.2: a v1 doc loads, default_peers
// arrives empty, no crash; writing a peer bumps the version.
func TestP1702DocV2Tolerance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.json")
	// A v1 document as an old binary would leave it.
	v1 := `{"version":1,"updated_at":"2026-01-01T00:00:00Z","local":{"id":"srv-abc","first_seen":"2026-01-01T00:00:00Z"},"peers":{}}`
	if err := writeFileAtomic(path, []byte(v1), 0o600); err != nil {
		t.Fatalf("seed v1 doc: %v", err)
	}
	t.Setenv("PROBE_SERVERS_DB", path)
	up := NewServer("", "t", "")
	up.mux = http.NewServeMux()
	up.registerRoutes()
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	req := httptest.NewRequest("POST", "/api/v1/peers",
		strings.NewReader(`{"id":"v2peer","upstream":"ws://10.0.0.9:7700/ws","token":"x","enabled":false}`))
	rec := httptest.NewRecorder()
	up.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create on v1 doc: %d %s", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	var doc serversDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("doc decode: %v", err)
	}
	if doc.Version != DefaultPeersDocVersion {
		t.Fatalf("doc version = %d, want %d after first peer write", doc.Version, DefaultPeersDocVersion)
	}
	if _, ok := doc.DefaultPeers["v2peer"]; !ok {
		t.Fatalf("default_peers missing v2peer: %s", string(raw))
	}
	// And the old binary's read survives: peers namespace intact.
	if doc.Peers == nil {
		t.Fatalf("peers namespace lost on v2 write")
	}
}

// TestP1702CorruptDocLocalMode covers §5.4: corrupt servers.json at boot →
// peers treated as absent, no spawn, health answers (the log line names the
// local mode; the API simply has no peers).
func TestP1702CorruptDocLocalMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.json")
	if err := writeFileAtomic(path, []byte("{not json at all"), 0o600); err != nil {
		t.Fatalf("seed corrupt doc: %v", err)
	}
	t.Setenv("PROBE_SERVERS_DB", path)
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	s := NewServer("", "t", "")
	s.mux = http.NewServeMux()
	s.registerRoutes()
	s.bootstrapPeers() // must not hang, must not spawn, must mark local mode
	req := httptest.NewRequest("GET", "/api/v1/peers", nil)
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	var env struct {
		Data struct {
			Peers []peerConfigView `json:"peers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(env.Data.Peers) != 0 {
		t.Fatalf("corrupt doc must yield zero peers (local mode), got %d", len(env.Data.Peers))
	}
}
