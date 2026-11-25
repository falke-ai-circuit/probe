package server

// P21-05 — one route map for relayed agents, and one resolver behind it.
//
// Before this file the conn-map was the only routing truth: a relayed agent
// lives there under the registry id "relay/<link>/<name>", which is not an id
// an operator can address (the API path parameter cannot carry slashes), so
// every control call against a relayed agent's own id answered
// AGENT_UNAVAILABLE while the agent was listed and healthy. Measured at
// 9cb2f4a: POST /api/v1/agents/eas1/exec -> 503 AGENT_UNAVAILABLE "agent eas1
// is in neither the conn-map nor the registry", and the prefixed id -> 404.
//
// Two maps, one meaning each:
//   - s.conns stays the truth for a DIRECT connection (*websocket.Conn);
//   - s.routeStore is the truth for a RELAYED one: agent id -> the relay link
//     and channel that carries it. It is written when the channel registers
//     and deleted the instant the channel (or the whole link) closes, so a
//     stale route cannot outlive its socket.
//
// resolveAgentTransport is the single resolver every control path uses. The
// predicate (agent_visibility.go) is derived from the same call, so the two
// cannot disagree — a router that routes where the predicate says it cannot is
// the two-truths defect in a new costume.
//
// Preference, and it is asserted by a test: a direct connection always wins.
// A route entry never displaces a live direct conn, and a command is written
// to exactly one of them.

import (
	"fmt"
	"sync"
	"time"
)

// Route refusal codes. Distinct from AGENT_UNAVAILABLE: the agent was known
// and its route died, which is a different fact from an id the server never
// had.
const (
	// RefusalRouteGone: a relay route for this agent existed and its channel
	// or link is gone. The command never left the server.
	RefusalRouteGone = "ROUTE_GONE"
	// RefusalRouteUpstreamNotLive: a route entry exists but its transport is
	// not live (the channel was removed from the session before the map was
	// swept, or the link is closed). Also pre-send.
	RefusalRouteUpstreamNotLive = "ROUTE_UPSTREAM_NOT_LIVE"
)

// routeTombstoneTTL bounds how long a dropped route is remembered for error
// NAMING only. It is a diagnostic ledger, never a route: the resolver cannot
// select anything from it.
const routeTombstoneTTL = 60 * time.Second

// RouteError is a named routing failure. Code is stable; the message names the
// link, the channel and the agent so an operator can see which socket died.
type RouteError struct {
	Code      string
	AgentID   string
	RelayID   string
	ChannelID uint32
	Reason    string
}

func (e *RouteError) Error() string { return e.Code + ": " + e.Reason }

// refRouteGone builds the "the route died" refusal.
func refRouteGone(agentID, relayID string, channelID uint32) error {
	return &RouteError{
		Code:      RefusalRouteGone,
		AgentID:   agentID,
		RelayID:   relayID,
		ChannelID: channelID,
		Reason: fmt.Sprintf(
			"relay route for agent %s is gone: link %s channel %d closed, nothing was sent",
			agentID, relayID, channelID),
	}
}

// refRouteNotLive builds the "entry present, transport dead" refusal.
func refRouteNotLive(agentID, relayID string, channelID uint32) error {
	return &RouteError{
		Code:      RefusalRouteUpstreamNotLive,
		AgentID:   agentID,
		RelayID:   relayID,
		ChannelID: channelID,
		Reason: fmt.Sprintf(
			"relay route for agent %s is not live: link %s channel %d is no longer open, nothing was sent",
			agentID, relayID, channelID),
	}
}

// relayRoute is ONE live relay channel, addressed by the ids the server knows
// for it. Dead is closed exactly once, when the route is removed, which is how
// an in-flight command learns its route died instead of waiting out its
// timeout.
type relayRoute struct {
	AgentID   string
	RelayID   string
	ChannelID uint32
	Conn      Conn // *virtualConn

	Dead chan struct{}
	once sync.Once
}

