package agent

// P19-06 — agent-side legs of the rotation repair.
//
// persistToken is now atomic (tmp + rename); a token file it produces can
// never be observed half-written.
// pickupRotatedToken refuses implausible material (P19R26 material gate) —
// the inversion of TestP19R1_F1_F2_NoRecoveryAfterLocalRotation's F2 step,
// which asserted garbage ADOPTION (the bug).

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestP19R26_GarbagePickupRefused — A2 inversion: the reviewer's probe fed
// "\x00\x01truncated-fragment" through the pickup and asserted adoption; at
// the P19-06 head the same file must be REFUSED (no pickup, current token
// untouched).
func TestP19R26_GarbagePickupRefused(t *testing.T) {
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
		t.Fatalf("P19-06 F2 REGRESSED: garbage adopted as pickup tok=%q ok=%v", tok, ok)
	}
	a.mu.Lock()
	current := a.cfg.Token
	a.mu.Unlock()
	if current != "tok-OLD" {
		t.Fatalf("in-process token clobbered by refused pickup: %q", current)
	}
}

// TestP19R26_GarbageOnlyFileStillExitsTyped — the loop contract survives the
// gate: with ONLY garbage in the token file, repeated 401s must still reach
// the existing 5x401 typed refusal (the bound stays the loudness mechanism;
// the gate must not convert it into a silent loop).
func TestP19R26_GarbageOnlyFileStillExitsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	tf := filepath.Join(t.TempDir(), ".probe-token")
	if err := os.WriteFile(tf, []byte("\x00\x01truncated-fragment"), 0600); err != nil {
		t.Fatal(err)
	}
	a := newP1904Agent(t, tf, "dead-token")
	a.cfg.URL = "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws"
	a.cfg.BackoffMin = time.Millisecond
	a.cfg.BackoffMax = 5 * time.Millisecond
	err := a.runOutbound()
	if err == nil {
		t.Fatal("garbage-only token file: loop did not exit")
	}
	msg := err.Error()
	if !strings.Contains(msg, "refused 5 consecutive times") ||
		!strings.Contains(msg, "401") ||
		!strings.Contains(msg, "refusing to loop silently") {
		t.Fatalf("exit not the typed 401 refusal: %q", msg)
	}
}

// TestP19R26_PersistTokenAtomicNoTmpResidue — persistToken leaves the real
// file intact with 0600 perms and no .tmp residue on success.
func TestP19R26_PersistTokenAtomicNoTmpResidue(t *testing.T) {
	dir := t.TempDir()
	tf := filepath.Join(dir, "token")
	a := &Agent{cfg: Config{TokenFile: tf}}
	if err := a.persistToken("rotated-token-NEW"); err != nil {
		t.Fatalf("persistToken: %v", err)
	}
	got, err := os.ReadFile(tf)
	if err != nil || string(got) != "rotated-token-NEW" {
		t.Fatalf("token file = %q err=%v", got, err)
	}
	if _, serr := os.Stat(tf + ".tmp"); !os.IsNotExist(serr) {
		t.Fatalf("tmp residue survived the rename: %v", serr)
	}
	fi, err := os.Stat(tf)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Fatalf("token file perms = %v, want 0600", perm)
	}
}
