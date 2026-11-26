package server

// P22-06 — relay_all is settable end to end and live-togglable (measured
// defect P23-11 cell 1: POST forward-policy {"action":"relay_all"} answered
// HTTP 400 and no shipped mechanism changed a policy under a running client).
//
// Three distinct policies, enforced exactly as written:
//   - local      forward none: the agent serves on this server only.
//   - relay      forward ONLY the individually-tagged client; an untagged
//                neighbour is never forwarded by inheritance (the unset
//                policy defaults to local — GetForwardPolicy).
//   - relay_all  forward every client of this relay: a per-agent row forwards
//                that client; the -forward-all server default (see
//                SetForwardDefaultAll) makes every untagged client behave
//                relay_all. An explicit `local` row always wins.
//
// Observable on BOTH ends: the upstream server sees relay/<link>/<name> rows
// (its live registration), and this server keeps the same-name mirror record
// (relayed=true, via=<link>) next to the agent's own local row.
//
// Live toggle, no relay restart: a forwarded direct client stays registered
// locally AND keeps its socket on this server (arrival path and toggle both
// use the same link + mirror records), so
//   - toggle-on  re-registers it upstream from its registry row (the exact
//                AgentInfo frame an arrival forwards) and hooks its frame
//                mirror,
//   - toggle-off closes exactly its upstream channel (channel_close) — its
//                row leaves the upstream list, its neighbour's channel is
//                untouched — while the client's socket stays connected.
// Isolation is per agent: one toggle touches one forwardLinks entry.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// IsValidForwardAction reports whether action is one of the three enforced
// policies. Setter and toggles validate through this one place so the
// accepted set cannot drift.
func IsValidForwardAction(action string) bool {
	switch action {
	case PolicyLocal, PolicyRelay, PolicyRelayAll:
		return true
	}
	return false
}

// SetForwardDefaultAll records the -forward-all default: an UNTAGGED client
// behaves relay_all instead of local. Explicit per-agent rows always win, so
// `local` for one client still holds under it.
func (s *Server) SetForwardDefaultAll(v bool) {
	s.forwardMu.Lock()
	s.forwardAll = v
	s.forwardMu.Unlock()
}

// forwardDefaultAll reports whether the operator configured -forward-all.
func (s *Server) forwardDefaultAll() bool {
	s.forwardMu.RLock()
	defer s.forwardMu.RUnlock()
	return s.forwardAll
}

// forwardLinkedAgent returns the open toggle/arrival-owned upstream link for
// agentID, if this server currently carries it.
func (s *Server) forwardLinkedAgent(agentID string) (*ForwardedLink, bool) {
	s.forwardMu.RLock()
	defer s.forwardMu.RUnlock()
	if s.forwardLinks == nil {
		return nil, false
	}
	link, ok := s.forwardLinks[agentID]
	return link, ok
}

// noteForwardLink records the upstream link this server owns for agentID
// (created by the arrival path or by a live toggle).
func (s *Server) noteForwardLink(agentID string, link *ForwardedLink) {
	s.forwardMu.Lock()
	if s.forwardLinks == nil {
		s.forwardLinks = make(map[string]*ForwardedLink)
	}
	s.forwardLinks[agentID] = link
	s.forwardMu.Unlock()
}

// forgetForwardLink drops the owned link record.
func (s *Server) forgetForwardLink(agentID string) {
	s.forwardMu.Lock()
	delete(s.forwardLinks, agentID)
	s.forwardMu.Unlock()
}

// refuseTeardownForwardedAgent tears down a forwarded agent's local twin
// after the upstream refused the channel post-open: registry row, session,
// link record, mirror. A refusal means the upstream registration never
// happened, so nothing may keep serving as if it did. The row is REMOVED,
// not marked inactive: Unregister keeps the row listed, and a refused chain
// must register the agent nowhere.
func (s *Server) refuseTeardownForwardedAgent(agentID string) {
	s.forwardMu.RLock()
	link, live := s.forwardLinks[agentID]
	s.forwardMu.RUnlock()
	if live {
		link.Close()
		s.forgetForwardLink(agentID)
	}
	s.dropMirrored(agentID)
	s.registry.Remove(agentID)
	s.sessions.RemoveSession(agentID)
	s.ClearTokenExpiry(agentID)
}

// ToggleRelayPolicy applies a forward policy to a named client at runtime.
// Returns (applied, error): applied=false with a nil error means the state
// already held (idempotent); applied=false with an error means the live
// action could not be taken (no uplink, piped link owned by another path)
// and the policy takes effect at the client's next arrival.
func (s *Server) ToggleRelayPolicy(agentID, action string) (bool, error) {
	if !IsValidForwardAction(action) {
		return false, fmt.Errorf("action must be 'relay', 'relay_all' or 'local', got: %s", action)
	}
	switch action {
	case PolicyLocal:
		return s.closeForwardPolicy(agentID)
	default: // PolicyRelay, PolicyRelayAll
		return s.openForwardPolicy(agentID)
	}
}

