package server

// P22-02 — server inventory: GET /api/v1/servers (federation spec P1).
//
// One endpoint answers "which servers exist, and is each one reachable".
// servers[0] is always THIS process (role "local"); peers follow, ordered
// up-before-down, then name, then id, so two responses can be diffed.
//
// A peer whose socket closed is a RECORD, not an absence: it keeps last_seen,
// down_since and a closed-vocabulary state_reason until the retention window
// expires. relay clean-up deletes the SESSION; the record is written first.
//
// The local entry is the single identity builder in the server: the pre-auth
// identity block on /api/v1/health is a four-key projection of the same entry,
// so the login badge and the authenticated badge cannot disagree.
//
// Caller contract: read-only. The live peer map (s.relays) supplies "up", the
// persisted document supplies "down" and the local id. Nothing here keeps a
// second cache of liveness. Writes go through writeFileAtomic (tmp + fsync +
// rename), never through the registry's bare WriteFile path.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Closed vocabulary for state_reason (§8.4). Anything else is a bug.
const (
	stateReasonServing          = "serving"
	stateReasonSocketOpen       = "socket_open"
	stateReasonPong             = "pong"
	stateReasonSocketClosed     = "socket_closed"
	stateReasonPongTimeout      = "pong_timeout"
	stateReasonDuplicateID      = "duplicate_id_replaced"
	stateReasonRecordUnreadable = "record_unreadable"
	stateReasonRetentionExpired = "retention_expired"

	roleLocal = "local"
	rolePeer  = "peer"

	transportKindListen = "listen"
	transportKindUplink = "uplink"

	wsStateListening = "listening"
	wsStateOpen      = "open"
	wsStateClosed    = "closed"
	wsStateUnknown   = "unknown"

	transportSourceLocalConfig  = "local_config"
	transportSourcePeerMetadata = "peer_metadata"
	transportSourcePeerReport   = "peer_report"
	transportSourceUnreported   = "unreported"

	// Defaults from §6.3 / §10. Env knobs override them per deployment.
	defaultServersRetention  = 24 * time.Hour
	defaultServersMaxPeers   = 64
	defaultServersWriteFloor = time.Second
)

// serverTransport is one observed link for a server: where it accepts agents
// (kind "listen") or how it reached us (kind "uplink"). The two are never
// merged — merging them is how a 443 relay gets labelled 7701.
type serverTransport struct {
	Kind string `json:"kind"`
	// URL is null when the peer never reported one. It is never synthesised
	// from listen_addr for an uplink.
	URL *string `json:"url"`
	// Port is the port the peer listens on or dialled — never the ephemeral
	// source port of an inbound socket (`conn.RemoteAddr()`), which changes
	// on every reconnect and contradicts the spec's acceptance values.
	Port *int `json:"port"`
	// WSState is one of listening|open|closed|connecting|unknown.
	WSState string `json:"ws_state"`
	// ConnectedAt is null when unknown; it is never back-filled from file
	// mtime or from metadata.
	ConnectedAt *string `json:"connected_at"`
	Source      string  `json:"source"`
}

// serverCounts is a count of agent rows already classified for another
// purpose. It is not a health verdict and is not a second liveness predicate:
// the conn map is read once, here, and P0 replaces that one read.
type serverCounts struct {
	DirectAgents  int `json:"direct_agents"`
	RelayedAgents int `json:"relayed_agents"`
}

// serverEntry is one row of data.servers. Every key is always emitted (R3):
// an unknown value is null, never a missing key.
type serverEntry struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Role             string            `json:"role"`
	ListenAddr       string            `json:"listen_addr"`
	ListenAddrRaw    string            `json:"listen_addr_raw"`
	ListenAddrSource string            `json:"listen_addr_source"`
	Version          *string           `json:"version"`
	UptimeSeconds    *int64            `json:"uptime_seconds"`
	State            string            `json:"state"`
	LastSeen         string            `json:"last_seen"`
	StateReason      string            `json:"state_reason"`
	DownSince        *string           `json:"down_since"`
	Transports       []serverTransport `json:"transports"`
	Counts           serverCounts      `json:"counts"`
	ForwardPolicy    *string           `json:"forward_policy"`
	MaxAgents        *int              `json:"max_agents"`
	TokenFP          *string           `json:"token_fp"`
	TokenFPVerified  bool              `json:"token_fp_verified"`
	TokenFPScope     string            `json:"token_fp_scope"`
}

