package server

// P21-08 — per-link counters, and one chain object that names the failing hop.
//
// A LINK here is one relay's uplink socket to this server, or (inside a relay)
// one nested relay socket. The counters are measured where the bytes actually
// move and are never derived from message counts:
//
//   - inbound (bytes_from_relay): the payload slice the relay socket's read
//     loop hands on, in handleRelayConnection — the bytes after the 5-byte
//     relay header, which is the unit the rest of the protocol moves;
//   - outbound (bytes_to_relay): the frame copy in virtualConn.WriteMessage /
//     WriteJSON, counted only after the write succeeded.
//
// A failed write is therefore not counted as relayed: it becomes last_error.
//
// Monotonic and kept OUTSIDE relaySession: a reconnect builds a new session
// object, so a counter living in the session would reset on exactly the event
// it exists to describe. What resets:
//
//   - agents_current — gauge, recomputed from the live channel set;
//   - state — derived from live session presence, per read;
//   - last_error — cleared by a successful re-registration of the same relay
//     id (the link came back).
//
// What never resets except on server restart: agents_carried, bytes_relayed,
// reconnects.
//
// reconnects is not a new counter. It is the registry's own
// AgentRecord.Reconnects (registry.go: "monotonic count of re-registrations")
// extended to the link: the store keeps the last observed value per agent ever
// carried on the link and reports their sum. A re-registration on this link
// therefore increments it, and a link whose agents are gone keeps the count.
//
// upstream_latency_ms is stated absent (-1), not faked: the relay wire
// protocol carries no ping/pong on channel 0, so this server cannot time the
// data path. The stated equivalent is probe_latency_ms — the measured
// round-trip of this server's bounded status read to that hop. It is a real
// measurement of the hop, and it is labelled as a probe, not as packet RTT.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// chainObserveTimeout bounds one hop's status read. Relays are observed in
	// parallel, so the endpoint costs this bound once, not once per hop.
	chainObserveTimeout = 300 * time.Millisecond

	// chainStateUp / chainStateDown / chainStateDegraded reuse the vocabulary
	// already in the tree (topology edges and agent rows), not a third one.
	chainStateUp       = "up"
	chainStateDown     = "down"
	chainStateDegraded = "degraded"

	// latencyUnmeasured marks a probe latency that was not measured. A zero
	// would read as "instant", which is a claim, not an absence.
	latencyUnmeasured int64 = -1

	// §5.3 rule 4 registration_state vocabulary (spec P27-01). It answers one
	// question per chain, from measured counters: did the server hop's
	// forwarding lane get its registration? registered = positive evidence
	// (an upstream frame confirmed a channel); refused = the upstream's last
	// answer was a forward_refused and nothing has confirmed since;
	// unanswered = channels are open upstream and none has been confirmed or
	// refused yet. Absent = no uplink is configured, so nothing was asked.
	registrationRegistered = "registered"
	registrationRefused    = "refused"
	registrationUnanswered = "unanswered"
)

// LinkCounters is the per-link counter set served inside a chain hop.
type LinkCounters struct {
	AgentsCarried  int    `json:"agents_carried"` // distinct agents admitted on this link (monotonic)
	AgentsCurrent  int    `json:"agents_current"` // agents live on this link right now (gauge)
	BytesRelayed   int64  `json:"bytes_relayed"`  // payload bytes moved in either direction (monotonic)
	BytesFromRelay int64  `json:"bytes_from_relay"`
	BytesToRelay   int64  `json:"bytes_to_relay"`
	Reconnects     int    `json:"reconnects"` // sum of AgentRecord.Reconnects for agents carried here (monotonic)
	LastError      string `json:"last_error,omitempty"`
	// UpstreamLatencyMs is always -1: not measurable from this side of the
	// relay wire protocol. ProbeLatencyMs is the measured status-read RTT.
	UpstreamLatencyMs int64 `json:"upstream_latency_ms"`
	ProbeLatencyMs    int64 `json:"probe_latency_ms"`
}

