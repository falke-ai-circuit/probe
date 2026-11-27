package server

// P21-04 — forward_policy is enforced, not merely stored.
//
// The policy surface existed since Step 11 (protocol.TypeForwardPolicy and
// POST/GET /api/v1/agents/{id}/forward-policy) but routing ignored it: every
// arriving client was registered on whichever server it reached. This file is
// the decision layer that runs BEFORE registration, plus the guards the
// forwarding path needs and the read-only mirror a downstream server keeps so
// its own UI can still show a forwarded agent as `relayed`.
//
// Three policies, one meaning each:
//   - local      register on the server the client reached. Nothing is
//                forwarded, even when an uplink exists.
//   - relay      the client registers UPSTREAM; this server keeps a read-only
//                mirror record (relayed=true, via=<forwarder id>).
//   - relay_all  as relay, and recursion is allowed to continue up the chain
//                until a server with `local` or a chain root (no uplink).
//
// Two properties are load-bearing and are why the guards ship here:
//   - a chain root (no uplink configured) TERMINATES the policy: relay and
//     relay_all register locally there. Without that rule the default policy
//     would refuse every agent on a standalone server.
//   - refusals are named. "forwarding failed" is the defect this unit removes;
//     every refusal carries one of the codes below and its own error string.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// PolicyLocal, PolicyRelay and PolicyRelayAll are the three enforcement
	// policies. The zero/unknown value is never treated as a policy.
	PolicyLocal    = "local"
	PolicyRelay    = "relay"
	PolicyRelayAll = "relay_all"

	// DefaultLinkMaxAgents is the relay default a per-link limit falls back
	// to when none is configured (internal/relay/relay.go:57).
	DefaultLinkMaxAgents = 100

	// MaxForwardHops bounds a chain even when no id repeats: a hop counter is
	// a backstop for a chain that grows without a cycle (misconfiguration,
	// relay spam), not a substitute for the traversed set.
	MaxForwardHops = 8
)

// Refusal codes for the forwarding path. Distinct per cause so a caller can
// tell policy denial from a limit, a loop, or an allowlist miss.
const (
	RefusalPolicyDenied  = "FORWARD_POLICY_DENIED"
	RefusalLinkMaxAgents = "FORWARD_MAX_AGENTS"
	RefusalLoop          = "FORWARD_LOOP"
	RefusalAllowlist     = "FORWARD_ALLOWLIST_DENIED"
	RefusalMirrorStale   = "FORWARD_MIRROR_HAS_NO_LIVE_UPSTREAM"
)

// ForwardRefusalError is a named refusal from the forwarding path. Code is
// stable; Error() is the human string quoted in tests and logs.
type ForwardRefusalError struct {
	Code    string
	AgentID string
	Reason  string
}

func (e *ForwardRefusalError) Error() string { return e.Code + ": " + e.Reason }

// refPolicyDenied builds the policy refusal.
func refPolicyDenied(agentID, policy string) error {
	return &ForwardRefusalError{
		Code:    RefusalPolicyDenied,
		AgentID: agentID,
		Reason:  fmt.Sprintf("policy denies forwarding for agent %s (policy=%s)", agentID, policy),
	}
}

// refLinkMaxAgents builds the per-link limit refusal. It names the limit: an
// operator must be able to raise it without reading the source.
func refLinkMaxAgents(linkID string, limit int) error {
	return &ForwardRefusalError{
		Code:   RefusalLinkMaxAgents,
		Reason: fmt.Sprintf("max_agents limit %d reached on link %s", limit, linkID),
	}
}

// refLoop builds the loop refusal for an id already present in the chain.
func refLoop(relayID string, chain Chain) error {
	return &ForwardRefusalError{
		Code:   RefusalLoop,
		Reason: fmt.Sprintf("loop refused — relay %s already in the uplink chain %v", relayID, chain.Traversed),
	}
}

// refAllowlist builds the allowlist refusal.
func refAllowlist(relayID string) error {
	return &ForwardRefusalError{
		Code:   RefusalAllowlist,
		Reason: fmt.Sprintf("relay %s is not in the downstream allowlist", relayID),
	}
}

// refMirrorStale builds the read-only mirror refusal: a write or exec against
// a mirrored record whose upstream is gone must fail, never answer from the
// stale copy.
func refMirrorStale(agentID, via string) error {
	return &ForwardRefusalError{
		Code:    RefusalMirrorStale,
		AgentID: agentID,
		Reason:  fmt.Sprintf("mirror is read-only: agent %s relayed via %s has no live upstream", agentID, via),
	}
}

