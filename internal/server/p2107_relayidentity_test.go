package server

// P21-07 — the three acceptance criteria, each proven by a test that can fail.
//
//  1. Fingerprints, never secrets. A scan that reads the persisted JSON and
//     the API bodies and FAILS when credential material appears is run twice:
//     once on the real flow (passes) and once against an injected secret
//     (fails). A scan that cannot fail proves nothing; the negative control
//     is part of the test.
//
//  2. Rotation re-forms one node's links without a restart. Two REAL relays
//     (internal/relay, not stubs) with a real downstream agent each. One is
//     rotated through the API; its agent re-forms on the same downstream
//     socket and answers a command again, while the peer relay keeps its
//     session object and answers throughout.
//
//  3. An installed allowlist. The implementation is installed from config,
//     a non-listed relay is refused with FORWARD_ALLOWLIST_DENIED, a listed
//     one is admitted, and the empty-list policy (deny-all when the flag was
//     given, allow-all when it was omitted) is asserted.
//
// Loopback only. Nothing here touches the Radxa or OVH.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/falke-ai-circuit/probe/internal/relay"
	"github.com/gorilla/websocket"
)

// p2107ScanForSecret is the fingerprint scan. It returns an error when the
// credential appears anywhere in the scanned bytes. It is deliberately dumb:
// a scan with heuristics is a scan that can quietly stop finding things.
func p2107ScanForSecret(blob []byte, secret string) error {
	if secret == "" {
		return fmt.Errorf("scan: empty credential cannot be searched for")
	}
	if bytes.Contains(blob, []byte(secret)) {
		return fmt.Errorf("scan: credential material appears in %d bytes scanned", len(blob))
	}
	return nil
}

// p2107Secret reads the credential in force for a relay. Test-only: production
// code has no getter because nothing outside the rotation path may hold it.
func p2107Secret(s *Server, relayID string) string {
	s.relayIdentityMu.Lock()
	defer s.relayIdentityMu.Unlock()
	if l := s.relaySecrets[relayID]; len(l) > 0 {
		return l[0]
	}
	return ""
}

