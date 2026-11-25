package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P21-01 — one liveness truth.
//
// P12-03 made the health row honest about the transport. It did not give the
// question a single home: /api/v1/agents built its own answer from the
// registry, and every refusal site built its own from the conn-map, so the two
// could disagree indefinitely (active + health 0.95 on the list, 503
// AGENT_UNAVAILABLE on exec). AgentDispatchability below is the one answer.
// The list handler and every refusal site derive from it; nothing else may
// restate "is this agent dispatchable" from first principles.
const (
	// LivenessSourceWS: a direct WebSocket is in the conn-map.
	LivenessSourceWS = "ws"
	// LivenessSourceRelay: the transport is a relayed virtual channel.
	LivenessSourceRelay = "relay"
	// LivenessSourceRegistryOnly: the registry knows the agent, no live
	// channel does. This is the state the old surface reported as green.
	LivenessSourceRegistryOnly = "registry-only"
)

// AgentDispatchability answers "can this agent be dispatched right now, and how
// do we know": (dispatchable, source, reason). reason is empty exactly when
// dispatchable is true; otherwise it is the sentence a client shows instead of
// a generic refusal.
//
// P21-05: the verdict comes from resolveAgentTransport — the same call every
// control path makes — so the predicate and the router cannot disagree. A
// route that exists is dispatchable; a route that died is refused BY NAME; an
// id with neither path is a registry-only answer that says no route exists.
func (s *Server) AgentDispatchability(agentID string) (bool, string, string) {
	if tr, err := s.resolveAgentTransport(agentID); err == nil {
		return true, tr.Source, ""
	} else if route, found := s.relayRouteFor(agentID); found {
		// A route entry is present but its transport is not live.
		return false, LivenessSourceRelay, refRouteNotLive(agentID, route.RelayID, route.ChannelID).Error()
	} else {
		if tomb, ok := s.lastRelayRoute(agentID); ok {
			// The route is gone (swept with its socket). The answer names the
			// link and channel that closed rather than pretending the agent
			// never existed.
			return false, LivenessSourceRelay, refRouteGone(agentID, tomb.RelayID, tomb.ChannelID).Error()
		}
		if relayID, found := s.relayChannelOwner(agentID); found {
			// A channel exists but its registration never wrote a route, so
			// nothing may be dispatched from it. Naming the holder is the
			// honest middle answer: not a green row, not a silent absence.
			return false, LivenessSourceRelay,
				fmt.Sprintf("relay %s holds a channel for agent %s but no route entry was written for it; nothing can be dispatched", relayID, agentID)
		}
		if _, direct := s.conns[agentID]; direct {
			// A transport with no write mutex cannot be framed safely.
			return false, LivenessSourceRegistryOnly,
				"agent has a transport but no per-agent write mutex; dispatch would race another writer"
		}
	}

	if s.registry == nil {
		// A Server without a registry has no second truth to fall back to:
		// the conn-map and the route map are the whole answer, and both said no.
		return false, LivenessSourceRegistryOnly,
			fmt.Sprintf("agent %s has no route: not in the direct conn-map and no relay channel carries it (this server has no registry)", agentID)
	}
	rec, err := s.registry.GetHealth(agentID)
	if err != nil {
		return false, LivenessSourceRegistryOnly,
			fmt.Sprintf("agent %s has no route: not in the direct conn-map and no relay channel carries it, and it is not in the registry", agentID)
	}
	return false, LivenessSourceRegistryOnly,
		fmt.Sprintf("agent %s has no route: not in the direct conn-map and no relay channel carries it; %s", agentID, registryOnlyReason(rec))
}

// relayChannelOwner reports which connected relay holds a channel for agentID.
// The conn-map is authoritative; this only names a channel that exists before
// the agent's own registration completed, so the reason can point at it.
func (s *Server) relayChannelOwner(agentID string) (string, bool) {
	s.relayMu.RLock()
	defer s.relayMu.RUnlock()
	for id, sess := range s.relays {
		sess.channelsMu.RLock()
		for _, vc := range sess.channels {
			if vc.agentID == agentID {
				sess.channelsMu.RUnlock()
				return id, true
			}
		}
		sess.channelsMu.RUnlock()
	}
	return "", false
}