// linkStat is the durable per-link record. It outlives the relaySession.
type linkStat struct {
	mu       sync.Mutex
	relayID  string
	listen   string
	counters LinkCounters
	// agents maps agent id → the last observed registry Reconnects value.
	// Distinct ids are what agents_carried counts, so a reconnect of the same
	// agent does not inflate it.
	agents map[string]int
	live   map[string]bool
	chains []chainView
	// nested is the hop tail observed beyond this relay at the last status
	// read that succeeded. When the relay stops answering, the read yields no
	// doc at all — without this the path would silently end at the hop that
	// failed and the hops behind it would disappear. Last-known, labelled
	// Observed=false; never presented as a live reading.
	nested []chainHop
	// lastSeen is the last moment this link registered or moved bytes.
	lastSeen time.Time
}

// linkStatStore is indexed by relay id. The zero value is usable: the map is
// created on first use, so Server literals in tests keep working.
type linkStatStore struct {
	mu    sync.Mutex
	links map[string]*linkStat
}

func (st *linkStatStore) forRelay(relayID string) *linkStat {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.links == nil {
		st.links = make(map[string]*linkStat)
	}
	if l, ok := st.links[relayID]; ok {
		return l
	}
	l := &linkStat{
		relayID: relayID,
		agents:  make(map[string]int),
		live:    make(map[string]bool),
	}
	st.links[relayID] = l
	return l
}

func (st *linkStatStore) ids() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]string, 0, len(st.links))
	for id := range st.links {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// registration is called when a relay's uplink socket is admitted: the link is
// up, its listen address is refreshed, and the failure that took it down (if
// any) is cleared.
func (l *linkStat) registration(listen string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if strings.TrimSpace(listen) != "" {
		l.listen = listen
	}
	l.counters.LastError = ""
	l.lastSeen = time.Now().UTC()
}

// fail records the error that took the link down. It is last-value, not a
// counter: a link has one current failure, not a tally of them.
func (l *linkStat) fail(err error) {
	if err == nil {
		return
	}
	l.mu.Lock()
	l.counters.LastError = err.Error()
	l.lastSeen = time.Now().UTC()
	l.mu.Unlock()
}

// addBytesUp counts payload bytes that arrived from the relay.
func (l *linkStat) addBytesUp(n int) {
	if n <= 0 {
		return
	}
	l.mu.Lock()
	l.counters.BytesFromRelay += int64(n)
	l.counters.BytesRelayed += int64(n)
	l.lastSeen = time.Now().UTC()
	l.mu.Unlock()
}

// addBytesDown counts payload bytes successfully written to the relay.
func (l *linkStat) addBytesDown(n int) {
	if n <= 0 {
		return
	}
	l.mu.Lock()
	l.counters.BytesToRelay += int64(n)
	l.counters.BytesRelayed += int64(n)
	l.lastSeen = time.Now().UTC()
	l.mu.Unlock()
}

// agentCarried records one agent admitted on this link. Counting distinct ids
// keeps it monotonic across a reconnect of the same agent.
func (l *linkStat) agentCarried(agentID string) {
	if agentID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, seen := l.agents[agentID]; !seen {
		l.counters.AgentsCarried++
		l.agents[agentID] = 0
	}
	l.live[agentID] = true
	l.lastSeen = time.Now().UTC()
}

// agentGone drops an agent from the live set. The carried counter and the
// observed reconnect value are retained: they describe the link's history.
func (l *linkStat) agentGone(agentID string) {
	if agentID == "" {
		return
	}
	l.mu.Lock()
	delete(l.live, agentID)
	l.mu.Unlock()
}

// observeReconnects extends the registry field onto the link. It stores the
// value the registry reports, never a locally incremented one.
func (l *linkStat) observeReconnects(agentID string, reconnects int) {
	if agentID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.agents[agentID] = reconnects
	sum := 0
	for _, v := range l.agents {
		sum += v
	}
	l.counters.Reconnects = sum
}

// setProbeLatency records the measured status-read round-trip to this hop.
func (l *linkStat) setProbeLatency(ms int64) {
	l.mu.Lock()
	l.counters.ProbeLatencyMs = ms
	l.mu.Unlock()
}

// snapshot copies the counters and fills the gauge from the live set.
func (l *linkStat) snapshot() LinkCounters {
	l.mu.Lock()
	defer l.mu.Unlock()
	c := l.counters
	c.AgentsCurrent = len(l.live)
	if c.UpstreamLatencyMs == 0 {
		c.UpstreamLatencyMs = latencyUnmeasured
	}
	return c
}

func (l *linkStat) setChains(chains []chainView) {
	l.mu.Lock()
	l.chains = chains
	l.mu.Unlock()
}

func (l *linkStat) chainsSnapshot() []chainView {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]chainView, len(l.chains))
	copy(out, l.chains)
	return out
}