// p2107Get performs an authenticated GET and returns status + raw body.
func p2107Get(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// p2107RegisterRelay performs a relay registration over a real socket and
// returns the connection after the server has answered peer_hello (i.e. the
// registration succeeded). Used where a real relay's reconnect timing is not
// what is under test.
func p2107RegisterRelay(t *testing.T, srv *Server, ts *httptest.Server, relayID, fp, token string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial relay socket: %v", err)
	}
	reg, _ := json.Marshal(map[string]interface{}{
		"type": "relay_register", "relay_id": relayID, "fingerprint": fp, "token": token,
	})
	if err := conn.WriteMessage(websocket.BinaryMessage, p2104Frame(0x45, 0, reg)); err != nil {
		t.Fatalf("send relay_register: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("relay %s: no answer to registration: %v", relayID, err)
	}
	_, _, payload, _ := parseRelayFrame(data)
	var ctrl map[string]interface{}
	json.Unmarshal(payload, &ctrl)
	if ctrl["type"] != "peer_hello" {
		t.Fatalf("relay %s: registration did not succeed: %s", relayID, string(payload))
	}
	conn.SetReadDeadline(time.Time{})
	p2104Wait(t, "relay "+relayID+" in the relay registry", func() bool {
		return p2107Session(srv, relayID) != nil
	})
	return conn
}

// p2107Session returns the server's live session for a relay id (nil when the
// session is gone).
func p2107Session(srv *Server, relayID string) *relaySession {
	srv.relayMu.RLock()
	defer srv.relayMu.RUnlock()
	return srv.relays[relayID]
}

// p2107Wait polls until cond is true or the budget expires, then fails naming
// the observable it was waiting for.
func p2107Wait(t *testing.T, budget time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", budget, what)
}

// TestP2107_FingerprintsOnlyOnDiskAndInResponses is the adversarial half of
// criterion 1: the scan is shown failing against an injected credential before
// it is trusted on the real flow.
func TestP2107_FingerprintsOnlyOnDiskAndInResponses(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	path := filepath.Join(t.TempDir(), "relays.json")
	if err := srv.SetRelayIdentityPath(path); err != nil {
		t.Fatalf("set identity path: %v", err)
	}

	conn := p2107RegisterRelay(t, srv, ts, "relay-a", "fp-hw-a", "test-token")
	defer conn.Close()

	status, body := p2105Post(t, ts, "/api/v1/relays/relay-a/rotate", "")
	if status != 200 {
		t.Fatalf("rotate: status %d body %s", status, body)
	}
	secret := p2107Secret(srv, "relay-a")
	if secret == "" {
		t.Fatal("rotation recorded no credential in memory")
	}

	// Sanity: the scan can fail. Inject the credential and require a finding.
	if err := p2107ScanForSecret([]byte(`{"relay":"relay-a","token":"`+secret+`"}`), secret); err == nil {
		t.Fatal("scan passed on a body that contains the credential — the scan is inert")
	}

	// 1a. On-disk JSON: no credential, fingerprint present.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read identity file: %v", err)
	}
	if err := p2107ScanForSecret(onDisk, secret); err != nil {
		t.Fatalf("persisted identity file: %v", err)
	}
	if !bytes.Contains(onDisk, []byte(tokenFingerprint(secret))) {
		t.Fatalf("persisted file does not carry the fingerprint: %s", onDisk)
	}
	// No key other than the fingerprint fields may mention a token.
	var doc struct {
		Relays []map[string]json.RawMessage `json:"relays"`
	}
	if err := json.Unmarshal(onDisk, &doc); err != nil {
		t.Fatalf("identity file is not JSON: %v", err)
	}
	for _, rec := range doc.Relays {
		// The only token-named keys allowed are the fingerprint fields. A
		// credential-bearing key (token, new_token, secret) fails here even
		// if its value happened to be empty.
		allowed := map[string]bool{
			"token_fp": true, "previous_token_fp": true,
			"token_fp_scope": true, "token_fp_verified": true,
		}
		for k := range rec {
			if strings.Contains(k, "token") && !allowed[k] {
				t.Fatalf("persisted record has a credential-bearing key %q: %s", k, onDisk)
			}
		}
	}

	// 1b. Response bodies: the list and the rotate answer.
	status, listBody := p2107Get(t, ts, "/api/v1/relays")
	if status != 200 {
		t.Fatalf("list relays: status %d body %s", status, listBody)
	}
	for _, b := range []struct{ name, body string }{
		{"GET /api/v1/relays", listBody},
		{"POST /api/v1/relays/{id}/rotate", body},
	} {
		if err := p2107ScanForSecret([]byte(b.body), secret); err != nil {
			t.Fatalf("%s: %v — body %s", b.name, err, b.body)
		}
		if !strings.Contains(b.body, "token_fp") {
			t.Fatalf("%s carries no fingerprint: %s", b.name, b.body)
		}
	}
	t.Logf("negative control: injected credential detected by the same scan")
	t.Logf("persisted relays=%d, keys checked for credential fields", len(doc.Relays))
}

// TestP2107_AllowlistPolicies states and tests the three policies.
func TestP2107_AllowlistPolicies(t *testing.T) {
	allowAll, err := NewConfigRelayAllowlist("", false)
	if err != nil {
		t.Fatalf("allow-all: %v", err)
	}
	if allowAll.Policy() != RelayAllowlistAllowAll {
		t.Fatalf("omitted flag must be allow-all, got %s", allowAll.Policy())
	}
	if !allowAll.Allows("anything", "deadbeef") {
		t.Fatal("allow-all refused a relay")
	}

	denyAll, err := NewConfigRelayAllowlist(" , ", true)
	if err != nil {
		t.Fatalf("deny-all: %v", err)
	}
	if denyAll.Policy() != RelayAllowlistDenyAll {
		t.Fatalf("given-but-empty must be deny-all, got %s", denyAll.Policy())
	}
	if denyAll.Allows("anything", "") {
		t.Fatal("deny-all admitted a relay — the empty list is not an explicit policy")
	}

	listed, err := NewConfigRelayAllowlist("relay-known:3f9a1c02,relay-idonly,*:abcdef12", true)
	if err != nil {
		t.Fatalf("listed: %v", err)
	}
	if listed.Policy() != RelayAllowlistListed {
		t.Fatalf("listed policy: got %s", listed.Policy())
	}
	cases := []struct {
		id, fp string
		want   bool
	}{
		{"relay-known", "3f9a1c02", true},
		{"relay-known", "11111111", false},
		{"relay-idonly", "", true},
		{"relay-idonly", "whatever", true},
		{"relay-other", "abcdef12", true},  // wildcard fingerprint
		{"relay-other", "00000000", false}, // no match at all
	}
	for _, c := range cases {
		if got := listed.Allows(c.id, c.fp); got != c.want {
			t.Fatalf("Allows(%q,%q)=%v want %v", c.id, c.fp, got, c.want)
		}
	}

	if _, err := NewConfigRelayAllowlist("relay-bad:zz", true); err == nil {
		t.Fatal("a malformed fingerprint entry was accepted")
	}
}

