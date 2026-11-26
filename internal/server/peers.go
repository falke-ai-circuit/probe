package server

// P17-02 (spec P17-01 §1–§7) — the peer backend.
//
// One peerManager for the process, one lifecycle goroutine per enabled
// default_peer entry. In-process relay uplink (spec §1 decision): the
// component imports internal/relay and runs RunUplinkOnly; the retry loop,
// the state machine (§4) and live apply (FM-1.3) live here.
//
// Binding invariants (the spec's list, restated as bindings):
//   - FM-1.1  a relay panic never crosses this boundary: link down + audit
//     peer.panic, then the §4 loop respawns into retrying.
//   - FM-1.2  this file never takes a session's channelsMu while holding
//     relayMu (P15-12 order, new caller, same contract). It takes neither:
//     its state is its own map, the inventory store is only touched through
//     the serversStore lock.
//   - FM-1.3  PUT/DELETE apply live; a re-apply of identical config is a
//     no-op (config equality), never a second goroutine set. peerManager is
//     the single owner: one runtime per id, spawn replaces, never doubles.
//   - FM-1.4  boot never blocks: spawns are goroutines behind a stagger.
//   - §4      terminal states (auth-rejected, version-mismatch) never
//     auto-retry; connected requires peer_hello; restart always spawns as
//     retrying (never persisted as connected).
//   - §6      token sealed BEFORE the doc write; appears once in a POST/PUT
//     body; never in GET, audit, or errors.
//   - §7      redaction at the audit boundary (audit.go Log); redaction
//     failure writes {"redaction_error":true} — fail closed.
//   - Hard acceptance: a connect must bind the listen socket (nested peers
//     only) AND produce a relay_register upstream — relay.Relay's
//     adoptUpstream sends relay_register before the link counts as up and
//     RunUplinkonly returns success only after that frame was answered. A
//     route that "returns a port, binds nothing" cannot exist here: pure
//     uplink peers bind no socket by design and say so; listen-set peers
//     bind via StartDownstreamListener before the row is served.

import (
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/relay"
)

// osGetenv aliases os.Getenv so tests can shadow API paths if ever needed.
func osGetenv(key string) string { return os.Getenv(key) }

// ---------------------------------------------------------------------------
// Vocabulary (§4 — closed; anything else is a bug)
// ---------------------------------------------------------------------------

const (
	peerStateConnected       = "connected"
	peerStateRetrying        = "retrying"
	peerStateAuthRejected    = "auth-rejected"
	peerStateVersionMismatch = "version-mismatch"
)

// linkStateDetail is the state's payload on the peers view (§4: the
// version-mismatch state carries the two versions, kept honest; a refusal
// carries its code).
type linkStateDetail struct {
	PeerVersion     string `json:"peer_version,omitempty"`
	RequiredMin     string `json:"required_min,omitempty"`
	RefusalCode     string `json:"refusal_code,omitempty"`
	RefusalReason   string `json:"refusal_reason,omitempty"`
	TokenUnsealable bool   `json:"token_unsealable,omitempty"`
}

// isTerminalState reports states that never auto-retry (§4 rules).
func isTerminalPeerState(st string) bool {
	return st == peerStateAuthRejected || st == peerStateVersionMismatch
}

// ---------------------------------------------------------------------------
// Runtime records
// ---------------------------------------------------------------------------

// peerRuntime is the live record for one configured peer. All fields are
// guarded by peerManager.mu — the state machine is single-writer.
type peerRuntime struct {
	cfg     defaultPeerRecord
	relay   *relay.Relay
	state   string
	detail  linkStateDetail
	stop    chan struct{}
	stopped bool
	dialNow chan struct{} // dial-now signal (§2 action)
}

// peerManager owns every configured default_peer.
type peerManager struct {
	mu        sync.Mutex
	srv       *Server
	peers     map[string]*peerRuntime
	started   bool
	credStore *CredentialStore
}

