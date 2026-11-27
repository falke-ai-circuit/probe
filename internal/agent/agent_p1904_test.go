package agent

// P19-04 — client-side token-rotation pickup tests.
//
// Contract under test: a 401 auth refusal on the reconnect loop is never a
// silent retry of the same dead token. The agent re-reads its token file; a
// changed file is adopted immediately (typed log line), an unchanged one is
// not, and after authRefusalMaxAttempts consecutive refusals the loop exits
// with a typed, quoted refusal instead of looping forever.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newP1904Agent(t *testing.T, tokenFile string, token string) *Agent {
	t.Helper()
	return New(Config{
		URL:       "ws://127.0.0.1:1/ws",
		Mode:      "outbound",
		Token:     token,
		TokenFile: tokenFile,
	})
}

// writeTokenFile writes the token file and touches its mtime explicitly so
// successive writes are distinguishable even on coarse-mtime filesystems.
func writeTokenFile(t *testing.T, path, tok string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(tok), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	fut := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, fut, fut); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// TestP1904_PickupRotatedTokenAdoptsChangedFile: a token file whose content
// changed since startup is picked up on the next check.
func TestP1904_PickupRotatedTokenAdoptsChangedFile(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, ".probe-token")
	a := newP1904Agent(t, tf, "dead-token")

	// Recorded baseline: no pickup before anything changed.
	if tok, ok := a.pickupRotatedToken(); ok {
		t.Fatalf("unexpected pickup before any change: %q ok=%v", tok, ok)
	}
	writeTokenFile(t, tf, "rotated-token-NEW")
	tok, ok := a.pickupRotatedToken()
	if !ok || tok != "rotated-token-NEW" {
		t.Fatalf("changed token file not adopted: tok=%q ok=%v", tok, ok)
	}
	// Second check with no further change: no pickup (no reset loop).
	if _, ok := a.pickupRotatedToken(); ok {
		t.Fatal("unchanged token file registered as a pickup")
	}
}

// TestP1904_PickupRotatedTokenIgnoresSameContent: a re-write with identical
// content is NOT a pickup — the dead-token retry counter must keep counting.
func TestP1904_PickupRotatedTokenIgnoresSameContent(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, ".probe-token")
	writeTokenFile(t, tf, "dead-token")
	a := newP1904Agent(t, tf, "dead-token")
	if tok, ok := a.pickupRotatedToken(); ok {
		t.Fatalf("identical content registered as pickup: %q", tok)
	}
}

// TestP1904_PickupNoTokenFileIsQuietFalse: no file, no error, no pickup.
func TestP1904_PickupNoTokenFileIsQuietFalse(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, "absent.token")
	a := newP1904Agent(t, tf, "dead-token")
	if tok, ok := a.pickupRotatedToken(); ok || tok != "" {
		t.Fatalf("absent file produced a pickup: tok=%q ok=%v", tok, ok)
	}
}

// TestP1904_PickupNoTokenFileConfigured: TokenFile empty → no pickup, ever.
func TestP1904_PickupNoTokenFileConfigured(t *testing.T) {
	a := newP1904Agent(t, "", "dead-token")
	if _, ok := a.pickupRotatedToken(); ok {
		t.Fatal("empty TokenFile produced a pickup")
	}
}

// TestP1904_UnauthorizedDialErrClassified: the 401-shape classification used
// by runOutbound. Network faults are NOT auth refusals; ErrBadHandshake (the
// only server-rejection shape gorilla surfaces through protocol.Dial) is.
func TestP1904_UnauthorizedDialErrClassified(t *testing.T) {
	if isUnauthorizedDialErr(nil) {
		t.Fatal("nil error classified as auth refusal")
	}
	if !isUnauthorizedDialErr(websocket.ErrBadHandshake) {
		t.Fatal("ErrBadHandshake not classified as auth refusal")
	}
	if isUnauthorizedDialErr(errors.New("connection refused")) {
		t.Fatal("plain network error classified as auth refusal")
	}
}

// TestP1904_RefusalBoundQuotedExact: the loop's typed refusal names the
// attempt count, the 401, the token file, and refuses to loop — the exact
// operator-facing string the unit contract quotes. A reachable server that
// answers the upgrade with 401 is the live shape; a refused TCP dial is
// NOT (proven by the flip in this test's sibling).
func TestP1904_RefusalBoundQuotedExact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	tf := filepath.Join(t.TempDir(), ".probe-token")
	a := newP1904Agent(t, tf, "dead-token")
	a.cfg.URL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	a.cfg.BackoffMin = time.Millisecond
	a.cfg.BackoffMax = 5 * time.Millisecond
	// Preseed one below the bound: the loop increments BEFORE the check, so
	// the next 401 is refusal #5 and must exit.
	a.authRefusals = authRefusalMaxAttempts - 1
	err := a.runOutbound()
	if err == nil {
		t.Fatal("bound reached but runOutbound returned nil (loop continued?)")
	}
	msg := err.Error()
	if !strings.Contains(msg, "refused 5 consecutive times") ||
		!strings.Contains(msg, "401") ||
		!strings.Contains(msg, tf) ||
		!strings.Contains(msg, "refusing to loop silently") {
		t.Fatalf("refusal text missing required parts: %q", msg)
	}
}

// TestP1904_RefusalCounterCountsAcross401s: starting from zero, the loop
// must exit after exactly authRefusalMaxAttempts consecutive 401 refusals
// (typed error), never loop forever.
func TestP1904_RefusalCounterCountsAcross401s(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	tf := filepath.Join(t.TempDir(), ".probe-token")
	a := newP1904Agent(t, tf, "dead-token")
	a.cfg.URL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	a.cfg.BackoffMin = time.Millisecond
	a.cfg.BackoffMax = 2 * time.Millisecond
	err := a.runOutbound()
	if err == nil {
		t.Fatal("loop never hit the refusal bound")
	}
	if !strings.Contains(err.Error(), "refused 5 consecutive times") {
		t.Fatalf("wrong refusal: %q", err)
	}
}