// Chain is the per-hop forwarding record: a hop counter plus the set of relay
// ids the request has already traversed. It is mutated per hop (Next) and
// carried on the wire so the receiving server can refuse a cycle.
type Chain struct {
	Hops      int      `json:"hops"`
	Traversed []string `json:"traversed,omitempty"`
}

// Has reports whether id already appears in the chain.
func (c Chain) Has(id string) bool {
	if id == "" {
		return false
	}
	for _, t := range c.Traversed {
		if t == id {
			return true
		}
	}
	return false
}

// Next returns the chain after one hop through relayID, or a loop refusal if
// that id is already present or the hop budget is spent.
func (c Chain) Next(relayID string) (Chain, error) {
	if c.Has(relayID) {
		return c, refLoop(relayID, c)
	}
	if c.Hops+1 > MaxForwardHops {
		return c, &ForwardRefusalError{
			Code:   RefusalLoop,
			Reason: fmt.Sprintf("hop budget %d exhausted at relay %s (chain %v)", MaxForwardHops, relayID, c.Traversed),
		}
	}
	next := Chain{
		Hops:      c.Hops + 1,
		Traversed: append(append([]string(nil), c.Traversed...), relayID),
	}
	return next, nil
}

// ForwardDecision is the outcome of applying the policy to one arrival.
// Forward=false always carries a Reason: "registered locally" is a decision,
// not an absence of one.
type ForwardDecision struct {
	Forward bool
	Policy  string
	Reason  string
}

// DecideForward applies the policy. ownID is this server's identity in the
// chain; linkID is the forwarding link (this server as an upstream client).
//
// Rules, in order:
//  1. no uplink -> chain root -> register locally (relay/relay_all terminate)
//  2. policy local -> register locally
//  3. own id already in the chain -> loop refusal (never forward into
//     ourselves)
//  4. relay / relay_all -> forward
func DecideForward(agentID, policy, ownID, linkID string, uplinkConfigured bool, chain Chain) (ForwardDecision, error) {
	if policy == "" {
		// GetForwardPolicy's documented default is relay: to be explicit
		// about recursion, an unset policy means relay_all semantics.
		policy = PolicyRelayAll
	}
	switch policy {
	case PolicyLocal, PolicyRelay, PolicyRelayAll:
	default:
		return ForwardDecision{Policy: policy}, refPolicyDenied(agentID, policy)
	}
	if !uplinkConfigured {
		return ForwardDecision{Policy: policy, Reason: "chain root: no uplink configured, registered locally"}, nil
	}
	if policy == PolicyLocal {
		return ForwardDecision{Policy: policy, Reason: "policy local: not forwarded"}, nil
	}
	if chain.Has(ownID) {
		return ForwardDecision{Policy: policy}, refLoop(ownID, chain)
	}
	if _, err := chain.Next(linkID); err != nil {
		return ForwardDecision{Policy: policy}, err
	}
	return ForwardDecision{Forward: true, Policy: policy, Reason: "policy " + policy + ": forwarded upstream"}, nil
}

// RelayAllowlist is the P7-owned allowlist mechanics behind one call. This
// unit must CALL it and refuse on a miss; it does not own its storage.
type RelayAllowlist interface {
	// Allows reports whether the relay identity may forward downstream
	// clients into this server. fingerprint may be empty for deployments
	// that key on id only.
	Allows(relayID, fingerprint string) bool
}

// MirroredAgent is the downstream server's read-only record of an agent it
// forwarded upstream. It is a mirror, not a registration: it never carries a
// write path and never answers a command.
type MirroredAgent struct {
	AgentID       string `json:"agent_id"`
	Via           string `json:"via"`
	Relayed       bool   `json:"relayed"`
	Policy        string `json:"policy,omitempty"`
	UpstreamLive  bool   `json:"upstream_live"`
	ForwardedAt   string `json:"forwarded_at,omitempty"`
	ChainTraversed []string `json:"chain_traversed,omitempty"`
}

// mirrorStore is the guarded map behind the mirror. A separate mutex keeps
// the mirror off the hot s.mu path.
type mirrorStore struct {
	mu      sync.RWMutex
	records map[string]MirroredAgent
}

// markMirrored records (or refreshes) a mirrored agent.
func (s *Server) markMirrored(rec MirroredAgent) {
	if rec.AgentID == "" {
		return
	}
	rec.Relayed = true
	s.mirror.mu.Lock()
	if s.mirror.records == nil {
		s.mirror.records = make(map[string]MirroredAgent)
	}
	s.mirror.records[rec.AgentID] = rec
	s.mirror.mu.Unlock()
}