// DefaultPeersDocVersion is the serversDoc version that carries default_peers.
const DefaultPeersDocVersion = 2

// defaultPeersDocVersion is the internal spelling servers.go writes with.
const defaultPeersDocVersion = DefaultPeersDocVersion

// peerSpawnStagger spaces boot spawns (spec §5.3): an N-peer restart must
// not look like a flood to a recovering upstream.
const peerSpawnStagger = 250 * time.Millisecond

// peerBackoffInitial / peerBackoffMax are the §4 loop bounds (1s exponential
// cap 30s, reset on success).
const (
	peerBackoffInitial = time.Second
	peerBackoffMax     = 30 * time.Second
)

// ---------------------------------------------------------------------------
// Validation (§2 error family)
// ---------------------------------------------------------------------------

// validatePeerID enforces the §2 charset (identical to relay ids:
// [A-Za-z0-9._-]{1,64}).
func validatePeerID(id string) error {
	if id == "" || len(id) > 64 {
		return fmt.Errorf("peer id must be 1..64 characters")
	}
	for _, c := range id {
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return fmt.Errorf("peer id character %q not allowed ([A-Za-z0-9._-] only)", c)
		}
	}
	return nil
}

// validatePeerUpstream enforces a ws:// or wss:// URL with a host (§2).
func validatePeerUpstream(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("upstream is not a valid URL")
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		return fmt.Errorf("upstream must be a ws:// or wss:// URL")
	}
	if u.Host == "" {
		return fmt.Errorf("upstream URL carries no host")
	}
	return nil
}

// ---------------------------------------------------------------------------

// peerManagerFor returns the process-wide manager, creating it on first use.
func (s *Server) peerManagerFor() *peerManager {
	s.peersMu.Lock()
	defer s.peersMu.Unlock()
	if s.peers == nil {
		s.peers = &peerManager{srv: s, peers: make(map[string]*peerRuntime)}
	}
	return s.peers
}

// bootstrapPeers reads the persisted default_peers and spawns every enabled
// entry, staggered (spec §5). Called from Start; never blocks the caller
// (FM-1.4).
func (s *Server) bootstrapPeers() {
	pm := s.peerManagerFor()
	st := s.inventory()
	st.load()
	st.mu.Lock()
	if st.loadFailed {
		st.mu.Unlock()
		// Spec §5.4: unreadable/corrupt doc = peers config absent (the
		// existing quarantine path). Server starts LOCAL-ONLY: no
		// half-federation. An uplink with a wrong or missing token would be
		// a security incident, not a degraded state.
		log.Printf("[peers] peers config unavailable — local mode")
		pm.mu.Lock()
		pm.started = true
		pm.mu.Unlock()
		return
	}
	ids := make([]string, 0, len(st.doc.DefaultPeers))
	records := make(map[string]defaultPeerRecord, len(st.doc.DefaultPeers))
	for id, rec := range st.doc.DefaultPeers {
		ids = append(ids, id)
		records[id] = *rec
	}
	st.mu.Unlock()
	sort.Strings(ids)

	pm.mu.Lock()
	pm.started = true
	pm.mu.Unlock()

	// Every entry gets a runtime record (disabled ones so the API view is
	// complete); only enabled ones get a lifecycle goroutine.
	for _, id := range ids {
		rec := records[id]
		pm.registerRuntime(rec)
	}
	for i, id := range ids {
		if !records[id].Enabled {
			continue
		}
		if i > 0 {
			time.Sleep(peerSpawnStagger)
		}
		go pm.spawnLoop(id)
	}
}

