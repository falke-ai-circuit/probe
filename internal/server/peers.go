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
	// P27-02 (spec P27-01 §1.2): two derived states, additive to §4.
	// disabled — not dialing by operator/window rule choice (enabled=false,
	// scheduled outside window, or the detail names the rule). unreachable —
	// a manual dial was attempted and failed, or a connected manual link
	// dropped, with NO auto-redial to follow (§5.4).
	peerStateDisabled    = "disabled"
	peerStateUnreachable = "unreachable"
)

// State-detail reasons the spec §3.3 matrix names verbatim.
const (
	detailOutsideScheduleWindow = "outside_schedule_window"
	detailManualUplinkNotDialed = "manual-uplink-not-dialed"
	detailManualPolicyChanged   = "policy changed to manual"
	detailManualNoAutoRedial    = "link dropped; manual uplink does not auto-redial"
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
	// P27-02 (spec P27-01 §1.1/§5.4): the rule-reason the row reports, e.g.
	// detailOutsideScheduleWindow / detailManualUplinkNotDialed.
	Reason string `json:"reason,omitempty"`
}

// ---------------------------------------------------------------------------
// Uplink dial disciplines (P27-02, spec P27-01 §3.1/§3.3)
// ---------------------------------------------------------------------------

const (
	uplinkTypeAlwaysOn  = "always-on"
	uplinkTypeScheduled = "scheduled"
	uplinkTypeManual    = "manual"
)

// peerWindowWatchTick is how often a running loop re-checks the scheduled
// window gate (§3.3 window-exit watcher).
const peerWindowWatchTick = 2 * time.Second

// validUplinkType enforces the §3.1 enum exactly.
func validUplinkType(v string) bool {
	switch v {
	case uplinkTypeAlwaysOn, uplinkTypeScheduled, uplinkTypeManual:
		return true
	}
	return false
}

// validateScheduleWindow enforces the §3.1 pair rules: both keys present and
// "HH:MM" (seconds-strict — "9:30" and "10:00:00" are refused). A
// midnight-crossing window (start > end) is legal and wraps through 00:00.
func validateScheduleWindow(start, end string) error {
	if _, _, ok := parseHHMM(start); !ok {
		return fmt.Errorf("schedule_start must be HH:MM (got %q)", start)
	}
	if _, _, ok := parseHHMM(end); !ok {
		return fmt.Errorf("schedule_end must be HH:MM (got %q)", end)
	}
	return nil
}

// parseHHMM parses a strict "HH:MM" clock string into minutes-since-midnight.
func parseHHMM(v string) (int, int, bool) {
	t, err := time.Parse("15:04", strings.TrimSpace(v))
	if err != nil {
		return 0, 0, false
	}
	return t.Hour(), t.Minute(), true
}

