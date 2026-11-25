package server

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// Stale-detection tuning. The background goroutine started by
// StartStaleDetector scans every staleCheckInterval for agents whose
// last heartbeat is older than staleThreshold and marks them "stale".
const (
	staleCheckInterval = 30 * time.Second
	staleThreshold     = 90 * time.Second
	// sameErrorThreshold is the number of consecutive identical errors after
	// which an agent is flagged NeedsAttention and its health score is capped
	// below 0.5 (audit F4 — repeated same-error auto-update failures).
	sameErrorThreshold = 5

	// P12-03: liveness probe tuning. The server pings every connected agent
	// and waits pingProbeTimeout for the pong. pingMissThreshold consecutive
	// unanswered pings mark the agent stale immediately — a wedged process
	// stops answering long before its TCP socket dies, which is the case
	// where exec/fs-write returned 503 while /health answered "ok".
	pingProbeInterval = 20 * time.Second
	pingProbeTimeout  = 5 * time.Second
	pingMissThreshold = 3

	// ExecutionContextAgentOwn is the contract selector id (P10-07 v3.1 §5.1)
	// for "no selector sent: the agent's own context". It is what an agent
	// serves until it advertises a concrete context id; the agent wire
	// protocol carries no context field today, so this is the honest value,
	// not a placeholder for a number that was dropped.
	ExecutionContextAgentOwn = "agent_own"
)

// ResourceInfo describes an agent's resource usage, typically reported
// via health_result messages. Zero values mean "not reported".
type ResourceInfo struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemoryMB   float64 `json:"memory_mb"`
	DiskFreeMB float64 `json:"disk_free_mb"`
}

// AgentRecord represents a registered agent.
type AgentRecord struct {
	AgentID       string        `json:"agent_id"`
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	OS            string        `json:"os"`
	Arch          string        `json:"arch"`
	Mode          string        `json:"mode"`
	Capabilities  []string      `json:"capabilities,omitempty"` // capabilities advertised by the agent
	ConnectedAt   string        `json:"connected_at"`
	LastHeartbeat string        `json:"last_heartbeat"`
	Status        string        `json:"status"` // "active", "inactive", "stale", "error"
	UptimeSeconds int64         `json:"uptime_seconds"`
	LastError     string        `json:"last_error,omitempty"`
	ErrorCount    int           `json:"error_count"`
	SameErrorCount int          `json:"same_error_count,omitempty"` // consecutive repeats of the same error (audit F4)
	NeedsAttention bool         `json:"needs_attention,omitempty"`  // same error persisted past threshold (audit F4)
	HealthScore   float64       `json:"health_score"` // 0.0-1.0 composite
	ResourceUsage *ResourceInfo `json:"resource_usage,omitempty"`

	// P12-03 — agent-state visibility. These are derived on every read
	// (applyLiveness), never trusted from the persisted file: a row that says
	// "active" because a previous server process wrote it is not evidence
	// that an agent is serving.
	LastSeen                string `json:"last_seen"`                 // RFC3339, last moment the agent was actually heard from
	LastSeenAgeSeconds      int64  `json:"last_seen_age_seconds"`     // -1 = never heard
	Stale                   bool   `json:"stale"`                     // true = not currently serving; read this, do not infer
	StaleReason             string `json:"stale_reason"`              // "ok" | "no_transport" | "inactive" | "heartbeat_age" | "ping_unanswered"
	StaleThresholdSeconds   int64  `json:"stale_threshold_seconds"`   // the threshold `stale` was derived from
	Reconnects              int    `json:"reconnects"`                // monotonic count of re-registrations
	ConnectionStartedAt     string `json:"connection_started_at"`     // RFC3339 start of the CURRENT connection
	ConnectionUptimeSeconds int64  `json:"connection_uptime_seconds"` // age of the current connection
	ExecutionContext        string `json:"execution_context"`         // contract selector id the agent serves
	PingTimeouts            int    `json:"ping_timeouts"`             // monotonic count of unanswered pings
	PingMisses              int    `json:"ping_misses"`               // current consecutive unanswered pings
	LastPingTimeoutAt       string `json:"last_ping_timeout_at,omitempty"`

	// internal — not serialized; parsed from the string fields on load.
	connectedAt         time.Time `json:"-"`
	lastHeartbeat       time.Time `json:"-"`
	connectionStartedAt time.Time `json:"-"`
}