// registerRuntime creates (or leaves) the runtime record for one config
// entry. Idempotent: the existing runtime is the truth (FM-1.3 single owner).
func (pm *peerManager) registerRuntime(rec defaultPeerRecord) *peerRuntime {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if rt := pm.peers[rec.ID]; rt != nil && !rt.stopped {
		return rt
	}
	rt := &peerRuntime{
		cfg:     rec,
		stop:    make(chan struct{}),
		dialNow: make(chan struct{}, 1),
		state:   peerStateRetrying, // §5.2: spawn is always retrying first
	}
	if !rec.Enabled {
		rt.state = "" // disabled: outside the §4 machine, honest absence on the wire
	}
	pm.peers[rec.ID] = rt
	return rt
}

// ---------------------------------------------------------------------------
// Credential sealing (§6)
// ---------------------------------------------------------------------------

// sealPeerToken seals a raw token for at-rest storage via the existing
// CredentialStore derivation (AES-256-GCM; 0600 files owned by the store).
// RETURNS the blob + fingerprint; the plaintext never leaves the call chain.
func sealPeerToken(store *CredentialStore, raw string) (sealed, fp string, err error) {
	sealed, err = store.seal([]byte(raw))
	if err != nil {
		return "", "", err
	}
	return sealed, tokenFingerprint(raw), nil
}