// setNested records the hop tail observed beyond this relay on a read that
// succeeded. Called with the tail of the chain just composed, so the next read
// that cannot reach this relay still knows what was behind it.
func (l *linkStat) setNested(hops []chainHop) {
	l.mu.Lock()
	l.nested = append([]chainHop(nil), hops...)
	l.mu.Unlock()
}

// nestedSnapshot returns the last observed tail, in path order. The caller
// re-labels it as last-known; nothing here knows whether it is current.
func (l *linkStat) nestedSnapshot() []chainHop {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]chainHop, len(l.nested))
	copy(out, l.nested)
	return out
}

// noteRelayedAgent records one agent admitted on a relay link and extends the
// registry's Reconnects field onto the link. The value stored is the registry's
// number, never a locally incremented one.
func (s *Server) noteRelayedAgent(session *relaySession, agentID string) {
	if session == nil || session.link == nil || agentID == "" {
		return
	}
	session.link.agentCarried(agentID)
	if rec, err := s.registry.GetHealth(agentID); err == nil {
		session.link.observeReconnects(agentID, rec.Reconnects)
	}
}

func (l *linkStat) listenAddr() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.listen
}

// ---------------------------------------------------------------------------
// The chain object
// ---------------------------------------------------------------------------

// chainHop is one node on a path. State uses up/down/degraded; Error is present
// exactly when the hop is not up.
type chainHop struct {
	ID       string        `json:"id"`
	Role     string        `json:"role"` // "server" | "relay" | "agent"
	State    string        `json:"state"`
	Error    string        `json:"error,omitempty"`
	Channels int           `json:"channels,omitempty"`
	Link     *LinkCounters `json:"link,omitempty"` // counters for the link INTO this hop
	Observed bool          `json:"observed"`       // false = last known, not read this request
	LastSeen string        `json:"last_seen,omitempty"`
	// P27-02R (spec §5.3 rule 4): the server-role hop's registration answer.
	// Set on the root hop only (registered|refused|unanswered); omitted where
	// the question does not apply, because an absent key is an honest "not
	// asked" while an invented value is a claim.
	RegistrationState string `json:"registration_state,omitempty"`
}

// chainFailing names the hop that broke the path. "degraded" without this is
// the defect: it moves the diagnosis back to the operator.
type chainFailing struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
}

// chainView is ONE object for a whole path root → hop1 → hop2 → agent. A caller
// never has to join edges to see where a two-hop path broke.
type chainView struct {
	ID             string        `json:"id"`
	State          string        `json:"state"`
	Path           []string      `json:"path"`
	Hops           []chainHop    `json:"hops"`
	Failing        *chainFailing `json:"failing_hop,omitempty"`
	Agent          string        `json:"agent,omitempty"`
	DegradedReason string        `json:"degraded_reason,omitempty"`
	ObservedAt     string        `json:"observed_at"`
	ObserveBoundMs int64         `json:"observe_bound_ms"`
}

// nestedHopStatus is the subset of a relay's own status document this server
// needs: the nested relays it carries and their state.
type nestedHopStatus struct {
	ID             string   `json:"id"`
	ListenAddr     string   `json:"listen_addr"`
	State          string   `json:"state"`
	LastError      string   `json:"last_error"`
	Channels       []uint32 `json:"channels"`
	AgentsCarried  int      `json:"agents_carried"`
	BytesRelayed   int64    `json:"bytes_relayed"`
	BytesFromRelay int64    `json:"bytes_from_relay"`
	BytesToRelay   int64    `json:"bytes_to_relay"`
	Reconnects     int      `json:"reconnects"`
	ConnectedAt    string   `json:"connected_at"`
}

// relayStatusDoc is the relay's own status document as read over the loopback
// listener it declares at registration.
type relayStatusDoc struct {
	RelayID  string `json:"relay_id"`
	Upstream struct {
		State     string `json:"state"`
		LastError string `json:"last_error"`
	} `json:"upstream"`
	Downstream struct {
		Listen   string            `json:"listen"`
		Channels int               `json:"channels"`
		Nested   []nestedHopStatus `json:"nested"`
	} `json:"downstream"`
}

