package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
)

// FlowEvent is a single emitted event from a flow step. Persisted to NDJSON
// by FlowEventStore (see A.5). Stub type here for A.2 dispatch use.
type FlowEvent struct {
	ID        string          `json:"id"`
	FlowID    string          `json:"flow_id"`
	RunID     string          `json:"run_id"`
	AgentID   string          `json:"agent_id,omitempty"`
	Signal    string          `json:"signal"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`
}

// FlowEventStore is a forward declaration. Full implementation in A.5.
type FlowEventStore interface {
	Append(ev *FlowEvent) error
	Query(filter FlowEventFilter) ([]*FlowEvent, error)
	Stop()
}

// FlowEventFilter narrows event queries.
type FlowEventFilter struct {
	FlowID  string
	AgentID string
	Signal  string
	From    time.Time
	To      time.Time
	Limit   int
}

// FlowDispatcher runs flow steps in sequence. Each run gets its own
// goroutine that walks the step DAG. Steps reference state by name.
type FlowDispatcher struct {
	server *Server
	mu     sync.Mutex
	runs   map[string]*FlowRun
	// Per-run goroutine cancel funcs.
	cancels map[string]context.CancelFunc
}

// NewFlowDispatcher creates a dispatcher bound to a server.
func NewFlowDispatcher(srv *Server) *FlowDispatcher {
	return &FlowDispatcher{
		server:  srv,
		runs:    make(map[string]*FlowRun),
		cancels: make(map[string]context.CancelFunc),
	}
}

// DispatchRun starts executing a flow run. Returns immediately; the run
// progresses asynchronously.
func (fd *FlowDispatcher) DispatchRun(run *FlowRun, flow *Flow) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	fd.mu.Lock()
	fd.runs[run.ID] = run
	fd.cancels[run.ID] = cancel
	fd.mu.Unlock()

	go fd.runFlow(ctx, run, flow)
}

// runFlow walks the step DAG. Returns when the flow ends or context is done.
func (fd *FlowDispatcher) runFlow(ctx context.Context, run *FlowRun, flow *Flow) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[flows] run %s panicked: %v", run.ID, r)
			fd.failRun(run, fmt.Sprintf("panic: %v", r))
		}
		fd.mu.Lock()
		delete(fd.runs, run.ID)
		delete(fd.cancels, run.ID)
		fd.mu.Unlock()
	}()

	fd.markRunStatus(run, FlowRunStatusRunning)

	state := make(map[string]json.RawMessage)
	steps := flow.Steps
	if len(steps) == 0 {
		fd.failRun(run, "flow has no steps")
		return
	}

	current := steps[0].ID
	visited := map[string]bool{}
	for current != "" {
		select {
		case <-ctx.Done():
			fd.failRun(run, "context cancelled")
			return
		default:
		}

		if visited[current] {
			fd.failRun(run, fmt.Sprintf("cycle detected at step %s", current))
			return
		}
		visited[current] = true

		step, ok := findStep(steps, current)
		if !ok {
			fd.failRun(run, fmt.Sprintf("step %s not found", current))
			return
		}

		next, err := fd.executeStep(ctx, run, flow, step, state)
		if err != nil {
			switch step.OnError {
			case "continue":
				log.Printf("[flows] run %s step %s error (continuing): %v", run.ID, step.ID, err)
				// Use the step's configured Next, since executeStep's returned next may be empty on error
				if next == "" {
					next = step.Next
				}
			default:
				fd.failRun(run, fmt.Sprintf("step %s: %v", step.ID, err))
				fd.auditStep(run, flow, step, "error", err.Error())
				return
			}
		}
		fd.auditStep(run, flow, step, "done", "")
		// Auto-advance: if the step didn't specify a Next step, fall through
		// to the next step in the ordered steps slice. This lets users write
		// flows as a linear list without chaining each step by ID.
		if next == "" {
			next = nextStepID(steps, step.ID)
		}
		current = next
	}

	now := time.Now().UTC()
	fd.mu.Lock()
	run.Status = FlowRunStatusCompleted
	run.CompletedAt = &now
	run.State, _ = json.Marshal(state)
	fd.mu.Unlock()
	log.Printf("[flows] run %s completed", run.ID)
}

