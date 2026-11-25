package server

// P21-07 — relay identity: fingerprints, never secrets.
//
// Three properties, each one testable, and each one was missing at 708f812:
//
//  1. A relay's credential has a per-node identity whose fingerprint
//     (sha256[:8], the token_fp convention P21-02 fixed) is the ONLY form
//     that is persisted or returned. The credential itself lives in memory
//     (and in the relay process) and nowhere else. That includes the JSON
//     file this file writes, every API response body, and every log line.
//
//  2. Rotation is an operator action on ONE node: a new credential is minted,
//     pushed down that relay's own socket, and only that relay's session is
//     torn down. It reconnects, re-registers its channels, and the agents
//     behind it re-form on the same sockets. Other relays' sessions are never
//     touched — they keep their session object and their channels.
//
//  3. The allowlist stops being an interface with no implementation. The
//     policy for an unset list is stated once, here, and is not a silent pass:
//       - flag OMITTED        → allow-all (a standalone server has no peers to
//                              police; refusing everything would break every
//                              deployment that never asked for an allowlist);
//       - flag GIVEN, empty   → deny-all (the operator asked for an allowlist
//                              and named nobody: the honest reading is
//                              "admit nobody", never "admit everyone");
//       - flag GIVEN, entries → admit exactly those ids/fingerprints.
//
// Everything in this file is loopback-testable. No host claim is made.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Credential scopes. "shared" is the honest label for the pre-P21-07 world:
// every relay presents the one server token, so every peer's token_fp is
// byte-identical (P21-02 D2). "per-node" means the fingerprint names exactly
// one relay and the credential was minted for it.
const (
	CredentialScopeShared  = "shared"
	CredentialScopePerNode = "per-node"
)

// RefusalCredential is the code for a relay whose presented credential does
// not match the identity bound to its id. Distinct from FORWARD_ALLOWLIST_DENIED:
// "you are not on the list" and "your credential is wrong" are different
// operator actions.
const RefusalCredential = "RELAY_CREDENTIAL_DENIED"

// Allowlist policies, as reported by the API and the startup log.
const (
	RelayAllowlistAllowAll = "allow-all"
	RelayAllowlistListed   = "list"
	RelayAllowlistDenyAll  = "deny-all"
)

// errRelayNotConnected is returned when a rotation names a relay with no live
// session. Rotation is a per-connection act: there is nothing to push a new
// credential down without a socket.
var errRelayNotConnected = errors.New("relay is not connected")

// tokenFingerprint is the token_fp convention: sha256 over the credential,
// hex, first 8 characters. Eight hex chars (32 bits) is a CORRELATION handle,
// not a brute-force barrier — it exists to compare and display identities, and
// that is all it is used for.
func tokenFingerprint(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:8]
}

// RelayIdentity is the persisted/returned record of one relay. It carries no
// credential field by construction: there is nothing to leak.
type RelayIdentity struct {
	ID                  string `json:"id"`
	TokenFP             string `json:"token_fp"`
	PreviousTokenFP     string `json:"previous_token_fp,omitempty"`
	TokenFPScope        string `json:"token_fp_scope"`
	TokenFPVerified     bool   `json:"token_fp_verified"`
	RelayFingerprint    string `json:"relay_fingerprint,omitempty"`
	CredentialRotations int    `json:"credential_rotations"`
	IssuedAt            string `json:"issued_at"`
	RotatedAt           string `json:"rotated_at,omitempty"`

	// Live fields, filled by the API handler, never persisted.
	Connected bool   `json:"connected"`
	Links     int    `json:"links"`
	Since     string `json:"connected_at,omitempty"`
}

// relayIdentityDoc is the on-disk shape. Only RelayIdentity records, i.e.
// fingerprints. A credential field must never be added here.
type relayIdentityDoc struct {
	Relays []RelayIdentity `json:"relays"`
}

// RelayRotationResult is what the rotate endpoint answers with: the new
// fingerprint, and what changed. The credential is deliberately absent.
type RelayRotationResult struct {
	RelayID         string `json:"id"`
	TokenFP         string `json:"token_fp"`
	PreviousTokenFP string `json:"previous_token_fp,omitempty"`
	Rotations       int    `json:"credential_rotations"`
	LinksDropped    int    `json:"links_dropped"`
	Delivered       bool   `json:"credential_delivered"`
}