// TestP2107_InstalledAllowlistRefusesAndAdmits proves the interface has an
// implementation AND that it is installed: the refusal path is P21-04's, the
// decision is this one's.
func TestP2107_InstalledAllowlistRefusesAndAdmits(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	if err := srv.InstallRelayAllowlist("relay-known", true); err != nil {
		t.Fatalf("install allowlist: %v", err)
	}

	// Non-listed: refused with its own code, over the wire.
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer test-token"},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	reg, _ := json.Marshal(map[string]interface{}{"type": "relay_register", "relay_id": "relay-unknown", "token": "test-token"})
	conn.WriteMessage(websocket.BinaryMessage, p2104Frame(0x45, 0, reg))
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("no refusal for a relay outside the allowlist: %v", err)
	}
	_, _, payload, _ := parseRelayFrame(data)
	var ctrl map[string]interface{}
	json.Unmarshal(payload, &ctrl)
	if ctrl["code"] != RefusalAllowlist {
		t.Fatalf("refusal code: got %v want %s (%s)", ctrl["code"], RefusalAllowlist, string(payload))
	}
	t.Logf("non-allowlisted refused: %s", string(payload))

	// Listed: admitted (peer_hello is the registration-success witness).
	good := p2107RegisterRelay(t, srv, ts, "relay-known", "fp-k", "test-token")
	defer good.Close()
	t.Logf("allowlisted admitted: relay-known registered")
}