// serversResponse is the envelope's data object.
type serversResponse struct {
	GeneratedAt      string        `json:"generated_at"`
	LocalID          string        `json:"local_id"`
	RetentionSeconds int64         `json:"retention_seconds"`
	Servers          []serverEntry `json:"servers"`
}

// preAuthIdentity is the four-key block published UNAUTHENTICATED on
// /api/v1/health. It is a projection of servers[0], not a second computation.
type preAuthIdentity struct {
	Name       string  `json:"name"`
	ListenAddr string  `json:"listen_addr"`
	Version    *string `json:"version"`
	Role       string  `json:"role"`
}

// peerRecord is the persisted record of one peer. It survives the session:
// that is the difference between "down" and "never existed".
type peerRecord struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	ListenAddrRaw  string            `json:"listen_addr_raw"`
	MaxAgents      *int              `json:"max_agents"`
	Version        *string           `json:"version"`
	FirstSeen      string            `json:"first_seen"`
	LastSeen       string            `json:"last_seen"`
	DownSince      *string           `json:"down_since,omitempty"`
	StateReason    string            `json:"state_reason"`
	LastTransports []serverTransport `json:"last_transports"`
	TokenFP        *string           `json:"token_fp"`
	TokenFPScope   string            `json:"token_fp_scope"`
}

// serversDoc is the on-disk document (§6.4). No secrets: fingerprints only.
type serversDoc struct {
	Version   int                    `json:"version"`
	UpdatedAt string                 `json:"updated_at"`
	Local     serversLocalRecord     `json:"local"`
	Peers     map[string]*peerRecord `json:"peers"`
}

type serversLocalRecord struct {
	ID        string `json:"id"`
	FirstSeen string `json:"first_seen"`
}

// serversStore owns servers.json: one document, one lock, coalesced writes.
type serversStore struct {
	mu     sync.Mutex
	path   string
	doc    serversDoc
	loaded bool
	dirty  bool
	last   time.Time

	retention time.Duration
	maxPeers  int
	floor     time.Duration
}

// inventory returns the process-wide inventory store, creating it on first
// use. Lazy so Server literals in tests keep working unchanged.
func (s *Server) inventory() *serversStore {
	s.invMu.Lock()
	defer s.invMu.Unlock()
	if s.inv != nil {
		return s.inv
	}
	st := &serversStore{
		path:      s.serversPath(),
		retention: envDuration("PROBE_SERVERS_RETENTION", defaultServersRetention),
		maxPeers:  envInt("PROBE_SERVERS_MAX_PEERS", defaultServersMaxPeers),
		floor:     envDuration("PROBE_SERVERS_WRITE_MIN_INTERVAL", defaultServersWriteFloor),
		doc: serversDoc{
			Version: 1,
			Peers:   make(map[string]*peerRecord),
		},
	}
	s.inv = st
	return st
}

// serversPath resolves the document beside the registry actually in use, so
// "beside registry.json" is true in production and in t.TempDir() tests
// without a second path knob.
func (s *Server) serversPath() string {
	if p := strings.TrimSpace(os.Getenv("PROBE_SERVERS_DB")); p != "" {
		return p
	}
	reg := ""
	if s.registry != nil {
		reg = s.registry.savePath
	}
	if reg != "" {
		return filepath.Join(filepath.Dir(reg), "servers.json")
	}
	return filepath.Join(os.TempDir(), "probe-servers.json")
}

// load reads the document once. A corrupt file is quarantined, not
// overwritten: evidence first, then serve local-only (F2).
func (st *serversStore) load() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.loadLocked()
}

