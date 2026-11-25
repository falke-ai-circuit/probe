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

// Task schedule types.
const (
	ScheduleOnce     = "once"     // execute once immediately (or at execute_at)
	ScheduleDelayed  = "delayed"  // execute once after delay_seconds
	ScheduleRecurring = "recurring" // execute every interval_seconds
)

// Task status values.
const (
	TaskStatusPending   = "pending"
	TaskStatusQueued    = "queued"
	TaskStatusRunning   = "running"
	TaskStatusCompleted = "completed"
	TaskStatusFailed    = "failed"
	TaskStatusCancelled = "cancelled"
)

// Task represents a scheduled or queued command to be forwarded to an agent.
type Task struct {
	ID          string          `json:"id"`
	AgentID     string          `json:"agent_id"`
	CommandType string          `json:"command_type"` // exec, fs_read, etc.
	Params      json.RawMessage `json:"params"`
	Schedule    TaskSchedule    `json:"schedule"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	ExecuteAt   time.Time       `json:"execute_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	OperatorID  string          `json:"operator_id,omitempty"`
}

// TaskSchedule controls when and how a task runs.
type TaskSchedule struct {
	Type            string `json:"type"`              // "once", "delayed", "recurring"
	DelaySeconds    int    `json:"delay_seconds,omitempty"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	MaxRetries      int    `json:"max_retries,omitempty"`
	RetryCount      int    `json:"retry_count,omitempty"`
}

// TaskFilter is used by List to filter tasks.
type TaskFilter struct {
	AgentID string
	Status  string
}

// TaskManager stores scheduled tasks, persists them to disk, and runs a
// background goroutine that executes pending tasks when their target agent
// is connected. Tasks whose agent is offline remain pending (offline queue)
// and are executed on reconnect.
type TaskManager struct {
	mu       sync.Mutex
	tasks    map[string]*Task
	savePath string
	server   *Server // reference for forwarding commands to agents

	stopCh   chan struct{}
	stopOnce sync.Once
	started  bool
}

// NewTaskManager creates a new TaskManager. savePath is the file path for
// persistence (empty = no persistence). server is the *Server used to
// forward commands to agents (may be nil for tests that only test CRUD).
func NewTaskManager(savePath string, srv *Server) *TaskManager {
	tm := &TaskManager{
		tasks:    make(map[string]*Task),
		savePath: savePath,
		server:   srv,
		stopCh:   make(chan struct{}),
	}
	tm.load()
	return tm
}

// generateTaskID produces a 16-byte hex task ID.
func generateTaskID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Create creates a new task, persists it, and returns the task.
func (tm *TaskManager) Create(agentID, commandType string, params json.RawMessage, schedule TaskSchedule, operatorID string) (*Task, error) {
	if agentID == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	if commandType == "" {
		return nil, fmt.Errorf("command_type is required")
	}
	if schedule.Type == "" {
		schedule.Type = ScheduleOnce
	}

	now := time.Now().UTC()
	task := &Task{
		ID:          generateTaskID(),
		AgentID:     agentID,
		CommandType: commandType,
		Params:      params,
		Schedule:    schedule,
		Status:      TaskStatusPending,
		CreatedAt:   now,
		ExecuteAt:   tm.computeExecuteAt(now, schedule),
		OperatorID:  operatorID,
	}

	tm.mu.Lock()
	tm.tasks[task.ID] = task
	tm.saveLocked()
	tm.mu.Unlock()

	return task, nil
}

// computeExecuteAt determines when a task should first execute based on its
// schedule type.
func (tm *TaskManager) computeExecuteAt(now time.Time, schedule TaskSchedule) time.Time {
	switch schedule.Type {
	case ScheduleDelayed:
		if schedule.DelaySeconds > 0 {
			return now.Add(time.Duration(schedule.DelaySeconds) * time.Second)
		}
		return now
	case ScheduleRecurring:
		if schedule.IntervalSeconds > 0 {
			return now.Add(time.Duration(schedule.IntervalSeconds) * time.Second)
		}
		return now
	default: // once
		return now
	}
}

// Cancel marks a task as cancelled. Returns an error if the task doesn't
// exist or is already in a terminal state (completed, failed, cancelled).
func (tm *TaskManager) Cancel(taskID string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	task, ok := tm.tasks[taskID]
	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}
	switch task.Status {
	case TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
		return fmt.Errorf("cannot cancel task in %s state", task.Status)
	}
	task.Status = TaskStatusCancelled
	now := time.Now().UTC()
	task.CompletedAt = &now
	tm.saveLocked()
	return nil
}

// Get returns a copy of a task by ID.
func (tm *TaskManager) Get(taskID string) (*Task, error) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	task, ok := tm.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	snap := *task
	return &snap, nil
}

// List returns tasks matching the given filter. A zero-value filter returns
// all tasks.
func (tm *TaskManager) List(filter TaskFilter) []*Task {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	result := make([]*Task, 0, len(tm.tasks))
	for _, task := range tm.tasks {
		if filter.AgentID != "" && task.AgentID != filter.AgentID {
			continue
		}
		if filter.Status != "" && task.Status != filter.Status {
			continue
		}
		snap := *task
		result = append(result, &snap)
	}
	return result
}

// ExecutePending checks all pending tasks whose ExecuteAt has passed. For
// each, if the target agent is connected, the command is forwarded and the
// result is stored. If the agent is offline, the task remains pending
// (offline queue — it will be retried on the next tick when the agent
// reconnects). Recurring tasks are rescheduled after successful execution.
func (tm *TaskManager) ExecutePending() {
	tm.mu.Lock()
	// Snapshot the pending tasks that are due, so we can execute without
	// holding the lock during the (potentially slow) agent round-trip.
	now := time.Now().UTC()
	var due []*Task
	for _, task := range tm.tasks {
		if task.Status != TaskStatusPending {
			continue
		}
		if now.Before(task.ExecuteAt) {
			continue
		}
		due = append(due, task)
	}
	tm.mu.Unlock()

	for _, task := range due {
		tm.executeTask(task)
	}
}

// executeTask forwards a single task's command to its agent. It handles
// status transitions, result storage, and recurring rescheduling. If the
// agent is offline the task stays pending.
func (tm *TaskManager) executeTask(task *Task) {
	srv := tm.server
	if srv == nil {
		return // no server reference — nothing to do (test mode)
	}

	// Check if agent is connected.
	srv.mu.RLock()
	_, connected := srv.conns[task.AgentID]
	srv.mu.RUnlock()
	if !connected {
		// Agent offline — leave pending. It will be retried next tick.
		return
	}

	// Mark running.
	now := time.Now().UTC()
	tm.mu.Lock()
	if task.Status != TaskStatusPending {
		// Task was cancelled or picked up concurrently.
		tm.mu.Unlock()
		return
	}
	task.Status = TaskStatusRunning
	task.StartedAt = &now
	tm.saveLocked()
	tm.mu.Unlock()

	// Forward to agent (without holding tm.mu).
	var params interface{}
	if len(task.Params) > 0 {
		params = task.Params
	}
	resp, err := srv.forwardToAgent(task.AgentID, task.CommandType, params)

	finishTime := time.Now().UTC()
	tm.mu.Lock()
	defer tm.mu.Unlock()
	task.CompletedAt = &finishTime

	if err != nil {
		task.Error = err.Error()
		// Retry logic for tasks with max_retries > 0.
		if task.Schedule.RetryCount < task.Schedule.MaxRetries {
			task.Schedule.RetryCount++
			task.Status = TaskStatusPending
			task.StartedAt = nil
			task.CompletedAt = nil
			task.ExecuteAt = finishTime.Add(time.Duration(task.Schedule.IntervalSeconds) * time.Second)
			log.Printf("[tasks] task %s failed (%v), retry %d/%d scheduled for %v",
				task.ID, err, task.Schedule.RetryCount, task.Schedule.MaxRetries, task.ExecuteAt)
		} else {
			task.Status = TaskStatusFailed
			log.Printf("[tasks] task %s failed after %d retries: %v", task.ID, task.Schedule.RetryCount, err)
		}
		tm.saveLocked()
		return
	}

	// Success — store result.
	resultData, _ := json.Marshal(resp)
	task.Result = resultData
	task.Error = ""

	// Recurring tasks get rescheduled.
	if task.Schedule.Type == ScheduleRecurring && task.Schedule.IntervalSeconds > 0 {
		task.Status = TaskStatusPending
		task.StartedAt = nil
		task.CompletedAt = nil
		task.ExecuteAt = finishTime.Add(time.Duration(task.Schedule.IntervalSeconds) * time.Second)
		log.Printf("[tasks] recurring task %s rescheduled for %v", task.ID, task.ExecuteAt)
	} else {
		task.Status = TaskStatusCompleted
	}
	tm.saveLocked()
}

// Start launches the background goroutine that calls ExecutePending every
// 5 seconds. Safe to call once; additional calls are no-ops.
func (tm *TaskManager) Start() {
	tm.mu.Lock()
	if tm.started {
		tm.mu.Unlock()
		return
	}
	tm.started = true
	tm.mu.Unlock()
	go tm.runLoop()
}

// Stop halts the background goroutine. Safe to call multiple times.
func (tm *TaskManager) Stop() {
	tm.stopOnce.Do(func() {
		close(tm.stopCh)
	})
}

// runLoop is the background ticker loop.
func (tm *TaskManager) runLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			tm.ExecutePending()
		case <-tm.stopCh:
			return
		}
	}
}

// saveLocked persists tasks to disk. Caller must hold tm.mu.
func (tm *TaskManager) saveLocked() {
	if tm.savePath == "" {
		return
	}
	dir := ""
	for i := len(tm.savePath) - 1; i >= 0; i-- {
		if tm.savePath[i] == '/' {
			dir = tm.savePath[:i]
			break
		}
	}
	if dir != "" && dir != "/" {
		os.MkdirAll(dir, 0755)
	}
	data, err := json.MarshalIndent(tm.tasks, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[tasks] save marshal error: %v\n", err)
		return
	}
	if err := writeFileAtomic(tm.savePath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "[tasks] save write error: %v\n", err)
	}
}

// load reads tasks from disk.
func (tm *TaskManager) load() {
	if tm.savePath == "" {
		return
	}
	data, err := os.ReadFile(tm.savePath)
	if err != nil {
		return // file doesn't exist yet
	}
	var tasks map[string]*Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		fmt.Fprintf(os.Stderr, "[tasks] load unmarshal error: %v\n", err)
		return
	}
	// Tasks that were running when the server crashed should be reset to
	// pending so they retry on next ExecutePending cycle.
	for _, task := range tasks {
		if task.Status == TaskStatusRunning {
			task.Status = TaskStatusPending
			task.StartedAt = nil
		}
	}
	tm.tasks = tasks
}