// Package replicator spawns and tracks detached child copies of this binary
// ("replicator agents") in connect mode, with built-in settings passed via
// flags/env (never argv for the token). It is a stdlib-only leaf so it can be
// imported by both cmd/probe and internal/server without an import cycle.
package replicator

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Record is a persisted spawn record for a replicated child agent.
type Record struct {
	Name        string `json:"name"`
	PID         int    `json:"pid"`
	Server      string `json:"server"`
	Mode        string `json:"mode"`
	Permissions string `json:"permissions"`
	SpawnedAt   int64  `json:"spawned_at"` // unix nanos; fingerprint seed
	Status      string `json:"status"`     // running | orphaned | dead
}

// Replicator manages the lifecycle of spawned child agents.
type Replicator struct {
	mu     sync.Mutex
	exe    string
	byName map[string]*child
	path   string // persistence file ("" = no persistence)
}

type child struct {
	record Record
	cmd    *exec.Cmd // nil for orphans restored from disk after restart
}

// New creates a Replicator that spawns the given executable (usually
// os.Executable()).
func New(exe, persistPath string) *Replicator {
	return &Replicator{
		exe:    exe,
		byName: make(map[string]*child),
		path:   persistPath,
	}
}

// Spawn launches a detached child in connect mode. The token is passed via
// PROBE_TOKEN env (never argv, so it stays off /proc/<pid>/cmdline), and the
// child env is filtered so it does not inherit the parent's own PROBE_TOKEN.
func (r *Replicator) Spawn(name, server, token, mode, permissions string) (*Record, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	if server == "" {
		return nil, fmt.Errorf("server is required")
	}
	r.mu.Lock()
	if _, exists := r.byName[name]; exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("replica %q already exists", name)
	}
	r.mu.Unlock()

	args := []string{"connect", "--server", server, "--name", name}
	if mode != "" {
		args = append(args, "--mode", mode)
	}
	if permissions != "" {
		args = append(args, "--permissions", permissions)
	}

	cmd := exec.Command(r.exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = filteredEnv(token)
	setProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn replica %q: %w", name, err)
	}

	rec := &Record{
		Name:        name,
		PID:         cmd.Process.Pid,
		Server:      server,
		Mode:        mode,
		Permissions: permissions,
		SpawnedAt:   time.Now().UnixNano(),
		Status:      "running",
	}
	r.mu.Lock()
	r.byName[name] = &child{record: *rec, cmd: cmd}
	r.mu.Unlock()
	r.persist()

	go r.reap(name, cmd)
	return rec, nil
}

// List returns the current spawn records keyed by name.
func (r *Replicator) List() map[string]Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]Record, len(r.byName))
	for n, c := range r.byName {
		out[n] = c.record
	}
	return out
}

// Kill terminates a replica by name. If the replica is orphaned (survived a
// restart, so no *exec.Cmd handle), it is killed by pid. The SpawnedAt
// fingerprint is recorded so a future reconcile can guard against pid reuse.
func (r *Replicator) Kill(name string) error {
	r.mu.Lock()
	c, ok := r.byName[name]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown replica %q", name)
	}
	if c.cmd != nil && c.cmd.Process != nil {
		if err := c.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("kill replica %q: %w", name, err)
		}
		_ = c.cmd.Wait()
	} else {
		if err := killByPid(c.record.PID); err != nil {
			return fmt.Errorf("kill orphaned replica %q: %w", name, err)
		}
	}
	r.mu.Lock()
	delete(r.byName, name)
	r.mu.Unlock()
	r.persist()
	return nil
}

// StopAll kills every tracked replica.
func (r *Replicator) StopAll() {
	r.mu.Lock()
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	r.mu.Unlock()
	for _, n := range names {
		_ = r.Kill(n)
	}
}

// Load restores spawn records from disk and reconciles status: a "running"
// record whose pid is dead becomes "dead"; a live pid with no cmd handle
// becomes "orphaned" (killable by pid). Records are retained (not dropped) so
// orphans stay visible and killable after a restart.
func (r *Replicator) Load() error {
	if r.path == "" {
		return nil
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var recs map[string]Record
	if err := json.Unmarshal(data, &recs); err != nil {
		return err
	}
	r.mu.Lock()
	for name, rec := range recs {
		switch rec.Status {
		case "running":
			if pidAlive(rec.PID) {
				rec.Status = "orphaned" // survived restart; no cmd handle
			} else {
				rec.Status = "dead"
			}
		}
		r.byName[name] = &child{record: rec, cmd: nil}
	}
	r.mu.Unlock()
	r.persist()
	return nil
}

func (r *Replicator) reap(name string, cmd *exec.Cmd) {
	_ = cmd.Wait()
	r.mu.Lock()
	if c, ok := r.byName[name]; ok && c.cmd == cmd {
		c.record.Status = "dead"
		c.cmd = nil
	}
	r.mu.Unlock()
	r.persist()
}

func (r *Replicator) persist() {
	if r.path == "" {
		return
	}
	r.mu.Lock()
	recs := make(map[string]Record, len(r.byName))
	for n, c := range r.byName {
		recs[n] = c.record
	}
	r.mu.Unlock()
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(r.path, data, 0o600)
}

// filteredEnv returns a child environment with the parent's PROBE_TOKEN
// stripped and the child's token set via PROBE_TOKEN (never argv).
func filteredEnv(token string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PROBE_TOKEN=") {
			continue
		}
		env = append(env, kv)
	}
	if token != "" {
		env = append(env, "PROBE_TOKEN="+token)
	}
	return env
}
