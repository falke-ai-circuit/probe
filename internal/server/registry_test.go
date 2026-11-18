package server

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestRegistry_Register verifies that Register adds a new agent record.
func TestRegistry_Register(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test-agent", "0.1", "linux", "amd64", "outbound", nil)

	agents := r.ListAgents()
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].AgentID != "agent-1" {
		t.Errorf("AgentID: got %q, want %q", agents[0].AgentID, "agent-1")
	}
	if agents[0].Name != "test-agent" {
		t.Errorf("Name: got %q, want %q", agents[0].Name, "test-agent")
	}
	if agents[0].Status != "active" {
		t.Errorf("Status: got %q, want %q", agents[0].Status, "active")
	}
	if agents[0].Version != "0.1" {
		t.Errorf("Version: got %q, want %q", agents[0].Version, "0.1")
	}
	if agents[0].OS != "linux" {
		t.Errorf("OS: got %q, want %q", agents[0].OS, "linux")
	}
	if agents[0].Arch != "amd64" {
		t.Errorf("Arch: got %q, want %q", agents[0].Arch, "amd64")
	}
	if agents[0].Mode != "outbound" {
		t.Errorf("Mode: got %q, want %q", agents[0].Mode, "outbound")
	}
}

// TestRegistry_Register_UpdateExisting verifies that registering an
// existing agent updates its record instead of creating a duplicate.
func TestRegistry_Register_UpdateExisting(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "name1", "0.1", "linux", "amd64", "outbound", nil)
	r.Register("agent-1", "name2", "0.2", "windows", "amd64", "inbound", nil)

	agents := r.ListAgents()
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (update), got %d", len(agents))
	}
	if agents[0].Name != "name2" {
		t.Errorf("Name: got %q, want %q", agents[0].Name, "name2")
	}
	if agents[0].Version != "0.2" {
		t.Errorf("Version: got %q, want %q", agents[0].Version, "0.2")
	}
	if agents[0].OS != "windows" {
		t.Errorf("OS: got %q, want %q", agents[0].OS, "windows")
	}
	if agents[0].Mode != "inbound" {
		t.Errorf("Mode: got %q, want %q", agents[0].Mode, "inbound")
	}
}

// TestRegistry_Unregister verifies that Unregister marks an agent inactive.
func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	r.Unregister("agent-1")

	agents := r.ListAgents()
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent (still in list), got %d", len(agents))
	}
	if agents[0].Status != "inactive" {
		t.Errorf("Status: got %q, want %q", agents[0].Status, "inactive")
	}
}

// TestRegistry_Unregister_NonExistent verifies Unregister is safe for
// non-existent agents (no panic).
func TestRegistry_Unregister_NonExistent(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	// Should not panic
	r.Unregister("no-such-agent")
}

// TestRegistry_Remove verifies Remove deletes the row from the map and from
// the persisted file while leaving unrelated agents intact.
func TestRegistry_Remove(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/registry.json"
	r := NewRegistry(path)
	defer r.Stop()

	r.Register("agent-1", "gone", "0.1", "linux", "amd64", "outbound", nil)
	r.Register("agent-2", "stays", "0.1", "linux", "amd64", "outbound", nil)

	if ok := r.Remove("agent-1"); !ok {
		t.Fatal("Remove returned false for an existing agent")
	}
	if ok := r.Remove("agent-1"); ok {
		t.Error("Remove returned true for an already-removed agent")
	}

	agents := r.ListAgents()
	if len(agents) != 1 || agents[0].AgentID != "agent-2" {
		t.Fatalf("expected only agent-2 to survive, got %+v", agents)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read registry file: %v", err)
	}
	if strings.Contains(string(raw), "agent-1") {
		t.Fatalf("registry file still contains agent-1:\n%s", raw)
	}
	if !strings.Contains(string(raw), "agent-2") {
		t.Fatalf("registry file lost agent-2:\n%s", raw)
	}
}

// TestRegistry_Heartbeat verifies that Heartbeat updates the last
// heartbeat timestamp and sets status to active.
func TestRegistry_Heartbeat(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	// Get initial heartbeat
	agents := r.ListAgents()
	initialHB := agents[0].LastHeartbeat

	// Wait enough to cross a second boundary (RFC3339 truncates to seconds)
	time.Sleep(1100 * time.Millisecond)
	r.Heartbeat("agent-1")

	agents = r.ListAgents()
	if agents[0].LastHeartbeat < initialHB {
		t.Errorf("expected heartbeat to update, got %q (before: %q)",
			agents[0].LastHeartbeat, initialHB)
	}
	if agents[0].Status != "active" {
		t.Errorf("expected status active, got %q", agents[0].Status)
	}
}