// computeHealthScore calculates a 0.0-1.0 composite health score:
//   - heartbeat recency (0.0–0.4): fresh heartbeat = 0.4, decays linearly
//     to 0 once the heartbeat is older than staleThreshold (90s)
//   - error count (0.0–0.3): 0 errors = 0.3, each error subtracts 0.05,
//     floored at 0
//   - uptime stability (0.0–0.3): scales linearly from 0 to 0.3 over the
//     first 5 minutes of uptime, capped at 0.3
//   - same-error persistence (audit F4): a repeated identical error is more
//     severe than distinct errors — it indicates a stuck condition. The
//     sameErrorThreshold (default 5) consecutive repeats of the same error
//     caps the total score at 0.4 (below the 0.5 needs-attention line) and
//     raises NeedsAttention.
func (rec *AgentRecord) computeHealthScore(now time.Time) float64 {
	var score float64

	// heartbeat recency (0.0–0.4)
	if !rec.lastHeartbeat.IsZero() {
		hbAge := now.Sub(rec.lastHeartbeat)
		if hbAge <= 0 {
			score += 0.4
		} else if hbAge >= staleThreshold {
			score += 0.0
		} else {
			score += 0.4 * (1.0 - float64(hbAge)/float64(staleThreshold))
		}
	}

	// error count (0.0–0.3)
	errPart := 0.3 - float64(rec.ErrorCount)*0.05
	if errPart < 0 {
		errPart = 0
	}
	score += errPart

	// uptime stability (0.0–0.3)
	if !rec.connectedAt.IsZero() {
		uptime := now.Sub(rec.connectedAt)
		if uptime >= 5*time.Minute {
			score += 0.3
		} else if uptime > 0 {
			score += 0.3 * (float64(uptime) / float64(5*time.Minute))
		}
	}

	// same-error persistence penalty (audit F4): >=5 repeats of the SAME
	// error drops the score below 0.5 and flags the agent for attention.
	if rec.SameErrorCount >= sameErrorThreshold {
		if score > 0.4 {
			score = 0.4
		}
		rec.NeedsAttention = true
	}

	return score
}

// Registry holds connected agents and persists them to disk.
type Registry struct {
	mu       sync.RWMutex
	agents   map[string]*AgentRecord
	savePath string

	stopCh         chan struct{}
	stopOnce       sync.Once
	detectorStarted bool
}

// NewRegistry creates a new agent registry.
func NewRegistry(savePath string) *Registry {
	r := &Registry{
		agents:   make(map[string]*AgentRecord),
		savePath: savePath,
		stopCh:   make(chan struct{}),
	}
	r.load()
	return r
}

// Register adds or updates an agent record.
func (r *Registry) Register(agentID, name, version, goos, arch, mode string, capabilities []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	if rec, ok := r.agents[agentID]; ok {
		rec.Name = name
		rec.Version = version
		rec.OS = goos
		rec.Arch = arch
		rec.Mode = mode
		rec.Capabilities = capabilities
		rec.LastHeartbeat = nowStr
		rec.lastHeartbeat = now
		rec.Status = "active"
		// A re-registration is a new connection: the reconnect count is
		// monotonic and the connection-scoped uptime restarts, so a caller
		// can tell a fresh process from a long-lived one.
		rec.Reconnects++
		rec.connectionStartedAt = now
		rec.ConnectionStartedAt = nowStr
		rec.PingMisses = 0
		if rec.ExecutionContext == "" {
			rec.ExecutionContext = ExecutionContextAgentOwn
		}
		// A clean re-registration implies the agent recovered — clear the
		// same-error attention flag (audit F4).
		rec.SameErrorCount = 0
		rec.NeedsAttention = false
		rec.HealthScore = rec.computeHealthScore(now)
	} else {
		rec := &AgentRecord{
			AgentID:             agentID,
			Name:                name,
			Version:             version,
			OS:                  goos,
			Arch:                arch,
			Mode:                mode,
			Capabilities:        capabilities,
			ConnectedAt:         nowStr,
			LastHeartbeat:       nowStr,
			Status:              "active",
			ConnectionStartedAt: nowStr,
			ExecutionContext:    ExecutionContextAgentOwn,
		}
		rec.connectedAt = now
		rec.lastHeartbeat = now
		rec.connectionStartedAt = now
		rec.HealthScore = rec.computeHealthScore(now)
		r.agents[agentID] = rec
	}
	r.save()
}

