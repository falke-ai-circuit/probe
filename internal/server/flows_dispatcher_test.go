package server

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEventStore is an in-memory implementation of FlowEventStore for tests.
type fakeEventStore struct {
	mu     sync.Mutex
	events []*FlowEvent
}

func (f *fakeEventStore) Append(ev *FlowEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *ev
	f.events = append(f.events, &cp)
	return nil
}

func (f *fakeEventStore) Query(filter FlowEventFilter) ([]*FlowEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*FlowEvent, 0)
	for _, ev := range f.events {
		if filter.FlowID != "" && ev.FlowID != filter.FlowID {
			continue
		}
		if filter.AgentID != "" && ev.AgentID != filter.AgentID {
			continue
		}
		if filter.Signal != "" && ev.Signal != filter.Signal {
			continue
		}
		if !filter.From.IsZero() && ev.Timestamp.Before(filter.From) {
			continue
		}
		if !filter.To.IsZero() && ev.Timestamp.After(filter.To) {
			continue
		}
		out = append(out, ev)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func newDispatcherForTest(t *testing.T) (*FlowDispatcher, *Server, *fakeEventStore) {
	t.Helper()
	srv := &Server{}
	store := &fakeEventStore{}
	srv.flowEvents = store
	srv.tasks = NewTaskManager("", srv)
	fd := NewFlowDispatcher(srv)
	return fd, srv, store
}

// waitForRunTerminal blocks until run reaches a terminal status. Used to
// synchronize tests with the dispatcher's goroutine.
func waitForRunTerminal(t *testing.T, fd *FlowDispatcher, run *FlowRun, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		fd.mu.Lock()
		s := run.Status
		fd.mu.Unlock()
		if s == FlowRunStatusCompleted || s == FlowRunStatusFailed {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDispatcher_WaitStepAdvances(t *testing.T) {
	fd, _, _ := newDispatcherForTest(t)
	steps := []FlowStep{
		{ID: "s1", Type: FlowStepWait, Seconds: 0, Next: "s2"},
		{ID: "s2", Type: FlowStepEmit, Signal: "done"},
	}
	flow := &Flow{ID: "f1", Name: "wait-test", Steps: steps}
	run := &FlowRun{ID: "r1", FlowID: "f1", Status: FlowRunStatusPending, StartedAt: time.Now()}
	fd.DispatchRun(run, flow)
	waitForRunTerminal(t, fd, run, 1*time.Second)
	if run.Status != FlowRunStatusCompleted {
		t.Errorf("status = %q, want %q (error: %s)", run.Status, FlowRunStatusCompleted, run.Error)
	}
}

func TestDispatcher_EmitStepWritesEvent(t *testing.T) {
	fd, _, store := newDispatcherForTest(t)
	steps := []FlowStep{
		{ID: "s1", Type: FlowStepEmit, Signal: "test_signal"},
	}
	flow := &Flow{ID: "f1", Steps: steps}
	run := &FlowRun{ID: "r1", FlowID: "f1", AgentID: "agt1", Status: FlowRunStatusPending, StartedAt: time.Now()}
	fd.DispatchRun(run, flow)
	waitForRunTerminal(t, fd, run, 1*time.Second)

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.events) != 1 {
		t.Fatalf("got %d events, want 1", len(store.events))
	}
	ev := store.events[0]
	if ev.Signal != "test_signal" {
		t.Errorf("Signal = %q, want %q", ev.Signal, "test_signal")
	}
	if ev.FlowID != "f1" {
		t.Errorf("FlowID = %q, want %q", ev.FlowID, "f1")
	}
	if ev.AgentID != "agt1" {
		t.Errorf("AgentID = %q, want %q", ev.AgentID, "agt1")
	}
}

func TestDispatcher_BranchConditionParsing(t *testing.T) {
	tests := []struct {
		name       string
		condition  string
		state      map[string]json.RawMessage
		wantResult bool
		wantErr    bool
	}{
		{
			name:       "equal true",
			condition:  "{{state.x}} == hello",
			state:      map[string]json.RawMessage{"x": json.RawMessage(`"hello"`)},
			wantResult: true,
		},
		{
			name:       "equal false",
			condition:  "{{state.x}} == hello",
			state:      map[string]json.RawMessage{"x": json.RawMessage(`"world"`)},
			wantResult: false,
		},
		{
			name:       "not equal true",
			condition:  "{{state.x}} != hello",
			state:      map[string]json.RawMessage{"x": json.RawMessage(`"world"`)},
			wantResult: true,
		},
		{
			name:       "contains true",
			condition:  "{{state.x}} contains bank",
			state:      map[string]json.RawMessage{"x": json.RawMessage(`"mybank.com"`)},
			wantResult: true,
		},
		{
			name:       "starts_with true",
			condition:  "{{state.x}} starts_with api",
			state:      map[string]json.RawMessage{"x": json.RawMessage(`"api.github.com"`)},
			wantResult: true,
		},
		{
			name:      "empty condition",
			condition: "",
			wantErr:   true,
		},
		{
			name:      "unparseable",
			condition: "garbage",
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evalCondition(tt.condition, tt.state)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.wantResult {
				t.Errorf("got %v, want %v", got, tt.wantResult)
			}
		})
	}
}

func TestDispatcher_BranchStepReturnsTrueNext(t *testing.T) {
	fd, _, _ := newDispatcherForTest(t)
	state := map[string]json.RawMessage{"flag": json.RawMessage(`"yes"`)}
	step := FlowStep{Type: FlowStepBranch, Condition: "{{state.flag}} == yes", IfTrue: "yes_path", IfFalse: "no_path"}
	next, err := fd.execBranchStep(step, state)
	if err != nil {
		t.Fatalf("execBranchStep: %v", err)
	}
	if next != "yes_path" {
		t.Errorf("next = %q, want yes_path", next)
	}
}

func TestDispatcher_DiffStepComputesChanges(t *testing.T) {
	fd, _, _ := newDispatcherForTest(t)
	state := map[string]json.RawMessage{
		"left_ref":  json.RawMessage(`{"a":1,"b":2}`),
		"right_ref": json.RawMessage(`{"b":2,"c":3}`),
	}
	step := FlowStep{Type: FlowStepDiff, Left: "left_ref", Right: "right_ref", DiffAs: "result"}

	next, err := fd.execDiffStep(step, state)
	if err != nil {
		t.Fatalf("execDiffStep: %v", err)
	}
	if next != "" {
		t.Errorf("next = %q, want empty", next)
	}
	result, ok := state["result"]
	if !ok {
		t.Fatal("result not in state")
	}
	var diff map[string]interface{}
	if err := json.Unmarshal(result, &diff); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	added, ok := diff["added"].(map[string]interface{})
	if !ok {
		t.Fatalf("added not a map: %T", diff["added"])
	}
	if _, exists := added["c"]; !exists {
		t.Errorf("added.c missing")
	}
	removed, ok := diff["removed"].(map[string]interface{})
	if !ok {
		t.Fatalf("removed not a map: %T", diff["removed"])
	}
	if _, exists := removed["a"]; !exists {
		t.Errorf("removed.a missing")
	}
}

func TestDispatcher_DiffStepMissingRef(t *testing.T) {
	fd, _, _ := newDispatcherForTest(t)
	state := map[string]json.RawMessage{}
	step := FlowStep{Type: FlowStepDiff, Left: "missing", Right: "also_missing"}
	if _, err := fd.execDiffStep(step, state); err == nil {
		t.Error("expected error for missing left ref")
	}
}

func TestDispatcher_ClassifyStepLabelsCorrectly(t *testing.T) {
	fd, _, _ := newDispatcherForTest(t)
	state := map[string]json.RawMessage{
		"domain": json.RawMessage(`"github.com"`),
	}
	step := FlowStep{
		Type:  FlowStepClassify,
		Input: "domain",
		Rules: []ClassifyRule{
			{If: "*github*", Label: "code-hosting"},
			{If: "*bank*", Label: "finance"},
		},
		ClassifyAs: "category",
	}
	next, err := fd.execClassifyStep(step, state)
	if err != nil {
		t.Fatalf("execClassifyStep: %v", err)
	}
	if next != "" {
		t.Errorf("next = %q, want empty", next)
	}
	var got map[string]string
	if err := json.Unmarshal(state["category"], &got); err != nil {
		t.Fatalf("unmarshal category: %v", err)
	}
	if got["label"] != "code-hosting" {
		t.Errorf("label = %q, want %q", got["label"], "code-hosting")
	}
}

func TestDispatcher_ClassifyStepNoMatch(t *testing.T) {
	fd, _, _ := newDispatcherForTest(t)
	state := map[string]json.RawMessage{
		"domain": json.RawMessage(`"unknown.example"`),
	}
	step := FlowStep{
		Type:  FlowStepClassify,
		Input: "domain",
		Rules: []ClassifyRule{
			{If: "*github*", Label: "code-hosting"},
		},
		ClassifyAs: "category",
	}
	_, err := fd.execClassifyStep(step, state)
	if err != nil {
		t.Fatalf("execClassifyStep: %v", err)
	}
	var got map[string]string
	json.Unmarshal(state["category"], &got)
	if got["label"] != "" {
		t.Errorf("label = %q, want empty", got["label"])
	}
}

func TestDispatcher_CycleDetection(t *testing.T) {
	fd, _, _ := newDispatcherForTest(t)
	steps := []FlowStep{
		{ID: "a", Type: FlowStepWait, Seconds: 0, Next: "b"},
		{ID: "b", Type: FlowStepWait, Seconds: 0, Next: "a"},
	}
	flow := &Flow{ID: "f1", Steps: steps}
	run := &FlowRun{ID: "r1", FlowID: "f1", Status: FlowRunStatusPending, StartedAt: time.Now()}
	fd.DispatchRun(run, flow)
	waitForRunTerminal(t, fd, run, 1*time.Second)
	if run.Status != FlowRunStatusFailed {
		t.Errorf("status = %q, want failed (cycle)", run.Status)
	}
	if !strings.Contains(run.Error, "cycle") {
		t.Errorf("error = %q, want cycle message", run.Error)
	}
}

func TestDispatcher_StepFailureMarksRunFailed(t *testing.T) {
	fd, _, _ := newDispatcherForTest(t)
	steps := []FlowStep{
		{ID: "s1", Type: FlowStepCommand, CommandType: "nonexistent_command", StoreAs: "r"},
	}
	flow := &Flow{ID: "f1", Steps: steps}
	run := &FlowRun{ID: "r1", FlowID: "f1", AgentID: "agt-not-connected", Status: FlowRunStatusPending, StartedAt: time.Now()}
	fd.DispatchRun(run, flow)
	waitForRunTerminal(t, fd, run, 2*time.Second)
	if run.Status != FlowRunStatusFailed {
		t.Errorf("status = %q, want failed", run.Status)
	}
}

func TestDispatcher_OnErrorContinue(t *testing.T) {
	fd, _, store := newDispatcherForTest(t)
	steps := []FlowStep{
		{ID: "s1", Type: FlowStepCommand, CommandType: "nonexistent", OnError: "continue", Next: "s2"},
		{ID: "s2", Type: FlowStepEmit, Signal: "reached_s2"},
	}
	flow := &Flow{ID: "f1", Steps: steps}
	run := &FlowRun{ID: "r1", FlowID: "f1", AgentID: "agt", Status: FlowRunStatusPending, StartedAt: time.Now()}
	fd.DispatchRun(run, flow)
	waitForRunTerminal(t, fd, run, 2*time.Second)
	if run.Status != FlowRunStatusCompleted {
		t.Errorf("status = %q, want completed (on_error=continue)", run.Status)
	}
	if len(store.events) != 1 {
		t.Errorf("events = %d, want 1", len(store.events))
	}
}

func TestDispatcher_EmptyFlowFails(t *testing.T) {
	fd, _, _ := newDispatcherForTest(t)
	flow := &Flow{ID: "f1", Steps: nil}
	run := &FlowRun{ID: "r1", FlowID: "f1", Status: FlowRunStatusPending, StartedAt: time.Now()}
	fd.DispatchRun(run, flow)
	waitForRunTerminal(t, fd, run, 1*time.Second)
	if run.Status != FlowRunStatusFailed {
		t.Errorf("status = %q, want failed", run.Status)
	}
}

func TestDispatcher_UnknownStepTypeFails(t *testing.T) {
	fd, _, _ := newDispatcherForTest(t)
	steps := []FlowStep{
		{ID: "s1", Type: "bogus_type"},
	}
	flow := &Flow{ID: "f1", Steps: steps}
	run := &FlowRun{ID: "r1", FlowID: "f1", Status: FlowRunStatusPending, StartedAt: time.Now()}
	fd.DispatchRun(run, flow)
	waitForRunTerminal(t, fd, run, 1*time.Second)
	if run.Status != FlowRunStatusFailed {
		t.Errorf("status = %q, want failed", run.Status)
	}
}

func TestDispatcher_GlobToRegex(t *testing.T) {
	tests := []struct {
		glob  string
		input string
		want  bool
	}{
		{"*bank*", "mybank.com", true},
		{"*bank*", "github.com", false},
		{"api.*", "api.github.com", true},
		{"api.*", "www.apix.com", false},
		{"exact", "exact", true},
		{"exact", "not_exact", false},
		{"*github*", "github.com", true},
	}
	for _, tt := range tests {
		t.Run(tt.glob+"_"+tt.input, func(t *testing.T) {
			re := globToRegex(tt.glob)
			matched := re.MatchString(tt.input)
			if matched != tt.want {
				t.Errorf("glob=%q input=%q: got %v, want %v (regex=%s)", tt.glob, tt.input, matched, tt.want, re.String())
			}
		})
	}
}

func TestDispatcher_ComputeDiffScalars(t *testing.T) {
	got := computeDiff(42, 42)
	if _, exists := got["changed"]; exists {
		t.Errorf("identical scalars should not produce changed, got %v", got)
	}
	got = computeDiff(42, 43)
	if _, exists := got["changed"]; !exists {
		t.Errorf("different scalars should produce changed, got %v", got)
	}
}

func TestDispatcher_ComputeDiffSlices(t *testing.T) {
	got := computeDiff([]interface{}{"a", "b"}, []interface{}{"b", "c"})
	added, _ := got["added"].([]string)
	removed, _ := got["removed"].([]string)
	if len(added) != 1 || added[0] != "c" {
		t.Errorf("added = %v, want [c]", added)
	}
	if len(removed) != 1 || removed[0] != "a" {
		t.Errorf("removed = %v, want [a]", removed)
	}
}

func TestDispatcher_ComputeDiffMaps(t *testing.T) {
	l := map[string]interface{}{"x": 1, "y": 2}
	r := map[string]interface{}{"y": 2, "z": 3}
	got := computeDiff(l, r)
	if added, ok := got["added"].(map[string]interface{}); !ok || added["z"] != 3 {
		t.Errorf("added.z missing or wrong: %v", got)
	}
	if removed, ok := got["removed"].(map[string]interface{}); !ok || removed["x"] != 1 {
		t.Errorf("removed.x missing or wrong: %v", got)
	}
}

func TestDispatcher_DispatchRunReachesDispatcher(t *testing.T) {
	// Verify that DispatchRun actually adds the run to the dispatcher's map
	// so future Status() lookups work.
	fd, _, _ := newDispatcherForTest(t)
	flow := &Flow{ID: "f1", Steps: []FlowStep{{ID: "s1", Type: FlowStepWait, Seconds: 0}}}
	run := &FlowRun{ID: "r1", FlowID: "f1", Status: FlowRunStatusPending, StartedAt: time.Now()}
	fd.DispatchRun(run, flow)

	fd.mu.Lock()
	_, ok := fd.runs["r1"]
	fd.mu.Unlock()
	if !ok {
		t.Error("run not added to dispatcher.runs map")
	}
	waitForRunTerminal(t, fd, run, 1*time.Second)

	// After completion, the run should be removed from the map
	fd.mu.Lock()
	_, stillThere := fd.runs["r1"]
	fd.mu.Unlock()
	if stillThere {
		t.Error("run should be removed from dispatcher.runs after completion")
	}
}

func TestDispatcher_ContextCancelFailsRun(t *testing.T) {
	// Direct test: cancel the context and verify failRun is called.
	fd, _, _ := newDispatcherForTest(t)
	flow := &Flow{ID: "f1", Steps: []FlowStep{
		{ID: "wait1", Type: FlowStepWait, Seconds: 5, Next: "s2"},
		{ID: "s2", Type: FlowStepEmit, Signal: "done"},
	}}
	run := &FlowRun{ID: "r1", FlowID: "f1", Status: FlowRunStatusPending, StartedAt: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	fd.runs[run.ID] = run
	fd.cancels[run.ID] = cancel
	go fd.runFlow(ctx, run, flow)

	time.Sleep(100 * time.Millisecond)
	cancel()
	waitForRunTerminal(t, fd, run, 1*time.Second)
	fd.mu.Lock()
	s := run.Status
	errMsg := run.Error
	fd.mu.Unlock()
	if s != FlowRunStatusFailed {
		t.Errorf("status = %q, want failed", s)
	}
	if !strings.Contains(errMsg, "context") && !strings.Contains(errMsg, "cancel") {
		t.Errorf("error = %q, want context/cancel message", errMsg)
	}
}

func (f *fakeEventStore) Stop() {
	// no-op for tests
}