// setMirrorUpstreamLive flips the liveness bit of a mirrored record. When a
// forwarder loses its upstream the mirror stays visible but stale.
func (s *Server) setMirrorUpstreamLive(via string, live bool) {
	s.mirror.mu.Lock()
	for id, rec := range s.mirror.records {
		if rec.Via == via {
			rec.UpstreamLive = live
			s.mirror.records[id] = rec
		}
	}
	s.mirror.mu.Unlock()
}

// dropMirrored removes a mirror record when its forwarder is gone.
func (s *Server) dropMirrored(agentID string) {
	s.mirror.mu.Lock()
	delete(s.mirror.records, agentID)
	s.mirror.mu.Unlock()
}

// mirroredAgent returns a copy of one record.
func (s *Server) mirroredAgent(agentID string) (MirroredAgent, bool) {
	s.mirror.mu.RLock()
	defer s.mirror.mu.RUnlock()
	rec, ok := s.mirror.records[agentID]
	return rec, ok
}

// MirroredAgents returns the mirror, ordered by agent id so callers and tests
// get a stable document.
func (s *Server) MirroredAgents() []MirroredAgent {
	s.mirror.mu.RLock()
	out := make([]MirroredAgent, 0, len(s.mirror.records))
	for _, rec := range s.mirror.records {
		out = append(out, rec)
	}
	s.mirror.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].AgentID < out[j].AgentID })
	return out
}

// MirrorWriteGuard is the read-only-by-construction check. Any write or exec
// aimed at a mirrored record is refused unless its upstream is live — and even
// then the write goes upstream, never to the mirror (the mirror has no write
// path). Returns nil for agents that are not mirrored.
func (s *Server) MirrorWriteGuard(agentID string) error {
	rec, ok := s.mirroredAgent(agentID)
	if !ok {
		return nil
	}
	if !rec.UpstreamLive {
		return refMirrorStale(agentID, rec.Via)
	}
	return &ForwardRefusalError{
		Code:    RefusalMirrorStale,
		AgentID: agentID,
		Reason:  fmt.Sprintf("mirror is read-only: agent %s is mirrored via %s; writes go upstream, not to the mirror", agentID, rec.Via),
	}
}

// serverChainID is this server's identity as a chain hop. It mirrors the id
// peerIdentitySnapshot publishes, so a peer's chain names what peers see.
func (s *Server) serverChainID() string {
	s.uplinkMu.RLock()
	id := s.serverID
	s.uplinkMu.RUnlock()
	if id != "" {
		return id
	}
	host, _ := os.Hostname()
	return "server-" + host
}

// forwardLinkID is the id this server uses when it forwards upstream. A
// forwarder may override it (SetForwarderID); the default is derived so two
// servers on one host never share an id.
func (s *Server) forwardLinkID() string {
	s.forwardMu.RLock()
	defer s.forwardMu.RUnlock()
	return s.forwardLinkIDLocked()
}

// forwardLinkIDLocked is forwardLinkID for callers already holding forwardMu.
func (s *Server) forwardLinkIDLocked() string {
	if s.forwarderID != "" {
		return s.forwarderID
	}
	return "fwd-" + s.serverChainID()
}

// uplinkForwardTarget returns the currently configured forward target
// (P25-05R): the address/DTO pair adoption compares against so re-adopting
// the same uplink is a no-op rather than a forwarder reset.
func (s *Server) uplinkForwardTarget() (addr, token string) {
	s.forwardMu.RLock()
	defer s.forwardMu.RUnlock()
	return s.forwardURL, s.forwardToken
}

// SetUplinkForwardTarget points this server at the upstream it forwards
// clients into. An empty address means no uplink: relay/relay_all terminate
// here as a chain root, which is also the default for every standalone
// deployment.
func (s *Server) SetUplinkForwardTarget(addr, token string) {
	s.forwardMu.Lock()
	s.forwardURL = strings.TrimSpace(addr)
	s.forwardToken = token
	f := s.forwarder
	s.forwardMu.Unlock()
	if f != nil {
		// A target change is a new link: stop the old one so the next
		// forward dials the configured address rather than the stale one.
		go f.Close()
	}
}

