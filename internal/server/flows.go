package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// Flow step types. Each step has a discriminator field "type".
const (
	FlowStepCommand    = "command"    // Forward an existing agent command (uses TaskManager)
	FlowStepWait       = "wait"       // Sleep for N seconds
	FlowStepBranch     = "branch"     // Conditional routing
	FlowStepDiff       = "compute_diff"  // Server-side diff between two snapshots
	FlowStepClassify   = "classify"   // Apply classification rules
	FlowStepEmit       = "emit"       // Append FlowEvent to NDJSON store
)

// Flow trigger types. Mirrors TaskManager schedule semantics.
const (
	FlowTriggerOnce     = "once"
	FlowTriggerDelayed  = "delayed"
	FlowTriggerRecurring = "recurring"
)

// Flow status values. Mirrors TaskStatus vocabulary.
const (
	FlowStatusEnabled  = "enabled"
	FlowStatusDisabled = "disabled"
)

// FlowRun status values.
const (
	FlowRunStatusPending   = "pending"
	FlowRunStatusRunning   = "running"
	FlowRunStatusCompleted = "completed"
	FlowRunStatusFailed    = "failed"
)

// FlowTrigger controls when and how a flow runs.
type FlowTrigger struct {
	Type            string `json:"type"`              // "once", "delayed", "recurring"
	DelaySeconds    int    `json:"delay_seconds,omitempty"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
}

// FlowStep is a single step in a flow DAG. Only one of the step-specific
// fields is populated, selected by the Type field.
type FlowStep struct {
	ID    string          `json:"id"`              // stable ID within the flow for state references
	Type  string          `json:"type"`            // command, wait, branch, compute_diff, classify, emit
	Next  string          `json:"next,omitempty"`  // next step ID (empty = end of flow)
	OnError string        `json:"on_error,omitempty"` // "fail" (default), "continue", "retry"

	// Type=command
	CommandType string          `json:"command_type,omitempty"` // e.g. "exec", "net_connections"
	Params      json.RawMessage `json:"params,omitempty"`
	StoreAs     string          `json:"as,omitempty"` // store result under this name in flow state

	// Type=wait
	Seconds int `json:"seconds,omitempty"`

	// Type=branch
	Condition string `json:"condition,omitempty"` // simple expression: "{{state.x}} == value"
	IfTrue    string `json:"if_true,omitempty"`
	IfFalse   string `json:"if_false,omitempty"`

	// Type=compute_diff
	Left       string `json:"left,omitempty"`  // ref into state
	Right      string `json:"right,omitempty"`
	DiffAs     string `json:"as,omitempty"`

	// Type=classify
	Input       string         `json:"input,omitempty"`    // ref into state
	Rules       []ClassifyRule `json:"rules,omitempty"`    // ordered, first match wins
	ClassifyAs  string         `json:"as,omitempty"`

	// Type=emit
	Signal  string          `json:"signal,omitempty"`  // survey signal name
	Payload json.RawMessage `json:"payload,omitempty"` // state reference or literal
}

// ClassifyRule matches an input value and emits a label.
type ClassifyRule struct {
	If    string `json:"if"`    // pattern: "domain == 'github.com'" or "*bank*"
	Label string `json:"label"` // output label
}

// Flow is a saved, possibly-assigned workflow.
type Flow struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Enabled     bool       `json:"enabled"`
	Trigger     FlowTrigger `json:"trigger"`
	Steps       []FlowStep `json:"steps"`
	// AgentIDs is the set of agents this flow is assigned to. Empty = unassigned (template).
	AgentIDs    []string  `json:"agent_ids,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedBy   string    `json:"created_by,omitempty"`
}

