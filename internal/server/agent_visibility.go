package server

// P15-12 port to the 1.19.2 stable line (R0 cut, protocol patch discipline).
//
// The captured production hang (528-goroutine dump
// /opt/data/tmp/probe119/P15-11_GOROUTINE.log) showed cleanupRelaySession
// holding a session's channelsMu (write) while waiting on s.relayMu, and
// relayChannelOwner holding s.relayMu (read) while waiting on that same
// session's channelsMu: a closed ABBA cycle that parked 251 health readers
// for 145 minutes.
//
// Unit rule: RELAY-FIRST. Never acquire a session's channelsMu while holding
// relayMu, and never hold channelsMu across a relayMu acquisition. Every site
// that touches both obeys it by snapshot-and-release.
//
// This port carries ONLY the lock-order fix surface from upstream 60c5950.
// The federation-side caller wiring (dispatchRefusal, resolveAgentTransport,
// routeStore, /api/v1/servers rows) belongs to the R1 federation line and is
// deliberately absent here: a patch release adds zero new surface
// (docs/RELEASE-PROTOCOL.md §1). The stable line already routes dispatches
// from its conn-map; the relay-scan branch below answers the same question
// under the corrected lock order.

import (
	"fmt"
	"time"
)

// LivenessSourceWS: a direct WebSocket is in the conn-map.
const LivenessSourceWS = "ws"

// LivenessSourceRelay: the transport is a relayed virtual channel.
const LivenessSourceRelay = "relay"

// LivenessSourceRegistryOnly: the registry knows the agent, no live
// channel does. This is the state the old surface reported as green.
const LivenessSourceRegistryOnly = "registry-only"

// AgentDispatchability answers "can this agent be dispatched right now, and
// how do we know": (dispatchable, source, reason). reason is empty exactly
// when dispatchable is true.
//
// P15-12 LOCK ORDER (relay-first), binding on every site that touches both
// relayMu and a session's channelsMu:
//   - never acquire a session's channelsMu while holding relayMu;
//   - never hold channelsMu across a relayMu acquisition.
//
// Sites obey it by snapshot-and-release: take relayMu, copy out what is
// needed (session pointers, ids), release, then read channelsMu per session.
func (s *Server) AgentDispatchability(agentID string) (bool, string, string) {
	s.mu.RLock()
	_, direct := s.conns[agentID]
	_, hasWriteMu := s.connWriteMu[agentID]
	s.mu.RUnlock()
	if direct {
		if hasWriteMu {
			return true, LivenessSourceWS, ""
		}
		// A transport with no write mutex cannot be framed safely.
		return false, LivenessSourceRegistryOnly,
			"agent has a transport but no per-agent write mutex; dispatch would race another writer"
	}

	// P15-12: relay-first order — snapshot the session set under relayMu,
	// release it, then read per-session channels under channelsMu. Holding
	// relayMu across channelsMu (the pre-fix shape) is the half of the ABBA
	// cycle that parked the health queue for 145 minutes.
	s.relayMu.RLock()
	sessions := make([]*relaySession, 0, len(s.relays))
	for _, sess := range s.relays {
		sessions = append(sessions, sess)
	}
	s.relayMu.RUnlock()
	for _, sess := range sessions {
		found := false
		sess.channelsMu.RLock()
		for _, vc := range sess.channels {
			if vc.agentID == agentID {
				found = true
				break
			}
		}
		sess.channelsMu.RUnlock()
		if found {
			// A live relayed channel carries the agent's frames; on the
			// stable line the dispatch path resolves the virtual conn from
			// the conn-map, which the direct branch above already checked.
			// Answering dispatchable here names the true transport.
			return true, LivenessSourceRelay, ""
		}
	}

	if s.registry == nil {
		// A Server without a registry has no second truth to fall back to:
		// the conn-map and the relay sessions are the whole answer.
		return false, LivenessSourceRegistryOnly,
			fmt.Sprintf("agent %s has no route: not in the direct conn-map and no relay channel carries it (this server has no registry)", agentID)
	}
	rec, err := s.registry.GetHealth(agentID)
	if err != nil {
		return false, LivenessSourceRegistryOnly,
			fmt.Sprintf("agent %s has no route: not in the direct conn-map and no relay channel carries it, and it is not in the registry", agentID)
	}
	return false, LivenessSourceRegistryOnly,
		fmt.Sprintf("agent %s has no route: not in the direct conn-map and no relay channel carries it; registered (status=%s), no live ws connection, %s",
			agentID, rec.Status, humanizeAge(heartbeatsAgo(rec)))
}

// heartbeatsAgo reports seconds since the record's last heartbeat (0 when it
// has never beaten but was just registered). The stable AgentRecord keeps the
// heartbeat as a time.Time only, so the age is derived here.
func heartbeatsAgo(rec AgentRecord) int64 {
	if rec.lastHeartbeat.IsZero() {
		return 0
	}
	age := time.Since(rec.lastHeartbeat).Seconds()
	if age < 0 {
		return 0
	}
	return int64(age)
}

// humanizeAge renders a second count as the coarsest unit that still reads
// truly: 4m, 3h, 2d.
func humanizeAge(seconds int64) string {
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/3600)
	case seconds < 86400:
		return fmt.Sprintf("%dh", seconds/3600)
	default:
		return fmt.Sprintf("%dd", seconds/86400)
	}
}
