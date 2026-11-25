package server

// P22-02 — GET /api/v1/servers against the contract of record
// (/opt/data/tmp/probe119/P21-02_INVENTORY_CONTRACT.md). These tests assert
// the shipped shape, not a schema: every one decodes a real response body.
//
// Scope note: the down/up flip is exercised over a live loopback websocket with
// the real relay wire format (one binary registration frame on channel 0), not
// by injecting into the relay map — an injection would prove nothing about the
// close path, which is where a peer used to vanish.

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// serversTestServer builds a v1 server whose inventory document lives in a
// temp dir and whose name is fixed, so assertions do not depend on the host.
func serversTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(t.TempDir(), "servers.json"))
	t.Setenv("PROBE_SERVER_NAME", "unit-label")
	srv, cleanup := newV1TestServer(t)
	srv.addr = "localhost:7700"
	srv.SetVersion("v1.19.1-test")
	return srv, func() {
		// Let a closing relay's read loop finish its record write before the
		// temp dir disappears: the write is real, the teardown is ours.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			srv.relayMu.RLock()
			n := len(srv.relays)
			srv.relayMu.RUnlock()
			if n == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cleanup()
	}
}

// getServers performs the request and returns the decoded envelope plus the
// raw body, so a failure can quote what actually came back.
func getServers(t *testing.T, srv *Server, header http.Header) (serversResponse, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/servers", nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/servers: status %d, body %s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK   bool            `json:"ok"`
		Data serversResponse `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if !env.OK {
		t.Fatalf("ok=false: %s", rec.Body.String())
	}
	return env.Data, rec.Body.String()
}

func getHealth(t *testing.T, srv *Server, header http.Header) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/health: status %d", rec.Code)
	}
	var env struct {
		OK   bool                   `json:"ok"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return env.Data
}

// dialRelay speaks the relay wire format by hand: one registration frame on
// channel 0. Returns the socket, live.
func dialRelay(t *testing.T, url, relayID, metadata string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	reg := `{"type":"relay_register","relay_id":"` + relayID + `","token":"test-token"`
	if metadata != "" {
		reg += `,"metadata":` + metadata
	}
	reg += `}`
	frame := make([]byte, 5+len(reg))
	frame[0] = 0x42
	binary.BigEndian.PutUint32(frame[1:5], 0)
	copy(frame[5:], reg)
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		conn.Close()
		t.Fatalf("send relay_register: %v", err)
	}
	// Read the peer_hello the server pushes at registration success; it proves
	// the session is established before the inventory is polled.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		conn.Close()
		t.Fatalf("no peer_hello after registration: %v", err)
	}
	conn.SetReadDeadline(time.Time{})
	return conn
}