// executeStep runs one step and returns the next step ID.
func (fd *FlowDispatcher) executeStep(ctx context.Context, run *FlowRun, flow *Flow, step FlowStep, state map[string]json.RawMessage) (string, error) {
	switch step.Type {
	case FlowStepCommand:
		return fd.execCommandStep(ctx, run, flow, step, state)
	case FlowStepWait:
		select {
		case <-time.After(time.Duration(step.Seconds) * time.Second):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return step.Next, nil
	case FlowStepBranch:
		return fd.execBranchStep(step, state)
	case FlowStepDiff:
		return fd.execDiffStep(step, state)
	case FlowStepClassify:
		return fd.execClassifyStep(step, state)
	case FlowStepEmit:
		return fd.execEmitStep(ctx, run, flow, step, state)
	default:
		return "", fmt.Errorf("unknown step type %q", step.Type)
	}
}

func (fd *FlowDispatcher) execCommandStep(ctx context.Context, run *FlowRun, flow *Flow, step FlowStep, state map[string]json.RawMessage) (string, error) {
	if fd.server == nil {
		return "", fmt.Errorf("no server bound")
	}
	if fd.server.tasks == nil {
		return "", fmt.Errorf("TaskManager not initialized")
	}
	if step.CommandType == "" {
		return "", fmt.Errorf("command_type is required")
	}
	if run.AgentID == "" {
		return "", fmt.Errorf("run has no agent_id; cannot forward command")
	}

	var params interface{}
	if len(step.Params) > 0 {
		params = step.Params
	}

	resp, err := fd.server.forwardToAgent(run.AgentID, step.CommandType, params)
	if err != nil {
		return "", err
	}

	resultData, _ := json.Marshal(resp)
	if step.StoreAs != "" {
		state[step.StoreAs] = resultData
	}
	return step.Next, nil
}

func (fd *FlowDispatcher) execBranchStep(step FlowStep, state map[string]json.RawMessage) (string, error) {
	result, err := evalCondition(step.Condition, state)
	if err != nil {
		return "", err
	}
	if result {
		return step.IfTrue, nil
	}
	return step.IfFalse, nil
}

func (fd *FlowDispatcher) execDiffStep(step FlowStep, state map[string]json.RawMessage) (string, error) {
	left, ok := state[step.Left]
	if !ok {
		return "", fmt.Errorf("left ref %q not in state", step.Left)
	}
	right, ok := state[step.Right]
	if !ok {
		return "", fmt.Errorf("right ref %q not in state", step.Right)
	}

	var l, r interface{}
	if err := json.Unmarshal(left, &l); err != nil {
		return "", fmt.Errorf("left unmarshal: %w", err)
	}
	if err := json.Unmarshal(right, &r); err != nil {
		return "", fmt.Errorf("right unmarshal: %w", err)
	}

	diff := computeDiff(l, r)
	diffData, _ := json.Marshal(diff)
	if step.DiffAs != "" {
		state[step.DiffAs] = diffData
	}
	return step.Next, nil
}

func (fd *FlowDispatcher) execClassifyStep(step FlowStep, state map[string]json.RawMessage) (string, error) {
	raw, ok := state[step.Input]
	if !ok {
		return "", fmt.Errorf("input ref %q not in state", step.Input)
	}
	var input interface{}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", fmt.Errorf("input unmarshal: %w", err)
	}

	inputStr := fmt.Sprintf("%v", input)
	for _, rule := range step.Rules {
		if globToRegex(rule.If).MatchString(inputStr) {
			out, _ := json.Marshal(map[string]string{"input": inputStr, "label": rule.Label})
			if step.ClassifyAs != "" {
				state[step.ClassifyAs] = out
			}
			return step.Next, nil
		}
	}
	// No rule matched — emit empty label
	out, _ := json.Marshal(map[string]string{"input": inputStr, "label": ""})
	if step.ClassifyAs != "" {
		state[step.ClassifyAs] = out
	}
	return step.Next, nil
}