// closeRoute closes the death signal exactly once.
func (r *relayRoute) closeRoute() {
	r.once.Do(func() { close(r.Dead) })
}

// live reports whether the channel this route names is still open on its
// session. The map is swept on close, so this only answers for the window
// between the socket dying and the sweep running.
func (r *relayRoute) live() bool {
	vc, ok := r.Conn.(*virtualConn)
	if !ok || vc == nil || vc.session == nil || vc.session.conn == nil {
		return false
	}
	vc.session.channelsMu.RLock()
	defer vc.session.channelsMu.RUnlock()
	_, open := vc.session.channels[r.ChannelID]
	return open
}

// routeTombstone is the name of a route that is already gone. Never routable.
type routeTombstone struct {
	RelayID   string
	ChannelID uint32
	ClosedAt  time.Time
}

// routeStore holds the relay routes plus the bounded tombstone ledger.
type routeStore struct {
	mu         sync.RWMutex
	routes     map[string]*relayRoute
	tombstones map[string]routeTombstone
}

// setRelayRoute records the route for a relayed agent under every id the
// server knows it by: the registry id (relay/<link>/<name>) and the agent's
// own published name, which is the id an operator addresses. Both keys point
// at the SAME route, so there is still one truth per channel.
func (s *Server) setRelayRoute(agentID, publishedName, relayID string, channelID uint32, conn Conn) *relayRoute {
	route := &relayRoute{
		AgentID:   agentID,
		RelayID:   relayID,
		ChannelID: channelID,
		Conn:      conn,
		Dead:      make(chan struct{}),
	}
	s.routeStore.mu.Lock()
	if s.routeStore.routes == nil {
		s.routeStore.routes = make(map[string]*relayRoute)
	}
	s.routeStore.routes[agentID] = route
	if publishedName != "" && publishedName != agentID {
		// The plain name is registered only if no other channel already owns
		// it: two relays carrying agents of the same name must not silently
		// shadow each other. The prefixed id remains addressable either way.
		if existing, taken := s.routeStore.routes[publishedName]; !taken || existing == route {
			s.routeStore.routes[publishedName] = route
		}
	}
	if s.routeStore.tombstones == nil {
		s.routeStore.tombstones = make(map[string]routeTombstone)
	}
	delete(s.routeStore.tombstones, agentID)
	delete(s.routeStore.tombstones, publishedName)
	s.routeStore.mu.Unlock()
	return route
}

// relayRouteFor returns the route an id resolves to, if any.
func (s *Server) relayRouteFor(agentID string) (*relayRoute, bool) {
	s.routeStore.mu.RLock()
	defer s.routeStore.mu.RUnlock()
	r, ok := s.routeStore.routes[agentID]
	return r, ok
}

// dropRelayRoute removes every key naming this route, remembers the name for
// a bounded window so a later call fails BY NAME, and closes the death
// signal. The sweeper is called from both channel_close and link teardown.
func (s *Server) dropRelayRoute(agentID string) {
	s.routeStore.mu.Lock()
	route, ok := s.routeStore.routes[agentID]
	if !ok {
		s.routeStore.mu.Unlock()
		return
	}
	if s.routeStore.tombstones == nil {
		s.routeStore.tombstones = make(map[string]routeTombstone)
	}
	tomb := routeTombstone{RelayID: route.RelayID, ChannelID: route.ChannelID, ClosedAt: time.Now()}
	for key, r := range s.routeStore.routes {
		if r == route {
			delete(s.routeStore.routes, key)
			s.routeStore.tombstones[key] = tomb
		}
	}
	s.routeStore.mu.Unlock()
	route.closeRoute()
}

