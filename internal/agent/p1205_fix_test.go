package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/lifecycle"
)

// TestP1205StockAgentAdvertisesUpdate is the P12-04 F1 acceptance: a stock
// agent — no explicit capability list — must advertise "update", because the
// server refuses agent_update for a node that does not, and the refusal used to
// be answered as a success. Without "update" every shipped agent is
// un-updatable through the API.
func TestP1205StockAgentAdvertisesUpdate(t *testing.T) {
	a := New(Config{Name: "stock-agent"})
	got := a.advertisedCapabilities()

	found := false
	for _, c := range got {
		if c == CapUpdate {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("stock agent advertises %v — no %q, so the server's capability gate refuses every agent_update (P12-04 F1)", got, CapUpdate)
	}

	// The explicit list is operator policy and is not rewritten; this documents
	// the boundary: a config that names its capabilities must include "update".
	policy := New(Config{Name: "policy-agent", Capabilities: []string{"exec"}})
	for _, c := range policy.advertisedCapabilities() {
		if c == CapUpdate {
			t.Fatalf("explicit capability list gained %q; policy lists must be honoured verbatim (only exec.system is injected)", CapUpdate)
		}
	}
}

// TestP1205PromotionRepublishesCanonicalIdentity is the F2 acceptance: after a
// committed swap the run-state record must carry the canonical agent name and
// the canonical executable path, not "-canary"/"<canonical>.next" (a path that
// no longer exists once the rename has moved that image to the canonical name).
func TestP1205PromotionRepublishesCanonicalIdentity(t *testing.T) {
	dir := t.TempDir()
	statusPath := filepath.Join(dir, "status.json")
	canonical := filepath.Join(dir, "probe-agent")
	if err := os.WriteFile(canonical, []byte("new-version-image"), 0o755); err != nil {
		t.Fatalf("write canonical: %v", err)
	}

	a := New(Config{Name: "p1204-agent-canary", CanaryMode: true, Mode: "connect", StatusPath: statusPath})
	retire := a.startStatusFile()
	defer retire()

	before, err := lifecycle.Read(statusPath)
	if err != nil {
		t.Fatalf("read status before promotion: %v", err)
	}
	if before.Name != "p1204-agent-canary" {
		t.Fatalf("precondition: record name = %q, want the canary name", before.Name)
	}

	a.promoteToCanonical(canonical)

	after, err := lifecycle.Read(statusPath)
	if err != nil {
		t.Fatalf("read status after promotion: %v", err)
	}
	if after.Name != "p1204-agent" {
		t.Fatalf("status record name = %q after promotion, want the canonical name %q (F2)", after.Name, "p1204-agent")
	}
	if after.Executable != canonical {
		t.Fatalf("status record executable = %q after promotion, want the canonical path %q (F2)", after.Executable, canonical)
	}
	if strings.HasSuffix(after.Executable, ".next") {
		t.Fatalf("status record still points at the staging path %q (F2)", after.Executable)
	}

	// The heartbeat shares the record: a later tick must not restore the
	// startup values captured before the swap.
	a.mu.Lock()
	pub := a.statusPub
	a.mu.Unlock()
	if pub == nil {
		t.Fatal("status publisher not registered; the heartbeat would not share the corrected record")
	}
	if err := pub.heartbeat(time.Now()); err != nil {
		t.Fatalf("heartbeat after promotion: %v", err)
	}
	beat, err := lifecycle.Read(statusPath)
	if err != nil {
		t.Fatalf("read status after heartbeat: %v", err)
	}
	if beat.Name != "p1204-agent" || beat.Executable != canonical {
		t.Fatalf("heartbeat reverted the identity: name=%q executable=%q, want %q / %q",
			beat.Name, beat.Executable, "p1204-agent", canonical)
	}

	if a.cfg.CanaryMode {
		t.Fatal("canary mode still set after promotion; the reconnect loop would treat the process as an aborted canary")
	}
	if strings.HasSuffix(a.cfg.Name, canaryNameSuffix) {
		t.Fatalf("canonical name = %q, still carries the canary suffix", a.cfg.Name)
	}
}

// exitRecorder stands in for the process exit so the test can observe the
// decision without killing the test binary.
type exitRecorder struct {
	called bool
	code   int
}

type closeRecorder struct{ closed bool }

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

// TestP1205FailedSwapTakesCanaryExitPath is the F4 acceptance at the swap head:
// a promote that cannot be verified must end the canary process, not leave it
// connected and registered as a second active agent beside the rolled-back one.
func TestP1205FailedSwapTakesCanaryExitPath(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "probe-agent")
	staged := canonical + ".next"
	if err := os.WriteFile(canonical, []byte("old-version-image"), 0o755); err != nil {
		t.Fatalf("write canonical: %v", err)
	}
	if err := os.WriteFile(staged, []byte("new-version-image"), 0o755); err != nil {
		t.Fatalf("write staged: %v", err)
	}

	// Point the swap at this scratch directory, and make the promote rename
	// (and therefore the restore rename) fail — a real failure on a real path.
	oldExec := canaryExecutable
	canaryExecutable = func() (string, error) { return staged, nil }
	t.Cleanup(func() { canaryExecutable = oldExec })

	oldRename := swapRename
	swapRename = func(from, to string) error {
		if to == canonical {
			return errors.New("forced rename failure")
		}
		return oldRename(from, to)
	}
	t.Cleanup(func() { swapRename = oldRename })

	oldExit := canaryExit
	exit := &exitRecorder{code: -1}
	canaryExit = func(code int) { exit.called, exit.code = true, code }
	t.Cleanup(func() { canaryExit = oldExit })

	a := New(Config{Name: "p1204-rb-canary", CanaryMode: true})
	conn := &closeRecorder{}

	// The exact branch runCanaryCommit takes when swapAndKillOld reports an
	// error: swap first (it fails), then abandon.
	if _, err := a.swapAndKillOld(); err == nil {
		t.Fatal("swap reported success although every promote/restore rename failed")
	}
	a.abandonFailedCanary(conn, errors.New("SWAP FAILED"))

	if !exit.called {
		t.Fatal("no process-exit path taken after a failed swap: the canary would stay connected as a second active agent (F4)")
	}
	if exit.code != canaryExitCode {
		t.Fatalf("exit code = %d, want %d", exit.code, canaryExitCode)
	}
	if !conn.closed {
		t.Fatal("connection not closed on the failed-swap path; the canary would stay registered")
	}
	if !a.cfg.CanaryMode {
		t.Fatal("canary mode cleared on a failed swap: Connect() would reconnect instead of aborting")
	}
}