// TestRegistry_Heartbeat_NonExistent verifies Heartbeat is safe for
// non-existent agents.
func TestRegistry_Heartbeat_NonExistent(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Heartbeat("no-such-agent")
	// Should not panic, no agents in list
	agents := r.ListAgents()
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

// TestRegistry_ListAgents verifies multiple agents are returned.
func TestRegistry_ListAgents(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "name1", "0.1", "linux", "amd64", "outbound", nil)
	r.Register("agent-2", "name2", "0.1", "windows", "amd64", "inbound", nil)
	r.Register("agent-3", "name3", "0.1", "darwin", "arm64", "dual", nil)

	agents := r.ListAgents()
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}

	ids := make(map[string]bool)
	for _, a := range agents {
		ids[a.AgentID] = true
	}
	if !ids["agent-1"] || !ids["agent-2"] || !ids["agent-3"] {
		t.Errorf("missing expected agent IDs: %v", ids)
	}
}

// TestRegistry_RecordError verifies error recording increments the error
// count and sets the last error message.
func TestRegistry_RecordError(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	r.RecordError("agent-1", "first error")
	r.RecordError("agent-1", "second error")

	rec, err := r.GetHealth("agent-1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if rec.ErrorCount != 2 {
		t.Errorf("ErrorCount: got %d, want %d", rec.ErrorCount, 2)
	}
	if rec.LastError != "second error" {
		t.Errorf("LastError: got %q, want %q", rec.LastError, "second error")
	}
}

// TestRegistry_RecordError_NonExistent verifies RecordError is safe for
// non-existent agents.
func TestRegistry_RecordError_NonExistent(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.RecordError("no-such-agent", "error")
	// Should not panic
}

// TestRegistry_UpdateHealth verifies that UpdateHealth stores resource
// info and refreshes the heartbeat.
func TestRegistry_UpdateHealth(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	// Get initial heartbeat
	agents := r.ListAgents()
	initialHB := agents[0].LastHeartbeat

	time.Sleep(1100 * time.Millisecond)
	r.UpdateHealth("agent-1", ResourceInfo{
		CPUPercent: 42.5,
		MemoryMB:   1024.0,
		DiskFreeMB: 50000.0,
	})

	rec, err := r.GetHealth("agent-1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if rec.ResourceUsage == nil {
		t.Fatal("ResourceUsage is nil")
	}
	if rec.ResourceUsage.CPUPercent != 42.5 {
		t.Errorf("CPU: got %f, want %f", rec.ResourceUsage.CPUPercent, 42.5)
	}
	if rec.ResourceUsage.MemoryMB != 1024.0 {
		t.Errorf("Memory: got %f, want %f", rec.ResourceUsage.MemoryMB, 1024.0)
	}
	if rec.ResourceUsage.DiskFreeMB != 50000.0 {
		t.Errorf("DiskFree: got %f, want %f", rec.ResourceUsage.DiskFreeMB, 50000.0)
	}
	if rec.LastHeartbeat < initialHB {
		t.Errorf("expected heartbeat to update after UpdateHealth")
	}
}

// TestRegistry_UpdateHealth_RevivesStale verifies that a health update
// revives a stale agent back to active.
func TestRegistry_UpdateHealth_RevivesStale(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	// Manually mark stale by manipulating the record
	r.mu.Lock()
	rec := r.agents["agent-1"]
	rec.Status = "stale"
	r.mu.Unlock()

	r.UpdateHealth("agent-1", ResourceInfo{CPUPercent: 10})

	rec2, err := r.GetHealth("agent-1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if rec2.Status != "active" {
		t.Errorf("expected status active after health update, got %q", rec2.Status)
	}
}