func (fd *FlowDispatcher) execEmitStep(ctx context.Context, run *FlowRun, flow *Flow, step FlowStep, state map[string]json.RawMessage) (string, error) {
	if fd.server == nil || fd.server.flowEvents == nil {
		return "", fmt.Errorf("flow event store not available")
	}
	payload := step.Payload
	if len(payload) == 0 {
		// If no explicit payload, emit the entire state
		payload, _ = json.Marshal(state)
	} else {
		// Resolve {{state.X}} references in payload template.
		payload = resolveStateRefs(payload, state)
	}
	ev := FlowEvent{
		ID:        generateFlowID(),
		FlowID:    flow.ID,
		RunID:     run.ID,
		AgentID:   run.AgentID,
		Signal:    step.Signal,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	}
	if err := fd.server.flowEvents.Append(&ev); err != nil {
		return "", fmt.Errorf("append event: %w", err)
	}
	return step.Next, nil
}

// resolveStateRefs replaces {{state.X}} references in a JSON payload with
// the corresponding value from the flow state. Each X must exist as a key
// in state, otherwise an empty string is substituted (matching evalCondition).
// The payload is treated as a JSON value of any type — if it's a string
// containing template syntax, substitution happens against the raw bytes.
// If it's an object/array, substitution only applies to the string fields
// within (rare but supported).
func resolveStateRefs(payload json.RawMessage, state map[string]json.RawMessage) json.RawMessage {
	// Fast path: no {{ in payload, return as-is.
	if !bytes.Contains(payload, []byte("{{")) {
		return payload
	}
	var asString string
	if err := json.Unmarshal(payload, &asString); err == nil {
		// String payload: substitute references.
		resolved := stateRefRegex.ReplaceAllStringFunc(asString, func(m string) string {
			key := strings.TrimSuffix(strings.TrimPrefix(m, "{{state."), "}}")
			v, ok := state[key]
			if !ok {
				return ""
			}
			return string(v)
		})
		out, _ := json.Marshal(resolved)
		return out
	}
	// Non-string payload: serialize the entire state instead (fallback).
	out, _ := json.Marshal(state)
	return out
}

func (fd *FlowDispatcher) markRunStatus(run *FlowRun, status string) {
	fd.mu.Lock()
	run.Status = status
	fd.mu.Unlock()
}

func (fd *FlowDispatcher) failRun(run *FlowRun, errMsg string) {
	now := time.Now().UTC()
	fd.mu.Lock()
	run.Status = FlowRunStatusFailed
	run.Error = errMsg
	run.CompletedAt = &now
	fd.mu.Unlock()
	log.Printf("[flows] run %s failed: %s", run.ID, errMsg)
}

// findStep returns the step with the given ID.
func findStep(steps []FlowStep, id string) (FlowStep, bool) {
	for _, s := range steps {
		if s.ID == id {
			return s, true
		}
	}
	return FlowStep{}, false
}

// nextStepID returns the ID of the step immediately after the one with
// the given ID in the ordered steps slice. Returns "" if no next step.
func nextStepID(steps []FlowStep, currentID string) string {
	for i, s := range steps {
		if s.ID == currentID {
			if i+1 < len(steps) {
				return steps[i+1].ID
			}
			return ""
		}
	}
	return ""
}