// registryOnlyReason is the sentence for "the registry knows it, no live
// channel does" — the disagreement the operator used to see as a green row.
func registryOnlyReason(rec AgentRecord) string {
	seen := "never heard from"
	if rec.LastSeenAgeSeconds >= 0 {
		seen = "last seen " + humanizeAge(rec.LastSeenAgeSeconds) + " ago"
	}
	return fmt.Sprintf("registered (status=%s), no live ws connection, %s", rec.Status, seen)
}

// humanizeAge renders a second count as the coarsest unit that still reads
// truly: 4m, 3h, 2d.
func humanizeAge(seconds int64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dd", seconds/86400)
	}
}

// DispatchRefusal is the honest answer to "why did this command not reach the
// agent". Status/Code/Message are the HTTP surface; Detail carries the
// predicate's own verdict so a caller can see which truth it was told.
type DispatchRefusal struct {
	Status  int
	Code    string
	Message string
	Detail  map[string]interface{}
}

// dispatchRefusal derives the refusal for a failed dispatch from the predicate.
//
// When the predicate says the agent IS dispatchable, the command was not
// refused for absence: whatever failed is the agent's own error or the link's,
// and it is passed through as such (502 AGENT_ERROR). Answering
// AGENT_UNAVAILABLE there is the lie this unit removes.
func (s *Server) dispatchRefusal(agentID, call string, err error) DispatchRefusal {
	detail := map[string]interface{}{
		"call":        call,
		"agent_id":    agentID,
	}
	// P21-05: a routing failure names its code and its link. An id that never
	// had a route is NOT this case — it stays the predicate's absence answer.
	var re *RouteError
	if errors.As(err, &re) && (re.RelayID != "" || s.hasRouteTombstone(agentID)) {
		detail["route_code"] = re.Code
		detail["relay_id"] = re.RelayID
		detail["channel_id"] = re.ChannelID
		if err != nil {
			detail["error"] = err.Error()
		}
		return DispatchRefusal{
			Status:  http.StatusServiceUnavailable,
			Code:    re.Code,
			Message: re.Reason,
			Detail:  detail,
		}
	}
	dispatchable, source, reason := s.AgentDispatchability(agentID)
	detail["dispatchable"] = dispatchable
	detail["liveness_source"] = source
	if err != nil {
		detail["error"] = err.Error()
	}
	if dispatchable {
		msg := "dispatch failed on a live agent"
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			msg = err.Error()
		}
		return DispatchRefusal{
			Status:  http.StatusBadGateway,
			Code:    "AGENT_ERROR",
			Message: msg,
			Detail:  detail,
		}
	}
	// The route was known and died: answer by name, not as an absence.
	if tomb, ok := s.lastRelayRoute(agentID); ok {
		gone := refRouteGone(agentID, tomb.RelayID, tomb.ChannelID)
		detail["route_code"] = RefusalRouteGone
		detail["relay_id"] = tomb.RelayID
		detail["channel_id"] = tomb.ChannelID
		return DispatchRefusal{
			Status:  http.StatusServiceUnavailable,
			Code:    RefusalRouteGone,
			Message: gone.Error(),
			Detail:  detail,
		}
	}
	return DispatchRefusal{
		Status:  http.StatusServiceUnavailable,
		Code:    "AGENT_UNAVAILABLE",
		Message: reason,
		Detail:  detail,
	}
}

// writeDispatchRefusal answers a command that never reached the agent with the
// predicate's reason instead of a generic sentence.
func (s *Server) writeDispatchRefusal(w http.ResponseWriter, agentID, call string, err error) {
	d := s.dispatchRefusal(agentID, call, err)
	writeErrorDetail(w, d.Status, d.Code, d.Message, d.Detail)
}

// P12-03 — agent-state visibility.
//
// The defect this file closes: /api/v1/health answered "ok" while exec and
// fs-write answered 503 for the same agent. Health described the server's own
// bookkeeping instead of asking the agent whether it was alive.
//
// Two mechanisms:
//
//  1. decorateAgentLiveness — every per-agent health response is stamped with
//     the transport truth the execution paths actually use (s.conns). If the
//     exec path would answer 503 "not connected", the health row says
//     stale:true, stale_reason:"no_transport". One source, no disagreement.
//  2. The ping probe — the server pings every connected agent on an interval
//     and counts the ones that never answer. A wedged agent keeps its TCP
//     socket open, so transport presence alone is not proof of life; a pong
//     (or its absence) is. Misses are a monotonic per-agent counter
//     (ping_timeouts), not a log line.

var pingProbeOnce sync.Once