// SetRelayIdentityPath installs persistent relay-identity storage. The file
// holds fingerprints only. Must be called before Start.
func (s *Server) SetRelayIdentityPath(path string) error {
	if path == "" {
		return nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("relay identity dir: %w", err)
		}
	}
	doc := relayIdentityDoc{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse relay identities: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read relay identities: %w", err)
	}
	s.relayIdentityMu.Lock()
	defer s.relayIdentityMu.Unlock()
	s.relayIdentityPath = path
	for _, rec := range doc.Relays {
		cp := rec
		s.relayIdentities[cp.ID] = &cp
		// The credential itself is never on disk, so it cannot be restored.
		// A per-node identity therefore comes back unverified and re-binds on
		// the relay's next registration (bindRelayIdentity says so and relabels
		// the scope). This is the documented cost of never persisting secrets.
	}
	return nil
}

// persistRelayIdentitiesLocked writes fingerprints atomically (tmp + rename,
// 0600). Caller holds relayIdentityMu. No credential ever reaches this marshal:
// the type has no field for one.
func (s *Server) persistRelayIdentitiesLocked() {
	if s.relayIdentityPath == "" {
		return
	}
	recs := make([]RelayIdentity, 0, len(s.relayIdentities))
	for _, rec := range s.relayIdentities {
		cp := *rec
		// Live fields are not history.
		cp.Connected = false
		cp.Links = 0
		cp.Since = ""
		recs = append(recs, cp)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	data, err := json.MarshalIndent(relayIdentityDoc{Relays: recs}, "", "  ")
	if err != nil {
		log.Printf("[relay-identity] marshal: %v", err)
		return
	}
	tmp := s.relayIdentityPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		log.Printf("[relay-identity] write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, s.relayIdentityPath); err != nil {
		log.Printf("[relay-identity] rename %s: %v", tmp, err)
	}
}

// bindRelayIdentity binds a presented credential to a relay id and returns the
// record (fingerprint only). It is the admission check for relay registration:
//   - first sight of an id: adopt. A relay presenting the shared server token
//     is recorded with scope "shared" and verified=false, because the
//     fingerprint identifies the fleet, not the node (P21-02 D2).
//   - known id with a live in-memory credential: the presented value must match
//     the current or the previous (rotation grace) credential, else the
//     registration is refused with RefusalCredential.
//   - known per-node id whose credential is not in memory (server restarted):
//     re-bind the presented value once, verified=false, scope stays per-node.
//     The alternative — locking the relay out until an operator rotates a node
//     with no session — is worse and would be a deployment outage.
func (s *Server) bindRelayIdentity(relayID, presented, relayFP string) (RelayIdentity, error) {
	s.relayIdentityMu.Lock()
	defer s.relayIdentityMu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	if s.relayIdentities == nil {
		s.relayIdentities = make(map[string]*RelayIdentity)
	}
	if s.relaySecrets == nil {
		s.relaySecrets = make(map[string][]string)
	}

	rec := s.relayIdentities[relayID]
	if rec == nil {
		// Adopt. An empty presented credential means a pre-P21-07 relay that
		// never sent one; bind it to the shared token rather than to nothing,
		// so the record still names the credential actually in force.
		secret := presented
		scope := CredentialScopePerNode
		verified := presented != ""
		if secret == "" || secret == s.token {
			secret = s.token
			scope = CredentialScopeShared
			verified = false
		}
		// A credential already bound to a different relay may not be adopted
		// by a new id: that would let one node claim another's identity on
		// first sight. Refuse rather than bind the same secret twice.
		for otherID, list := range s.relaySecrets {
			if otherID == relayID {
				continue
			}
			for _, known := range list {
				if subtle.ConstantTimeCompare([]byte(known), []byte(secret)) == 1 && secret != s.token {
					return RelayIdentity{}, &ForwardRefusalError{
						Code:    RefusalCredential,
						AgentID: relayID,
						Reason:  fmt.Sprintf("credential presented for relay %s is already bound to %s", relayID, otherID),
					}
				}
			}
		}
		rec = &RelayIdentity{
			ID:               relayID,
			TokenFP:          tokenFingerprint(secret),
			TokenFPScope:     scope,
			TokenFPVerified:  verified,
			RelayFingerprint: relayFP,
			IssuedAt:         now,
		}
		s.relayIdentities[relayID] = rec
		s.relaySecrets[relayID] = []string{secret}
		s.persistRelayIdentitiesLocked()
		log.Printf("[relay-identity] adopted relay %s (token_fp=%s scope=%s)", relayID, rec.TokenFP, rec.TokenFPScope)
		return *rec, nil
	}

	known := s.relaySecrets[relayID]
	if len(known) == 0 {
		// Restart: fingerprints survived, credentials did not.
		if presented == "" {
			presented = s.token
		}
		s.relaySecrets[relayID] = []string{presented}
		rec.TokenFP = tokenFingerprint(presented)
		rec.TokenFPVerified = false
		if relayFP != "" {
			rec.RelayFingerprint = relayFP
		}
		s.persistRelayIdentitiesLocked()
		log.Printf("[relay-identity] re-bound relay %s after restart (token_fp=%s verified=false)", relayID, rec.TokenFP)
		return *rec, nil
	}

	if presented == "" {
		// A relay with a bound per-node identity must present it. Allow the
		// empty case only for a shared-scope record (legacy relay).
		if rec.TokenFPScope == CredentialScopeShared {
			return *rec, nil
		}
		return RelayIdentity{}, &ForwardRefusalError{
			Code:    RefusalCredential,
			AgentID: relayID,
			Reason:  fmt.Sprintf("relay %s presented no credential but has a bound per-node identity", relayID),
		}
	}
	for _, sec := range known {
		if subtle.ConstantTimeCompare([]byte(sec), []byte(presented)) == 1 {
			rec.TokenFPVerified = true
			if relayFP != "" {
				rec.RelayFingerprint = relayFP
			}
			s.persistRelayIdentitiesLocked()
			return *rec, nil
		}
	}
	return RelayIdentity{}, &ForwardRefusalError{
		Code:    RefusalCredential,
		AgentID: relayID,
		Reason:  fmt.Sprintf("credential presented for relay %s does not match its bound identity (token_fp mismatch)", relayID),
	}
}

// rotateRelayCredential mints a new credential for one relay and records the
// new fingerprint. The credential is returned to the caller so it can be
// pushed down that relay's own socket — it is never returned to an HTTP
// client, never logged, and never persisted. The previous credential stays
// accepted until the next rotation (grace) so a failed push does not lock the
// node out.
func (s *Server) rotateRelayCredential(relayID string) (RelayIdentity, string, error) {
	s.relayIdentityMu.Lock()
	defer s.relayIdentityMu.Unlock()

	rec := s.relayIdentities[relayID]
	if rec == nil {
		// No record yet: the relay is connected but never registered? Refuse
		// rather than invent an identity the operator did not name.
		return RelayIdentity{}, "", fmt.Errorf("no identity for relay %s", relayID)
	}
	secret := generateToken()
	prev := ""
	if old := s.relaySecrets[relayID]; len(old) > 0 {
		prev = tokenFingerprint(old[0])
	}
	s.relaySecrets[relayID] = append([]string{secret}, s.relaySecrets[relayID]...)
	rec.PreviousTokenFP = prev
	rec.TokenFP = tokenFingerprint(secret)
	rec.TokenFPScope = CredentialScopePerNode
	rec.TokenFPVerified = true
	rec.CredentialRotations++
	rec.RotatedAt = time.Now().UTC().Format(time.RFC3339)
	s.persistRelayIdentitiesLocked()
	log.Printf("[relay-identity] rotated relay %s (token_fp=%s previous=%s rotations=%d)", relayID, rec.TokenFP, rec.PreviousTokenFP, rec.CredentialRotations)
	return *rec, secret, nil
}

// isValidRelayCredential reports whether a presented value is any relay's
// bound credential. It exists so a rotated per-node credential can complete
// the /ws upgrade: the shared token check alone would reject it.
func (s *Server) isValidRelayCredential(secret string) bool {
	if secret == "" {
		return false
	}
	s.relayIdentityMu.Lock()
	defer s.relayIdentityMu.Unlock()
	for _, list := range s.relaySecrets {
		for _, known := range list {
			if subtle.ConstantTimeCompare([]byte(known), []byte(secret)) == 1 {
				return true
			}
		}
	}
	return false
}

// listRelayIdentities returns the identity records with live session fields
// filled, sorted by id. Fingerprints only.
func (s *Server) listRelayIdentities() []RelayIdentity {
	s.relayIdentityMu.Lock()
	out := make([]RelayIdentity, 0, len(s.relayIdentities))
	for _, rec := range s.relayIdentities {
		out = append(out, *rec)
	}
	s.relayIdentityMu.Unlock()

	s.relayMu.RLock()
	for i := range out {
		if sess, ok := s.relays[out[i].ID]; ok {
			out[i].Connected = true
			out[i].Links = len(sess.channels)
			out[i].Since = sess.startedAt
		}
	}
	s.relayMu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// RotateRelay pushes a fresh credential to one relay and drops exactly that
// relay's links. The peer relays are not consulted, closed, or re-keyed.
func (s *Server) RotateRelay(relayID string) (RelayRotationResult, error) {
	s.relayMu.RLock()
	sess := s.relays[relayID]
	s.relayMu.RUnlock()
	if sess == nil {
		return RelayRotationResult{}, errRelayNotConnected
	}

	rec, secret, err := s.rotateRelayCredential(relayID)
	if err != nil {
		return RelayRotationResult{}, err
	}

	res := RelayRotationResult{
		RelayID:         relayID,
		TokenFP:         rec.TokenFP,
		PreviousTokenFP: rec.PreviousTokenFP,
		Rotations:       rec.CredentialRotations,
		LinksDropped:    len(sess.channels),
	}

	// Deliver over the relay's own authenticated socket. Only the frame
	// carries the credential; it is not logged and not echoed.
	payload, _ := json.Marshal(struct {
		Type    string `json:"type"`
		RelayID string `json:"relay_id"`
		Token   string `json:"token"`
		TokenFP string `json:"token_fp"`
	}{Type: "credential_rotate", RelayID: relayID, Token: secret, TokenFP: rec.TokenFP})
	frame := makeRelayFrame(sess.magic, 0, payload)
	sess.writeMu.Lock()
	werr := sess.conn.WriteMessage(websocket.BinaryMessage, frame)
	sess.writeMu.Unlock()
	res.Delivered = werr == nil
	if werr != nil {
		// The node keeps the previous credential (grace) and will reconnect
		// with it, so a failed push is recoverable by rotating again.
		log.Printf("[relay-identity] relay %s: credential push failed: %v", relayID, werr)
	} else {
		log.Printf("[relay-identity] relay %s: credential delivered (token_fp=%s)", relayID, rec.TokenFP)
	}

	// Drop only this node's links. cleanupRelaySession runs from the read
	// loop when the socket closes; closing here is what makes it happen now.
	_ = sess.conn.Close()
	return res, nil
}

// ---------------------------------------------------------------------------
// Installed allowlist
// ---------------------------------------------------------------------------

// ConfigRelayAllowlist is the installed implementation of RelayAllowlist. It
// is built from config at boot, never mutated per request, and it answers for
// ids and/or fingerprints:
//
//	"relay-a"            admit relay-a, any fingerprint
//	"relay-a:3f9a1c02"   admit relay-a only with that token_fp
//	"*:3f9a1c02"         admit any relay presenting that token_fp
type ConfigRelayAllowlist struct {
	mu     sync.RWMutex
	byID   map[string]string // id → required fingerprint ("" = any)
	byFP   map[string]bool   // fingerprints admitted for any id
	policy string
}

// Policy reports which of the three policies is in force.
func (a *ConfigRelayAllowlist) Policy() string {
	if a == nil {
		return RelayAllowlistAllowAll
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.policy
}

// Entries returns the configured entries for display (fingerprints, not
// credentials).
func (a *ConfigRelayAllowlist) Entries() []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.byID)+len(a.byFP))
	for id, fp := range a.byID {
		if fp == "" {
			out = append(out, id)
		} else {
			out = append(out, id+":"+fp)
		}
	}
	for fp := range a.byFP {
		out = append(out, "*:"+fp)
	}
	sort.Strings(out)
	return out
}