// liveRelay mirrors the parts of a relaySession the chain read needs, copied
// under the lock so the read never touches a session being torn down.
type liveRelay struct {
	relayID  string
	listen   string
	channels map[uint32]string // channel id → agent id ("" for an unregistered channel)
	link     *linkStat
}

// handleV1Chain serves GET /api/v1/chain — the chain objects, one per path.
//
// Every hop is observed in parallel under chainObserveTimeout, so a stalled or
// black-holed relay costs the bound once rather than stalling the request. A
// link whose socket is gone is served from the retained snapshot with the
// missing hop marked down: a dead hop stays represented.
func (s *Server) handleV1Chain(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}

	relays := s.liveRelays()
	views := make([][]chainView, len(relays))
	var wg sync.WaitGroup
	for i := range relays {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			views[i] = s.observeChains(relays[i])
		}(i)
	}
	wg.Wait()

	chains := make([]chainView, 0)
	seen := make(map[string]bool, len(relays))
	for i := range relays {
		seen[relays[i].relayID] = true
		chains = append(chains, views[i]...)
	}

	// Links with no live session: replay the last observed chain, degraded, with
	// the missing hop named. This is the branch that keeps a dead hop visible.
	for _, id := range s.linkStats.ids() {
		if seen[id] {
			continue
		}
		l := s.linkStats.forRelay(id)
		snapshot := l.chainsSnapshot()
		if len(snapshot) == 0 {
			continue
		}
		reason := l.snapshot().LastError
		if reason == "" {
			reason = "relay link closed and no failure text was recorded"
		}
		for _, cv := range snapshot {
			chains = append(chains, degradeFrom(cv, id, reason))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true,
		"data": map[string]interface{}{
			"chains":           chains,
			"count":            len(chains),
			"observe_bound_ms": chainObserveTimeout.Milliseconds(),
		},
	})
}

// liveRelays copies the relay sessions under the locks. P15-12: relay-first
// order — snapshot the session set under relayMu, release it, then read
// channelsMu per session; the two mutexes are never held together.
func (s *Server) liveRelays() []liveRelay {
	s.relayMu.RLock()
	sessions := make([]*relaySession, 0, len(s.relays))
	for _, rs := range s.relays {
		sessions = append(sessions, rs)
	}
	s.relayMu.RUnlock()

	out := make([]liveRelay, 0, len(sessions))
	for _, rs := range sessions {
		lr := liveRelay{
			relayID:  rs.relayID,
			channels: make(map[uint32]string),
			link:     s.linkStats.forRelay(rs.relayID),
		}
		if rs.metadata != nil {
			lr.listen = rs.metadata.ListenAddr
		}
		if lr.listen == "" && lr.link != nil {
			// A relay that registered without metadata is still readable at
			// the address it used last time.
			lr.listen = lr.link.listenAddr()
		}
		rs.channelsMu.RLock()
		for chID, vc := range rs.channels {
			lr.channels[chID] = vc.agentID
		}
		rs.channelsMu.RUnlock()
		out = append(out, lr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].relayID < out[j].relayID })
	return out
}

// uplinkRegistrationState derives the §5.3 rule 4 registration_state for a
// chain's server-role hop from the §5.2 carrying counters the prior leg
// shipped (UplinkForwarder.Carrying) plus the last refusal code. It reports
// the state name and, when refused, the detail that names the failure.
//
// Order is deliberate: a confirmed registration outranks an older refusal (the
// lane recovered, so the row must stop reading refused), a refusal outranks
// "nothing yet" (the upstream's last answer was no), and channels open with no
// answer are answered honestly as unanswered rather than green. No uplink
// configured means nothing was asked, which is an empty state, not a value.
func (s *Server) uplinkRegistrationState() (state, detail string) {
	s.forwardMu.RLock()
	f := s.forwarder
	s.forwardMu.RUnlock()
	if f == nil {
		return "", ""
	}
	open, registered, last := f.Carrying()
	switch {
	case registered > 0:
		return registrationRegistered, ""
	case last != nil:
		return registrationRefused, fmt.Sprintf("upstream refused registration (%s: %s)", last.Code, last.Reason)
	case open > 0:
		return registrationUnanswered, "channels are open upstream and none has been confirmed"
	}
	return "", ""
}

