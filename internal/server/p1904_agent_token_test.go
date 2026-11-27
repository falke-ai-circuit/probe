package server

// P19-04 — server-side per-agent token rotation tests.
//
// Acceptance 1: rotate one agent end-to-end in-suite — the rotated token is
// captured in transit fake-side, is accepted on reconnect, the OLD token
// survives under the one-slot grace (cut = supersession), and every state
// change is loud (audit line on disk, fp-only).
// Acceptance 2: the credentials surface — GET /api/v1/credentials[...], the
// rotate RESPONSE, and every refusal body — carries zero token occurrences
// (P17-03's TokenNeverInGetBodies inverted for the credentials family).

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

func itoaP1904(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func tokenAlikeRegexP1904() *regexp.Regexp {
	return regexp.MustCompile(`"([^"]*)"\s*:\s*"[0-9a-f]{8}"`)
}

// p1904rotateAgent is a Conn that records the token_rotate envelope instead
// of decrypting a credential handle. It models the agent contract at exactly
// the rotation seam: server sends auth_refresh with a new_token; the agent
// applies it (captured here for assertions) and would reconnect with it.
type p1904rotateAgent struct {
	srv  *Server
	mu   sync.Mutex
	got  protocol.TokenRotateParams
	sent int
}

func (f *p1904rotateAgent) WriteMessage(int, []byte) error { return nil }
func (f *p1904rotateAgent) Close() error                   { return nil }
func (f *p1904rotateAgent) WriteJSON(v interface{}) error {
	raw, _ := json.Marshal(v)
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	if env.Type == protocol.TypeAuthRefresh || env.Type == protocol.TypeTokenRotate {
		f.mu.Lock()
		f.sent++
		json.Unmarshal(env.Params, &f.got)
		f.mu.Unlock()
	}
	return nil
}

// attachP1904Agent wires the capture fake as agent-1's transport.
func attachP1904Agent(srv *Server, f *p1904rotateAgent) {
	srv.mu.Lock()
	srv.conns["agent-1"] = f
	srv.connWriteMu["agent-1"] = &sync.Mutex{}
	srv.mu.Unlock()
}

// TestP1904_RotateAgentEndToEnd — Acceptance 1.
func TestP1904_RotateAgentEndToEnd(t *testing.T) {
	srv, _, auditPath := newCredTestServer(t)
	f := &p1904rotateAgent{srv: srv}
	// Replace the credential fake with the rotation capture fake: same rig,
	// different seam in view.
	srv.conns["agent-1"] = f
	attachP1904Agent(srv, f)

	rec := doJSON(srv, "POST", "/api/v1/credentials/agents/agent-1/rotate", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	f.mu.Lock()
	newTok := f.got.NewToken
	sent := f.sent
	f.mu.Unlock()
	if sent != 1 || newTok == "" {
		t.Fatalf("agent received %d rotate envelopes (new token %q)", sent, newTok)
	}
	if strings.Contains(body, newTok) || strings.Contains(body, "test-token") {
		t.Fatalf("rotate response carried token material: %s", body)
	}

	var resp struct {
		Data AgentTokenRotationResult `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	d := resp.Data
	if !d.Delivered || d.TokenFP != tokenFingerprint(newTok) {
		t.Fatalf("result mismatch: delivered=%v fp=%s want %s", d.Delivered, d.TokenFP, tokenFingerprint(newTok))
	}
	if d.Transport != LivenessSourceWS {
		t.Fatalf("transport = %q, want %q", d.Transport, LivenessSourceWS)
	}

	// New token accepted on reconnect (rotatedTokens slot), OLD token still
	// accepted — the grace decision (stated in agent_token_api.go): cut only
	// on supersession, because a mid-upgrade cut is row M-1's outage.
	if !srv.isValidToken("Bearer " + newTok) {
		t.Fatal("rotated token not accepted after rotation")
	}
	if !srv.isValidToken("Bearer test-token") {
		t.Fatal("old token cut instead of kept under one-slot grace")
	}

	// Supersede: a second rotation cuts the FIRST rotated token.
	rec2 := doJSON(srv, "POST", "/api/v1/credentials/agents/agent-1/rotate", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second rotate = %d", rec2.Code)
	}
	if srv.isValidToken("Bearer " + newTok) {
		t.Fatal("superseded rotated token still accepted — cut-on-supersession broken")
	}
	f.mu.Lock()
	second := f.got.NewToken
	f.mu.Unlock()
	if !srv.isValidToken("Bearer "+second) || second == newTok {
		t.Fatal("second rotated token not live")
	}

	// Loudness: the audit trail names the rotation with fingerprints only.
	raw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("audit read: %v", err)
	}
	audit := string(raw)
	if !strings.Contains(audit, "credential.rotate") {
		t.Fatalf("no credential.rotate audit line: %s", audit)
	}
	if strings.Contains(audit, newTok) || strings.Contains(audit, second) {
		t.Fatalf("audit carried plaintext token material")
	}
}

// TestP1904_RotateUnknownAgentRefused — typed refusal for a dead identity:
// 503 AGENT_NOT_CONNECTED, nothing minted, loud refusal path (audit).
func TestP1904_RotateUnknownAgentRefused(t *testing.T) {
	srv, _, _ := newCredTestServer(t)
	rec := doJSON(srv, "POST", "/api/v1/credentials/agents/ghost-7/rotate", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ghost rotate = %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), RefusalAgentTokenRotateConnected) {
		t.Fatalf("refusal not typed: %s", rec.Body.String())
	}
}

// TestP1904_CredentialsGetBodiesNeverCarryToken — Acceptance 2. Captured:
// every GET on the credentials family (list, detail, detail-404, rotated
// names again), the rotate RESPONSE, and both refusal shapes. The contract
// is zero occurrences of every token that ever existed in the test.
func TestP1904_CredentialsGetBodiesNeverCarryToken(t *testing.T) {
	srv, _, _ := newCredTestServer(t)
	f := &p1904rotateAgent{srv: srv}
	attachP1904Agent(srv, f)

	const sidetok = "p1904-side-cred-f00d"

	bodies := map[string]string{}
	add := func(label, method, path string, body interface{}) {
		t.Helper()
		rec := doJSON(srv, method, path, body)
		bodies[label] = "HTTP " + rec.Header().Get("Content-Encoding") + " | " + rec.Body.String()
	}

	// Populate a credential (its PUT response joins the capture, its value
	// must never come back through any of these bodies).
	add("put cred", "PUT", "/api/v1/credentials/sw-a", map[string]string{"value": sidetok})
	add("list", "GET", "/api/v1/credentials", nil)
	add("detail", "GET", "/api/v1/credentials/sw-a", nil)
	add("detail-404", "GET", "/api/v1/credentials/nope", nil)

	// Rotate: response + the unknown-agent refusal ride the same capture.
	add("rotate ok", "POST", "/api/v1/credentials/agents/agent-1/rotate", nil)
	add("rotate ghost", "POST", "/api/v1/credentials/agents/ghost-7/rotate", nil)

	// After the rotation the GETs are captured AGAIN: the rotated era must be
	// as clean as the pre-rotation one.
	add("list post-rotate", "GET", "/api/v1/credentials", nil)
	add("detail post-rotate", "GET", "/api/v1/credentials/sw-a", nil)

	f.mu.Lock()
	rotTok := f.got.NewToken
	f.mu.Unlock()

	secrets := []string{"test-token", sidetok, rotTok}
	var hits []string
	for label, b := range bodies {
		for _, sec := range secrets {
			n := strings.Count(b, sec)
			if n > 0 {
				hits = append(hits, label+" carries a token plaintext x"+itoaP1904(n))
			}
		}
		for _, m := range tokenAlikeRegexP1904().FindAllStringSubmatch(b, -1) {
			if m[1] != "token_fp" {
				hits = append(hits, label+" carries a fp-shaped digest under key "+m[1])
			}
		}
	}
	if len(hits) > 0 {
		t.Fatalf("credentials surface leaked token material: %v\n\nRAW BODIES:\n%v", hits, bodies)
	}
	t.Logf("P1904-BODIES-OK %d captured bodies, zero token occurrences across %d secrets", len(bodies), len(secrets))
}

// TestP1904_RotateRespectsAuthOptionalOff — with requireAPIAuth ON and no
// valid bearer, the rotate endpoint refuses with 401 (the write path is at
// least as guarded as the read paths in the same family).
func TestP1904_RotateRespectsRequireAPIAuth(t *testing.T) {
	srv, _, _ := newCredTestServer(t)
	srv.SetRequireAPIAuth(true)
	f := &p1904rotateAgent{srv: srv}
	attachP1904Agent(srv, f)

	rec := doJSON(srv, "POST", "/api/v1/credentials/agents/agent-1/rotate", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated rotate = %d %s", rec.Code, rec.Body.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sent != 0 {
		t.Fatal("rotation envelope sent despite 401")
	}
}
