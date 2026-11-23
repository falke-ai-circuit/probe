package server

// P8-10 — unit coverage for the platform-hygiene surfaces: idempotency journal
// state machine, capability inventory version gating, and the watch marker
// arithmetic. The HTTP-level replay/one-side-effect proof lives in the live
// run recorded in /opt/data/tmp/probe119/P8-10_HYGIENE.md.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestP810IdemClaimReplayAndTTL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idem.json")
	st := newIdempotencyStore(path)

	if _, state, err := st.begin("a1", "k1"); err != nil || state != "new" {
		t.Fatalf("first claim: state=%q err=%v, want new", state, err)
	}
	if _, state, err := st.begin("a1", "k1"); state != "inflight" || err == nil {
		t.Fatalf("second claim while in flight: state=%q err=%v, want inflight error", state, err)
	}
	st.complete("a1", "k1", []byte(`{"exit_code":0}`))

	body, state, err := st.begin("a1", "k1")
	if err != nil || state != "replay" {
		t.Fatalf("replay after complete: state=%q err=%v", state, err)
	}
	if string(body) != `{"exit_code":0}` {
		t.Fatalf("replay body = %s", body)
	}
	// Keys are scoped per agent.
	if _, state, _ := st.begin("a2", "k1"); state != "new" {
		t.Fatalf("same key on another agent must be new, got %q", state)
	}

	// An in-flight claim that survived a restart must be refused, not rerun.
	st2 := newIdempotencyStore(path)
	if _, state, err := st2.begin("a1", "k1"); state != "replay" || err != nil {
		t.Fatalf("persisted done record must replay, got %q %v", state, err)
	}
}

func TestP810IdemInFlightBecomesUnknownAfterRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idem.json")
	st := newIdempotencyStore(path)
	if _, _, err := st.begin("a1", "crash"); err != nil {
		t.Fatal(err)
	}
	st2 := newIdempotencyStore(path)
	_, state, err := st2.begin("a1", "crash")
	if state != "unknown" || err != errIdemStateUnknown {
		t.Fatalf("state=%q err=%v, want unknown", state, err)
	}
}

func TestP810IdemExpiredRecordIsReclaimable(t *testing.T) {
	dir := t.TempDir()
	st := newIdempotencyStore(filepath.Join(dir, "idem.json"))
	if _, _, err := st.begin("a1", "old"); err != nil {
		t.Fatal(err)
	}
	st.complete("a1", "old", []byte(`{"ok":1}`))
	// Age the record past the 24 h TTL.
	st.mu.Lock()
	st.records[idemIndex("a1", "old")].CompletedAt = time.Now().Add(-25 * time.Hour)
	st.mu.Unlock()
	if _, state, _ := st.begin("a1", "old"); state != "new" {
		t.Fatalf("expired key must be reclaimable, got %q", state)
	}
}

func TestP810IdemKeyValidation(t *testing.T) {
	if err := validateIdempotencyKey("ok-key_1"); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}
	if err := validateIdempotencyKey("bad\nkey"); err == nil {
		t.Fatal("control character accepted")
	}
	if err := validateIdempotencyKey(string(make([]byte, 201))); err == nil {
		t.Fatal("201-byte key accepted")
	}
}

func TestP810CapabilityInventoryGatesByAdvertisedSet(t *testing.T) {
	newRec := AgentRecord{AgentID: "n1", Version: "1.19.0",
		Capabilities: []string{"exec", "filesystem", "exec.system", "credential"}}
	inv := buildCapabilityInventory(newRec)
	if inv.ProtocolVersion != "2" {
		t.Fatalf("protocol_version = %q, want 2", inv.ProtocolVersion)
	}
	if !inv.Inventory.Supported["exec.system"] || !inv.Inventory.Supported["credential"] {
		t.Fatal("1.19 markers must be supported when advertised")
	}
	if inv.Inventory.Supported["capture"] {
		t.Fatal("capture not advertised but reported supported")
	}
	if inv.Inventory.LegacyAll {
		t.Fatal("legacy_all set for an advertising agent")
	}

	oldRec := AgentRecord{AgentID: "o1", Version: "1.18.6"}
	oldInv := buildCapabilityInventory(oldRec)
	if !oldInv.Inventory.LegacyAll {
		t.Fatal("empty capability record must report legacy_all")
	}
	if oldInv.ProtocolVersion != "1" {
		t.Fatalf("old agent protocol_version = %q, want 1", oldInv.ProtocolVersion)
	}
	if oldInv.Inventory.Supported["exec.system"] {
		t.Fatal("1.18.6 cannot support exec.system")
	}
	// Absent keys are listed, not omitted.
	found := false
	for _, c := range oldInv.Inventory.Absent {
		if c == "credential" {
			found = true
		}
	}
	if !found {
		t.Fatal("absent capability not enumerated")
	}
}

func TestP810WatchMarkerArithmetic(t *testing.T) {
	buf := []byte("line1\nline2\npartial")
	if got := lastNewline(buf); got != 11 {
		t.Fatalf("lastNewline = %d, want 11", got)
	}
	lines := splitLines(buf[:12])
	if len(lines) != 2 || lines[0] != "line1" || lines[1] != "line2" {
		t.Fatalf("splitLines = %#v", lines)
	}
	if lastNewline([]byte("no newline")) != -1 {
		t.Fatal("lastNewline must be -1 without a newline")
	}
}

// TestP810JournalFileIsPrivate guards the on-disk contract: the journal holds
// command output and must not be world-readable.
func TestP810JournalFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idem.json")
	st := newIdempotencyStore(path)
	if _, _, err := st.begin("a1", "k"); err != nil {
		t.Fatal(err)
	}
	st.complete("a1", "k", []byte(`{"stdout":""}`))
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode = %v, want 0600", fi.Mode().Perm())
	}
}