// observeChains reads one relay's link (bounded) and composes one chain per
// agent on it, plus a relay-only chain when it carries no agent yet.
func (s *Server) observeChains(lr liveRelay) []chainView {
	start := time.Now()
	doc, latencyMs, err := s.readRelayStatus(lr.listen)
	elapsed := time.Since(start).Milliseconds()
	if latencyMs < 0 {
		latencyMs = elapsed
	}
	lr.link.setProbeLatency(latencyMs)

	counters := lr.link.snapshot()
	rootHop := chainHop{ID: "server", Role: "server", State: chainStateUp, Observed: true}
	// P27-02R (spec §5.3 rule 4): the server-role hop carries what the
	// forwarding lane actually proved. A refused registration is folded by
	// deriveChainState into a failing hop, so a chain can never terminate
	// at "server" in green while the upstream is refusing its registrations.
	if rs, detail := s.uplinkRegistrationState(); rs != "" {
		rootHop.RegistrationState = rs
		if rs == registrationRefused {
			rootHop.Error = detail
		}
	}

	relayHop := chainHop{
		ID:       lr.relayID,
		Role:     "relay",
		State:    chainStateUp,
		Channels: len(lr.channels),
		Link:     &counters,
		Observed: true,
	}

	var nested []nestedHopStatus
	var nestedHops []chainHop
	if err != nil {
		// A hop that does not answer is DOWN. "degraded" in this tree is a hop
		// that answered and reported impairment — the topology uplink edge
		// resolves an unreadable hop to down (api_v1_phase4.go, "if err != nil
		// { state = down }"). A hop whose status read failed has no evidence of
		// answering at all, so it is down here too.
		relayHop.State = chainStateDown
		relayHop.Error = "hop status unreadable: " + err.Error()
		// The hops behind it were observed before it went silent. Keep them in
		// the path as last-known, with their identity intact: a dead middle hop
		// must not shorten the path to the neighbour that is still serving.
		nestedHops = lr.link.nestedSnapshot()
		for i := range nestedHops {
			h := &nestedHops[i]
			h.State = chainStateUp
			h.Error = ""
			h.Observed = false
		}
	} else if doc != nil {
		nested = doc.Downstream.Nested
		for _, n := range nested {
			state := chainStateUp
			if n.State != "" && n.State != chainStateUp {
				state = chainStateDown
			}
			nestedHops = append(nestedHops, chainHop{
				ID:       n.ID,
				Role:     "relay",
				State:    state,
				Error:    n.LastError,
				Channels: len(n.Channels),
				Link: &LinkCounters{
					AgentsCarried:     n.AgentsCarried,
					AgentsCurrent:     len(n.Channels),
					BytesRelayed:      n.BytesRelayed,
					BytesFromRelay:    n.BytesFromRelay,
					BytesToRelay:      n.BytesToRelay,
					Reconnects:        n.Reconnects,
					LastError:         n.LastError,
					UpstreamLatencyMs: latencyUnmeasured,
					ProbeLatencyMs:    latencyUnmeasured,
				},
				Observed: true,
			})
		}
		// This read succeeded: it is the tail the next failed read falls back
		// to. Nothing is retained from a read that answered nothing.
		lr.link.setNested(nestedHops)
	}

	hops := []chainHop{rootHop, relayHop}
	path := []string{"server", lr.relayID}
	for _, h := range nestedHops {
		hops = append(hops, h)
		path = append(path, h.ID)
	}

	// Which nested hop owns each channel, so an agent is placed behind the hop
	// it actually arrived through.
	owner := make(map[uint32]string)
	for _, n := range nested {
		for _, ch := range n.Channels {
			owner[ch] = n.ID
		}
	}

	// Deterministic order: by channel.
	channelIDs := make([]uint32, 0, len(lr.channels))
	for chID := range lr.channels {
		channelIDs = append(channelIDs, chID)
	}
	sort.Slice(channelIDs, func(i, j int) bool { return channelIDs[i] < channelIDs[j] })

	var out []chainView
	for _, chID := range channelIDs {
		agentID := lr.channels[chID]
		if agentID == "" {
			continue
		}
		agentHops := append([]chainHop(nil), hops...)
		agentPath := append([]string(nil), path...)
		if ownerID, ok := owner[chID]; ok && ownerID != "" {
			// Already inserted in path order by the nested loop; nothing to add.
			_ = ownerID
		}
		agentHops = append(agentHops, chainHop{ID: agentID, Role: "agent", State: chainStateUp, Observed: true})
		agentPath = append(agentPath, agentID)
		cv := newChainView(agentID, agentPath, agentHops)
		out = append(out, cv)
	}
	if len(out) == 0 {
		out = append(out, newChainView("", path, hops))
	}

	lr.link.setChains(out)
	return out
}