// uplinkForward returns the forwarder, creating it on first use. nil means
// this server has no uplink and is therefore a chain root.
func (s *Server) uplinkForward() *UplinkForwarder {
	s.forwardMu.RLock()
	f, addr := s.forwarder, s.forwardURL
	s.forwardMu.RUnlock()
	if f != nil || addr == "" {
		return f
	}
	s.forwardMu.Lock()
	defer s.forwardMu.Unlock()
	if s.forwarder == nil && s.forwardURL != "" {
		nf := NewUplinkForwarder(s.forwardURL, s.forwardToken, s.forwardLinkIDLocked())
		nf.SetLossCallback(func() { s.setMirrorUpstreamLive(nf.LinkID(), false) })
		s.forwarder = nf
	}
	return s.forwarder
}

// forwardArrival applies the policy to one arriving client and, when the
// policy says forward, registers it upstream and returns the live link. A nil
// link with a nil error means "registered locally" — a decision, not a
// failure. first is the client's first payload (its AgentInfo envelope), which
// is also the upstream's registration signal on the new channel.
func (s *Server) forwardArrival(agentID string, chain Chain, deliver func([]byte), first []byte) (*ForwardedLink, error) {
	// P22-06: the policy resolver is forwardPolicyFor — the suffix-aware
	// lookup that maps a relayed row (relay/<link>/<name>) onto the
	// per-name policy row, and honours the -forward-all default. Deciding
	// from GetForwardPolicy here would lose the suffix and strand every
	// relayed client behind the untagged default.
	policy := s.forwardPolicyFor(agentID)
	f := s.uplinkForward()
	dec, err := DecideForward(agentID, policy, s.serverChainID(), s.forwardLinkID(), f != nil, chain)
	if err != nil {
		return nil, err
	}
	if !dec.Forward {
		return nil, nil
	}
	link, err := f.ForwardAgent(ForwardRegistration{
		AgentID: agentID,
		Chain:   chain,
		Deliver: deliver,
		// P22-06: a post-open refusal means the upstream registration never
		// happened, so the local twin (registry row, session, link record,
		// mirror) is torn down BEFORE the refusal travels downstream —
		// otherwise a loop refusal leaves a dual-registered row behind and
		// the chain "terminates" nowhere.
		OnRefused: func(code, reason string) {
			s.refuseTeardownForwardedAgent(agentID)
			log.Printf("[forward] upstream refused agent %s after channel open (%s: %s) — local twin torn down", agentID, code, reason)
		},
	}, first)
	if err != nil {
		return nil, err
	}
	s.markMirrored(MirroredAgent{
		AgentID:        agentID,
		Via:            link.Via,
		Relayed:        true,
		Policy:         dec.Policy,
		UpstreamLive:   true,
		ForwardedAt:    nowRFC3339(),
		ChainTraversed: append([]string(nil), chain.Traversed...),
	})
	s.noteForwardLink(agentID, link)
	return link, nil
}

// P22-06: pipeForwardedAgent is gone — a forwarded direct client now keeps
// its socket on this server (dual registration in handleWebSocket) and its
// frames mirror upstream from handleMessages, so a policy toggle can take
// effect mid-run without touching the client's connection.

// writeForwardRefusal tells a client why it was not admitted, in the protocol
// envelope shape an agent already understands, then the caller closes.
func writeForwardRefusal(conn *websocket.Conn, code string, err error) {
	if conn == nil {
		return
	}
	payload, _ := json.Marshal(map[string]string{
		"type":  "error",
		"code":  code,
		"error": err.Error(),
	})
	conn.WriteMessage(websocket.TextMessage, payload)
}

// forwardCounters instruments admission so parity between the direct and
// relayed paths is measurable instead of assumed.
type forwardCounters struct {
	admittedDirect  atomic.Int64
	admittedRelayed atomic.Int64
	deniedDirect    atomic.Int64
	deniedRelayed   atomic.Int64
}

// admitInbound applies the server's rate limiter to an inbound agent message.
// Both paths — a direct WebSocket and a channel behind a relay — call this,
// which is the parity the unit requires: a relayed client is admitted by the
// same limiter, not by an inherited accident of the transport. A nil limiter
// means unlimited (the standalone default) and is reported as admitted.
func (s *Server) admitInbound(agentID string, relayed bool) bool {
	ok := true
	if s.rateLimit != nil {
		ok = s.rateLimit.Allow(agentID)
		if ok {
			// Admission only: the slot is released immediately. Commands
			// that hold a slot hold it around their own in-flight window.
			s.rateLimit.Release()
		}
	}
	switch {
	case relayed && ok:
		s.forwardStats.admittedRelayed.Add(1)
	case relayed && !ok:
		s.forwardStats.deniedRelayed.Add(1)
	case ok:
		s.forwardStats.admittedDirect.Add(1)
	default:
		s.forwardStats.deniedDirect.Add(1)
	}
	return ok
}