// UpdateCapabilities updates the capability list advertised by an agent, e.g.
// when capabilities arrive via a health report or heartbeat. Empty input is
// ignored so a report lacking capabilities can't wipe the persisted set.
func (r *Registry) UpdateCapabilities(agentID string, capabilities []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.agents[agentID]
	if !ok {
		return
	}
	if len(capabilities) == 0 {
		return
	}
	rec.Capabilities = capabilities
	r.save()
}

// Unregister marks an agent as inactive. It keeps the row: disconnect
// bookkeeping wants the agent to remain visible with its history. Callers
// that mean "delete this row" must use Remove instead.
//
// P12-03: a disconnect does NOT refresh last_seen. last_seen answers "when
// was this agent last heard from", and the disconnect moment is not a
// message from the agent — overwriting it would make a dead agent look
// fresh for a whole staleThreshold.
func (r *Registry) Unregister(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.agents[agentID]; ok {
		now := time.Now().UTC()
		rec.Status = "inactive"
		rec.PingMisses = 0
		rec.HealthScore = rec.computeHealthScore(now)
		r.save()
	}
}

// Remove deletes an agent from the in-memory map and from the persisted
// registry, and reports whether a record existed. This is the operator
// delete path — after Remove the id must not appear in ListAgents or in
// the registry file. Unregister is not a substitute: it only flips status.
func (r *Registry) Remove(agentID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[agentID]; !ok {
		return false
	}
	delete(r.agents, agentID)
	r.save()
	return true
}

// applyLiveness fills the derived liveness fields from `now`. Every read
// path calls it, so `stale` is always a statement about the present, never a
// value that happened to be persisted. Caller may hold mu (it only touches
// the caller's copy).
func (rec *AgentRecord) applyLiveness(now time.Time) {
	rec.StaleThresholdSeconds = int64(staleThreshold.Seconds())
	age := int64(-1)
	if !rec.lastHeartbeat.IsZero() {
		d := now.Sub(rec.lastHeartbeat)
		if d < 0 {
			d = 0
		}
		age = int64(d.Seconds())
		rec.LastSeen = rec.lastHeartbeat.UTC().Format(time.RFC3339)
	} else if rec.LastHeartbeat != "" {
		// Loaded from disk before the record was ever heard from in this
		// process: report the stored stamp, never a synthesized one.
		rec.LastSeen = rec.LastHeartbeat
	}
	rec.LastSeenAgeSeconds = age

	if !rec.connectionStartedAt.IsZero() {
		up := int64(now.Sub(rec.connectionStartedAt).Seconds())
		if up < 0 {
			up = 0
		}
		// A finished connection has no "current" uptime: report 0 and keep
		// the start stamp as history. Reporting a growing age for a
		// connection that is over is the same class of lie as a green
		// health row for a dead agent.
		if rec.Status == "inactive" {
			up = 0
		}
		rec.ConnectionUptimeSeconds = up
		rec.ConnectionStartedAt = rec.connectionStartedAt.UTC().Format(time.RFC3339)
	}

	switch {
	case rec.Status == "inactive":
		rec.Stale, rec.StaleReason = true, "inactive"
	case rec.Status == "error":
		rec.Stale, rec.StaleReason = true, "error"
	case age < 0:
		rec.Stale, rec.StaleReason = true, "never_seen"
	case age > rec.StaleThresholdSeconds:
		rec.Stale, rec.StaleReason = true, "heartbeat_age"
	case rec.PingMisses >= pingMissThreshold:
		rec.Stale, rec.StaleReason = true, "ping_unanswered"
	case rec.Status != "active":
		rec.Stale, rec.StaleReason = true, rec.Status
	default:
		rec.Stale, rec.StaleReason = false, "ok"
	}
}

