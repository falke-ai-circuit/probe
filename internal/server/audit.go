package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// AuditEntry records a single auditable action: who did what, to which
// agent, when, and what the outcome was.
type AuditEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	AgentID    string    `json:"agent_id"`
	OperatorID string    `json:"operator_id"`
	Action     string    `json:"action"`
	Params     any       `json:"params,omitempty"`
	Result     string    `json:"result"` // "success", "denied", "error"
	ErrorMsg   string    `json:"error_msg,omitempty"`
	DurationMs int64     `json:"duration_ms,omitempty"`
	Bypass     bool      `json:"bypass"`
	// Flow-specific fields. Present on flow.* actions so they can be
	// filtered/queried without unpacking Params.
	FlowID    string `json:"flow_id,omitempty"`
	StepID    string `json:"step_id,omitempty"`
	EventType string `json:"event_type,omitempty"`
}

// AuditFilter is used by Query to narrow the audit log.
type AuditFilter struct {
	AgentID    string    `json:"agent_id,omitempty"`
	OperatorID string    `json:"operator_id,omitempty"`
	Action     string    `json:"action,omitempty"`
	FromTime   time.Time `json:"from_time,omitempty"`
	ToTime     time.Time `json:"to_time,omitempty"`
	Limit      int       `json:"limit,omitempty"` // 0 = no limit
}

// AuditLogger persists audit entries to a JSONL file (one JSON object per
// line, append-only). It is safe for concurrent use.
type AuditLogger struct {
	mu      sync.Mutex
	filePath string
}

// NewAuditLogger creates a new AuditLogger. If filePath is non-empty,
// entries are appended to that file as JSONL. If empty, logging is
// in-memory only (useful for tests).
func NewAuditLogger(filePath string) *AuditLogger {
	if filePath != "" {
		dir := ""
		for i := len(filePath) - 1; i >= 0; i-- {
			if filePath[i] == '/' {
				dir = filePath[:i]
				break
			}
		}
		if dir != "" && dir != "/" {
			os.MkdirAll(dir, 0755)
		}
	}
	return &AuditLogger{filePath: filePath}
}

// IsActive returns true when the audit logger has a file path configured
// and will persist entries to disk.
func (al *AuditLogger) IsActive() bool {
	return al.filePath != ""
}

// LogFlow writes a flow-related audit entry. The flowID, stepID, and
// eventType are first-class fields on the audit entry so they can be
// queried/filtered without parsing the params map.
func (a *AuditLogger) LogFlow(flowID, stepID, eventType, action, agentID, operatorID string, extra map[string]string) {
	entry := AuditEntry{
		Timestamp:  time.Now().UTC(),
		FlowID:     flowID,
		StepID:     stepID,
		EventType:  eventType,
		Action:     action,
		AgentID:    agentID,
		OperatorID: operatorID,
		Result:     "success",
	}
	if extra != nil {
		entry.Params = extra
	}
	a.Log(entry)
}

// Log writes a single audit entry to the JSONL file. If the file path is
// empty the entry is silently dropped (test mode without persistence).
func (al *AuditLogger) Log(entry AuditEntry) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if al.filePath == "" {
		return
	}
	al.mu.Lock()
	defer al.mu.Unlock()

	f, err := os.OpenFile(al.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[audit] cannot open log file: %v\n", err)
		return
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[audit] marshal error: %v\n", err)
		return
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "[audit] write error: %v\n", err)
	}
}

// Query reads the audit log and returns entries matching the filter.
// Entries are returned in chronological order (oldest first). If filter.Limit
// is > 0, at most that many entries are returned (the most recent N).
func (al *AuditLogger) Query(filter AuditFilter) []AuditEntry {
	if al.filePath == "" {
		return nil
	}
	al.mu.Lock()
	defer al.mu.Unlock()

	f, err := os.Open(al.filePath)
	if err != nil {
		return nil // file doesn't exist yet
	}
	defer f.Close()

	var matches []AuditEntry
	scanner := bufio.NewScanner(f)
	// Allow lines up to 1MB for large param payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue // skip malformed lines
		}
		if !auditMatch(entry, filter) {
			continue
		}
		matches = append(matches, entry)
	}

	// Apply limit: keep the last N entries (most recent).
	if filter.Limit > 0 && len(matches) > filter.Limit {
		matches = matches[len(matches)-filter.Limit:]
	}
	return matches
}

// auditMatch returns true when the entry satisfies all non-zero filter fields.
func auditMatch(entry AuditEntry, f AuditFilter) bool {
	if f.AgentID != "" && entry.AgentID != f.AgentID {
		return false
	}
	if f.OperatorID != "" && entry.OperatorID != f.OperatorID {
		return false
	}
	if f.Action != "" && entry.Action != f.Action {
		return false
	}
	if !f.FromTime.IsZero() && entry.Timestamp.Before(f.FromTime) {
		return false
	}
	if !f.ToTime.IsZero() && entry.Timestamp.After(f.ToTime) {
		return false
	}
	return true
}