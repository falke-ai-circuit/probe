package server

// P8-10 (P8-02 §8.4) — exec idempotency.
//
// A caller may attach `idempotency_key` to exec. The key is scoped per agent
// and the record is persisted BEFORE dispatch, so a server crash between
// dispatch and result leaves an IN_FLIGHT record that is refused on replay
// rather than re-executed. That refusal is the point: a silently retried
// `shutdown` on a live port pair is worse than an operator inspecting once.
//
// States:
//
//	IN_FLIGHT — written before dispatch, no result yet. Replay → 409.
//	DONE      — the stored response envelope. Replay → 200 with replayed:true.
//	UNKNOWN   — the server restarted while IN_FLIGHT. Replay → 409; the
//	            command may or may not have run. Never retried automatically.
//
// TTL is 24 h from completion. Expired keys are purged on load and treated as
// new keys, so a key can be reused after a day.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	idemStateInFlight = "in_flight"
	idemStateDone     = "done"
	idemStateUnknown  = "unknown"

	idemTTL = 24 * time.Hour
)

// errIdemInFlight is returned when a key is still executing.
var errIdemInFlight = errors.New("idempotency key is in flight")

// errIdemStateUnknown is returned when the server cannot know whether the
// command ran (journal written, process died before the result was stored).
var errIdemStateUnknown = errors.New("idempotency state unknown")

type idemRecord struct {
	AgentID     string          `json:"agent_id"`
	Key         string          `json:"key"`
	State       string          `json:"state"`
	Body        json.RawMessage `json:"body,omitempty"`
	DispatchedAt time.Time      `json:"dispatched_at"`
	CompletedAt time.Time       `json:"completed_at,omitempty"`
}

// idempotencyStore is a small persistent journal. One file, rewritten
// atomically on every transition; the maps are guarded by a mutex because
// several exec handlers run concurrently.
type idempotencyStore struct {
	mu      sync.Mutex
	path    string
	records map[string]*idemRecord // agentID + "\x00" + key
	loaded  bool
}

func newIdempotencyStore(path string) *idempotencyStore {
	if path == "" {
		path = os.Getenv("PROBE_IDEMPOTENCY_DB")
	}
	if path == "" {
		path = "/tmp/probe-idempotency.json"
	}
	return &idempotencyStore{path: path, records: make(map[string]*idemRecord)}
}

func idemIndex(agentID, key string) string { return agentID + "\x00" + key }

// load reads the journal once. Records that were IN_FLIGHT when the previous
// process died become UNKNOWN: the command was dispatched, and whether it ran
// is not knowable from here.
func (st *idempotencyStore) load() {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.loaded {
		return
	}
	st.loaded = true
	raw, err := os.ReadFile(st.path)
	if err != nil {
		return
	}
	var recs []*idemRecord
	if err := json.Unmarshal(raw, &recs); err != nil {
		log.Printf("[idempotency] journal %s unreadable: %v (starting empty)", st.path, err)
		return
	}
	now := time.Now()
	for _, r := range recs {
		if r == nil || r.AgentID == "" || r.Key == "" {
			continue
		}
		if r.State == idemStateDone && !r.CompletedAt.IsZero() && now.Sub(r.CompletedAt) > idemTTL {
			continue
		}
		if r.State == idemStateInFlight {
			r.State = idemStateUnknown
		}
		st.records[idemIndex(r.AgentID, r.Key)] = r
	}
}

func (st *idempotencyStore) saveLocked() {
	recs := make([]*idemRecord, 0, len(st.records))
	for _, r := range st.records {
		recs = append(recs, r)
	}
	raw, err := json.Marshal(recs)
	if err != nil {
		log.Printf("[idempotency] marshal: %v", err)
		return
	}
	dir := filepath.Dir(st.path)
	if dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		log.Printf("[idempotency] write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, st.path); err != nil {
		log.Printf("[idempotency] rename: %v", err)
	}
}

// begin claims a key. It returns (storedBody, state, error):
//
//	state "new"      → the caller must dispatch, then call complete or abort.
//	state "replay"   → storedBody is the completed envelope; no dispatch.
//	state "inflight" → errIdemInFlight.
//	state "unknown"  → errIdemStateUnknown.
func (st *idempotencyStore) begin(agentID, key string) (json.RawMessage, string, error) {
	st.load()
	st.mu.Lock()
	defer st.mu.Unlock()

	idx := idemIndex(agentID, key)
	now := time.Now()
	if r, ok := st.records[idx]; ok {
		// Expiry applies to completed records only; an in-flight record is
		// never expired out from under a live call.
		if r.State == idemStateDone && !r.CompletedAt.IsZero() && now.Sub(r.CompletedAt) > idemTTL {
			delete(st.records, idx)
		} else {
			switch r.State {
			case idemStateDone:
				return r.Body, "replay", nil
			case idemStateInFlight:
				return nil, "inflight", errIdemInFlight
			case idemStateUnknown:
				return nil, "unknown", errIdemStateUnknown
			}
		}
	}

	st.records[idx] = &idemRecord{
		AgentID:      agentID,
		Key:          key,
		State:        idemStateInFlight,
		DispatchedAt: now,
	}
	st.saveLocked()
	return nil, "new", nil
}

// complete stores the envelope that was returned to the caller. The stored
// bytes are the redacted body, so a replay cannot leak a credential value the
// first response already filtered.
func (st *idempotencyStore) complete(agentID, key string, body []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()
	idx := idemIndex(agentID, key)
	r, ok := st.records[idx]
	if !ok {
		r = &idemRecord{AgentID: agentID, Key: key}
		st.records[idx] = r
	}
	r.State = idemStateDone
	r.Body = append(json.RawMessage(nil), body...)
	r.CompletedAt = time.Now()
	st.saveLocked()
}

// abort removes a claim for a call that provably did not reach the agent.
func (st *idempotencyStore) abort(agentID, key string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.records, idemIndex(agentID, key))
	st.saveLocked()
}

// markUnknown keeps a claim for a call that may have reached the agent but did
// not produce a result (timeout, agent dropped). Replays are refused.
func (st *idempotencyStore) markUnknown(agentID, key string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if r, ok := st.records[idemIndex(agentID, key)]; ok && r.State == idemStateInFlight {
		r.State = idemStateUnknown
		st.saveLocked()
	}
}

// serverIDem store is lazy so tests that construct a Server literal still work.
func (s *Server) idempotency() *idempotencyStore {
	st := s.idemStore
	if st != nil {
		return st
	}
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	if s.idemStore == nil {
		s.idemStore = newIdempotencyStore("")
	}
	return s.idemStore
}

// validateIdempotencyKey keeps the key a bounded printable token: it becomes
// part of a journal file and an error message, so newlines and control bytes
// are refused rather than escaped.
func validateIdempotencyKey(k string) error {
	if len(k) > 200 {
		return fmt.Errorf("idempotency_key exceeds 200 bytes")
	}
	for _, r := range k {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("idempotency_key contains a control character")
		}
	}
	return nil
}
