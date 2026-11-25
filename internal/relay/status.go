package relay

// P21-03 — the uplink's own record.
//
// The relay is the only party that speaks on the uplink, so it is the only
// party that can report what the uplink looks like. This file holds the
// state the relay learns from the upstream server (peer_hello) and the
// status document it serves on its own listen port.
//
// Honesty rules for the document:
//   - forward_policy has no relay-side source; it is served as "" rather
//     than a guessed value.
//   - Downstream channels carry no agent identity: the relay forwards opaque
//     frames and never parses AgentInfo, so agents[] describes channels, not
//     named agents.
//   - connected_at is the moment the current peer_hello was received. On
//     link loss it is retained: it is the last time the peer was known.
//   - last_error is the failure that took the link down, cleared by the next
//     successful registration. state=down always carries a non-empty reason.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// PeerHello is the server identity pushed down the uplink on registration
// success (spec P2). It is the downstream side's only record of its peer.
type PeerHello struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	ListenAddr string `json:"listen_addr"`
}

// UpstreamStatus describes the link to the upstream server.
type UpstreamStatus struct {
	URL         string     `json:"url"`
	Port        int        `json:"port"`
	// P21-09: Transport names the port the winning candidate actually dialled,
	// as a string ("443"). It is derived from the connection that upgraded, not
	// from config order, and it is retained when the link drops: it is the last
	// transport that won, which is what an operator goes and looks at.
	Transport   string            `json:"transport,omitempty"`
	Candidates  []CandidateStatus `json:"candidates,omitempty"`
	State       string     `json:"state"` // "up" | "down"
	ConnectedAt string     `json:"connected_at,omitempty"`
	Peer        *PeerHello `json:"peer,omitempty"` // last known, retained on loss
	LastError   string     `json:"last_error"`
}

// CandidateStatus is one entry of the ordered upstream candidate list, as the
// relay last saw it. State is "up" for the candidate carrying the link, "down"
// for one whose attempt failed, "untried" for a candidate no walk has reached
// yet. LastError is why that candidate was skipped: a failover that reports only
// "connected" hides which candidates are dead.
type CandidateStatus struct {
	URL       string `json:"url"`
	Port      int    `json:"port"`
	State     string `json:"state"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
}

// DownstreamChannel is one multiplexed downstream channel. Kind is "agent"
// for a direct WebSocket or "nested" for a relay-chained virtual channel.
type DownstreamChannel struct {
	Channel uint32 `json:"channel"`
	Kind    string `json:"kind"`
}

// DownstreamStatus describes what the relay is carrying for its clients.
type DownstreamStatus struct {
	Listen   string              `json:"listen"`
	Agents   []DownstreamChannel `json:"agents"`
	Channels int                 `json:"channels"`
	// P21-08: the nested relays this relay carries. Each entry is one hop the
	// server cannot see directly; it is retained after the nested socket dies
	// with state=down, so the chain keeps naming the dead hop.
	Nested []NestedStatus `json:"nested,omitempty"`
}

// Status is the document served at GET /api/v1/relay/status on the relay's
// own listen port.
type Status struct {
	RelayID       string           `json:"relay_id"`
	MaxAgents     int              `json:"max_agents"`
	ForwardPolicy string           `json:"forward_policy"`
	Upstream      UpstreamStatus   `json:"upstream"`
	Downstream    DownstreamStatus `json:"downstream"`
}

// uplinkState is the relay's mutable record of the link. Guarded by
// Relay.statusMu; copied out whole so readers never race a writer.
type uplinkState struct {
	peer        *PeerHello
	connectedAt time.Time
	lastErr     string
	// P21-09: the candidate that won the last successful walk. Retained on
	// link loss; empty until some candidate has completed an upgrade.
	activeURL  string
	activePort int
}

func (r *Relay) recordPeerHello(peer *PeerHello) {
	if peer == nil {
		return
	}
	cp := *peer
	r.statusMu.Lock()
	r.uplink.peer = &cp
	r.uplink.connectedAt = time.Now().UTC()
	r.uplink.lastErr = ""
	r.statusMu.Unlock()
}

func (r *Relay) recordUpstreamError(err error) {
	if err == nil {
		return
	}
	r.statusMu.Lock()
	r.uplink.lastErr = err.Error()
	r.statusMu.Unlock()
}

func (r *Relay) clearUpstreamError() {
	r.statusMu.Lock()
	r.uplink.lastErr = ""
	r.statusMu.Unlock()
}

// Status returns a consistent snapshot of the relay's own state.
func (r *Relay) Status() Status {
	r.statusMu.RLock()
	peer := r.uplink.peer
	if peer != nil {
		cp := *peer
		peer = &cp
	}
	connectedAt := r.uplink.connectedAt
	lastErr := r.uplink.lastErr
	activeURL := r.uplink.activeURL
	activePort := r.uplink.activePort
	r.statusMu.RUnlock()

	state := "down"
	if r.upstreamOK.Load() {
		state = "up"
	}

	// Until a candidate has won, the first configured candidate is the honest
	// answer to "what are you trying to reach"; transport stays empty because
	// no port has actually been used.
	url, port := activeURL, activePort
	if url == "" && len(r.candidates) > 0 {
		url, port = r.candidates[0].url, r.candidates[0].port
	}

	up := UpstreamStatus{
		URL:        url,
		Port:       port,
		Transport:  transportName(activePort),
		Candidates: r.candidatesSnapshot(),
		State:     state,
		Peer:      peer,
		LastError: lastErr,
	}
	if !connectedAt.IsZero() {
		up.ConnectedAt = connectedAt.Format(time.RFC3339Nano)
	}

	channels := r.channels.All()
	agents := make([]DownstreamChannel, 0, len(channels))
	for _, ch := range channels {
		kind := "agent"
		if ch.Conn == nil {
			kind = "nested"
		}
		agents = append(agents, DownstreamChannel{Channel: ch.ID, Kind: kind})
	}

	return Status{
		RelayID:       r.cfg.RelayID,
		MaxAgents:     r.cfg.MaxAgents,
		ForwardPolicy: "",
		Upstream:      up,
		Downstream: DownstreamStatus{
			Listen:   r.cfg.ListenAddr,
			Agents:   agents,
			Channels: len(channels),
			Nested:   r.nestedSnapshot(),
		},
	}
}

// handleStatus serves the relay's view of its own uplink and downstream.
// It is registered on the relay's mux, not the server's: an implementation
// on the server binary would pass a unit test and fail the acceptance.
func (r *Relay) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(r.Status())
}

// upstreamPort resolves the port a client would dial from the upstream URL,
// falling back to the scheme default. A URL with no parseable port yields 0
// (stated, not guessed).
func upstreamPort(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err == nil {
			return n
		}
		return 0
	}
	switch u.Scheme {
	case "wss", "https":
		return 443
	case "ws", "http":
		return 80
	}
	return 0
}

// dialableListen returns the relay's listen address in host:port form. A
// wildcard bind is reported as the loopback address it is actually reachable
// on from the same host.
func dialableListen(bind string) string {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return bind
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return bind
}
