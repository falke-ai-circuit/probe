package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// P19R1 F3: counter reset boundary. Live httptest peer alternating 401/200.
func TestP19R1_F3_401sAcrossReconnectsNeverExit(t *testing.T) {
	var hits int64
	upgraded := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&hits, 1)
		if n%2 == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Real reset boundary: perform an actual WebSocket upgrade, then drop
		// the socket. Its ErrUnexpectedEOF is a network-class failure, never
		// isUnauthorizedDialErr — so the 401 counter hits the success path.
		var up websocket.Upgrader
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		select {
		case upgraded <- struct{}{}:
		default:
		}
		c.Close()
	}))
	defer srv.Close()

	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("tok-A"), 0600); err != nil {
		t.Fatal(err)
	}

	a := &Agent{cfg: Config{URL: "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/ws", Token: "dead", TokenFile: tf, BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond}, debugMgr: newDebugManager()}

	done := make(chan error, 1)
	go func() { done <- a.runOutbound() }()

	// Expected shape: 401 (refusal counter +1) then upgrade+drop (counter
	// reset by the success path, agent.go:472) forever. A successful upgrade
	// is the reset boundary itself — never a failure. The falsifying case is
	// the EXIT firing despite never-5-consecutive refusals.
	select {
	case err := <-done:
		t.Fatalf("F3: alternating 401/upgrade peer made the P19-04 bound fire: %v", err)
	case <-time.After(6 * time.Second):
	}
	n := atomic.LoadInt64(&hits)
	if n < 4 {
		t.Fatalf("F3: expected repeated reconnects against alternator, got %d dials", n)
	}
	t.Logf("F3-EVID alternating peer survived %d dials (mixed 401s + upgrade-reset) without the 5-refusal exit", n)
}

// P19R1 F3b: bound only fires when the peer refuses consecutively.
func TestP19R1_F3b_Consecutive401sExitAfter5(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("tok-A"), 0600); err != nil {
		t.Fatal(err)
	}

	a := &Agent{cfg: Config{URL: "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/ws", Token: "dead", TokenFile: tf, BackoffMin: 10 * time.Millisecond, BackoffMax: 20 * time.Millisecond}, debugMgr: newDebugManager()}

	done := make(chan error, 1)
	go func() { done <- a.runOutbound() }()
	select {
	case err := <-done:
		if !strings.Contains(err.Error(), "authentication refused") || !strings.Contains(err.Error(), "401") {
			t.Fatalf("expected typed 401 refusal, got: %v", err)
		}
		t.Logf("F3b-EVID consecutive-401 bound fires: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("5x401 local peer did not exit within 10s")
	}
}

// P19R1 F1/F2: once handleTokenRotate fires, the in-process old token is
// gone — recovery against a server that lost rotatedTokens cannot succeed.
// (Direct analogue of the restart-orphan path; handleTokenRotate is the
// production line agent.go:1526-1550.)
//
// P19-06 SUPERSESSION (named per the brief's A2 decision): the F2 GARBAGE-
// ADOPTION step below was the BUG as reviewer-measured — pickup adopted
// "\x00\x01truncated-fragment". P19-06 adds the material gate
// (protocol.IsPlausibleToken), so a garbage file is now REFUSED. This test
// keeps its F1 network half (the typed 5x401 exit) but the garbage steps are
// superseded by TestP19R26_GarbagePickupRefused in p19r26_token_resilience_test.go;
// the plausible-token pickup adversarial case is re-tested there via a valid
// file write (TestP1904_PickupRotatedTokenAdoptsChangedFile still covers the
// positive adoption path).
func TestP19R1_F1_F2_NoRecoveryAfterLocalRotation(t *testing.T) {
	// F2 half, superseded expectation: the same garbage file the reviewer
	// used must now be REFUSED, not adopted (inversion documented above).
	a := &Agent{cfg: Config{Token: "tok-OLD"}}
	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	if err := os.WriteFile(tf, []byte("\x00\x01truncated-fragment"), 0600); err != nil {
		t.Fatal(err)
	}
	a.cfg.TokenFile = tf
	lastPersistedTokenMtime = time.Time{} // baseline: mtime will differ from zero value
	tok, ok := a.pickupRotatedToken()
	if ok || tok != "" {
		t.Fatalf("P19-06: expected garbage refusal at this head, got ok=%v tok=%q", ok, tok)
	}
	a.mu.Lock()
	current := a.cfg.Token
	a.mu.Unlock()
	if current != "tok-OLD" {
		t.Fatalf("P19-06: refused pickup still clobbered the in-process token: %q", current)
	}
	t.Logf("F2-EVID (superseded P19-06): garbage pickup now REFUSED by the material gate")

	// F1 half: refusing server -> 5 401s -> typed exit, and
	// NO returned opportunity to restore "tok-OLD" exists in the loop.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	a.cfg.URL = "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/ws"
	a.mu.Lock()
	a.cfg.BackoffMin = 10 * time.Millisecond
	a.cfg.BackoffMax = 20 * time.Millisecond
	a.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- a.runOutbound() }()
	select {
	case err := <-done:
		t.Logf("F1-EVID botched token + lost rotatedTokens = unrecoverable locally: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no exit after adoption of dead token")
	}
}