func peerByID(entries []serverEntry, id string) *serverEntry {
	for i := range entries {
		if entries[i].ID == id {
			return &entries[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------

func TestP2202_ServersShapeAndLocalFirst(t *testing.T) {
	srv, cleanup := serversTestServer(t)
	defer cleanup()

	data, body := getServers(t, srv, nil)
	t.Logf("GET /api/v1/servers -> %s", body)

	if len(data.Servers) != 1 {
		t.Fatalf("expected 1 server with no peers, got %d: %s", len(data.Servers), body)
	}
	local := data.Servers[0]
	if local.Role != roleLocal {
		t.Fatalf("servers[0].role = %q, want local", local.Role)
	}
	if local.ID != data.LocalID {
		t.Fatalf("local_id %q != servers[0].id %q", data.LocalID, local.ID)
	}
	if data.RetentionSeconds != int64(defaultServersRetention.Seconds()) {
		t.Errorf("retention_seconds = %d, want %d", data.RetentionSeconds, int64(defaultServersRetention.Seconds()))
	}

	host, _ := os.Hostname()
	wantAddr := net.JoinHostPort(host, "7700")
	if local.Name != "unit-label" {
		t.Errorf("name = %q, want the configured PROBE_SERVER_NAME", local.Name)
	}
	if local.Name == srv.addr {
		t.Errorf("name defaulted to the bind address %q — that is D3 reproduced", srv.addr)
	}
	if local.ListenAddr != wantAddr {
		t.Errorf("listen_addr = %q, want %q (hostname substituted)", local.ListenAddr, wantAddr)
	}
	if local.ListenAddrRaw != "localhost:7700" {
		t.Errorf("listen_addr_raw = %q, want the configured string verbatim", local.ListenAddrRaw)
	}
	if local.ListenAddrSource != "hostname" {
		t.Errorf("listen_addr_source = %q, want hostname (the substitution is a receipt)", local.ListenAddrSource)
	}
	if local.Version == nil || *local.Version != "v1.19.1-test" {
		t.Errorf("version = %v, want the server version", local.Version)
	}
	if local.UptimeSeconds == nil || *local.UptimeSeconds < 0 {
		t.Errorf("uptime_seconds = %v, want a real measurement", local.UptimeSeconds)
	}
	if local.State != "up" || local.StateReason != stateReasonServing {
		t.Errorf("state/state_reason = %q/%q, want up/serving", local.State, local.StateReason)
	}
	if local.DownSince != nil {
		t.Errorf("down_since = %v on an up entry, want null", *local.DownSince)
	}
	if len(local.Transports) != 1 || local.Transports[0].Kind != transportKindListen ||
		local.Transports[0].WSState != wsStateListening || local.Transports[0].Source != transportSourceLocalConfig {
		t.Fatalf("transports = %+v, want one listening local_config row", local.Transports)
	}
	if local.Transports[0].Port == nil || *local.Transports[0].Port != 7700 {
		t.Errorf("transport port = %v, want 7700", local.Transports[0].Port)
	}
	// D4: a plain server has no max_agents concept. null, never 0, never 100.
	if local.MaxAgents != nil {
		t.Errorf("max_agents = %d, want null (unreported is not 0 and not a guessed 100)", *local.MaxAgents)
	}
	if local.ForwardPolicy != nil {
		t.Errorf("forward_policy = %v, want null (no server-level default exists)", *local.ForwardPolicy)
	}
	wantFP := tokenFingerprint("test-token")
	if local.TokenFP == nil || *local.TokenFP != wantFP {
		t.Errorf("token_fp = %v, want %s", local.TokenFP, wantFP)
	}
	if len(wantFP) != 8 || strings.ToLower(wantFP) != wantFP {
		t.Errorf("token_fp %q is not 8 lowercase hex", wantFP)
	}
	if !local.TokenFPVerified {
		t.Error("local token_fp_verified = false; we hold our own secret")
	}
	if local.TokenFPScope != "shared" {
		t.Errorf("token_fp_scope = %q, want shared until P7", local.TokenFPScope)
	}

	// R3: keys are present even when null — a missing key is unreadable.
	var raw struct {
		Data struct {
			Servers []map[string]json.RawMessage `json:"servers"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	for _, key := range []string{"id", "name", "role", "listen_addr", "listen_addr_raw",
		"listen_addr_source", "version", "uptime_seconds", "state", "last_seen",
		"state_reason", "down_since", "transports", "counts", "forward_policy",
		"max_agents", "token_fp", "token_fp_verified", "token_fp_scope"} {
		if _, ok := raw.Data.Servers[0][key]; !ok {
			t.Errorf("key %q missing from the entry — R3 forbids omission", key)
		}
	}

	// One endpoint only: no badge-specific second surface.
	req := httptest.NewRequest("GET", "/api/v1/servers/summary", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/v1/servers/summary = %d, want 404 (no second endpoint)", rec.Code)
	}
}

func TestP2202_PeerUpDownRetainedAndBackUp(t *testing.T) {
	srv, cleanup := serversTestServer(t)
	defer cleanup()

	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"

	conn := dialRelay(t, wsURL, "relay-loopback-1", `{"listen_addr":":7701","max_agents":100,"upstream":"ws://127.0.0.1:7700/ws"}`)

	data, body := getServers(t, srv, nil)
	t.Logf("while connected -> %s", body)
	up := peerByID(data.Servers, "relay-loopback-1")
	if up == nil {
		t.Fatalf("peer missing while its socket is open: %s", body)
	}
	if up.State != "up" || up.StateReason != stateReasonSocketOpen {
		t.Fatalf("peer state = %q/%q, want up/socket_open", up.State, up.StateReason)
	}
	if up.ListenAddr != net.JoinHostPort(mustHostname(t), "7701") {
		t.Errorf("peer listen_addr = %q, want hostname:7701", up.ListenAddr)
	}
	if up.ListenAddrRaw != ":7701" {
		t.Errorf("peer listen_addr_raw = %q, want verbatim", up.ListenAddrRaw)
	}
	if up.MaxAgents == nil || *up.MaxAgents != 100 {
		t.Errorf("peer max_agents = %v, want the reported 100", up.MaxAgents)
	}
	if up.TokenFPVerified {
		t.Error("peer token_fp_verified = true; the shared token identifies no peer (D2)")
	}
	if up.TokenFPScope != "shared" {
		t.Errorf("peer token_fp_scope = %q, want shared", up.TokenFPScope)
	}
	if up.Version != nil || up.UptimeSeconds != nil {
		t.Errorf("peer version/uptime = %v/%v, want null until P2", up.Version, up.UptimeSeconds)
	}
	var listen, uplink *serverTransport
	for i := range up.Transports {
		switch up.Transports[i].Kind {
		case transportKindListen:
			listen = &up.Transports[i]
		case transportKindUplink:
			uplink = &up.Transports[i]
		}
	}
	if listen == nil || listen.WSState != wsStateOpen {
		t.Fatalf("listen transport = %+v, want one open row", listen)
	}
	if uplink == nil || uplink.Port == nil || *uplink.Port != 7700 {
		t.Fatalf("uplink transport = %+v, want the reported dial port 7700 (not the ephemeral source port)", uplink)
	}
	upLastSeen := up.LastSeen

	// Kill the relay. The peer must become a DOWN RECORD, not an absence.
	conn.Close()

	var down *serverEntry
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, _ := getServers(t, srv, nil)
		if p := peerByID(d.Servers, "relay-loopback-1"); p != nil && p.State == "down" {
			down = p
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if down == nil {
		t.Fatal("peer vanished (or never flipped down) after its socket closed — a disconnect must not delete the record")
	}
	afterBody := ""
	if _, afterBody = getServers(t, srv, nil); afterBody != "" {
		t.Logf("after disconnect -> %s", afterBody)
	}
	if down.StateReason != stateReasonSocketClosed && down.StateReason != stateReasonPongTimeout {
		t.Errorf("state_reason = %q, want socket_closed or pong_timeout", down.StateReason)
	}
	if down.DownSince == nil {
		t.Error("down_since is null on a down record")
	}
	if down.LastSeen != upLastSeen {
		t.Errorf("last_seen changed on the flip: %q -> %q (it is preserved, not restamped)", upLastSeen, down.LastSeen)
	}
	if down.LastSeen == "" {
		t.Error("last_seen empty on a down record")
	}
	if _, err := time.Parse(time.RFC3339, down.LastSeen); err != nil {
		t.Errorf("last_seen %q is not RFC3339: %v", down.LastSeen, err)
	}
	for _, tr := range down.Transports {
		if tr.WSState == wsStateOpen {
			t.Errorf("down record still claims an open transport: %+v", tr)
		}
	}

	// Reconnect: the same id comes back UP and down_since clears.
	conn2 := dialRelay(t, wsURL, "relay-loopback-1", `{"listen_addr":":7701","max_agents":100}`)
	defer conn2.Close()

	var back *serverEntry
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, _ := getServers(t, srv, nil)
		if p := peerByID(d.Servers, "relay-loopback-1"); p != nil && p.State == "up" {
			back = p
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if back == nil {
		t.Fatal("peer did not return to up after reconnect")
	}
	if back.DownSince != nil {
		t.Errorf("down_since = %v after reconnect, want null", *back.DownSince)
	}
	if back.StateReason != stateReasonSocketOpen {
		t.Errorf("state_reason = %q after reconnect, want socket_open", back.StateReason)
	}
	t.Logf("after reconnect -> state=%s reason=%s", back.State, back.StateReason)
}

func TestP2202_PeerWithoutMetadataInventsNoPort(t *testing.T) {
	srv, cleanup := serversTestServer(t)
	defer cleanup()

	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn := dialRelay(t, wsURL, "relay-bare", "")
	defer conn.Close()

	data, body := getServers(t, srv, nil)
	p := peerByID(data.Servers, "relay-bare")
	if p == nil {
		t.Fatalf("peer missing: %s", body)
	}
	if p.MaxAgents != nil {
		t.Errorf("max_agents = %d, want null when unreported", *p.MaxAgents)
	}
	if p.ListenAddr != "" {
		t.Errorf("listen_addr = %q, want empty when no port was reported (never an invented :7701)", p.ListenAddr)
	}
	for _, tr := range p.Transports {
		if tr.Source != transportSourceUnreported {
			t.Errorf("transport source = %q, want unreported", tr.Source)
		}
		if tr.Port != nil {
			t.Errorf("transport port = %d, want null", *tr.Port)
		}
	}
}

func TestP2202_HealthIdentityIsTheSameProjection(t *testing.T) {
	srv, cleanup := serversTestServer(t)
	defer cleanup()

	servers, _ := getServers(t, srv, nil)
	local := servers.Servers[0]

	health := getHealth(t, srv, nil)
	identityRaw, ok := health["identity"]
	if !ok {
		t.Fatal("health.data.identity missing — the login screen has nothing to render")
	}
	identity, ok := identityRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("identity is %T, want an object", identityRaw)
	}
	if len(identity) != 4 {
		t.Errorf("identity has %d keys (%v), want exactly name/listen_addr/version/role", len(identity), identity)
	}
	for _, k := range []string{"name", "listen_addr", "version", "role"} {
		if _, ok := identity[k]; !ok {
			t.Errorf("identity key %q missing", k)
		}
	}
	if identity["name"] != local.Name || identity["listen_addr"] != local.ListenAddr {
		t.Errorf("identity diverges from servers[0]: %v vs name=%q listen=%q", identity, local.Name, local.ListenAddr)
	}
	if identity["version"] == nil || *local.Version != identity["version"].(string) {
		t.Errorf("identity.version %v != servers[0].version %v", identity["version"], local.Version)
	}
	if identity["role"] != roleLocal {
		t.Errorf("identity.role = %v, want local", identity["role"])
	}

	// D8: a request header may not steer identity.
	for _, h := range []http.Header{
		{"Host": []string{"evil.example.com"}},
		{"X-Forwarded-Host": []string{"evil.example.com"}},
		{"X-Real-Host": []string{"evil.example.com"}},
	} {
		got := getHealth(t, srv, h)
		if strings.Contains(mustJSON(t, got), "evil.example.com") {
			t.Errorf("header %v reached the identity payload: %s", h, mustJSON(t, got))
		}
	}

	// The off-switch keeps the key and nulls the value (R3, §3.1 rule 7).
	t.Setenv("PROBE_PREAUTH_IDENTITY", "off")
	off := getHealth(t, srv, nil)
	if v, ok := off["identity"]; !ok || v != nil {
		t.Errorf("identity with PROBE_PREAUTH_IDENTITY=off = %v (present=%v), want null and present", v, ok)
	}
}

func TestP2202_AuthGateRespected(t *testing.T) {
	srv, cleanup := serversTestServer(t)
	defer cleanup()
	srv.requireAPIAuth = true

	req := httptest.NewRequest("GET", "/api/v1/servers", nil)
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET = %d, want 401 when --require-api-auth is set", rec.Code)
	}

	req = httptest.NewRequest("GET", "/api/v1/servers", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated GET = %d, want 200", rec.Code)
	}
}

func TestP2202_CorruptDocumentQuarantinedNotInvented(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROBE_SERVERS_DB", path)
	t.Setenv("PROBE_SERVER_NAME", "unit-label")
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.addr = "localhost:7700"

	data, body := getServers(t, srv, nil)
	if len(data.Servers) != 1 || data.Servers[0].Role != roleLocal {
		t.Fatalf("corrupt document must degrade to the local entry only, got: %s", body)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "servers.json.corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one quarantined file, got %v", matches)
	}
}

func mustHostname(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil || h == "" {
		t.Fatalf("os.Hostname: %v", err)
	}
	return h
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
