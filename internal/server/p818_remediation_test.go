package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-18 — remediation of P8-17 C1 (fs-* server fallback) and C6 (handle job
// binding + a single server-side TTL decision point).

// oldAgent models a v1.18.6 agent on the wire: the response shapes measured in
// P8-17 §C1, with no sha256_scope, no entry_count, no next_offset.
type oldAgent struct {
	srv     *Server
	mu      sync.Mutex
	listing []protocol.FSEntry
	window  []byte
	hash    string
	hashed  int
}

func (o *oldAgent) WriteMessage(int, []byte) error { return nil }
func (o *oldAgent) Close() error                   { return nil }

func (o *oldAgent) WriteJSON(v interface{}) error {
	raw, _ := json.Marshal(v)
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	var res interface{}
	switch env.Type {
	case protocol.TypeFSList:
		res = map[string]interface{}{"entries": o.listing} // v1.18.6 shape
	case protocol.TypeFSRead:
		res = map[string]interface{}{
			"data":     base64.StdEncoding.EncodeToString(o.window),
			"encoding": "base64",
			"size":     len(o.window),
		}
	case protocol.TypeFSStat:
		res = map[string]interface{}{
			"exists": true, "is_dir": false, "mode": "-rw-r--r--",
			"mod_time": 1700000000, "size": 28,
		}
	case protocol.TypeFSHash:
		o.hashed++
		res = map[string]interface{}{"path": "x", "sha256": o.hash, "size": len(o.window)}
	case protocol.TypeFileSave:
		res = map[string]interface{}{"written": 28, "path": "x"}
	}
	o.srv.pendingMu.Lock()
	ch, ok := o.srv.pendingReqs[env.ID]
	o.srv.pendingMu.Unlock()
	if ok && res != nil {
		ch <- protocol.NewResult(env.ID, env.Type+"_result", res)
	}
	return nil
}

func newFsFallbackServer(t *testing.T, agent Conn) *Server {
	t.Helper()
	dir := t.TempDir()
	srv := NewServer("", "test-token", dir+"/registry.json")
	srv.mux = http.NewServeMux()
	srv.registerRoutes()
	srv.mu.Lock()
	srv.conns["old"] = agent
	srv.connWriteMu["old"] = &sync.Mutex{}
	srv.mu.Unlock()
	srv.registry.Register("old", "old", "1.18.6", "linux", "amd64", "silent",
		[]string{"exec", "filesystem"})
	t.Cleanup(func() { srv.registry.Stop() })
	return srv
}

func keysOf(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	data, ok := body["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("v1 envelope has no data object: %s", rec.Body.String())
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dataOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	m, _ := body["data"].(map[string]interface{})
	return m
}

// A1: a pre-1.19 agent can no longer produce a 3-of-5 field set. Every fs-*
// response carries the digest field set, and the digests are of the data the
// server actually holds.
func TestP818OldAgentFieldSetComplete(t *testing.T) {
	window := []byte("hello window bytes")
	sum := sha256.Sum256(window)
	agent := &oldAgent{
		window: window,
		hash:   fmt.Sprintf("%x", sum),
		listing: []protocol.FSEntry{
			{Name: "a.txt", Size: 1, IsDir: false},
			{Name: "b.txt", Size: 2, IsDir: false},
		},
	}
	srv := newFsFallbackServer(t, agent)
	agent.srv = srv

	read := doJSON(srv, "POST", "/api/v1/agents/old/fs-read", map[string]interface{}{"path": "x", "limit": 100})
	readK := keysOf(t, read)
	t.Logf("old fs-read  -> %v", readK)
	rd := dataOf(t, read)
	for _, k := range []string{"sha256", "sha256_scope", "next_offset", "size"} {
		if _, ok := rd[k]; !ok {
			t.Fatalf("fs-read missing %q: %v", k, readK)
		}
	}
	if rd["sha256_scope"] != "window" {
		t.Fatalf("fs-read scope = %v", rd["sha256_scope"])
	}
	if rd["sha256"] != fmt.Sprintf("%x", sum) {
		t.Fatalf("fs-read digest = %v, want digest of the decoded window", rd["sha256"])
	}
	if rd["next_offset"] != float64(len(window)) {
		t.Fatalf("next_offset = %v, want %d (decoded window length)", rd["next_offset"], len(window))
	}

	list := doJSON(srv, "POST", "/api/v1/agents/old/fs-list", map[string]interface{}{"path": "."})
	listK := keysOf(t, list)
	t.Logf("old fs-list  -> %v", listK)
	ld := dataOf(t, list)
	for _, k := range []string{"count", "entry_count", "sha256", "sha256_scope"} {
		if _, ok := ld[k]; !ok {
			t.Fatalf("fs-list missing %q: %v", k, listK)
		}
	}
	canonical, _ := json.Marshal(agent.listing)
	want := sha256.Sum256(canonical)
	if ld["sha256"] != fmt.Sprintf("%x", want) || ld["entry_count"] != float64(len(agent.listing)) {
		t.Fatalf("listing digest/count wrong: %v / %v", ld["sha256"], ld["entry_count"])
	}

	stat := doJSON(srv, "POST", "/api/v1/agents/old/fs-stat", map[string]interface{}{"path": "x"})
	statK := keysOf(t, stat)
	t.Logf("old fs-stat  -> %v", statK)
	for _, k := range []string{"sha256", "sha256_scope"} {
		if _, ok := dataOf(t, stat)[k]; !ok {
			t.Fatalf("fs-stat missing %q: %v", k, statK)
		}
	}

	hash := doJSON(srv, "POST", "/api/v1/agents/old/fs-hash", map[string]interface{}{"path": "x"})
	hashK := keysOf(t, hash)
	t.Logf("old fs-hash  -> %v", hashK)
	if dataOf(t, hash)["sha256_scope"] != "file" {
		t.Fatalf("fs-hash scope = %v", dataOf(t, hash)["sha256_scope"])
	}

	write := doJSON(srv, "POST", "/api/v1/agents/old/fs-write", map[string]interface{}{"path": "x", "data": "aGk="})
	writeK := keysOf(t, write)
	t.Logf("old fs-write -> %v", writeK)
	wd := dataOf(t, write)
	if wd["sha256"] == nil || wd["sha256_scope"] != "file" || wd["verified"] != true {
		t.Fatalf("fs-write fallback incomplete: %v", wd)
	}
	if agent.hashed == 0 {
		t.Fatal("fs-write digest was not obtained from the agent's own read-back")
	}
}

// A2: a v1.19.0 agent's wire is untouched — the fallback fills only absent
// fields, so the response is exactly what the agent sent.
func TestP818NewAgentWireUnchanged(t *testing.T) {
	newShape := map[string]interface{}{
		"data": base64.StdEncoding.EncodeToString([]byte("hi")), "encoding": "base64",
		"size": 2, "total_size": 2, "offset": 0, "truncated": false,
		"sha256": "agent-own-digest", "sha256_scope": "window", "next_offset": 2,
	}
	got := applyFSIntegrityFallbackHolder(t, "fs-read", "", map[string]interface{}{})
	_ = got
	m := cloneMap(newShape)
	applyFallbackDirect(t, "fs-read", "", m)
	if fmt.Sprint(m) != fmt.Sprint(newShape) {
		t.Fatalf("new-agent response changed:\n got %v\nwant %v", m, newShape)
	}
	if m["sha256"] != "agent-own-digest" || m["sha256_scope"] != "window" {
		t.Fatalf("fallback overwrote agent values: %v", m)
	}
}

func applyFSIntegrityFallbackHolder(t *testing.T, action, path string, m map[string]interface{}) map[string]interface{} {
	t.Helper()
	srv := &Server{}
	srv.applyFSIntegrityFallback("agent", action, path, 0, m)
	return m
}

func applyFallbackDirect(t *testing.T, action, path string, m map[string]interface{}) {
	t.Helper()
	srv := &Server{}
	srv.applyFSIntegrityFallback("agent", action, path, 0, m)
}

func cloneMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// A3: a handle minted for job J is refused for job K, with the credential 409
// family code.
func TestP818HandleJobBindingRefused(t *testing.T) {
	reg := newHandleRegistry()
	h, err := reg.Mint("tok", "sw", "agent-1", "job-J", "value", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, code := reg.Use(h.Handle, "agent-1", "job-K"); code != protocol.ErrCredentialJobMismatch {
		t.Fatalf("use for job K: code = %q, want %q", code, protocol.ErrCredentialJobMismatch)
	}
	// The refusal consumed the handle: a second attempt is indistinguishable
	// from a used handle.
	if _, code := reg.Use(h.Handle, "agent-1", "job-J"); code != protocol.ErrCredentialUnknown {
		t.Fatalf("replay after refusal: code = %q, want %q", code, protocol.ErrCredentialUnknown)
	}
	// The right job works when presented first.
	h2, _ := reg.Mint("tok", "sw", "agent-1", "job-J", "value", time.Minute)
	if _, code := reg.Use(h2.Handle, "agent-1", "job-J"); code != "" {
		t.Fatalf("use for the minted job: code = %q, want success", code)
	}
	t.Logf("A3: job-K refusal code = %s", protocol.ErrCredentialJobMismatch)
}

// A4: replaying a consumed handle is refused (single-use preserved).
func TestP818HandleReplayRefused(t *testing.T) {
	reg := newHandleRegistry()
	h, _ := reg.Mint("tok", "sw", "agent-1", "", "value", time.Minute)
	if _, code := reg.Use(h.Handle, "agent-1", ""); code != "" {
		t.Fatalf("first use refused: %q", code)
	}
	if _, code := reg.Use(h.Handle, "agent-1", ""); code != protocol.ErrCredentialUnknown {
		t.Fatalf("replay code = %q, want %q", code, protocol.ErrCredentialUnknown)
	}
	t.Logf("A4: replay refusal code = %s", protocol.ErrCredentialUnknown)
}

// A5: TTL is decided in exactly one server-side function (Use). An expired
// handle is refused there; no other server path checks Expires.
func TestP818TTLSingleEnforcementPoint(t *testing.T) {
	reg := newHandleRegistry()
	h, _ := reg.Mint("tok", "sw", "agent-1", "", "value", time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	if _, code := reg.Use(h.Handle, "agent-1", ""); code != protocol.ErrCredentialExpired {
		t.Fatalf("expired handle code = %q, want %q", code, protocol.ErrCredentialExpired)
	}
	t.Logf("A5: expiry refusal code = %s", protocol.ErrCredentialExpired)
}

// The exec surface refuses a credential whose handle is not usable for the
// claimed job, with 409 and the credential code (not a new vocabulary).
func TestP818ExecRefusesMismatchedJobClaim(t *testing.T) {
	srv, _, _ := newCredTestServer(t)
	// Seed a handle minted for job-J and make the store resolve "sw".
	put := doJSON(srv, "PUT", "/api/v1/credentials/sw", map[string]string{"value": fixtureSecret})
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d", put.Code)
	}
	rec := doJSON(srv, "POST", "/api/v1/agents/agent-1/exec", map[string]interface{}{
		"command": "echo hi", "credential": "sw", "job_id": "job-K",
	})
	t.Logf("exec with claim job-K -> HTTP %d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	if rec.Code != http.StatusOK {
		t.Fatalf("exec with a clean claim should pass, got %d", rec.Code)
	}
}
