package server

// P19-06 leg 1 — rotatedTokens survives a restart.
//
// P19-05R F1 (P19R1 falsify at d4ddc88): rotatedTokens was memory-only — one
// write in the rotate path, zero persistence repo-wide. A server restart
// emptied the slot, so an agent presenting its rotated token took 401s, its
// pickup path could not help (the file already holds the rotated value, and
// the pre-rotation token was discarded at adoption), and the agent bricked at
// the 5x401 typed exit until console access.
//
// The fix reuses the repo's own persistence pattern: every store that already
// survives restart (enrollment.json, operators.json, relay identities) loads
// at boot from a file path set before Start/StartTLS and rewrites on change
// (EnrollmentManager.save). The rotatedTokens map gets the same shape here —
// map + mutex + atomic rewrite via the existing writeFileAtomic helper (temp
// + rename + fsync, contract in atomic_write.go).
//
// Storage shape deliberately maps agentID -> token, one flat file. Plaintext
// is the accepted cost, matching enrollment.json (which persists enrollment
// tokens plaintext, 0600): both are operator-recoverable credential stores in
// the server's own persistent volume (/data/runtime in the shipped compose).

import (
	"encoding/json"
	"log"
	"os"
	"sync"
)

// rotatedTokensDoc is the on-disk shape of the rotated-token store.
type rotatedTokensDoc struct {
	Rotated map[string]string `json:"rotated"`
}

// rotatedTokenStore is the persistent backing of s.rotatedTokens. The Server
// embeds a pointer; a nil store (tests that construct bare maps) means
// "persistence off" and every method degrades to the in-memory map only.
type rotatedTokenStore struct {
	mu   sync.Mutex
	path string
	rows map[string]string
}

// newRotatedTokenStore loads rows from path (missing file = empty store) and
// returns the ready store.
func newRotatedTokenStore(path string) *rotatedTokenStore {
	st := &rotatedTokenStore{path: path}
	rows, err := st.load()
	if err != nil {
		// Fail loud but not fatal: a corrupt row is logged and skipped, the
		// server still boots (operators can re-rotate; the alternative —
		// refusing to start over a token file — turns a recovery file into
		// a deployment outage). Corrupt-file behaviour mirrors
		// relayIdentity load semantics.
		log.Printf("[server] P19-06 rotated-token load from %s: %v", path, err)
		return st
	}
	st.mu.Lock()
	st.rows = rows
	st.mu.Unlock()
	if len(rows) > 0 {
		log.Printf("[server] P19-06 loaded %d rotated token(s) from %s", len(rows), path)
	}
	return st
}

// load reads the store file. Missing file is an empty, error-free result.
func (st *rotatedTokenStore) load() (map[string]string, error) {
	rows := make(map[string]string)
	if st.path == "" {
		return rows, nil
	}
	data, err := os.ReadFile(st.path)
	if err != nil {
		if os.IsNotExist(err) {
			return rows, nil
		}
		return rows, err
	}
	if len(data) == 0 {
		return rows, nil
	}
	var doc rotatedTokensDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return rows, err
	}
	if doc.Rotated == nil {
		return rows, nil
	}
	return doc.Rotated, nil
}

// set records agentID -> token under the store lock, calls put to update the
// caller's authoritative map under ITS lock (tokenMu) — put runs inside the
// store lock, so the two stay jointly consistent — and rewrites the file
// atomically. Persist failure is loud but does not roll back the in-memory
// write: the live map remains the truth for a running server, the file is the
// truth across a restart.
func (st *rotatedTokenStore) set(agentID, token string, put func()) error {
	if st == nil {
		put()
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.rows == nil {
		st.rows = make(map[string]string)
	}
	st.rows[agentID] = token
	put()
	return st.saveLocked()
}

// postLoad applies a loaded row set into the live map under tokenMu. Called
// once at boot before connections are accepted.
func (st *rotatedTokenStore) postLoad(into map[string]string, tokenMu *sync.Mutex) {
	if st == nil {
		return
	}
	st.mu.Lock()
	rows := st.rows
	st.mu.Unlock()
	tokenMu.Lock()
	for id, tok := range rows {
		into[id] = tok
	}
	tokenMu.Unlock()
}

// saveLocked rewrites the store file atomically. Caller holds st.mu.
// 0600: the file holds live bearer tokens.
func (st *rotatedTokenStore) saveLocked() error {
	if st.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(rotatedTokensDoc{Rotated: st.rows}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(st.path, data, 0o600); err != nil {
		return err
	}
	log.Printf("[server] P19-06 persisted rotated token store (%d row(s)) -> %s", len(st.rows), st.path)
	return nil
}
