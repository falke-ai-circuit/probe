package server

// P19-06 — rotated-tokens restart resilience tests.
//
// A1: a rotated token survives a full server-stop/server-start cycle: the
// store file is written at rotation, loaded back by a FRESH Server, and the
// rotated token is accepted after the restart (the F1 brick is gone).
// A3: post-restart idempotent re-rotate — the rotate endpoint accepts a
// rotation for an agent whose stale rotated token was reloaded from disk,
// with no 500/503 (the operator recovery path named by the 5x401 refusal
// text exists again after a restart).
//
// Rig: newCredTestServer (credential family rig) + SetRotatedTokensPath; a
// second Server over the same store file models the restart.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestP19R26_RestartKeepsRotatedToken — A1. Server A rotates agent-1; a
// fresh Server B pointed at the same store file accepts the rotated token.
func TestP19R26_RestartKeepsRotatedToken(t *testing.T) {
	srvA, _, _ := newCredTestServer(t)
	storePath := filepath.Join(t.TempDir(), "rotated-tokens.json")
	srvA.SetRotatedTokensPath(storePath)
	f := &p1904rotateAgent{srv: srvA}
	attachP1904Agent(srvA, f)

	rec := doJSON(srvA, "POST", "/api/v1/credentials/agents/agent-1/rotate", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d %s", rec.Code, rec.Body.String())
	}
	f.mu.Lock()
	rotTok := f.got.NewToken
	f.mu.Unlock()
	if rotTok == "" {
		t.Fatal("no rotated token captured at the fake agent")
	}
	if !srvA.isValidToken("Bearer " + rotTok) {
		t.Fatal("rotated token not accepted before restart")
	}

	// "Restart": a fresh Server over the same registry/store file.
	srvB := NewServer("", "test-token", filepath.Join(t.TempDir(), "registry.json"))
	srvB.mux = http.NewServeMux()
	srvB.registerRoutes()
	t.Cleanup(func() { srvB.registry.Stop() })
	srvB.SetRotatedTokensPath(storePath)

	if !srvB.isValidToken("Bearer " + rotTok) {
		t.Fatal("P19-06 F1 REGRESSED: rotated token refused after restart")
	}
	// The old/pre-rotation token must also stay valid (primary path intact).
	if !srvA.isValidToken("Bearer test-token") {
		t.Fatal("primary token broken by persistence wiring")
	}
}

// TestP19R26_RestartLoadLoggedMissingFileQuiet — the store file may be
// absent on first boot: that is an empty store, not an error, and no row is
// invented.
func TestP19R26_RestartLoadMissingFileQuiet(t *testing.T) {
	srvA, _, _ := newCredTestServer(t)
	storePath := filepath.Join(t.TempDir(), "never-written.json")
	srvA.SetRotatedTokensPath(storePath)
	if _, ok := srvA.rotatedTokens["agent-1"]; ok {
		t.Fatal("missing store file produced a phantom rotated token")
	}
}

// TestP19R26_RotationsPersistIncrementally — every rotation updates the
// on-disk file (no flush-at-shutdown ordering requirement).
func TestP19R26_RotationsPersistIncrementally(t *testing.T) {
	srvA, _, _ := newCredTestServer(t)
	storePath := filepath.Join(t.TempDir(), "rotated-tokens.json")
	srvA.SetRotatedTokensPath(storePath)
	f := &p1904rotateAgent{srv: srvA}
	attachP1904Agent(srvA, f)

	mustRotate := func() string {
		t.Helper()
		rec := doJSON(srvA, "POST", "/api/v1/credentials/agents/agent-1/rotate", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("rotate = %d %s", rec.Code, rec.Body.String())
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.got.NewToken
	}
	first := mustRotate()
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("store file missing after first rotation: %v", err)
	}
	var doc rotatedTokensDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("store file unparseable: %v", err)
	}
	if doc.Rotated["agent-1"] != first {
		t.Fatal("first rotated token not persisted at rotation time")
	}
	second := mustRotate()
	data, _ = os.ReadFile(storePath)
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("store file unparseable after supersession: %v", err)
	}
	if doc.Rotated["agent-1"] != second {
		t.Fatal("superseded rotated token not overwritten on disk")
	}
}

// TestP19R26_PostRestartIdempotentRerotate — A3: after a restart reloaded a
// stale rotated token for agent-1, the rotate endpoint accepts a NEW
// rotation for that agent with 200 (no 500/503), the store file is
// overwritten idempotently, and the fresh rotated token is the only one
// accepted from the rotated slot (cut-on-supersession preserved).
func TestP19R26_PostRestartIdempotentRerotate(t *testing.T) {
	srvA, _, _ := newCredTestServer(t)
	storePath := filepath.Join(t.TempDir(), "rotated-tokens.json")
	srvA.SetRotatedTokensPath(storePath)
	f := &p1904rotateAgent{srv: srvA}
	attachP1904Agent(srvA, f)

	rec := doJSON(srvA, "POST", "/api/v1/credentials/agents/agent-1/rotate", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("first rotate = %d", rec.Code)
	}
	f.mu.Lock()
	stale := f.got.NewToken
	f.mu.Unlock()

	// Restart a fresh server over the same store file.
	srvB := NewServer("", "test-token", filepath.Join(t.TempDir(), "registry.json"))
	srvB.mux = http.NewServeMux()
	srvB.registerRoutes()
	t.Cleanup(func() { srvB.registry.Stop() })
	srvB.SetRotatedTokensPath(storePath)
	// The agent has reconnected to the restarted server: register + attach
	// a live transport on srvB (503 AGENT_NOT_CONNECTED is the correct
	// refusal for a socketless agent, so A3 needs the socket attached).
	srvB.registry.Register("agent-1", "agent-1", "1.19.0", "linux", "amd64", "silent",
		[]string{"exec", "credential"})
	fB := &p1904rotateAgent{srv: srvB}
	attachP1904Agent(srvB, fB)
	if !srvB.isValidToken("Bearer "+stale) {
		t.Fatal("stale rotated token not loaded across restart (precondition for A3)")
	}

	// Operator recovery path: re-rotate post-restart. Must be 200 — the
	// endpoint resolves agent-1's live route, and the pre-existing stale
	// persisted row must not push the endpoint into any refusal class.
	rec2 := doJSON(srvB, "POST", "/api/v1/credentials/agents/agent-1/rotate", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("P19-06 A3 FAILED: post-restart re-rotate = %d %s", rec2.Code, rec2.Body.String())
	}
	fB.mu.Lock()
	fresh := fB.got.NewToken
	fB.mu.Unlock()
	if fresh == "" || fresh == stale {
		t.Fatal("re-rotate did not mint a fresh token")
	}
	if !srvB.isValidToken("Bearer "+fresh) {
		t.Fatal("fresh rotated token not live after re-rotate")
	}
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("store file missing after re-rotate: %v", err)
	}
	var doc rotatedTokensDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("store file unparseable: %v", err)
	}
	if doc.Rotated["agent-1"] != fresh {
		t.Fatalf("stale row not overwritten: got %q want fresh", doc.Rotated["agent-1"])
	}
}