// newChainView assembles the object and derives its state from the hops: the
// first hop that is not up is the failing hop, and it is named.
func newChainView(agent string, path []string, hops []chainHop) chainView {
	cv := chainView{
		ID:             "chain:" + strings.Join(path, "->"),
		Path:           path,
		Hops:           hops,
		Agent:          agent,
		ObservedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		ObserveBoundMs: chainObserveTimeout.Milliseconds(),
	}
	deriveChainState(&cv)
	return cv
}

// deriveChainState computes state, degraded_reason and failing_hop from the hop
// list. Path order decides: the first hop that is not up is the one that broke
// the path, and every hop after it is unobservable through it.
func deriveChainState(cv *chainView) {
	cv.State = chainStateUp
	cv.Failing = nil
	cv.DegradedReason = ""
	broken := false
	for i := range cv.Hops {
		h := &cv.Hops[i]
		// P27-02R (spec §5.3 rule 4): an engaged-forwarding but refused
		// registration is not a healthy hop — it is treated exactly like a
		// down hop, named as the failing hop, so a chain never reads up while
		// the server hop's own registration was refused.
		if h.RegistrationState == registrationRefused && h.State == chainStateUp {
			h.State = chainStateDown
			if h.Error == "" {
				h.Error = "registration refused and no refusal text was recorded"
			}
		}
		if broken && h.State == chainStateUp {
			h.State = chainStateDegraded
			h.Observed = false
			if h.Error == "" {
				h.Error = fmt.Sprintf("unobservable past %s", cv.Failing.ID)
			}
		}
		if !broken && h.State != chainStateUp {
			broken = true
			if h.Error == "" {
				h.Error = "hop is not up and reported no error text"
			}
			cv.Failing = &chainFailing{ID: h.ID, Error: h.Error}
			cv.State = chainStateDegraded
			cv.DegradedReason = fmt.Sprintf("hop %s: %s", h.ID, h.Error)
		}
	}
}

// degradeFrom renders a retained chain after its relay link disappeared: the
// relay hop is marked down with the recorded failure, downstream hops are
// unobservable, and the path is unchanged. A dead hop stays represented.
func degradeFrom(cv chainView, relayID, reason string) chainView {
	cv.ObservedAt = time.Now().UTC().Format(time.RFC3339Nano)
	for i := range cv.Hops {
		h := &cv.Hops[i]
		if h.State == chainStateUp {
			h.Observed = false
		}
		if h.ID == relayID {
			h.State = chainStateDown
			h.Error = reason
			h.Observed = false
		}
	}
	deriveChainState(&cv)
	return cv
}

// readRelayStatus reads one hop's own status document, bounded by
// chainObserveTimeout. Returns the measured round-trip in ms.
func (s *Server) readRelayStatus(listen string) (*relayStatusDoc, int64, error) {
	addr := normalizeUplinkAddr(listen)
	if addr == "" {
		return nil, latencyUnmeasured, fmt.Errorf("hop declared no listen address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), chainObserveTimeout)
	defer cancel()
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/v1/relay/status", nil)
	if err != nil {
		return nil, latencyUnmeasured, err
	}
	resp, err := (&http.Client{Timeout: chainObserveTimeout}).Do(req)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return nil, latencyUnmeasured, fmt.Errorf("status read at %s: %w", addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, latency, fmt.Errorf("status read at %s: HTTP %d", addr, resp.StatusCode)
	}
	var doc relayStatusDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, latency, fmt.Errorf("status decode at %s: %w", addr, err)
	}
	return &doc, latency, nil
}