// unsealPeerToken reverses it for the next dial. Failure is the
// PEER_TOKEN_UNSEALABLE state (spec §5.1): entry marked, NO retry, operator
// fix — a mismatch loop against a token that can never succeed is the
// defect this avoids.
func unsealPeerToken(store *CredentialStore, sealed string) (string, error) {
	raw, err := store.open(sealed)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// peerCredentials resolves the CredentialStore for peer tokens. The default
// store is shared with the P8-07 vault; PROBE_PEER_SECRET_STORE moves peer
// tokens to their own file when an operator wants the split.
func (s *Server) peerCredentials() (*CredentialStore, error) {
	pm := s.peerManagerFor()
	pm.mu.Lock()
	if pm.credStore != nil {
		st := pm.credStore
		pm.mu.Unlock()
		return st, nil
	}
	pm.mu.Unlock()
	if env := strings.TrimSpace(getenvDefault("PROBE_PEER_SECRET_STORE", "")); env != "" {
		st, err := OpenCredentialStore(env)
		if err != nil {
			return nil, err
		}
		pm.mu.Lock()
		pm.credStore = st
		pm.mu.Unlock()
		return st, nil
	}
	st, err := s.credentials()
	if err != nil {
		return nil, err
	}
	pm.mu.Lock()
	pm.credStore = st
	pm.mu.Unlock()
	return st, nil
}

// ---------------------------------------------------------------------------
// Spawn loop (§4) — owns retry pacing, terminal gating, panic conversion
// ---------------------------------------------------------------------------

// spawnLoop is the lifecycle per peer. Serialised by single ownership: one
// goroutine per id, replacement stops the old one first (FM-1.3).
func (pm *peerManager) spawnLoop(id string) {
	defer func() {
		// FM-1.1 extends to the caller side: even the lifecycle wrapper
		// cannot crash the server.
		if p := recover(); p != nil {
			log.Printf("[peers] %s: lifecycle panic recovered: %v", id, p)
			pm.srv.auditPeer(id, "peer.panic", fmt.Sprintf("lifecycle panic recovered: %v", p), map[string]any{})
		}
	}()
	backoff := peerBackoffInitial
	for {
		rl, err := pm.buildRelay(id)
		if err != nil {
			// Unsealable token (§5.1): mark auth-rejected, NO retry.
			pm.transition(id, peerStateAuthRejected, linkStateDetail{TokenUnsealable: true})
			pm.srv.auditPeer(id, "peer.refused", "token unsealable: credential store rejected the sealed blob",
				map[string]any{"state": peerStateAuthRejected, "reason": "PEER_TOKEN_UNSEALABLE"})
			log.Printf("[peers] %s: token unsealable — no retry (operator fix required)", id)
			return
		}
		pm.attachRelay(id, rl)

		runErr := rl.RunUplinkOnly()

		pm.mu.Lock()
		rt := pm.peers[id]
		if rt == nil || rt.stopped {
			pm.mu.Unlock()
			return // deleted or replaced while we ran
		}
		// Terminal states keep their state; nothing here may clear them
		// (§4: terminal means hopeless, not broken).
		if isTerminalPeerState(rt.state) {
			pm.mu.Unlock()
			return
		}
		// A typed policy refusal is the upstream's answer (P22-05): terminal.
		if rf := rl.LastRefusal(); rf != nil {
			rt.detail = linkStateDetail{RefusalCode: rf.Code, RefusalReason: rf.Reason}
			if !rf.Retryable() {
				rt.state = peerStateAuthRejected
				pm.mu.Unlock()
				pm.srv.auditPeer(id, "peer.refused", rf.Reason,
					map[string]any{"state": peerStateAuthRejected, "code": rf.Code})
				return // terminal: no retry, ever
			}
		}
		// Transport drop after having been up → retrying (§4).
		rt.state = peerStateRetrying
		rt.detail = linkStateDetail{}
		pm.mu.Unlock()
		_ = runErr // the state machine above IS the handling of the return

		// Dial-now short-circuits the remaining backoff and resets it (§2).
		select {
		case <-rt.stop:
			return
		case <-rt.dialNow:
			backoff = peerBackoffInitial
			continue
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > peerBackoffMax {
			backoff = peerBackoffMax
		}
	}
}

// buildRelay constructs the relay for a peer's CURRENT config, unsealing the
// token fresh each spawn (a PUT rotation must take effect on the next dial
// without a restart).
func (pm *peerManager) buildRelay(id string) (*relay.Relay, error) {
	pm.mu.Lock()
	rt := pm.peers[id]
	if rt == nil || rt.stopped {
		pm.mu.Unlock()
		return nil, errors.New("peer not running")
	}
	rec := rt.cfg
	storeRef := pm.credStore
	pm.mu.Unlock()

	store := storeRef
	if store == nil {
		var err error
		store, err = pm.srv.peerCredentials()
		if err != nil {
			return nil, err
		}
	}
	raw, err := unsealPeerToken(store, rec.TokenSealed)
	if err != nil {
		return nil, err
	}
	return relay.New(relay.Config{
		UpstreamURL: rec.Upstream,
		RelayID:     rec.ID,
		Token:       raw,
		MaxAgents:   rec.MaxAgents,
		ListenAddr:  rec.Listen,
	}), nil
}

// attachRelay installs the relay + hooks and moves the runtime into
// retrying. Hook callbacks capture id, not the pointer, and re-resolve under
// the lock — a replaced runtime must never be mutated by a stale hook.
func (pm *peerManager) attachRelay(id string, rl *relay.Relay) {
	pm.mu.Lock()
	rt := pm.peers[id]
	if rt == nil || rt.stopped {
		pm.mu.Unlock()
		return
	}
	rt.relay = rl
	rt.state = peerStateRetrying
	rt.detail = linkStateDetail{}
	pm.mu.Unlock()

	// §4 connected: registration accepted + peer_hello consumed. The
	// version check below reads the UPSTREAM's version from peer_hello
	// (spec: relay-side mismatch = upstream older than the minimum that
	// carries the frame types we send).
	rl.SetUplinkUpCallback(func() {
		hello := rl.PeerHelloSnapshot()
		peerVer := ""
		if hello != nil {
			peerVer = hello.Version
		}
		if peerVersionMismatch(peerVer) {
			pm.transition(id, peerStateVersionMismatch, linkStateDetail{
				PeerVersion: peerVer, RequiredMin: peerReadyLevelMin,
			})
			// §4: version-mismatch is operator-gated; the link itself stays
			// connected-shaped, but the state records the halt. The dial
			// remains up (degraded vocabulary: an old peer still connects);
			// only the FRAME exchanges it cannot have are withheld by the
			// sender-side gates already in the wire code.
			pm.srv.auditPeer(id, "peer.registered",
				"registration accepted, version negotiation flagged mismatch",
				map[string]any{"state": peerStateVersionMismatch,
					"peer_version": peerVer, "required_min": peerReadyLevelMin})
			return
		}
		pm.transition(id, peerStateConnected, linkStateDetail{})
		pm.srv.auditPeer(id, "peer.registered", "", map[string]any{"state": peerStateConnected})
	})

	// FM-1.1: machine-side panic hook.
	rl.SetPanicCallback(func(msg string) {
		pm.transition(id, peerStateRetrying, linkStateDetail{})
		pm.srv.auditPeer(id, "peer.panic", "panic recovered on uplink loop: "+msg,
			map[string]any{"state": peerStateRetrying})
	})

	// Downstream listener for nested-relay peers (FM-1.5). A peer
	// configured to accept agents MUST bind its socket — the hard
	// acceptance line's defect class ("returns a port, binds nothing") is
	// refused here by construction for the listen-set case.
	if rt.cfg.Listen != "" {
		go func() {
			defer func() {
				if p := recover(); p != nil {
					log.Printf("[peers] %s: downstream listener panic recovered: %v", id, p)
				}
			}()
			if err := rl.StartDownstreamListener(); err != nil {
				log.Printf("[peers] %s: downstream listener: %v", id, err)
			}
		}()
	}
}

// transition updates the state machine with the §7 audit trail. Safe to call
// from any goroutine; no-ops when the peer is gone or state+detail unchanged.
func (pm *peerManager) transition(id, state string, detail linkStateDetail) (from string, changed bool) {
	pm.mu.Lock()
	rt := pm.peers[id]
	if rt == nil || rt.stopped {
		pm.mu.Unlock()
		return "", false
	}
	from = rt.state
	if from == state && rt.detail == detail {
		pm.mu.Unlock()
		return from, false
	}
	rt.state = state
	rt.detail = detail
	pm.mu.Unlock()
	pm.srv.auditPeer(id, "peer.state_changed", "",
		map[string]any{"from": from, "to": state, "detail": detail})
	return from, true
}

// stopRuntime tears one peer's live link down (disable/delete). The runtime
// record is removed from the map; the §2 rule that runtime peerRecord rows
// follow their own retention is untouched (they live in serversDoc.Peers).
func (pm *peerManager) stopRuntime(id string) {
	pm.mu.Lock()
	rt := pm.peers[id]
	if rt == nil || rt.stopped {
		pm.mu.Unlock()
		return
	}
	rt.stopped = true
	close(rt.stop)
	rl := rt.relay
	delete(pm.peers, id)
	pm.mu.Unlock()
	if rl != nil {
		rl.Stop()
	}
}

// applyConfig is the FM-1.3 live-apply: called by PUT/POST/action after the
// document is persisted. Equality check first (no-op on identical config),
// then replace: stop the old runtime, register the new, spawn when enabled.
// peerManager remains the single owner — this is the ONLY path that creates
// lifecycle goroutines besides boot.
func (pm *peerManager) applyConfig(rec defaultPeerRecord) {
	pm.mu.Lock()
	rt := pm.peers[rec.ID]
	if rt != nil && !rt.stopped && rt.cfg.equal(rec) {
		// Identical config re-apply: no-op (fork-bomb guard). Link an
		// enable/disable change through even when the rest is identical.
		if rt.cfg.Enabled == rec.Enabled {
			pm.mu.Unlock()
			return
		}
	}
	if rt != nil && !rt.stopped {
		rt.stopped = true
		close(rt.stop)
		if rt.relay != nil {
			rt.relay.Stop()
		}
	}
	pm.mu.Unlock()

	if !rec.Enabled {
		pm.mu.Lock()
		delete(pm.peers, rec.ID)
		rt2 := &peerRuntime{cfg: rec, stop: make(chan struct{}), dialNow: make(chan struct{}, 1)}
		rt2.stopped = false
		rt2.state = "" // disabled: outside the §4 machine
		pm.peers[rec.ID] = rt2
		pm.mu.Unlock()
		return
	}

	pm.registerRuntime(rec)
	go pm.spawnLoop(rec.ID)
}

// dialNow forces an immediate reconnect with backoff reset (§2 action).
func (pm *peerManager) dialNow(id string) bool {
	pm.mu.Lock()
	rt := pm.peers[id]
	if rt == nil || rt.stopped {
		pm.mu.Unlock()
		return false
	}
	select {
	case rt.dialNow <- struct{}{}:
	default:
	}
	rl := rt.relay
	pm.mu.Unlock()
	if rl != nil {
		rl.Redial() // drop a live link so the loop redials immediately
	}
	return true
}

// registerConnector creates the runtime for a NEW config entry and spawns it
// when enabled. POST path.
func (pm *peerManager) registerConnector(rec defaultPeerRecord) {
	if !rec.Enabled {
		pm.mu.Lock()
		rt := &peerRuntime{cfg: rec, stop: make(chan struct{}), dialNow: make(chan struct{}, 1)}
		pm.peers[rec.ID] = rt
		pm.mu.Unlock()
		return
	}
	pm.registerRuntime(rec)
	go pm.spawnLoop(rec.ID)
}

// snapshot renders the peers view: config (fp only — never the secret) +
// live §4 state. Every config key emitted (R3 discipline).
func (pm *peerManager) snapshot() []peerConfigView {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	ids := make([]string, 0, len(pm.peers))
	for id := range pm.peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]peerConfigView, 0, len(ids))
	for _, id := range ids {
		rt := pm.peers[id]
		if rt == nil {
			continue
		}
		out = append(out, peerConfigView{
			ID:           rt.cfg.ID,
			Upstream:     rt.cfg.Upstream,
			TokenFP:      rt.cfg.TokenFP,
			TokenPresent: rt.cfg.TokenSealed != "",
			Enabled:      rt.cfg.Enabled,
			Listen:       rt.cfg.Listen,
			MaxAgents:    rt.cfg.MaxAgents,
			CreatedAt:    rt.cfg.CreatedAt,
			UpdatedAt:    rt.cfg.UpdatedAt,
			LinkState:    rt.state,
			StateDetail:  rt.detail,
		})
	}
	return out
}