// evalCondition evaluates a simple condition like "{{state.x}} == value"
// or "value != other". Supports ==, !=, contains, starts_with.
func evalCondition(cond string, state map[string]json.RawMessage) (bool, error) {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return false, fmt.Errorf("empty condition")
	}

	// Resolve {{state.X}} references
	resolved := stateRefRegex.ReplaceAllStringFunc(cond, func(m string) string {
		// m is like "{{state.foo}}"
		key := strings.TrimSuffix(strings.TrimPrefix(m, "{{state."), "}}")
		v, ok := state[key]
		if !ok {
			return ""
		}
		// Try to extract string value from JSON
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			return s
		}
		return string(v)
	})

	// Split on operator
	for _, op := range []string{"==", "!=", " contains ", " starts_with "} {
		if idx := strings.Index(resolved, op); idx > 0 {
			lhs := strings.TrimSpace(resolved[:idx])
			rhs := strings.TrimSpace(resolved[idx+len(op):])
			rhs = strings.Trim(rhs, "\"'")
			switch op {
			case "==":
				return lhs == rhs, nil
			case "!=":
				return lhs != rhs, nil
			case " contains ":
				return strings.Contains(lhs, rhs), nil
			case " starts_with ":
				return strings.HasPrefix(lhs, rhs), nil
			}
		}
	}
	return false, fmt.Errorf("unparseable condition: %q", cond)
}

var stateRefRegex = regexp.MustCompile(`\{\{state\.[a-zA-Z0-9_]+\}\}`)

// globToRegex converts a simple glob like "*bank*" to a regex.
func globToRegex(glob string) *regexp.Regexp {
	re := regexp.QuoteMeta(glob)
	re = strings.ReplaceAll(re, `\*`, ".*")
	re = strings.ReplaceAll(re, `\?`, ".")
	return regexp.MustCompile("^" + re + "$")
}

// computeDiff returns added/removed/changed between two JSON-decoded values.
// Recursive for maps and slices; scalar equality for primitives.
func computeDiff(left, right interface{}) map[string]interface{} {
	out := map[string]interface{}{}

	switch l := left.(type) {
	case map[string]interface{}:
		r, ok := right.(map[string]interface{})
		if !ok {
			out["_type_change"] = fmt.Sprintf("%T -> %T", left, right)
			return out
		}
		added := map[string]interface{}{}
		removed := map[string]interface{}{}
		changed := map[string]interface{}{}
		for k, lv := range l {
			if rv, ok := r[k]; ok {
				if !deepEqualJSON(lv, rv) {
					changed[k] = map[string]interface{}{"from": lv, "to": rv}
				}
			} else {
				removed[k] = lv
			}
		}
		for k, rv := range r {
			if _, ok := l[k]; !ok {
				added[k] = rv
			}
		}
		if len(added) > 0 {
			out["added"] = added
		}
		if len(removed) > 0 {
			out["removed"] = removed
		}
		if len(changed) > 0 {
			out["changed"] = changed
		}
	case []interface{}:
		r, ok := right.([]interface{})
		if !ok {
			out["_type_change"] = fmt.Sprintf("%T -> %T", left, right)
			return out
		}
		// Treat slice as set: compare element membership
		lset := make(map[string]bool)
		for _, v := range l {
			lset[fmt.Sprintf("%v", v)] = true
		}
		rset := make(map[string]bool)
		for _, v := range r {
			rset[fmt.Sprintf("%v", v)] = true
		}
		var added, removed []string
		for k := range rset {
			if !lset[k] {
				added = append(added, k)
			}
		}
		for k := range lset {
			if !rset[k] {
				removed = append(removed, k)
			}
		}
		if len(added) > 0 {
			out["added"] = added
		}
		if len(removed) > 0 {
			out["removed"] = removed
		}
	default:
		if !deepEqualJSON(left, right) {
			out["changed"] = map[string]interface{}{"from": left, "to": right}
		}
	}
	return out
}

func deepEqualJSON(a, b interface{}) bool {
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	return string(aj) == string(bj)
}
// auditStep writes a per-step audit entry. Called from the dispatcher on
// both success and error paths. No-op if audit is nil or the server has
// no audit logger configured.
func (fd *FlowDispatcher) auditStep(run *FlowRun, flow *Flow, step FlowStep, eventType string, errMsg string) {
	if fd.server == nil || fd.server.audit == nil {
		return
	}
	extra := map[string]string{
		"run_id":  run.ID,
		"flow_name": flow.Name,
	}
	if errMsg != "" {
		extra["error"] = errMsg
	}
	fd.server.audit.LogFlow(flow.ID, step.ID, eventType, "flow.step", run.AgentID, "", extra)
}
