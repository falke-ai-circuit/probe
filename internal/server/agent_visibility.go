package server

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

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
// read as healthy here.
func (s *Server) decorateAgentLiveness(rec *AgentRecord) {
	s.mu.RLock()
	_, connected := s.conns[rec.AgentID]
	s.mu.RUnlock()
	if !connected {
		rec.Stale = true
		rec.StaleReason = "no_transport"
	}
}

// decorateAgentListLiveness applies decorateAgentLiveness to a slice, one
// RLock for the whole list instead of one per row.
func (s *Server) decorateAgentListLiveness(recs []AgentRecord) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := range recs {
		if _, ok := s.conns[recs[i].AgentID]; !ok {
			recs[i].Stale = true
			recs[i].StaleReason = "no_transport"
		}
	}
}