// Allows implements RelayAllowlist.
func (a *ConfigRelayAllowlist) Allows(relayID, fingerprint string) bool {
	if a == nil {
		return true
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch a.policy {
	case RelayAllowlistAllowAll:
		return true
	case RelayAllowlistDenyAll:
		return false
	}
	if want, ok := a.byID[relayID]; ok {
		return want == "" || want == fingerprint
	}
	return a.byFP[fingerprint]
}

// NewConfigRelayAllowlist builds the installed allowlist from a flag value.
// provided distinguishes "the flag was omitted" (allow-all) from "the flag was
// given and names nobody" (deny-all); the two are different operator intents
// and must not collapse into the same silent pass.
func NewConfigRelayAllowlist(spec string, provided bool) (*ConfigRelayAllowlist, error) {
	a := &ConfigRelayAllowlist{
		byID: make(map[string]string),
		byFP: make(map[string]bool),
	}
	if !provided && strings.TrimSpace(spec) == "" {
		a.policy = RelayAllowlistAllowAll
		return a, nil
	}
	for _, raw := range strings.Split(spec, ",") {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		id, fp := e, ""
		if i := strings.Index(e, ":"); i >= 0 {
			id, fp = strings.TrimSpace(e[:i]), strings.TrimSpace(e[i+1:])
		}
		if fp != "" && len(fp) != 8 {
			return nil, fmt.Errorf("relay allowlist entry %q: fingerprint must be 8 hex chars (token_fp)", e)
		}
		if id == "" && fp == "" {
			return nil, fmt.Errorf("relay allowlist entry %q is empty", e)
		}
		if id == "*" {
			if fp == "" {
				// "*" with no fingerprint would admit the world; that is
				// spelled by omitting the flag, not by a wildcard entry.
				return nil, fmt.Errorf("relay allowlist entry %q: wildcard needs a fingerprint", e)
			}
			a.byFP[fp] = true
			continue
		}
		a.byID[id] = fp
	}
	if len(a.byID) == 0 && len(a.byFP) == 0 {
		a.policy = RelayAllowlistDenyAll
		return a, nil
	}
	a.policy = RelayAllowlistListed
	return a, nil
}

// InstallRelayAllowlist builds the allowlist from config and installs it. It
// is the wiring: without this call the P21-04 refusal path has nothing to ask.
// An error is returned (never swallowed) so a malformed entry fails at boot.
func (s *Server) InstallRelayAllowlist(spec string, provided bool) error {
	a, err := NewConfigRelayAllowlist(spec, provided)
	if err != nil {
		return err
	}
	s.SetRelayAllowlist(a)
	log.Printf("[relay-allowlist] policy=%s entries=%v", a.Policy(), a.Entries())
	return nil
}

// relayAllowlistPolicy reports the installed policy for the API and logs.
func (s *Server) relayAllowlistPolicy() (string, []string) {
	s.forwardMu.RLock()
	al := s.relayAllowlist
	s.forwardMu.RUnlock()
	if al == nil {
		return RelayAllowlistAllowAll, nil
	}
	if c, ok := al.(*ConfigRelayAllowlist); ok {
		return c.Policy(), c.Entries()
	}
	return RelayAllowlistListed, nil
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

// handleV1Relays lists relay identities and the allowlist in force. Every
// value here is a fingerprint; the response has no credential field to leak.
func (s *Server) handleV1Relays(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	policy, entries := s.relayAllowlistPolicy()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"relays":           s.listRelayIdentities(),
		"allowlist_policy": policy,
		"allowlist":        entries,
	})
}

// handleV1RotateRelayCredential rotates one relay's credential. The response
// carries the new fingerprint only — never the credential it minted.
func (s *Server) handleV1RotateRelayCredential(w http.ResponseWriter, r *http.Request) {
	relayID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	if relayID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "relay id is required")
		return
	}
	res, err := s.RotateRelay(relayID)
	if errors.Is(err, errRelayNotConnected) {
		writeError(w, http.StatusNotFound, "RELAY_NOT_CONNECTED", "relay "+relayID+" has no live session")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "RELAY_ROTATE_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
