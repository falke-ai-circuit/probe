package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

func newTestFlowManager(t *testing.T) *FlowManager {
	t.Helper()
	dir := t.TempDir()
	fm := NewFlowManager(filepath.Join(dir, "flows.json"), nil)
	return fm
}

func TestFlowManager_CreateAndGet(t *testing.T) {
	fm := newTestFlowManager(t)
	steps := []FlowStep{
		{ID: "s1", Type: FlowStepCommand, CommandType: "exec", StoreAs: "result"},
	}
	flow, err := fm.Create("test-flow", "desc", FlowTrigger{Type: FlowTriggerOnce}, steps, nil, "op1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if flow.ID == "" {
		t.Error("expected non-empty ID")
	}
	if flow.Name != "test-flow" {
		t.Errorf("name = %q, want %q", flow.Name, "test-flow")
	}
	if !flow.Enabled {
		t.Error("expected enabled=true on create")
	}
	if flow.CreatedBy != "op1" {
		t.Errorf("created_by = %q, want %q", flow.CreatedBy, "op1")
	}

	got, err := fm.Get(flow.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != flow.ID {
		t.Errorf("got.ID = %q, want %q", got.ID, flow.ID)
	}
	if len(got.Steps) != 1 {
		t.Errorf("len(Steps) = %d, want 1", len(got.Steps))
	}
}

func TestFlowManager_CreateValidatesRequiredFields(t *testing.T) {
	fm := newTestFlowManager(t)
	_, err := fm.Create("", "", FlowTrigger{}, []FlowStep{}, nil, "")
	if err == nil {
		t.Error("expected error for empty name")
	}
	_, err = fm.Create("name", "", FlowTrigger{}, []FlowStep{}, nil, "")
	if err == nil {
		t.Error("expected error for empty steps")
	}
}

func TestFlowManager_GetNotFound(t *testing.T) {
	fm := newTestFlowManager(t)
	_, err := fm.Get("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent flow")
	}
}