func (st *serversStore) loadLocked() {
	if st.loaded {
		return
	}
	st.loaded = true
	if st.doc.Peers == nil {
		st.doc.Peers = make(map[string]*peerRecord)
	}
	st.doc.Version = 1
	raw, err := os.ReadFile(st.path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[servers] servers.json unreadable: %v — serving local only", err)
		}
		return
	}
	var doc serversDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Fail-closed toward truth: quarantine the evidence, fabricate nothing.
		quarantine := fmt.Sprintf("%s.corrupt-%d", st.path, time.Now().Unix())
		if mvErr := os.Rename(st.path, quarantine); mvErr != nil {
			log.Printf("[servers] servers.json corrupt: %v — quarantine failed: %v", err, mvErr)
		} else {
			log.Printf("[servers] servers.json corrupt: %v — moved to %s, serving local only", err, quarantine)
		}
		return
	}
	if doc.Peers == nil {
		doc.Peers = make(map[string]*peerRecord)
	}
	doc.Version = 1
	st.doc = doc
}

// localID returns the persisted id of this process, generating it once.
func (st *serversStore) localID() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.loadLocked()
	if st.doc.Local.ID == "" {
		st.doc.Local.ID = "srv-" + randomHex(4)
		st.doc.Local.FirstSeen = time.Now().UTC().Format(time.RFC3339)
		st.dirty = true
		st.writeLocked(true)
	}
	return st.doc.Local.ID
}

// flush persists the document if it is dirty and the coalescing floor has
// elapsed, or unconditionally when force is set (a state transition).
func (st *serversStore) flush(force bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.writeLocked(force)
}

func (st *serversStore) writeLocked(force bool) {
	if !st.dirty && !force {
		return
	}
	if !force && time.Since(st.last) < st.floor {
		return
	}
	st.doc.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	raw, err := json.MarshalIndent(st.doc, "", "  ")
	if err != nil {
		log.Printf("[servers] servers.json marshal: %v", err)
		return
	}
	if err := writeFileAtomic(st.path, raw, 0o600); err != nil {
		log.Printf("[servers] servers.json write: %v", err)
		return
	}
	st.last = time.Now()
	st.dirty = false
}

// ---------------------------------------------------------------------------
// Local entry — the one identity builder
// ---------------------------------------------------------------------------

// localName is the only naming rule in the server (§7.2):
// PROBE_SERVER_NAME → --server-name (set via SetServerIdentity) → hostname →
// "probe". s.addr is forbidden: it is a bind configuration, not a name (D3).
func (s *Server) localName() string {
	s.uplinkMu.RLock()
	configured := s.serverName
	s.uplinkMu.RUnlock()
	if n := strings.TrimSpace(configured); n != "" {
		return n
	}
	if n := strings.TrimSpace(os.Getenv("PROBE_SERVER_NAME")); n != "" {
		return n
	}
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return strings.TrimSpace(h)
	}
	return "probe"
}

// localEntry builds servers[0]. Every field has exactly one source; nothing
// here reads a request header (D8).
func (s *Server) localEntry(now time.Time) serverEntry {
	addr, port, source := dialableAddrParts("", s.addr)
	transports := []serverTransport{}
	if addr != "" {
		scheme := "ws"
		if s.certFile != "" && s.keyFile != "" {
			scheme = "wss"
		}
		url := fmt.Sprintf("%s://%s/ws", scheme, addr)
		connected := startTime.UTC().Format(time.RFC3339)
		transports = append(transports, serverTransport{
			Kind:        transportKindListen,
			URL:         &url,
			Port:        port,
			WSState:     wsStateListening,
			ConnectedAt: &connected,
			Source:      transportSourceLocalConfig,
		})
	}

	var version *string
	if v := strings.TrimSpace(s.version); v != "" {
		version = &v
	}
	uptime := int64(time.Since(startTime).Seconds())
	direct, relayed := s.agentCounts()

	return serverEntry{
		ID:               s.inventory().localID(),
		Name:             s.localName(),
		Role:             roleLocal,
		ListenAddr:       addr,
		ListenAddrRaw:    s.addr,
		ListenAddrSource: source,
		Version:          version,
		UptimeSeconds:    &uptime,
		State:            "up",
		// The response's existence is the evidence that this process serves.
		LastSeen:        now.UTC().Format(time.RFC3339),
		StateReason:     stateReasonServing,
		DownSince:       nil,
		Transports:      transports,
		Counts:          serverCounts{DirectAgents: direct, RelayedAgents: relayed},
		ForwardPolicy:   nil, // no server-level default exists yet (P4)
		MaxAgents:       nil, // not a server concept today: null, never 0, never 100 (D4)
		TokenFP:         tokenFPForResponse(s.token),
		TokenFPVerified: true, // we hold our own secret
		TokenFPScope:    "shared",
	}
}