// ListAgents returns a slice of all agent records with fresh uptime and
// health scores computed on read.
func (r *Registry) ListAgents() []AgentRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	now := time.Now()
	result := make([]AgentRecord, 0, len(r.agents))
	for _, rec := range r.agents {
		snap := *rec // value copy
		if !snap.connectedAt.IsZero() {
			snap.UptimeSeconds = int64(now.Sub(snap.connectedAt).Seconds())
			if snap.UptimeSeconds < 0 {
				snap.UptimeSeconds = 0
			}
		}
		snap.HealthScore = snap.computeHealthScore(now)
		snap.applyLiveness(now)
		result = append(result, snap)
	}
	return result
}

// Heartbeat updates the last heartbeat timestamp for an agent.
func (r *Registry) Heartbeat(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.agents[agentID]; ok {
		now := time.Now().UTC()
		rec.LastHeartbeat = now.Format(time.RFC3339)
		rec.lastHeartbeat = now
		rec.Status = "active"
		rec.HealthScore = rec.computeHealthScore(now)
		r.save()
	}
}

// RecordError records an error for an agent: sets LastError, increments
// ErrorCount (and SameErrorCount when the error repeats identically), and
// recalculates HealthScore. When the same error persists past
// sameErrorThreshold, the agent is flagged NeedsAttention and its score is
// capped below 0.5 (audit F4).
func (r *Registry) RecordError(agentID string, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.agents[agentID]
	if !ok {
		return
	}
	if rec.LastError == errMsg && errMsg != "" {
		rec.SameErrorCount++
	} else {
		rec.SameErrorCount = 1
	}
	rec.LastError = errMsg
	rec.ErrorCount++
	rec.HealthScore = rec.computeHealthScore(time.Now())
	r.save()
}

// UpdateHealth updates resource usage for an agent, refreshes the
// heartbeat (a health report proves the agent is alive), and
// recalculates HealthScore.
func (r *Registry) UpdateHealth(agentID string, resource ResourceInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.agents[agentID]
	if !ok {
		return
	}
	now := time.Now().UTC()
	rec.ResourceUsage = &resource
	rec.LastHeartbeat = now.Format(time.RFC3339)
	rec.lastHeartbeat = now
	if rec.Status == "stale" {
		rec.Status = "active"
	}
	rec.HealthScore = rec.computeHealthScore(now)
	r.save()
}

// GetHealth returns the full health record for an agent, recomputing
// uptime and health score on read.
func (r *Registry) GetHealth(agentID string) (AgentRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.agents[agentID]
	if !ok {
		return AgentRecord{}, fmt.Errorf("agent %s not found", agentID)
	}
	now := time.Now()
	if !rec.connectedAt.IsZero() {
		rec.UptimeSeconds = int64(now.Sub(rec.connectedAt).Seconds())
		if rec.UptimeSeconds < 0 {
			rec.UptimeSeconds = 0
		}
	}
	rec.HealthScore = rec.computeHealthScore(now)
	rec.applyLiveness(now)
	return *rec, nil
}

// RecordPingTimeout counts one ping the agent failed to answer. PingTimeouts
// is monotonic for the life of the record; PingMisses is the current
// consecutive run, and at pingMissThreshold the agent is marked stale
// immediately — past that point the health surface must not report it as
// serving, because it demonstrably answers nothing.
func (r *Registry) RecordPingTimeout(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.agents[agentID]
	if !ok {
		return
	}
	now := time.Now().UTC()
	rec.PingTimeouts++
	rec.PingMisses++
	rec.LastPingTimeoutAt = now.Format(time.RFC3339)
	if rec.PingMisses >= pingMissThreshold {
		rec.Status = "stale"
		log.Printf("[registry] agent %s did not answer %d consecutive pings — marked stale", agentID, rec.PingMisses)
	}
	rec.HealthScore = rec.computeHealthScore(now)
	r.save()
}