// nowRFC3339 is the timestamp format the mirror uses.
func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// refFingerprint normalizes a relay's self-declared fingerprint slot before
// it reaches the allowlist. Empty stays empty: P7 owns what a fingerprint is.
func refFingerprint(fp string) string { return strings.TrimSpace(fp) }

// forwardRefusalPayload marshals the one-frame refusal an agent/relay peer
// understands. Shared by the direct path (writeForwardRefusal) and the
// in-session path (handleMessages P23-14 refusal) so the wire shape has one
// author.
func forwardRefusalPayload(fr *ForwardRefusalError) []byte {
	payload, _ := json.Marshal(map[string]string{
		"type":  "error",
		"code":  fr.Code,
		"error": fr.Error(),
	})
	return payload
}

// refuseRelayRegistration answers a relay registration with one
// forward_refused control frame on channel 0, then closes the socket. The
// frame carries the code, so a forwarder can surface the named refusal instead
// of a bare disconnect.
func (s *Server) refuseRelayRegistration(conn *websocket.Conn, magic byte, err error) {
	code, _ := isForwardRefusal(err)
	reason := err.Error()
	if code == "" {
		code = RefusalPolicyDenied
	}
	payload, _ := json.Marshal(map[string]string{
		"type":   "forward_refused",
		"code":   code,
		"reason": reason,
	})
	if conn == nil {
		return
	}
	conn.WriteMessage(websocket.BinaryMessage, makeRelayFrame(magic, 0, payload))
	conn.Close()
}

// refuseRelayChannel answers one channel with a forward_refused control frame
// carrying that channel's id, so the relay can fail the right client.
func (s *Server) refuseRelayChannel(session *relaySession, channelID uint32, err error) {
	code, _ := isForwardRefusal(err)
	if code == "" {
		code = RefusalPolicyDenied
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"type":       "forward_refused",
		"channel_id": channelID,
		"code":       code,
		"reason":     err.Error(),
	})
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	session.conn.WriteMessage(websocket.BinaryMessage, makeRelayFrame(session.magic, 0, payload))
}

// forwardPolicyFor resolves the policy for an agent id as this server knows
// it. A relayed client's registry id is relay/<link>/<name>; an operator sets
// the policy against the name they see on that agent, so the exact key is
// tried first and the trailing name second. Otherwise a relayed client would
// silently take the default and never recurse.
func (s *Server) forwardPolicyFor(agentID string) string {
	s.forwardMu.RLock()
	p, ok := s.forwardPolicies[agentID]
	all := s.forwardAll
	s.forwardMu.RUnlock()
	if ok {
		return p
	}
	if i := strings.LastIndex(agentID, "/"); i >= 0 && i+1 < len(agentID) {
		s.forwardMu.RLock()
		p, ok = s.forwardPolicies[agentID[i+1:]]
		s.forwardMu.RUnlock()
		if ok {
			return p
		}
	}
	if all {
		return PolicyRelayAll
	}
	return PolicyLocal
}

// SetForwarderID names this server when it acts as a forwarding hop.
func (s *Server) SetForwarderID(id string) {
	s.forwardMu.Lock()
	s.forwarderID = strings.TrimSpace(id)
	s.forwardMu.Unlock()
}

// SetRelayAllowlist installs the P7-owned allowlist called before a relay's
// downstream clients are accepted. nil means "no allowlist configured"
// (documented, not a silent pass): the gate is inactive on a standalone
// deployment that never sets it.
func (s *Server) SetRelayAllowlist(a RelayAllowlist) {
	s.forwardMu.Lock()
	s.relayAllowlist = a
	s.forwardMu.Unlock()
}

// relayAllowed calls the allowlist. It returns nil when no allowlist is
// installed, and a named refusal on a miss — never a silent accept.
func (s *Server) relayAllowed(relayID, fingerprint string) error {
	s.forwardMu.RLock()
	al := s.relayAllowlist
	s.forwardMu.RUnlock()
	if al == nil {
		return nil
	}
	if al.Allows(relayID, fingerprint) {
		return nil
	}
	return refAllowlist(relayID)
}

// isForwardRefusal reports whether err is a named forwarding refusal, and its
// code. Callers use it to answer with the right status instead of a generic
// failure string.
func isForwardRefusal(err error) (string, bool) {
	var fr *ForwardRefusalError
	if errors.As(err, &fr) {
		return fr.Code, true
	}
	return "", false
}