// preAuthIdentity projects the four keys the login screen may see. It is the
// SAME builder as servers[0], so a divergence is impossible by construction.
// PROBE_PREAUTH_IDENTITY=off returns nil (key present, value null).
func (s *Server) preAuthIdentity() *preAuthIdentity {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("PROBE_PREAUTH_IDENTITY")), "off") {
		return nil
	}
	local := s.localEntry(time.Now())
	return &preAuthIdentity{
		Name:       local.Name,
		ListenAddr: local.ListenAddr,
		Version:    local.Version,
		Role:       roleLocal,
	}
}

// agentCounts is the ONE conn-map read in the inventory (§9). P0 replaces this
// site; no third reader of s.conns may be added anywhere.
func (s *Server) agentCounts() (direct, relayed int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.conns {
		if _, ok := c.(*virtualConn); ok {
			relayed++
		} else {
			direct++
		}
	}
	return direct, relayed
}

// tokenFPForResponse is sha256(secret)[:8]; null when auth is disabled or the
// fingerprint is switched off. Never the secret, never a prefix of it.
func tokenFPForResponse(secret string) *string {
	if secret == "" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("PROBE_TOKEN_FP_MODE")), "off") {
		return nil
	}
	fp := tokenFingerprint(secret)
	if fp == "" {
		return nil
	}
	return &fp
}

// dialableAddrParts returns the dialable host:port, the port, and the
// provenance of the host. The receiving side may substitute the OS hostname
// for a bind string no other machine can dial — and says so, so the
// substitution is a receipt rather than an invention.
func dialableAddrParts(advertised, bind string) (addr string, port *int, source string) {
	raw := strings.TrimSpace(advertised)
	source = "configured"
	if raw == "" {
		raw = strings.TrimSpace(bind)
	} else if p, ok := portOf(raw); ok {
		return raw, &p, source
	} else {
		return raw, nil, source
	}
	host, portStr, err := net.SplitHostPort(raw)
	if err != nil {
		// No port to name. Returning the raw string is honest; inventing a
		// port is not (F5/F6).
		return raw, nil, source
	}
	n, convErr := strconv.Atoi(portStr)
	if convErr != nil || n <= 0 {
		return raw, nil, source
	}
	ip := net.ParseIP(host)
	if host == "" || host == "localhost" || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		if h, herr := os.Hostname(); herr == nil && strings.TrimSpace(h) != "" {
			return net.JoinHostPort(strings.TrimSpace(h), portStr), &n, "hostname"
		}
		return raw, &n, source
	}
	return raw, &n, source
}