// RecordPong records an answered ping: it clears the consecutive-miss run and
// promotes a ping-marked agent back to active. Uptime and reconnect counters
// are untouched — a pong is proof of life, not a new connection.
func (r *Registry) RecordPong(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.agents[agentID]
	if !ok {
		return
	}
	now := time.Now().UTC()
	rec.PingMisses = 0
	rec.LastHeartbeat = now.Format(time.RFC3339)
	rec.lastHeartbeat = now
	if rec.PingMisses == 0 && rec.Status == "stale" {
		rec.Status = "active"
	}
	rec.HealthScore = rec.computeHealthScore(now)
	r.save()
}

// StartStaleDetector starts a background goroutine that marks agents
// "stale" when their last heartbeat exceeds staleThreshold (90s). It
// scans every staleCheckInterval (30s). Safe to call once; additional
// calls are no-ops.
func (r *Registry) StartStaleDetector() {
	r.mu.Lock()
	if r.detectorStarted {
		r.mu.Unlock()
		return
	}
	r.detectorStarted = true
	r.mu.Unlock()
	go r.runStaleDetector()
}

// runStaleDetector is the background loop. It exits when stopCh is closed.
func (r *Registry) runStaleDetector() {
	ticker := time.NewTicker(staleCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.checkStale()
		case <-r.stopCh:
			return
		}
	}
}

// checkStale marks active agents with stale heartbeats as "stale".
func (r *Registry) checkStale() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	changed := false
	for id, rec := range r.agents {
		if rec.Status == "active" && !rec.lastHeartbeat.IsZero() && now.Sub(rec.lastHeartbeat) > staleThreshold {
			rec.Status = "stale"
			rec.HealthScore = rec.computeHealthScore(now)
			log.Printf("[registry] agent %s marked stale (no heartbeat for %v)", id, now.Sub(rec.lastHeartbeat).Round(time.Second))
			changed = true
		}
	}
	if changed {
		r.save()
	}
}

// Stop halts the stale-detector goroutine. Safe to call multiple times.
func (r *Registry) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
}

// save persists the registry to disk. Caller must hold mu.
func (r *Registry) save() {
	if r.savePath == "" {
		return
	}
	dir := "/"
	for i := len(r.savePath) - 1; i >= 0; i-- {
		if r.savePath[i] == '/' {
			dir = r.savePath[:i]
			break
		}
	}
	if dir != "" && dir != "/" {
		os.MkdirAll(dir, 0755)
	}
	data, err := json.MarshalIndent(r.agents, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[registry] save marshal error: %v\n", err)
		return
	}
	if err := writeFileAtomic(r.savePath, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "[registry] save write error: %v\n", err)
	}
}

// load reads the registry from disk and reconstructs internal time fields
// from the serialized string timestamps.
func (r *Registry) load() {
	data, err := os.ReadFile(r.savePath)
	if err != nil {
		return // file doesn't exist yet, that's fine
	}
	var agents map[string]*AgentRecord
	if err := json.Unmarshal(data, &agents); err != nil {
		fmt.Fprintf(os.Stderr, "[registry] load unmarshal error: %v\n", err)
		return
	}
	r.agents = agents
	// Reconstruct internal time.Time fields from the serialized strings.
	//
	// P12-03: a row loaded from disk describes a PREVIOUS process. Whatever
	// status it was written with, no agent is connected to this process yet,
	// so status drops to "inactive" and `stale` reads true until a real
	// handshake arrives. Reporting the persisted "active" is exactly the
	// defect this unit exists to kill.
	for _, rec := range r.agents {
		rec.Status = "inactive"
		if rec.ExecutionContext == "" {
			rec.ExecutionContext = ExecutionContextAgentOwn
		}
		if rec.ConnectedAt != "" {
			if t, err := time.Parse(time.RFC3339, rec.ConnectedAt); err == nil {
				rec.connectedAt = t
			}
		}
		if rec.LastHeartbeat != "" {
			if t, err := time.Parse(time.RFC3339, rec.LastHeartbeat); err == nil {
				rec.lastHeartbeat = t
			}
		}
	}
}