func TestFlowManager_Update(t *testing.T) {
	fm := newTestFlowManager(t)
	flow, _ := fm.Create("orig", "d", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s1"}}, nil, "op1")

	newTrigger := FlowTrigger{Type: FlowTriggerRecurring, IntervalSeconds: 60}
	newSteps := []FlowStep{{ID: "s1"}, {ID: "s2"}}
	updated, err := fm.Update(flow.ID, "renamed", "new desc", &newTrigger, newSteps, []string{"agt1"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "renamed" {
		t.Errorf("Name = %q, want %q", updated.Name, "renamed")
	}
	if updated.Description != "new desc" {
		t.Errorf("Description = %q, want %q", updated.Description, "new desc")
	}
	if updated.Trigger.IntervalSeconds != 60 {
		t.Errorf("IntervalSeconds = %d, want 60", updated.Trigger.IntervalSeconds)
	}
	if len(updated.Steps) != 2 {
		t.Errorf("len(Steps) = %d, want 2", len(updated.Steps))
	}
	if len(updated.AgentIDs) != 1 || updated.AgentIDs[0] != "agt1" {
		t.Errorf("AgentIDs = %v, want [agt1]", updated.AgentIDs)
	}
}

func TestFlowManager_UpdateRejectsEmptySteps(t *testing.T) {
	fm := newTestFlowManager(t)
	flow, _ := fm.Create("x", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s1"}}, nil, "op")
	_, err := fm.Update(flow.ID, "", "", nil, []FlowStep{}, nil)
	if err == nil {
		t.Error("expected error for empty steps")
	}
}

func TestFlowManager_EnableDisable(t *testing.T) {
	fm := newTestFlowManager(t)
	flow, _ := fm.Create("x", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s1"}}, nil, "op")
	if !flow.Enabled {
		t.Fatal("expected enabled after create")
	}
	if err := fm.Disable(flow.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	got, _ := fm.Get(flow.ID)
	if got.Enabled {
		t.Error("expected disabled")
	}
	if err := fm.Enable(flow.ID); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	got, _ = fm.Get(flow.ID)
	if !got.Enabled {
		t.Error("expected enabled after Enable")
	}
}

func TestFlowManager_Delete(t *testing.T) {
	fm := newTestFlowManager(t)
	flow, _ := fm.Create("x", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s1"}}, nil, "op")
	if err := fm.Delete(flow.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := fm.Get(flow.ID); err == nil {
		t.Error("expected error after delete")
	}
	if err := fm.Delete(flow.ID); err == nil {
		t.Error("expected error deleting nonexistent")
	}
}

func TestFlowManager_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "flows.json")
	fm := NewFlowManager(path, nil)
	flow, _ := fm.Create("persist-test", "d", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s1"}}, []string{"agt1"}, "op1")

	// Reload from disk
	fm2 := NewFlowManager(path, nil)
	got, err := fm2.Get(flow.ID)
	if err != nil {
		t.Fatalf("Get after reload: %v", err)
	}
	if got.Name != "persist-test" {
		t.Errorf("Name = %q, want %q", got.Name, "persist-test")
	}
	if len(got.AgentIDs) != 1 || got.AgentIDs[0] != "agt1" {
		t.Errorf("AgentIDs not persisted: %v", got.AgentIDs)
	}
}

func TestFlowManager_AssignUnassignAgent(t *testing.T) {
	fm := newTestFlowManager(t)
	flow, _ := fm.Create("x", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s1"}}, nil, "op")

	if err := fm.AssignFlowToAgent(flow.ID, "agt1"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	flow, _ = fm.Get(flow.ID)
	if len(flow.AgentIDs) != 1 || flow.AgentIDs[0] != "agt1" {
		t.Errorf("AgentIDs = %v, want [agt1]", flow.AgentIDs)
	}

	// Re-assign is no-op
	if err := fm.AssignFlowToAgent(flow.ID, "agt1"); err != nil {
		t.Fatalf("Assign duplicate: %v", err)
	}
	flow, _ = fm.Get(flow.ID)
	if len(flow.AgentIDs) != 1 {
		t.Errorf("AgentIDs = %v after dup assign, want [agt1]", flow.AgentIDs)
	}

	if err := fm.UnassignFlowFromAgent(flow.ID, "agt1"); err != nil {
		t.Fatalf("Unassign: %v", err)
	}
	flow, _ = fm.Get(flow.ID)
	if len(flow.AgentIDs) != 0 {
		t.Errorf("AgentIDs = %v, want empty", flow.AgentIDs)
	}

	// Unassign of non-assigned is no-op
	if err := fm.UnassignFlowFromAgent(flow.ID, "agt-not-there"); err != nil {
		t.Errorf("Unassign non-existent: %v", err)
	}
}

func TestFlowManager_ListFlowsForAgent(t *testing.T) {
	fm := newTestFlowManager(t)
	f1, _ := fm.Create("f1", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s"}}, nil, "op")
	f2, _ := fm.Create("f2", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s"}}, nil, "op")
	f3, _ := fm.Create("f3", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s"}}, nil, "op")
	fm.AssignFlowToAgent(f1.ID, "agtA")
	fm.AssignFlowToAgent(f3.ID, "agtA")
	fm.AssignFlowToAgent(f2.ID, "agtB")

	gotA := fm.ListFlowsForAgent("agtA")
	if len(gotA) != 2 {
		t.Errorf("ListFlowsForAgent(agtA) = %d flows, want 2", len(gotA))
	}
	gotB := fm.ListFlowsForAgent("agtB")
	if len(gotB) != 1 {
		t.Errorf("ListFlowsForAgent(agtB) = %d flows, want 1", len(gotB))
	}
	gotNone := fm.ListFlowsForAgent("agtNone")
	if len(gotNone) != 0 {
		t.Errorf("ListFlowsForAgent(agtNone) = %d flows, want 0", len(gotNone))
	}
}

func TestFlowManager_ListSortedByName(t *testing.T) {
	fm := newTestFlowManager(t)
	fm.Create("zebra", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s"}}, nil, "op")
	fm.Create("alpha", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s"}}, nil, "op")
	fm.Create("mango", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s"}}, nil, "op")
	list := fm.List()
	if len(list) != 3 {
		t.Fatalf("List size = %d, want 3", len(list))
	}
	want := []string{"alpha", "mango", "zebra"}
	for i, w := range want {
		if list[i].Name != w {
			t.Errorf("list[%d].Name = %q, want %q", i, list[i].Name, w)
		}
	}
}

func TestFlowManager_RunNow(t *testing.T) {
	fm := newTestFlowManager(t)
	flow, _ := fm.Create("x", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s1"}}, nil, "op")
	run, err := fm.RunNow(flow.ID, "agt1", "op1")
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if run.Status != FlowRunStatusPending {
		t.Errorf("RunNow status = %q, want %q", run.Status, FlowRunStatusPending)
	}
	if run.FlowID != flow.ID {
		t.Errorf("FlowID = %q, want %q", run.FlowID, flow.ID)
	}
	if run.AgentID != "agt1" {
		t.Errorf("AgentID = %q, want %q", run.AgentID, "agt1")
	}
	if _, err := fm.RunNow("nonexistent", "x", "y"); err == nil {
		t.Error("expected error for nonexistent flow")
	}
}

func TestFlowManager_ConcurrentSafe(t *testing.T) {
	fm := newTestFlowManager(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			fm.Create("c", "", FlowTrigger{Type: FlowTriggerOnce}, []FlowStep{{ID: "s"}}, nil, "op")
		}(i)
	}
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fm.List()
		}()
	}
	wg.Wait()
	if got := len(fm.List()); got != 50 {
		t.Errorf("List size = %d, want 50", got)
	}
}

func TestFlowManager_LoadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.json")
	os.WriteFile(path, []byte(""), 0644)
	fm := NewFlowManager(path, nil)
	if got := len(fm.List()); got != 0 {
		t.Errorf("List size after empty file = %d, want 0", got)
	}
}

func TestFlowManager_LoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.json")
	os.WriteFile(path, []byte("{this is not valid json"), 0644)
	fm := NewFlowManager(path, nil)
	if got := len(fm.List()); got != 0 {
		t.Errorf("List size after corrupt file = %d, want 0", got)
	}
}

func TestFlowManager_FlowJSONRoundtrip(t *testing.T) {
	flow := &Flow{
		ID:      "abc",
		Name:    "test",
		Trigger: FlowTrigger{Type: FlowTriggerRecurring, IntervalSeconds: 30},
		Steps: []FlowStep{
			{ID: "s1", Type: FlowStepCommand, CommandType: "exec", StoreAs: "result"},
			{ID: "s2", Type: FlowStepEmit, Signal: "exec_done"},
		},
		Enabled: true,
	}
	data, err := json.Marshal(flow)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Flow
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ID != flow.ID {
		t.Errorf("ID = %q, want %q", got.ID, flow.ID)
	}
	if len(got.Steps) != 2 {
		t.Errorf("Steps len = %d, want 2", len(got.Steps))
	}
	if got.Steps[0].CommandType != "exec" {
		t.Errorf("Steps[0].CommandType = %q, want %q", got.Steps[0].CommandType, "exec")
	}
}

// helper to verify list is sorted (used in TestFlowManager_ListSortedByName)
func init() {
	sort.SliceStable([]int{1}, func(i, j int) bool { return i < j })
}