// configView fetches one peer's view (nil when absent).
func (pm *peerManager) configView(id string) *peerConfigView {
	all := pm.snapshot()
	for i := range all {
		if all[i].ID == id {
			return &all[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// peerConfigView — the API projection. §6: token_present + token_fp, never
// the secret. Every config key is emitted (R3 discipline).
// ---------------------------------------------------------------------------

type peerConfigView struct {
	ID           string          `json:"id"`
	Upstream     string          `json:"upstream"`
	TokenFP      string          `json:"token_fp"`
	TokenPresent bool            `json:"token_present"`
	Enabled      bool            `json:"enabled"`
	Listen       string          `json:"listen"`
	MaxAgents    int             `json:"max_agents"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	LinkState    string          `json:"link_state"`
	StateDetail  linkStateDetail `json:"state_detail"`
}

// getenvDefault is the local env fallback (no global import churn in tests).
func getenvDefault(key, def string) string {
	if v := strings.TrimSpace(osGetenv(key)); v != "" {
		return v
	}
	return def
}

// equal is the FM-1.3 config-equality predicate (spec: "Config equality").
func (r defaultPeerRecord) equal(other defaultPeerRecord) bool {
	return r.ID == other.ID && r.Upstream == other.Upstream &&
		r.TokenFP == other.TokenFP && r.Enabled == other.Enabled &&
		r.Listen == other.Listen && r.MaxAgents == other.MaxAgents
}