// portOf extracts a port from host:port or a URL without splitting hosts.
func portOf(raw string) (int, bool) {
	v := strings.TrimPrefix(strings.TrimPrefix(raw, "ws://"), strings.TrimPrefix(raw, "wss://"))
	v = strings.TrimPrefix(v, "http://")
	v = strings.TrimPrefix(v, "https://")
	v = strings.TrimSuffix(v, "/ws")
	v = strings.TrimSuffix(v, "/")
	_, portStr, err := net.SplitHostPort(v)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(portStr)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// randomHex returns n random bytes as lowercase hex. The id it forms must not
// be derivable from the address, which changes when the box moves.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// ---------------------------------------------------------------------------
// Peer records
// ---------------------------------------------------------------------------

// peerRegistered records a peer as up, immediately and durably. Called after
// the session is in s.relays. A displaced session (same relay id, new socket)
// leaves the reason duplicate_id_replaced on the record it replaced.
func (s *Server) peerRegistered(session *relaySession) {
	st := s.inventory()
	st.load()

	now := time.Now().UTC().Format(time.RFC3339)
	rec := &peerRecord{
		ID:             session.relayID,
		Name:           session.relayID, // peer_hello.peer.name is P2; until then the id is the honest label
		ListenAddrRaw:  "",
		StateReason:    stateReasonSocketOpen,
		LastSeen:       now,
		TokenFPScope:   "shared", // the shared token identifies no peer (D2)
		FirstSeen:      now,
		LastTransports: peerTransports(session, wsStateOpen),
	}
	if session.metadata != nil {
		rec.ListenAddrRaw = session.metadata.ListenAddr
		if session.metadata.MaxAgents > 0 {
			n := session.metadata.MaxAgents
			rec.MaxAgents = &n
		}
	}
	if session.tokenFP != "" {
		fp := session.tokenFP
		rec.TokenFP = &fp
	}

	st.mu.Lock()
	st.loadLocked()
	if prev, ok := st.doc.Peers[session.relayID]; ok {
		rec.FirstSeen = prev.FirstSeen
		if rec.TokenFP == nil {
			rec.TokenFP = prev.TokenFP
		}
	}
	st.doc.Peers[session.relayID] = rec
	st.evictLocked()
	st.dirty = true
	st.writeLocked(true) // a state transition is flushed at once
	st.mu.Unlock()

	log.Printf("[servers] peer up id=%s listen=%s", session.relayID, rec.ListenAddrRaw)
}

// peerLost marks a peer down BEFORE its session is deleted. The record is the
// only thing a later reader can cite for "down" rather than "absent".
func (s *Server) peerLost(session *relaySession, reason string) {
	if reason == "" {
		reason = stateReasonSocketClosed
	}
	st := s.inventory()
	st.load()

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	lastSeen := session.seenAt()
	if lastSeen.IsZero() {
		lastSeen = now
	}

	st.mu.Lock()
	st.loadLocked()
	rec := st.doc.Peers[session.relayID]
	if rec == nil {
		rec = &peerRecord{
			ID:           session.relayID,
			Name:         session.relayID,
			FirstSeen:    lastSeen.UTC().Format(time.RFC3339),
			TokenFPScope: "shared",
		}
		st.doc.Peers[session.relayID] = rec
	}
	rec.StateReason = reason
	rec.LastSeen = lastSeen.UTC().Format(time.RFC3339)
	if rec.DownSince == nil {
		rec.DownSince = &nowStr // the FIRST transition into down, preserved after
	}
	rec.LastTransports = peerTransports(session, wsStateClosed)
	rec.MaxAgents = nil
	st.evictLocked()
	st.dirty = true
	st.writeLocked(true)
	st.mu.Unlock()

	log.Printf("[servers] peer down id=%s reason=%s last_seen=%s", session.relayID, reason, rec.LastSeen)
}

// peerTransports renders a peer's two distinguishable links. The uplink is
// only reported when the peer said something about it; an unreported uplink is
// "unknown" with a null URL, never synthesised from the listen address.
func peerTransports(session *relaySession, listenState string) []serverTransport {
	out := []serverTransport{}
	if session.metadata != nil && strings.TrimSpace(session.metadata.ListenAddr) != "" {
		raw := session.metadata.ListenAddr
		addr, port, _ := dialableAddrParts("", raw)
		var url *string
		if addr != "" && port != nil {
			u := fmt.Sprintf("ws://%s/ws", addr)
			url = &u
		}
		connected := session.startedAt
		var connectedPtr *string
		if connected != "" {
			connectedPtr = &connected
		}
		out = append(out, serverTransport{
			Kind:        transportKindListen,
			URL:         url,
			Port:        port,
			WSState:     listenState,
			ConnectedAt: connectedPtr,
			Source:      transportSourcePeerMetadata,
		})
	} else {
		// F5: nothing reported. Provenance says so; no port is invented.
		out = append(out, serverTransport{
			Kind:    transportKindListen,
			URL:     nil,
			Port:    nil,
			WSState: wsStateUnknown,
			Source:  transportSourceUnreported,
		})
	}

	if session.metadata != nil && strings.TrimSpace(session.metadata.Upstream) != "" {
		up := session.metadata.Upstream
		var portPtr *int
		if p, ok := portOf(up); ok {
			portPtr = &p
		}
		connected := session.startedAt
		var connectedPtr *string
		if connected != "" {
			connectedPtr = &connected
		}
		out = append(out, serverTransport{
			Kind:        transportKindUplink,
			URL:         &up,
			Port:        portPtr,
			WSState:     listenState,
			ConnectedAt: connectedPtr,
			Source:      transportSourcePeerReport,
		})
	}
	return out
}

// evictLocked enforces the retention window and the peer cap. A live peer is
// never evicted by pressure — that would hide exactly the thing this endpoint
// exists to show.
func (st *serversStore) evictLocked() {
	now := time.Now().UTC()
	for id, rec := range st.doc.Peers {
		if rec.DownSince == nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, *rec.DownSince); err == nil {
			if now.Sub(t) > st.retention {
				log.Printf("[servers] peer %s evicted: down since %s exceeds retention %s", id, *rec.DownSince, st.retention)
				delete(st.doc.Peers, id)
				st.dirty = true
			}
		}
	}
	if len(st.doc.Peers) <= st.maxPeers {
		return
	}
	type aged struct {
		id   string
		since time.Time
	}
	down := []aged{}
	for id, rec := range st.doc.Peers {
		if rec.DownSince == nil {
			continue
		}
		t, _ := time.Parse(time.RFC3339, *rec.DownSince)
		down = append(down, aged{id: id, since: t})
	}
	sort.Slice(down, func(i, j int) bool { return down[i].since.Before(down[j].since) })
	for _, a := range down {
		if len(st.doc.Peers) <= st.maxPeers {
			break
		}
		delete(st.doc.Peers, a.id)
		st.dirty = true
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// handleV1Servers serves GET /api/v1/servers. It never returns 503 and never
// an empty list: the process answering is proof that servers[0] is up.
func (s *Server) handleV1Servers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	now := time.Now().UTC()
	local := s.localEntry(now)

	entries := []serverEntry{local}
	entries = append(entries, s.peerEntries(now)...)

	st := s.inventory()
	st.flush(false) // bounded: at most one write per floor, and only if dirty

	writeJSON(w, http.StatusOK, serversResponse{
		GeneratedAt:      now.Format(time.RFC3339),
		LocalID:          local.ID,
		RetentionSeconds: int64(s.inventory().retention.Seconds()),
		Servers:          entries,
	})
}

// peerEntries merges the live relay map (up) with persisted down records.
func (s *Server) peerEntries(now time.Time) []serverEntry {
	st := s.inventory()
	st.load()

	live := map[string]*relaySession{}
	s.relayMu.RLock()
	for id, sess := range s.relays {
		live[id] = sess
	}
	s.relayMu.RUnlock()

	entries := []serverEntry{}
	for id, sess := range live {
		entries = append(entries, s.livePeerEntry(st, id, sess, now))
	}

	st.mu.Lock()
	ids := make([]string, 0, len(st.doc.Peers))
	for id := range st.doc.Peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := live[id]; ok {
			continue // live rows come from the socket, not the file
		}
		rec := st.doc.Peers[id]
		entries = append(entries, serverEntry{
			ID:               rec.ID,
			Name:             peerName(rec),
			Role:             rolePeer,
			ListenAddr:       peerListenAddr(rec),
			ListenAddrRaw:    rec.ListenAddrRaw,
			ListenAddrSource: peerListenSource(rec),
			Version:          rec.Version,
			UptimeSeconds:    nil,
			State:            "down",
			LastSeen:         rec.LastSeen,
			StateReason:      rec.StateReason,
			DownSince:        rec.DownSince,
			Transports:       closedTransports(rec.LastTransports),
			Counts:           serverCounts{},
			ForwardPolicy:    nil,
			MaxAgents:        rec.MaxAgents,
			TokenFP:          rec.TokenFP,
			TokenFPVerified:  false, // the shared token identifies no peer (D2)
			TokenFPScope:     "shared",
		})
	}
	st.mu.Unlock()

	// R4: up before down, then name, then id — deterministic across polls.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].State != entries[j].State {
			return entries[i].State == "up"
		}
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].ID < entries[j].ID
	})
	return entries
}