// TestP2107_RotationReformsOneNodeOnly is criterion 2 against two real relays
// and two real downstream agents on loopback.
func TestP2107_RotationReformsOneNodeOnly(t *testing.T) {
	srv, ts, cleanup := p2104Server(t)
	defer cleanup()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	portA := p2106FreePort(t)
	portB := p2106FreePort(t)

	relayA := relay.New(relay.Config{UpstreamURL: wsURL, ListenAddr: fmt.Sprintf("127.0.0.1:%d", portA), Token: "test-token", RelayID: "relay-a", MaxAgents: 10})
	relayB := relay.New(relay.Config{UpstreamURL: wsURL, ListenAddr: fmt.Sprintf("127.0.0.1:%d", portB), Token: "test-token", RelayID: "relay-b", MaxAgents: 10})
	go func() { _ = relayA.Run() }()
	go func() { _ = relayB.Run() }()
	t.Cleanup(func() { relayA.Stop(); relayB.Stop() })

	p2107Wait(t, 10*time.Second, "both relays registered", func() bool {
		return p2107Session(srv, "relay-a") != nil && p2107Session(srv, "relay-b") != nil
	})

	// Two downstream agents, one behind each relay, on their own sockets.
	agentA := p2107DialDownstreamAgent(t, portA, "agent-a")
	defer agentA.Close()
	agentB := p2107DialDownstreamAgent(t, portB, "agent-b")
	defer agentB.Close()

	p2107Wait(t, 10*time.Second, "both agents registered through their relays", func() bool {
		return p2104RegistryHas(srv, "relay/relay-a/agent-a") && p2104RegistryHas(srv, "relay/relay-b/agent-b")
	})

	oldA := p2107Session(srv, "relay-a")
	oldB := p2107Session(srv, "relay-b")
	if oldA == nil || oldB == nil {
		t.Fatal("relay sessions missing before rotation")
	}
	linksB := len(oldB.channels)

	// Both nodes answer before the rotation — otherwise "survives" is vacuous.
	if got := p2107Exec(t, ts, "agent-a", "pre-a"); !strings.Contains(got, "agent-a-echo:pre-a") {
		t.Fatalf("agent-a did not answer before rotation: %s", got)
	}
	if got := p2107Exec(t, ts, "agent-b", "pre-b"); !strings.Contains(got, "agent-b-echo:pre-b") {
		t.Fatalf("agent-b did not answer before rotation: %s", got)
	}

	status, body := p2105Post(t, ts, "/api/v1/relays/relay-a/rotate", "")
	if status != 200 {
		t.Fatalf("rotate relay-a: status %d body %s", status, body)
	}
	var rotated struct {
		OK   bool                `json:"ok"`
		Data RelayRotationResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &rotated); err != nil || !rotated.OK {
		t.Fatalf("rotate body: %s", body)
	}
	if rotated.Data.LinksDropped < 1 {
		t.Fatalf("rotation dropped no links: %s", body)
	}
	if !rotated.Data.Delivered {
		t.Fatalf("rotation push was not delivered: %s", body)
	}
	newFP := rotated.Data.TokenFP

	// The rotated node re-forms: a NEW session object, the same agent id
	// registered again, and a command answered on the fresh link.
	p2107Wait(t, 45*time.Second, "relay-a to reconnect and re-form its agent", func() bool {
		sess := p2107Session(srv, "relay-a")
		return sess != nil && sess != oldA && p2104RegistryHas(srv, "relay/relay-a/agent-a")
	})
	p2107Wait(t, 15*time.Second, "agent-a to answer again after rotation", func() bool {
		return strings.Contains(p2107Exec(t, ts, "agent-a", "post-a"), "agent-a-echo:post-a")
	})

	// The peer is untouched: same session object, same channel count, agent
	// still registered, command still answered.
	sessB := p2107Session(srv, "relay-b")
	if sessB == nil || sessB != oldB {
		t.Fatal("relay-b's session was replaced by relay-a's rotation")
	}
	if len(sessB.channels) != linksB {
		t.Fatalf("relay-b channel count changed: %d → %d", linksB, len(sessB.channels))
	}
	if !p2104RegistryHas(srv, "relay/relay-b/agent-b") {
		t.Fatal("relay-b's agent vanished during relay-a's rotation")
	}
	if got := p2107Exec(t, ts, "agent-b", "post-b"); !strings.Contains(got, "agent-b-echo:post-b") {
		t.Fatalf("agent-b stopped answering: %s", got)
	}

	if relayA.TokenFP() != newFP {
		t.Fatalf("relay-a holds token_fp=%s, server issued %s", relayA.TokenFP(), newFP)
	}
	if relayB.TokenFP() == newFP {
		t.Fatalf("relay-b picked up relay-a's rotated credential")
	}

	t.Logf("rotated=relay-a links_dropped=%d new token_fp=%s", rotated.Data.LinksDropped, newFP)
	t.Logf("relay-a: relinked old_session=%p new_session=%p agent-a answered post-rotation", oldA, p2107Session(srv, "relay-a"))
	t.Logf("relay-b: untouched session=%p links=%d agent-b answered throughout", oldB, linksB)
}

// p2107DialDownstreamAgent connects a real downstream agent socket to a relay
// and sends its AgentInfo, which the relay forwards upstream as the channel's
// first frame.
func p2107DialDownstreamAgent(t *testing.T, port int, name string) *websocket.Conn {
	t.Helper()
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws", port)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial downstream %s: %v", name, err)
	}
	res, _ := json.Marshal(protocol.AgentInfo{Name: name, Version: "v-test", OS: "linux", Arch: "amd64"})
	env, _ := json.Marshal(protocol.Envelope{ID: "reg-" + name, Type: "agent_info", Result: res})
	if err := conn.WriteMessage(websocket.TextMessage, env); err != nil {
		t.Fatalf("send agent_info %s: %v", name, err)
	}
	p2105AgentStub(t, conn, name)
	return conn
}

// p2107Exec sends one command to a relayed agent and returns the raw answer
// body (the caller asserts on its shape).
func p2107Exec(t *testing.T, ts *httptest.Server, agentID, command string) string {
	t.Helper()
	_, body := p2105Post(t, ts, "/api/v1/agents/"+agentID+"/exec", `{"command":"`+command+`"}`)
	return body
}