// lastRelayRoute reports the name of a route that was dropped inside the
// tombstone window. It never returns a routable entry.
func (s *Server) lastRelayRoute(agentID string) (routeTombstone, bool) {
	s.routeStore.mu.RLock()
	t, ok := s.routeStore.tombstones[agentID]
	s.routeStore.mu.RUnlock()
	if !ok {
		return routeTombstone{}, false
	}
	if time.Since(t.ClosedAt) > routeTombstoneTTL {
		s.routeStore.mu.Lock()
		delete(s.routeStore.tombstones, agentID)
		s.routeStore.mu.Unlock()
		return routeTombstone{}, false
	}
	return t, true
}

// hasRouteTombstone reports whether a route for this id died inside the
// naming window. Used to decide whether a refusal may name a route at all.
func (s *Server) hasRouteTombstone(agentID string) bool {
	_, ok := s.lastRelayRoute(agentID)
	return ok
}

// relayRouteCount is the number of live routes (tests and /health surfaces use
// it as the map's own count, not a derived one).
func (s *Server) relayRouteCount() int {
	s.routeStore.mu.RLock()
	defer s.routeStore.mu.RUnlock()
	seen := make(map[*relayRoute]struct{}, len(s.routeStore.routes))
	for _, r := range s.routeStore.routes {
		seen[r] = struct{}{}
	}
	return len(seen)
}

// resolvedTransport is what a control path needs to write one command: the
// connection, which truth selected it, the lock discipline for it, and — for a
// relayed agent — a signal that fires when the route dies mid-call.
type resolvedTransport struct {
	Conn           Conn
	Source         string
	WriteMu        *sync.Mutex // nil for a virtualConn (it locks internally)
	NeedsWriteLock bool
	Dead           <-chan struct{} // nil for a direct connection
	route          *relayRoute
}

// resolveAgentTransport is THE resolver. Every control path calls it; no
// handler resolves an agent from s.conns directly any more.
//
// Order is the preference order: a direct connection first (never a double
// send, never a relay hop for an agent the server can reach itself), the relay
// route second. A route entry that is not live is refused by name rather than
// selected and then failing on the wire.
func (s *Server) resolveAgentTransport(agentID string) (resolvedTransport, error) {
	s.mu.RLock()
	conn, ok := s.conns[agentID]
	writeMu, wmuOk := s.connWriteMu[agentID]
	s.mu.RUnlock()

	if ok {
		if vc, isVirtual := conn.(*virtualConn); isVirtual {
			// A relayed agent that is already in the conn-map keeps working
			// exactly as before; the route map is the addressable name for it.
			tr := resolvedTransport{Conn: conn, Source: LivenessSourceRelay}
			if r, found := s.relayRouteFor(agentID); found {
				tr.Dead = r.Dead
				tr.route = r
			} else if r, found := s.relayRouteFor(vc.agentID); found {
				tr.Dead = r.Dead
				tr.route = r
			}
			return tr, nil
		}
		if !wmuOk || writeMu == nil {
			return resolvedTransport{}, fmt.Errorf(
				"agent %s has a direct transport but no per-agent write mutex; dispatch would race another writer", agentID)
		}
		return resolvedTransport{
			Conn:           conn,
			Source:         LivenessSourceWS,
			WriteMu:        writeMu,
			NeedsWriteLock: true,
		}, nil
	}

	if route, found := s.relayRouteFor(agentID); found {
		if !route.live() {
			return resolvedTransport{}, refRouteNotLive(agentID, route.RelayID, route.ChannelID)
		}
		return resolvedTransport{
			Conn:   route.Conn,
			Source: LivenessSourceRelay,
			Dead:   route.Dead,
			route:  route,
		}, nil
	}

	return resolvedTransport{}, missingRouteError(agentID)
}

// missingRouteError names what is absent. The phrase "no route" is load-
// bearing: it is what the predicate reports and what a caller is told.
func missingRouteError(agentID string) error {
	return &RouteError{
		Code:    RefusalRouteGone,
		AgentID: agentID,
		Reason: fmt.Sprintf(
			"agent %s has no route: not in the direct conn-map and no relay channel carries it", agentID),
	}
}