// reRegisterAgentRow forwards a connected agent upstream using its registry
// row as the AgentInfo payload — exactly the frame the arrival path forwards
// as the first message, so the upstream cannot tell a toggled link from a
// fresh arrival. deliver is where upstream frames for this agent land.
func (s *Server) reRegisterAgentRow(agentID string, deliver func([]byte)) (*ForwardedLink, error) {
	f := s.uplinkForward()
	if f == nil {
		return nil, fmt.Errorf("no uplink configured: %s cannot take effect on a chain root", PolicyRelayAll)
	}
	rec, err := s.registry.GetHealth(agentID)
	if err != nil {
		return nil, fmt.Errorf("agent %s is not in the registry: %w", agentID, err)
	}
	first, err := json.Marshal(protocol.Envelope{
		Type: "agent_info",
	 ID:   "fwd-toggle-" + nowRFC3339(),
		Result: json.RawMessage(mustMarshalRaw(protocol.AgentInfo{
			Name:         rec.Name,
			Version:      rec.Version,
			OS:           rec.OS,
			Arch:         rec.Arch,
			Mode:         rec.Mode,
			Capabilities: rec.Capabilities,
		})),
	})
	if err != nil {
		return nil, fmt.Errorf("agent %s: rebuild agent_info: %w", agentID, err)
	}
	link, err := f.ForwardAgent(ForwardRegistration{AgentID: agentID, Chain: Chain{}, Deliver: deliver}, first)
	if err != nil {
		return nil, err
	}
	s.markMirrored(MirroredAgent{
		AgentID:      agentID,
		Via:          link.Via,
		Relayed:      true,
		Policy:       PolicyRelayAll,
		UpstreamLive: true,
		ForwardedAt:  nowRFC3339(),
	})
	s.noteForwardLink(agentID, link)
	log.Printf("[forward] agent %s re-registered upstream via %s (policy toggle)", agentID, link.Via)
	return link, nil
}

// openForwardPolicy turns forwarding ON under a live client: it re-registers
// the agent upstream right now, without waiting for the client to reconnect.
// The client stays registered and serving locally; its frames mirror upstream
// from the handleMessages hook from now on.
// Returns applied=false (nil error) when the agent is already forwarded —
// idempotent, not a failure.
func (s *Server) openForwardPolicy(agentID string) (bool, error) {
	if _, live := s.forwardLinkedAgent(agentID); live {
		return false, nil
	}
	if rec, ok := s.mirroredAgent(agentID); ok && rec.UpstreamLive {
		// An arrival-time forward owns the live link; re-registering would
		// double-register the agent upstream.
		return false, nil
	}
	deliver := func(p []byte) {
		tr, terr := s.resolveAgentTransport(agentID)
		if terr != nil {
			return
		}
		if tr.NeedsWriteLock && tr.WriteMu != nil {
			tr.WriteMu.Lock()
			defer tr.WriteMu.Unlock()
		}
		_ = tr.Conn.WriteMessage(websocket.TextMessage, p)
	}
	if _, err := s.reRegisterAgentRow(agentID, deliver); err != nil {
		return false, err
	}
	return true, nil
}

// closeForwardPolicy turns forwarding OFF under a live client: the upstream
// channel closes (its registration and the mirror end with it) while the
// client's local serving continues — its row and socket are untouched.
// Returns applied=false when the agent was not forwarded through a link this
// server's map owns.
func (s *Server) closeForwardPolicy(agentID string) (bool, error) {
	link, live := s.forwardLinkedAgent(agentID)
	if !live {
		if mirror, ok := s.mirroredAgent(agentID); ok && mirror.UpstreamLive {
			return false, fmt.Errorf("agent %s is forwarded on a piped link; policy applies to new arrivals", agentID)
		}
		return false, nil
	}
	link.Close()
	s.forgetForwardLink(agentID)
	s.dropMirrored(agentID)
	log.Printf("[forward] agent %s pulled back local (policy toggle)", agentID)
	return true, nil
}

// handleV1ForwardPolicySet validates and applies one POST /forward-policy
// body. The policy row is persisted BEFORE the live attempt (a chain root or
// a foreign piped link reports applied=false with a note, and the policy
// takes effect at the client's next arrival); only an invalid action is a
// 400. GET reads the same row back through GetForwardPolicy.
func (s *Server) handleV1ForwardPolicySet(w http.ResponseWriter, agentID, action string) {
	if !IsValidForwardAction(action) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":    false,
			"agent": agentID,
			"error": fmt.Sprintf("action must be 'relay', 'relay_all' or 'local', got: %s", action),
		})
		return
	}
	s.SetForwardPolicy(agentID, action)
	applied, terr := s.ToggleRelayPolicy(agentID, action)
	if terr != nil {
		log.Printf("[forward] policy %s for %s persisted; live apply deferred: %v", action, agentID, terr)
	}
	note := ""
	switch {
	case terr != nil:
		note = "policy persisted; takes effect at next arrival: " + terr.Error()
	case applied:
		note = "applied live: agent state updated without restart"
	case action == PolicyLocal:
		note = "already local"
	default:
		note = "already forwarded"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true, "agent": agentID, "policy": action, "applied": applied, "note": note,
	})
}