// livePeerEntry renders a peer whose socket is open right now.
func (s *Server) livePeerEntry(st *serversStore, id string, sess *relaySession, now time.Time) serverEntry {
	lastSeen := sess.seenAt()
	if lastSeen.IsZero() {
		lastSeen = now
	}
	// A peer's clock cannot be trusted; clamp instead of printing the future.
	if lastSeen.After(now) {
		lastSeen = now
	}

	var maxAgents *int
	var raw string
	var version *string
	if sess.metadata != nil {
		raw = sess.metadata.ListenAddr
		if sess.metadata.MaxAgents > 0 {
			n := sess.metadata.MaxAgents
			maxAgents = &n
		}
	}
	st.mu.Lock()
	if rec := st.doc.Peers[id]; rec != nil {
		version = rec.Version
	}
	st.mu.Unlock()

	relayed := 0
	sess.channelsMu.RLock()
	for _, vc := range sess.channels {
		if vc.agentID != "" {
			relayed++
		}
	}
	sess.channelsMu.RUnlock()

	addr, _, source := dialableAddrParts("", raw)
	var tokenFP *string
	if sess.tokenFP != "" {
		fp := sess.tokenFP
		tokenFP = &fp
		if strings.EqualFold(strings.TrimSpace(os.Getenv("PROBE_TOKEN_FP_MODE")), "off") {
			tokenFP = nil
		}
	}

	return serverEntry{
		ID:               id,
		Name:             id, // peer-reported name arrives with P2
		Role:             rolePeer,
		ListenAddr:       addr,
		ListenAddrRaw:    raw,
		ListenAddrSource: source,
		Version:          version,
		UptimeSeconds:    nil,
		State:            "up",
		LastSeen:         lastSeen.UTC().Format(time.RFC3339),
		StateReason:      stateReasonSocketOpen,
		DownSince:        nil,
		Transports:       peerTransports(sess, wsStateOpen),
		Counts:           serverCounts{RelayedAgents: relayed},
		ForwardPolicy:    nil,
		MaxAgents:        maxAgents,
		TokenFP:          tokenFP,
		TokenFPVerified:  false, // the shared token cannot identify a peer (D2)
		TokenFPScope:     "shared",
	}
}

// peerName: peer_hello.peer.name is P2; until then the id is the label. It is
// never the bind address.
func peerName(rec *peerRecord) string {
	if rec.Name != "" {
		return rec.Name
	}
	return rec.ID
}

// peerListenAddr renders the stored raw listen string and never invents a port.
func peerListenAddr(rec *peerRecord) string {
	addr, _, _ := dialableAddrParts("", rec.ListenAddrRaw)
	return addr
}

func peerListenSource(rec *peerRecord) string {
	_, _, source := dialableAddrParts("", rec.ListenAddrRaw)
	return source
}

func closedTransports(in []serverTransport) []serverTransport {
	if len(in) == 0 {
		return []serverTransport{}
	}
	out := make([]serverTransport, 0, len(in))
	for _, t := range in {
		if t.Kind == transportKindListen {
			t.WSState = wsStateClosed
		} else {
			t.WSState = wsStateUnknown // an uplink we no longer hold cannot be claimed open
		}
		out = append(out, t)
	}
	return out
}

func envDuration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return def
}

func envInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	return def
}

// statusText is used by tests and logs to describe an entry in one line.
func (e serverEntry) statusText() string {
	return fmt.Sprintf("%s/%s/%s", e.ID, e.Role, e.State)
}
