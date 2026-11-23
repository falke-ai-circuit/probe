package server

// P9-14 — the completion half of an update. P9-12 A2 measured the defect: the
// `purgeCanaryRegistryRow` call sat inside the success branch of
// server_ws.go:140-148, so when proc_kill came back with an error (the old
// process had already exited — the log line at :141 says so) the stale
// `<agent>-canary` registry row survived. That is defect B of P9-08 alive after
// the commit that claimed to fix it. These tests drive the completion path
// itself, which no test reached before (TestP908CanaryRegistryCleanup calls the
// helper; TestP908UpdateStartDropsStaleCanary drives the start path).

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// failingWriteConn makes every server→agent write fail, which is how
// forwardToAgentWithTimeout reports a kill that could not be delivered. An
// agent-side error reply is NOT this path: it comes back as a result map with a
// nil error (server_api.go:843-855), so only a transport failure reaches the
// error branch under test.
type failingWriteConn struct{}

func (failingWriteConn) WriteJSON(interface{}) error     { return fmt.Errorf("simulated transport failure") }
func (failingWriteConn) WriteMessage(int, []byte) error { return fmt.Errorf("simulated transport failure") }
func (failingWriteConn) Close() error                   { return nil }

// killAnsweringConn records proc_kill envelopes and answers them with a success
// result, so the successful-completion branch runs without a 30 s timeout.
type killAnsweringConn struct {
	srv   *Server
	mu    sync.Mutex
	kills []protocol.TaskStopParams
}

func (c *killAnsweringConn) WriteMessage(int, []byte) error { return nil }
func (c *killAnsweringConn) Close() error                   { return nil }

func (c *killAnsweringConn) WriteJSON(v interface{}) error {
	raw, _ := json.Marshal(v)
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	if env.Type != protocol.TypeProcKill {
		return nil
	}
	var p protocol.TaskStopParams
	_ = json.Unmarshal(env.Params, &p)
	c.mu.Lock()
	c.kills = append(c.kills, p)
	c.mu.Unlock()

	c.srv.pendingMu.Lock()
	ch, ok := c.srv.pendingReqs[env.ID]
	c.srv.pendingMu.Unlock()
	if ok {
		ch <- protocol.NewResult(env.ID, protocol.TypeProcKill+"_result",
			map[string]interface{}{"success": true})
	}
	return nil
}

func (c *killAnsweringConn) killCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.kills)
}

// useFastKillDelay shortens the stabilisation wait for one test and restores it.
func useFastKillDelay(t *testing.T) {
	t.Helper()
	restore := updatePromotionKillDelay
	updatePromotionKillDelay = time.Millisecond
	t.Cleanup(func() { updatePromotionKillDelay = restore })
}

// TestP914CompletionPurgesCanaryOnKillError is the test the completion path
// lacked: a failed kill must still remove the suffixed registry row.
func TestP914CompletionPurgesCanaryOnKillError(t *testing.T) {
	const agentID = "TEST-OLD-1189"
	srv, _ := newUpdateTestServer(t, agentID)
	// proc_kill needs the "process" capability, otherwise the forwarder refuses
	// before writing and the test would exercise the capability gate, not the
	// transport failure it is about.
	srv.registry.Register(agentID, agentID, "1.19.0", "windows", "amd64", "silent",
		[]string{"exec", "filesystem", "update", "process"})

	// The measured post-promotion state: canonical row + suffixed row.
	srv.registry.Register(agentID+canaryRegistrySuffix, agentID+canaryRegistrySuffix,
		"1.19.0", "windows", "amd64", "silent", nil)

	// Make the kill undeliverable: the promoted agent's connection is gone.
	srv.mu.Lock()
	srv.conns[agentID] = failingWriteConn{}
	srv.mu.Unlock()

	useFastKillDelay(t)
	srv.completeUpdatePromotion(agentID, 4242)

	if _, err := srv.registry.GetHealth(agentID + canaryRegistrySuffix); err == nil {
		t.Fatal("kill error left the canary registry row behind: the purge is still inside the success branch")
	}
	if _, err := srv.registry.GetHealth(agentID); err != nil {
		t.Fatalf("completion removed the canonical row: %v", err)
	}
}

// TestP914CompletionPurgesCanaryOnKillSuccess pins the behaviour that already
// worked, so hoisting the purge cannot regress it.
func TestP914CompletionPurgesCanaryOnKillSuccess(t *testing.T) {
	const agentID = "TEST-OLD-1190"
	srv, _ := newUpdateTestServer(t, agentID)
	srv.registry.Register(agentID, agentID, "1.19.0", "windows", "amd64", "silent",
		[]string{"exec", "filesystem", "update", "process"})
	srv.registry.Register(agentID+canaryRegistrySuffix, agentID+canaryRegistrySuffix,
		"1.19.0", "windows", "amd64", "silent", nil)

	conn := &killAnsweringConn{srv: srv}
	srv.mu.Lock()
	srv.conns[agentID] = conn
	srv.mu.Unlock()

	useFastKillDelay(t)
	srv.completeUpdatePromotion(agentID, 4242)

	if conn.killCount() != 1 {
		t.Fatalf("proc_kill sends = %d, want 1", conn.killCount())
	}
	if _, err := srv.registry.GetHealth(agentID + canaryRegistrySuffix); err == nil {
		t.Fatal("canary row survived a successful completion")
	}
	if _, err := srv.registry.GetHealth(agentID); err != nil {
		t.Fatalf("completion removed the canonical row: %v", err)
	}
}