// inWindowNow reports whether the record's scheduled window (server-local
// time) contains right now. start == end means the full 24h window.
func (r defaultPeerRecord) inWindowNow() bool {
	sh, sm, ok1 := parseHHMM(r.ScheduleStart)
	eh, em, ok2 := parseHHMM(r.ScheduleEnd)
	if !ok1 || !ok2 {
		return false
	}
	s := sh*60 + sm
	e := eh*60 + em
	n := time.Now()
	cur := n.Hour()*60 + n.Minute()
	if s == e {
		return true
	}
	if s < e {
		return cur >= s && cur < e
	}
	// Midnight-crossing: window runs s..24:00 and 00:00..e.
	return cur >= s || cur < e
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
	// P27-02 (spec §3.3): the scheduled-window watcher's stop channel, set
	// while a scheduled link runs and closed when the run exits. Guarded by
	// peerManager.mu like every other field here.
	windowWatch chan struct{}
	// P27-02 (spec §3.3): closed when a manual inline dial run ends (any
	// outcome). Guards a second dial while a run is live, and is the join
	// point the manual→always-on keep-link takeover waits on. nil when no
	// inline run has ever been started. Guarded by peerManager.mu.
	runOver chan struct{}
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
// Dial-discipline helpers (P27-02, spec P27-01 §3.3). All read the runtime
// under peerManager.mu (single-writer contract FM-1.3) with default-on
// semantics: an absent/unknown uplink_type IS always-on (C1 backward compat).
// ---------------------------------------------------------------------------

// dialDiscipline resolves the effective discipline for a running peer.
func (pm *peerManager) dialDiscipline(id string) string {
	pm.mu.Lock()
	rt := pm.peers[id]
	pm.mu.Unlock()
	if rt == nil {
		return uplinkTypeAlwaysOn
	}
	switch rt.cfg.UplinkType {
	case uplinkTypeScheduled, uplinkTypeManual:
		return rt.cfg.UplinkType
	default:
		// "" and "always-on" (validation refuses anything else at the API
		// boundary — legacy docs carry the empty value and read always-on).
		return uplinkTypeAlwaysOn
	}
}

// inWindow reports the record's current scheduled-window state.
func (pm *peerManager) inWindow(id string) bool {
	pm.mu.Lock()
	rt := pm.peers[id]
	pm.mu.Unlock()
	if rt == nil {
		return false
	}
	return rt.cfg.inWindowNow()
}

// loopExpired reports whether this peer's lifecycle slot was taken over
// (stop/replace) — the guard that keeps a stale spawnLoop from dialing after
// its runtime was replaced or stopped.
func (pm *peerManager) loopExpired(id string) bool {
	pm.mu.Lock()
	rt := pm.peers[id]
	pm.mu.Unlock()
	return rt == nil || rt.stopped
}

// stopChan exposes the runtime's stop channel to the spawn loop's selects.
func (pm *peerManager) stopChan(id string) chan struct{} {
	pm.mu.Lock()
	rt := pm.peers[id]
	pm.mu.Unlock()
	if rt == nil {
		return nil
	}
	return rt.stop
}

// setWindowWatch records the scheduled-window watcher's stop channel; a nil
// value clears it. Closing happens on watch exit.
func (pm *peerManager) setWindowWatch(id string, ch chan struct{}) {
	pm.mu.Lock()
	if rt := pm.peers[id]; rt != nil {
		rt.windowWatch = ch
	}
	pm.mu.Unlock()
}

// stopWindowWatch tears down the scheduled-window watcher if one is armed
// and this (wch) is still the armed watch. Single-close owner: the spawn
// loop; the watcher itself never closes (it exits via this channel or a
// loopExpired check, leaving the channel to be GC'd there).
func (pm *peerManager) stopWindowWatch(id string, wch chan struct{}) {
	pm.mu.Lock()
	rt := pm.peers[id]
	if rt != nil && rt.windowWatch == wch {
		rt.windowWatch = nil
		close(wch)
	}
	pm.mu.Unlock()
}

// watchScheduledWindow is the §3.3 window-exit watcher: while a scheduled
// link runs, exiting the window stops the link NOW (mirror flips through the
// existing onLoss path). The run loop continues past RunUplinkOnly's return
// and re-parks as disabled/outside_schedule_window. Exits when wch is closed
// (run over / configured away) or the runtime is gone.
func (pm *peerManager) watchScheduledWindow(id string, rl *relay.Relay, wch chan struct{}) {
	tick := time.NewTicker(peerWindowWatchTick)
	defer tick.Stop()
	for {
		select {
		case <-wch:
			return
		case <-tick.C:
			if pm.loopExpired(id) {
				return
			}
			if !pm.inWindow(id) {
				log.Printf("[peers] %s: scheduled window closed — stopping link (outside_schedule_window)", id)
				rl.Stop()
				return
			}
		}
	}
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
		// P27-02 (spec §3.3): a peer whose config changed discipline (or was
		// disabled) while this loop ran is stopped before its next dial.
		if pm.loopExpired(id) {
			return
		}
		// P27-02 (spec §3.3): the discipline gate. always-on dials as before;
		// scheduled dials only inside its server-local window; manual NEVER
		// auto-dials (its dial is the action endpoint's single bounded dial).
		switch pm.dialDiscipline(id) {
		case uplinkTypeManual:
			// Spec §5.4: a manual uplink's truth after boot is "not dialed
			// (manual)" — disabled/manual-uplink-not-dialed, NOT the
			// pre-restart unreachable (stale unreachable reads as an
			// outage; "not dialed yet" reads as intention).
			pm.transition(id, peerStateDisabled, linkStateDetail{Reason: detailManualUplinkNotDialed})
			return // no auto-dial, no auto-redial — operator action only
		case uplinkTypeScheduled:
			// Recomputed before every dial: outside the window the loop parks
			// as disabled and re-checks on the window tick until re-entry.
			for !pm.inWindow(id) {
				if pm.loopExpired(id) {
					return
				}
				pm.transition(id, peerStateDisabled, linkStateDetail{Reason: detailOutsideScheduleWindow})
				select {
				case <-pm.stopChan(id):
					return
				case <-time.After(peerWindowWatchTick):
				}
			}
			// In-window: dial like always-on below.
		}
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

		if pm.loopExpired(id) {
			return
		}
		// P27-02 (spec §3.3): a scheduled link's window is re-checked while
		// the loop runs — window exit mid-link stops the link NOW.
		var wch chan struct{}
		if pm.dialDiscipline(id) == uplinkTypeScheduled {
			wch = make(chan struct{})
			go pm.watchScheduledWindow(id, rl, wch)
			pm.setWindowWatch(id, wch)
		}

		runErr := rl.RunUplinkOnly()

		if wch != nil {
			pm.stopWindowWatch(id, wch)
		}

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
		// Transport drop after having been up → retrying (§4) — EXCEPT a
		// manual link, which must never auto-redial (spec §3.3): it lands
		// unreachable with no retry loop behind it.
		if pm.dialDiscipline(id) == uplinkTypeManual {
			pm.transition(id, peerStateUnreachable, linkStateDetail{Reason: detailManualNoAutoRedial})
			pm.srv.auditPeer(id, "peer.state_changed", "", map[string]any{
				"from": peerStateConnected, "to": peerStateUnreachable, "reason": detailManualNoAutoRedial})
			return // NEVER redial a manual uplink
		}
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
		// P19-01 compat: peer records carry no downstream agent-token field,
		// so a nested-relay peer keeps its pre-P19-01 admission. The gap —
		// per-peer downstream tokens for nested relays — is flagged for
		// P19-02, which owns the protocol work this unit defers.
		AllowAnyDownstream: true,
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
		// P25-05R: adoption is the moment the forward policy learns its
		// uplink. A peer flagged `uplink` wires THIS server's forwardURL to
		// the adopted peer's server URL + token, so relay/relay_all stop
		// deferring on chain root the moment the peer is live. Idempotent
		// per value: re-adopting the same target is a logged no-op.
		// Config is re-read by id inside applyPeerUplink (hook contract:
		// capture id, never the runtime pointer).
		pm.applyPeerUplink(id)
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

// applyPeerUplink wires the server's forward target to an adopted peer's
// server URL + token (P25-05R). A no-op unless the peer's config carries
// uplink:true. The token is unsealed here, inside the adoption path, and
// handed to SetUplinkForwardTarget; sealed bytes never leave the call
// chain (§6). Same-target re-adoption is a no-op so a peer redial does
// not reset a live forwarder.
func (pm *peerManager) applyPeerUplink(id string) {
	pm.mu.Lock()
	rt := pm.peers[id]
	if rt == nil || rt.stopped || !rt.cfg.Uplink {
		pm.mu.Unlock()
		return
	}
	peerURL := rt.cfg.Upstream
	sealed := rt.cfg.TokenSealed
	pm.mu.Unlock()

	cur, curTok := pm.srv.uplinkForwardTarget()
	if cur == peerURL && curTok != "" {
		return
	}
	store, err := pm.srv.peerCredentials()
	if err != nil {
		log.Printf("[peers] %s: uplink wiring skipped, credential store unavailable: %v", id, err)
		return
	}
	raw, err := unsealPeerToken(store, sealed)
	if err != nil {
		log.Printf("[peers] %s: uplink wiring skipped, token unsealable: %v", id, err)
		return
	}
	pm.srv.SetUplinkForwardTarget(peerURL, raw)
	log.Printf("[peers] %s: uplink wired — forward target set to %s (peer adoption)", id, peerURL)
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
	// P27-02 (spec §3.3 PUT transitions):
	//   manual→always-on while CONNECTED ⇒ keep the healthy link, arm the
	//   takeover goroutine (no forced redial — re-dialing a live link would
	//   violate the no-op equality predicate);
	//   always-on→manual ⇒ no keep: the replace below tears the link down
	//   at apply (an unattended tunnel must not survive the loss of its
	//   always-on justification) and the fresh runtime parks as
	//   disabled/manual-uplink-not-dialed via the spawnLoop gate (the
	//   park-reason names the policy change).
	wasRuntime := rt != nil && !rt.stopped
	wasLive := wasRuntime && rt.relay != nil && rt.relay.UplinkConnected()
	wasManual := wasRuntime && rt.cfg.effectiveUplinkType() == uplinkTypeManual
	nowAlwaysOn := rec.effectiveUplinkType() == uplinkTypeAlwaysOn
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

	if wasManual && wasLive && nowAlwaysOn {
		// Keep-link transition: preserve the live link by NOT replacing its
		// runtime. Store the new config on the existing runtime, stamp the
		// policy reason, and arm the retry-loop takeover for when the inline
		// run eventually ends.
		pm.mu.Lock()
		rt.cfg = rec
		rt.stopped = false
		rt.detail = linkStateDetail{Reason: detailManualNoAutoRedial}
		pm.peers[rec.ID] = rt
		id := rec.ID
		pm.mu.Unlock()
		pm.armAlwaysOnTakeover(id)
		return
	}

	pm.registerRuntime(rec)
	// P27-02 (spec §3.3): an always-on→manual apply must not silently keep
	// or restart a link — the new runtime parks; report the policy change.
	if rec.effectiveUplinkType() == uplinkTypeManual && wasRuntime && !wasManual {
		pm.transition(rec.ID, peerStateDisabled, linkStateDetail{Reason: detailManualPolicyChanged})
		go pm.spawnLoop(rec.ID) // the gate re-parks + returns (no dial)
		return
	}
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
			ID:            rt.cfg.ID,
			Upstream:      rt.cfg.Upstream,
			TokenFP:       rt.cfg.TokenFP,
			TokenPresent:  rt.cfg.TokenSealed != "",
			Enabled:       rt.cfg.Enabled,
			Uplink:        rt.cfg.Uplink,
			UplinkType:    rt.cfg.effectiveUplinkType(),
			ScheduleStart: rt.cfg.ScheduleStart,
			ScheduleEnd:   rt.cfg.ScheduleEnd,
			Listen:        rt.cfg.Listen,
			MaxAgents:     rt.cfg.MaxAgents,
			CreatedAt:     rt.cfg.CreatedAt,
			UpdatedAt:     rt.cfg.UpdatedAt,
			LinkState:     rt.state,
			StateDetail:   rt.detail,
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
	ID           string `json:"id"`
	Upstream     string `json:"upstream"`
	TokenFP      string `json:"token_fp"`
	TokenPresent bool   `json:"token_present"`
	Enabled      bool   `json:"enabled"`
	// P25-05R: true when this peer is wired as the server's forward uplink
	// on adoption (R3: every config key is emitted).
	Uplink bool `json:"uplink"`
	// P27-02 (spec §3.2): the same three keys the stored record carries —
	// one contract for the POST read-back and the list view. Empty
	// UplinkType still renders always-on via effectiveUplinkType() at
	// write side; here the stored resolution is echoed.
	UplinkType    string          `json:"uplink_type"`
	ScheduleStart string          `json:"schedule_start"`
	ScheduleEnd   string          `json:"schedule_end"`
	Listen        string          `json:"listen"`
	MaxAgents     int             `json:"max_agents"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
	LinkState     string          `json:"link_state"`
	StateDetail   linkStateDetail `json:"state_detail"`
}

// getenvDefault is the local env fallback (no global import churn in tests).
func getenvDefault(key, def string) string {
	if v := strings.TrimSpace(osGetenv(key)); v != "" {
		return v
	}
	return def
}

// effectiveUplinkType resolves the §3.1 discipline with C1 defaulting: an
// absent/empty stored value reads always-on (every pre-P27 record behaved
// always-on; the doc never grows a fake value — the view layer resolves it).
func (r defaultPeerRecord) effectiveUplinkType() string {
	if validUplinkType(r.UplinkType) {
		return r.UplinkType
	}
	return uplinkTypeAlwaysOn
}

// manualDial is the spec §3.3 manual action: ONE bounded dial — one
// connect+registration wait, no retry loop, no watcher. Connected ⇒ nil
// (the link keeps running on the relay's dispatch goroutine); refused or
// failed ⇒ unreachable (refusal carried in the state detail, cleared by the
// next operator dial). A manual peer has NO lifecycle goroutine in the
// parked state: the runtime is registered without a spawnLoop and this call
// runs the relay inline. The HTTP handler never blocks on a live link: it
// returns at registration; the link's eventual drop is handled by the run
// goroutine (unreachable, no auto-redial).
func (pm *peerManager) manualDial(id string) error {
	pm.mu.Lock()
	rt := pm.peers[id]
	if rt != nil && rt.runOver != nil {
		select {
		case <-rt.runOver: // previous run already over — safe to dial again
		default:
			pm.mu.Unlock()
			return fmt.Errorf("PEER_ALREADY_CONNECTED: peer %s is connected; repeated dial refused", id)
		}
	}
	pm.mu.Unlock()
	if rt == nil || rt.stopped {
		return fmt.Errorf("PEER_NOT_RUNNING: peer %s is not enabled", id)
	}
	if rt.relay != nil && rt.relay.UplinkConnected() {
		return fmt.Errorf("PEER_ALREADY_CONNECTED: peer %s is connected", id)
	}

	rl, err := pm.buildRelay(id)
	if err != nil {
		pm.transition(id, peerStateUnreachable, linkStateDetail{Reason: "dial failed: " + err.Error()})
		return fmt.Errorf("dial failed: %v", err)
	}
	pm.attachRelay(id, rl)

	done := make(chan error, 1)
	ro := make(chan struct{})
	pm.mu.Lock()
	if rt2 := pm.peers[id]; rt2 != nil {
		rt2.runOver = ro // the keep-link takeover (applyConfig) waits on this
	}
	pm.mu.Unlock()

	go func() {
		defer close(ro)
		err := rl.RunUplinkOnly()
		if err != nil {
			// Registration refusal or transport failure: the dial failed.
			if rf := rl.LastRefusal(); rf != nil {
				pm.transition(id, peerStateUnreachable, linkStateDetail{
					RefusalCode: rf.Code, RefusalReason: rf.Reason})
			} else {
				pm.transition(id, peerStateUnreachable, linkStateDetail{Reason: "dial failed: " + err.Error()})
			}
			done <- err
			return
		}
		// Run exited without error ⇒ the link dropped after having been up
		// (or Stop fired). A manual uplink does not auto-redial (§3.3).
		if pm.dialDiscipline(id) == uplinkTypeManual && !pm.loopExpired(id) {
			pm.transition(id, peerStateUnreachable, linkStateDetail{Reason: detailManualNoAutoRedial})
		}
		done <- nil
	}()

	// Bounded wait for the registration (≈ the forwardRegTimeout discipline,
	// with headroom for the dial itself).
	deadline := time.After(10 * time.Second)
	for {
		if rl.UplinkConnected() {
			return nil // connected transition stamped by the up-callback
		}
		select {
		case err := <-done: // refused / failed fast
			if rf := rl.LastRefusal(); rf != nil {
				return fmt.Errorf("%s: %s", rf.Code, rf.Reason)
			}
			return fmt.Errorf("dial failed: %v", err)
		case <-deadline:
			rl.Stop() // bounded: stop the run, reap it
			<-done
			return fmt.Errorf("dial failed: timeout waiting for registration")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// armAlwaysOnTakeover is the §3.3 keep-link transition's back half: a
// manual→always-on apply keeps the healthy link and arms the goroutine that
// enters the standard retry loop only when the current inline run ends (link
// drops). Until then re-dialing it would violate the no-op equality
// predicate — the link is healthy, not respawned.
func (pm *peerManager) armAlwaysOnTakeover(id string) {
	pm.mu.Lock()
	ro := chan (struct{})(nil)
	if rt := pm.peers[id]; rt != nil {
		ro = rt.runOver
	}
	pm.mu.Unlock()
	if ro == nil {
		// No inline run recorded (e.g. disabled→enable re-apply): spawn now.
		if !pm.loopExpired(id) {
			go pm.spawnLoop(id)
		}
		return
	}
	go func() {
		<-ro
		if !pm.loopExpired(id) {
			go pm.spawnLoop(id)
		}
	}()
}

// equal is the FM-1.3 config-equality predicate (spec: "Config equality").
// P27-02: the dial-discipline keys join the predicate — a type or window
// change is NOT an identical-config re-apply, so it live-applies (spec §3.3
// PUT transitions; including always-on→manual tearing the link down at
// apply, which applyConfig does via stop+respawn below).
func (r defaultPeerRecord) equal(other defaultPeerRecord) bool {
	return r.ID == other.ID && r.Upstream == other.Upstream &&
		r.TokenFP == other.TokenFP && r.Enabled == other.Enabled &&
		r.Listen == other.Listen && r.MaxAgents == other.MaxAgents &&
		r.Uplink == other.Uplink &&
		r.UplinkType == other.UplinkType &&
		r.ScheduleStart == other.ScheduleStart &&
		r.ScheduleEnd == other.ScheduleEnd
}