// TestRegistry_GetHealth verifies GetHealth returns the agent record.
func TestRegistry_GetHealth(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	rec, err := r.GetHealth("agent-1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}
	if rec.AgentID != "agent-1" {
		t.Errorf("AgentID: got %q, want %q", rec.AgentID, "agent-1")
	}
}

// TestRegistry_GetHealth_NotFound verifies GetHealth returns an error
// for non-existent agents.
func TestRegistry_GetHealth_NotFound(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	_, err := r.GetHealth("no-such-agent")
	if err == nil {
		t.Error("expected error for non-existent agent")
	}
}

// TestRegistry_HealthScore verifies that the health score is computed
// correctly for a fresh agent.
func TestRegistry_HealthScore(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	rec, err := r.GetHealth("agent-1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}

	// Fresh agent: heartbeat recency = 0.4, error count = 0.3,
	// uptime stability = small (just connected) but >= 0
	// So score should be >= 0.7 (0.4 + 0.3 = 0.7 minimum)
	if rec.HealthScore < 0.69 {
		t.Errorf("expected health score >= 0.69 for fresh agent, got %f", rec.HealthScore)
	}
	// Max is 0.4 + 0.3 + 0.3 = 1.0
	if rec.HealthScore > 1.0 {
		t.Errorf("expected health score <= 1.0, got %f", rec.HealthScore)
	}
}

// TestRegistry_HealthScore_ErrorsDecrement verifies that recording errors
// decreases the health score.
func TestRegistry_HealthScore_ErrorsDecrement(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	rec1, _ := r.GetHealth("agent-1")

	r.RecordError("agent-1", "error 1")
	r.RecordError("agent-1", "error 2")
	r.RecordError("agent-1", "error 3")

	rec3, _ := r.GetHealth("agent-1")

	// Each error subtracts 0.05 from the error component (0.3 max)
	// 3 errors → 0.3 - 0.15 = 0.15 (vs 0.3 with 0 errors)
	// So score should drop by ~0.15
	if rec3.HealthScore >= rec1.HealthScore {
		t.Errorf("expected health score to decrease after errors: before=%f, after=%f",
			rec1.HealthScore, rec3.HealthScore)
	}
}

// TestRegistry_HealthScore_ManyErrors verifies that the error component
// floors at 0.
func TestRegistry_HealthScore_ManyErrors(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	for i := 0; i < 20; i++ {
		r.RecordError("agent-1", "error")
	}

	rec, _ := r.GetHealth("agent-1")

	// Error component should be 0 (floored), so score = heartbeat + uptime only
	// heartbeat ~0.4, uptime small → score < 0.7 (no error bonus)
	if rec.HealthScore > 0.7 {
		t.Errorf("expected health score < 0.7 with many errors, got %f", rec.HealthScore)
	}
	if rec.HealthScore < 0 {
		t.Errorf("health score should not be negative, got %f", rec.HealthScore)
	}
}

// TestRegistry_Persistence verifies that registry state persists to disk
// and can be loaded back.
func TestRegistry_Persistence(t *testing.T) {
	dir := t.TempDir()
	registryPath := dir + "/registry.json"

	// Create registry, register agents, then stop
	r1 := NewRegistry(registryPath)
	r1.Register("agent-1", "test1", "0.1", "linux", "amd64", "outbound", nil)
	r1.Register("agent-2", "test2", "0.1", "windows", "amd64", "inbound", nil)
	r1.Stop()

	// Create a new registry from the same file
	r2 := NewRegistry(registryPath)
	defer r2.Stop()

	agents := r2.ListAgents()
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents after reload, got %d", len(agents))
	}

	ids := make(map[string]bool)
	for _, a := range agents {
		ids[a.AgentID] = true
	}
	if !ids["agent-1"] || !ids["agent-2"] {
		t.Errorf("missing expected agents after reload: %v", ids)
	}
}

// TestRegistry_StaleDetector verifies that the stale detector marks
// agents as stale when their heartbeat is too old. We can't wait 90
// seconds in a unit test, so we manually manipulate the heartbeat time.
func TestRegistry_StaleDetector(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	// Manually set the heartbeat to 2 minutes ago (beyond staleThreshold of 90s)
	r.mu.Lock()
	rec := r.agents["agent-1"]
	rec.lastHeartbeat = time.Now().Add(-2 * time.Minute)
	rec.Status = "active"
	r.mu.Unlock()

	// Run checkStale manually
	r.checkStale()

	agents := r.ListAgents()
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(agents))
	}
	if agents[0].Status != "stale" {
		t.Errorf("expected status stale, got %q", agents[0].Status)
	}
}

// TestRegistry_StaleDetector_NotStale verifies that agents with fresh
// heartbeats are NOT marked stale.
func TestRegistry_StaleDetector_NotStale(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)

	// Heartbeat is fresh (just registered)
	r.checkStale()

	agents := r.ListAgents()
	if agents[0].Status != "active" {
		t.Errorf("expected status active (fresh heartbeat), got %q", agents[0].Status)
	}
}

// TestRegistry_StartStaleDetector_Idempotent verifies that calling
// StartStaleDetector multiple times is safe.
func TestRegistry_StartStaleDetector_Idempotent(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	r.StartStaleDetector()
	r.StartStaleDetector()
	r.StartStaleDetector()
	// Should not panic or start multiple goroutines
}

// TestRegistry_Stop_Multiple verifies that Stop is safe to call multiple times.
func TestRegistry_Stop_Multiple(t *testing.T) {
	r := NewRegistry("")
	r.Stop()
	r.Stop()
	r.Stop()
	// Should not panic
}

// TestRegistry_EmptyList verifies an empty registry returns an empty list.
func TestRegistry_EmptyList(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()
	agents := r.ListAgents()
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

// TestRegistry_ConcurrentAccess verifies that concurrent operations
// on the registry don't cause data races. Run with -race.
func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := NewRegistry("")
	defer r.Stop()

	done := make(chan struct{})

	// Writer goroutine 1: Register
	go func() {
		for i := 0; i < 50; i++ {
			r.Register("agent-1", "test", "0.1", "linux", "amd64", "outbound", nil)
		}
		done <- struct{}{}
	}()

	// Writer goroutine 2: Heartbeat
	go func() {
		for i := 0; i < 50; i++ {
			r.Heartbeat("agent-1")
		}
		done <- struct{}{}
	}()

	// Reader goroutine: ListAgents
	go func() {
		for i := 0; i < 50; i++ {
			r.ListAgents()
		}
		done <- struct{}{}
	}()

	// Reader goroutine: GetHealth
	go func() {
		for i := 0; i < 50; i++ {
			r.GetHealth("agent-1")
		}
		done <- struct{}{}
	}()

	// Error recorder
	go func() {
		for i := 0; i < 50; i++ {
			r.RecordError("agent-1", "concurrent error")
		}
		done <- struct{}{}
	}()

	for i := 0; i < 5; i++ {
		<-done
	}
}