// FlowRun is one execution instance of a flow.
type FlowRun struct {
	ID         string          `json:"id"`
	FlowID     string          `json:"flow_id"`
	AgentID    string          `json:"agent_id,omitempty"`
	Status     string          `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	Error      string          `json:"error,omitempty"`
	State      json.RawMessage `json:"state,omitempty"` // step state: {stepID: result}
}

// FlowManager stores flows and runs, persists to disk, and tracks state.
// Mirrors TaskManager API surface for consistency.
type FlowManager struct {
	mu       sync.Mutex
	flows    map[string]*Flow
	runs     map[string]*FlowRun
	savePath string
	server   *Server

	stopCh   chan struct{}
	stopOnce sync.Once
	started  bool
}

// NewFlowManager creates a new FlowManager. savePath is the file path for
// persistence (empty = no persistence). server is the *Server used for
// forwarding agent commands (may be nil for tests that only test CRUD).
func NewFlowManager(savePath string, srv *Server) *FlowManager {
	fm := &FlowManager{
		flows:    make(map[string]*Flow),
		runs:     make(map[string]*FlowRun),
		savePath: savePath,
		server:   srv,
		stopCh:   make(chan struct{}),
	}
	fm.load()
	return fm
}

// generateFlowID produces a 16-byte hex flow ID.
func generateFlowID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("flow-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// generateRunID produces a 16-byte hex run ID.
func generateRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Create creates a new flow, persists it, and returns the flow.
func (fm *FlowManager) Create(name string, description string, trigger FlowTrigger, steps []FlowStep, agentIDs []string, operatorID string) (*Flow, error) {
	if name == "" {
		return nil, fmt.Errorf("flow name is required")
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("flow must have at least one step")
	}
	if trigger.Type == "" {
		trigger.Type = FlowTriggerOnce
	}

	now := time.Now().UTC()
	flow := &Flow{
		ID:          generateFlowID(),
		Name:        name,
		Description: description,
		Enabled:     true,
		Trigger:     trigger,
		Steps:       steps,
		AgentIDs:    agentIDs,
		CreatedAt:   now,
		UpdatedAt:   now,
		CreatedBy:   operatorID,
	}

	fm.mu.Lock()
	fm.flows[flow.ID] = flow
	fm.saveLocked()
	fm.mu.Unlock()

	return flow, nil
}

// Get returns a copy of a flow by ID.
func (fm *FlowManager) Get(flowID string) (*Flow, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	flow, ok := fm.flows[flowID]
	if !ok {
		return nil, fmt.Errorf("flow %s not found", flowID)
	}
	snap := *flow
	return &snap, nil
}

// List returns all flows, sorted by name (stable order).
func (fm *FlowManager) List() []*Flow {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	result := make([]*Flow, 0, len(fm.flows))
	for _, flow := range fm.flows {
		snap := *flow
		result = append(result, &snap)
	}
	// Stable sort by name then ID
	for i := 0; i < len(result); i++ {
		for j := i + 1; j < len(result); j++ {
			if result[j].Name < result[i].Name ||
				(result[j].Name == result[i].Name && result[j].ID < result[i].ID) {
				result[i], result[j] = result[j], result[i]
			}
		}
	}
	return result
}

// Update modifies an existing flow. Only non-empty fields are applied.
func (fm *FlowManager) Update(flowID string, name string, description string, trigger *FlowTrigger, steps []FlowStep, agentIDs []string) (*Flow, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	flow, ok := fm.flows[flowID]
	if !ok {
		return nil, fmt.Errorf("flow %s not found", flowID)
	}
	if name != "" {
		flow.Name = name
	}
	if description != "" {
		flow.Description = description
	}
	if trigger != nil {
		flow.Trigger = *trigger
	}
	if steps != nil {
		if len(steps) == 0 {
			return nil, fmt.Errorf("flow must have at least one step")
		}
		flow.Steps = steps
	}
	if agentIDs != nil {
		flow.AgentIDs = agentIDs
	}
	flow.UpdatedAt = time.Now().UTC()
	fm.saveLocked()
	snap := *flow
	return &snap, nil
}

// Delete removes a flow by ID.
func (fm *FlowManager) Delete(flowID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if _, ok := fm.flows[flowID]; !ok {
		return fmt.Errorf("flow %s not found", flowID)
	}
	delete(fm.flows, flowID)
	fm.saveLocked()
	return nil
}

// Enable sets a flow to enabled.
func (fm *FlowManager) Enable(flowID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	flow, ok := fm.flows[flowID]
	if !ok {
		return fmt.Errorf("flow %s not found", flowID)
	}
	flow.Enabled = true
	flow.UpdatedAt = time.Now().UTC()
	fm.saveLocked()
	return nil
}

// Disable sets a flow to disabled.
func (fm *FlowManager) Disable(flowID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	flow, ok := fm.flows[flowID]
	if !ok {
		return fmt.Errorf("flow %s not found", flowID)
	}
	flow.Enabled = false
	flow.UpdatedAt = time.Now().UTC()
	fm.saveLocked()
	return nil
}

// RunNow creates a FlowRun for immediate execution.
func (fm *FlowManager) RunNow(flowID string, agentID string, operatorID string) (*FlowRun, error) {
	fm.mu.Lock()
	_, ok := fm.flows[flowID]
	if !ok {
		fm.mu.Unlock()
		return nil, fmt.Errorf("flow %s not found", flowID)
	}
	now := time.Now().UTC()
	run := &FlowRun{
		ID:        generateRunID(),
		FlowID:    flowID,
		AgentID:   agentID,
		Status:    FlowRunStatusPending,
		StartedAt: now,
		State:     json.RawMessage("{}"),
	}
	fm.runs[run.ID] = run
	fm.mu.Unlock()
	return run, nil
}

// GetRun returns a copy of a FlowRun by ID.
func (fm *FlowManager) GetRun(runID string) (*FlowRun, error) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	run, ok := fm.runs[runID]
	if !ok {
		return nil, fmt.Errorf("run %s not found", runID)
	}
	snap := *run
	return &snap, nil
}

// ListRuns returns runs matching the filter.
func (fm *FlowManager) ListRuns(filter RunFilter) []*FlowRun {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	result := make([]*FlowRun, 0)
	for _, run := range fm.runs {
		if filter.FlowID != "" && run.FlowID != filter.FlowID {
			continue
		}
		if filter.AgentID != "" && run.AgentID != filter.AgentID {
			continue
		}
		if filter.Status != "" && run.Status != filter.Status {
			continue
		}
		snap := *run
		result = append(result, &snap)
	}
	return result
}

// RunFilter narrows ListRuns.
type RunFilter struct {
	FlowID  string
	AgentID string
	Status  string
}

// AssignFlowToAgent adds an agent ID to a flow's AgentIDs list.
func (fm *FlowManager) AssignFlowToAgent(flowID string, agentID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	flow, ok := fm.flows[flowID]
	if !ok {
		return fmt.Errorf("flow %s not found", flowID)
	}
	for _, existing := range flow.AgentIDs {
		if existing == agentID {
			return nil // already assigned, not modified
		}
	}
	flow.AgentIDs = append(flow.AgentIDs, agentID)
	flow.UpdatedAt = time.Now().UTC()
	fm.saveLocked()
	return nil
}

// UnassignFlowFromAgent removes an agent ID from a flow's AgentIDs list.
func (fm *FlowManager) UnassignFlowFromAgent(flowID string, agentID string) error {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	flow, ok := fm.flows[flowID]
	if !ok {
		return fmt.Errorf("flow %s not found", flowID)
	}
	for i, existing := range flow.AgentIDs {
		if existing == agentID {
			flow.AgentIDs = append(flow.AgentIDs[:i], flow.AgentIDs[i+1:]...)
			flow.UpdatedAt = time.Now().UTC()
			fm.saveLocked()
			return nil
		}
	}
	return nil // not assigned — no-op
}

// ListFlowsForAgent returns flows assigned to the given agent ID.
func (fm *FlowManager) ListFlowsForAgent(agentID string) []*Flow {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	result := make([]*Flow, 0)
	for _, flow := range fm.flows {
		for _, assigned := range flow.AgentIDs {
			if assigned == agentID {
				snap := *flow
				result = append(result, &snap)
				break
			}
		}
	}
	return result
}

// saveLocked persists flows to disk. Caller must hold fm.mu.
func (fm *FlowManager) saveLocked() {
	if fm.savePath == "" {
		return
	}
	dir := ""
	for i := len(fm.savePath) - 1; i >= 0; i-- {
		if fm.savePath[i] == '/' {
			dir = fm.savePath[:i]
			break
		}
	}
	if dir != "" && dir != "/" {
		os.MkdirAll(dir, 0755)
	}
	data, err := json.MarshalIndent(fm.flows, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[flows] save marshal error: %v\n", err)
		return
	}
	if err := os.WriteFile(fm.savePath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "[flows] save write error: %v\n", err)
	}
}

// load reads flows from disk.
func (fm *FlowManager) load() {
	if fm.savePath == "" {
		return
	}
	data, err := os.ReadFile(fm.savePath)
	if err != nil {
		return // file doesn't exist yet
	}
	var flows map[string]*Flow
	if err := json.Unmarshal(data, &flows); err != nil {
		fmt.Fprintf(os.Stderr, "[flows] load unmarshal error: %v\n", err)
		return
	}
	fm.flows = flows
	if fm.flows == nil {
		fm.flows = make(map[string]*Flow)
	}
	log.Printf("[flows] loaded %d flows from %s", len(fm.flows), fm.savePath)
}