// StartAgentPingProbe starts the background ping probe. Safe to call more
// than once. Started by Server.Start/StartTLS so tests that build a mux
// directly never run it.
func (s *Server) StartAgentPingProbe() {
	pingProbeOnce.Do(func() {
		s.pingProbeInterval = pingProbeInterval
		s.pingProbeTimeout = pingProbeTimeout
		go s.runAgentPingProbe()
	})
}

// runAgentPingProbe pings every connected agent on the configured interval.
func (s *Server) runAgentPingProbe() {
	interval := s.pingProbeInterval
	if interval <= 0 {
		interval = pingProbeInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		s.probeConnectedAgents()
	}
}

// probeConnectedAgents pings each agent that has an open transport, one
// goroutine per agent so a slow agent cannot delay the others.
func (s *Server) probeConnectedAgents() {
	s.mu.RLock()
	ids := make([]string, 0, len(s.conns))
	for id := range s.conns {
		ids = append(ids, id)
	}
	s.mu.RUnlock()
	for _, id := range ids {
		go func(agentID string) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[ping-probe] %s: recovered from panic: %v", agentID, r)
				}
			}()
			if err := s.pingAgent(agentID); err != nil {
				s.registry.RecordPingTimeout(agentID)
				log.Printf("[ping-probe] %s: no pong: %v", agentID, err)
				return
			}
			s.registry.RecordPong(agentID)
		}(id)
	}
}

// pingAgent sends one protocol ping to an agent and waits for the matching
// pong. The pong is routed to this channel by the WebSocket read loops
// (server_ws.go / relay_ws.go) through pendingReqs, keyed on the ping id the
// agent echoes back — no wire change, the agent already answers TypePing.
func (s *Server) pingAgent(agentID string) error {
	timeout := s.pingProbeTimeout
	if timeout <= 0 {
		timeout = pingProbeTimeout
	}
	s.mu.RLock()
	conn, ok := s.conns[agentID]
	writeMu, wmuOk := s.connWriteMu[agentID]
	s.mu.RUnlock()
	if !ok || !wmuOk {
		return fmt.Errorf("agent %s not connected", agentID)
	}

	reqID := fmt.Sprintf("probe-ping-%d", time.Now().UnixNano())
	respCh := make(chan protocol.Envelope, 1)
	s.pendingMu.Lock()
	s.pendingReqs[reqID] = respCh
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pendingReqs, reqID)
		s.pendingMu.Unlock()
	}()

	// Same per-connection write discipline as forwardToAgent: relayed agents
	// (virtualConn) lock internally, direct connections are locked here.
	if _, isVirtual := conn.(*virtualConn); !isVirtual {
		writeMu.Lock()
	}
	err := conn.WriteJSON(protocol.Envelope{ID: reqID, Type: protocol.TypePing})
	if _, isVirtual := conn.(*virtualConn); !isVirtual {
		writeMu.Unlock()
	}
	if err != nil {
		return fmt.Errorf("send ping failed: %w", err)
	}

	select {
	case <-respCh:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("agent did not answer ping within %v", timeout)
	}
}

// pongToPending hands a pong to the waiting ping probe, if one is waiting.
// Returns true when the pong was consumed. Callers still record the pong as
// proof of life regardless.
func (s *Server) pongToPending(env protocol.Envelope) bool {
	s.pendingMu.Lock()
	ch, ok := s.pendingReqs[env.ID]
	if ok {
		delete(s.pendingReqs, env.ID)
	}
	s.pendingMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- env:
	default:
	}
	return true
}

// decorateAgentLiveness stamps a health snapshot with the transport truth the
// execution paths use. A row the exec path would refuse with 503 must never
// read as healthy here, and the row now says which truth it rests on.
func (s *Server) decorateAgentLiveness(rec *AgentRecord) {
	dispatchable, source, reason := s.AgentDispatchability(rec.AgentID)
	rec.Dispatchable = dispatchable
	rec.LivenessSource = source
	rec.DispatchError = reason
	if !dispatchable {
		rec.Stale = true
		rec.StaleReason = "no_transport"
	}
}

// decorateAgentListLiveness applies decorateAgentLiveness to a slice. It does
// not hold s.mu across the call: the predicate takes the conn-map lock itself,
// and Go mutexes are not reentrant.
func (s *Server) decorateAgentListLiveness(recs []AgentRecord) {
	for i := range recs {
		s.decorateAgentLiveness(&recs[i])
	}
}
