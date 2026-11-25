package server

// P21-03 — the server's record of its own uplink.
//
// The server speaks to its upstream only through the relay, so it has no
// direct view of that link. The relay is asked over HTTP on the loopback
// address where it serves /api/v1/relay/status, and the answer is merged
// into /api/v1/topology as an "uplink" edge.
//
// Two properties are load-bearing:
//   - the read is bounded (uplinkStatusTimeout) so a stalled relay cannot
//     stall the topology endpoint;
//   - the last successful snapshot is retained, so a link that goes down is
//     rendered as degraded with its last known peer, not as an absence.
//
// Absence and "down" are different answers: no uplink configured means no
// edge at all; a configured uplink that cannot be read means state=down with
// a non-empty state_reason.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// uplinkStatusTimeout bounds the relay status read. The topology endpoint
// answers within this bound plus local work even when the relay is stalled.
const uplinkStatusTimeout = 300 * time.Millisecond

// peerIdentity is the server's identity as pushed down the uplink in
// peer_hello and echoed back into topology.
type peerIdentity struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	ListenAddr string `json:"listen_addr"`
}

// uplinkUpstream is the subset of the relay's upstream block the server
// needs. Unknown members are ignored on decode.
type uplinkUpstream struct {
	URL         string        `json:"url"`
	Port        int           `json:"port"`
	State       string        `json:"state"`
	ConnectedAt string        `json:"connected_at"`
	Peer        *peerIdentity `json:"peer"`
	LastError   string        `json:"last_error"`
}

// uplinkRelayStatus is the relay's status document as the server reads it.
type uplinkRelayStatus struct {
	RelayID       string         `json:"relay_id"`
	MaxAgents     int            `json:"max_agents"`
	ForwardPolicy string         `json:"forward_policy"`
	Upstream      uplinkUpstream `json:"upstream"`
	Downstream    struct {
		Listen   string `json:"listen"`
		Channels int    `json:"channels"`
	} `json:"downstream"`
}

// SetUplinkRelayStatus points the server at the loopback address where the
// relay serving this server's uplink answers GET /api/v1/relay/status.
// Accepts host:port, :port, or an http(s) URL. Empty disables the uplink
// view entirely (no edge — absence, not "down").
func (s *Server) SetUplinkRelayStatus(addr string) {
	s.uplinkMu.Lock()
	s.uplinkAddr = normalizeUplinkAddr(addr)
	if s.uplinkAddr == "" {
		s.uplinkLast = nil
	}
	s.uplinkMu.Unlock()
}

// SetAdvertisedAddr overrides the address reported to peers in peer_hello.
// When empty, the bind address is translated: wildcard and loopback binds
// are replaced with the host name, because a peer cannot dial "localhost".
func (s *Server) SetAdvertisedAddr(addr string) {
	s.uplinkMu.Lock()
	s.advertisedAddr = strings.TrimSpace(addr)
	s.uplinkMu.Unlock()
}

// SetServerIdentity overrides the id/name reported to peers. Defaults are
// derived from the host name.
func (s *Server) SetServerIdentity(id, name string) {
	s.uplinkMu.Lock()
	s.serverID = strings.TrimSpace(id)
	s.serverName = strings.TrimSpace(name)
	s.uplinkMu.Unlock()
}

// normalizeUplinkAddr turns a configured listen address into host:port.
// A wildcard host is read as loopback: this read is local by design.
func normalizeUplinkAddr(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	v = strings.TrimPrefix(strings.TrimPrefix(v, "http://"), "https://")
	v = strings.TrimSuffix(v, "/")
	if host, port, err := net.SplitHostPort(v); err == nil {
		switch host {
		case "", "0.0.0.0", "::", "[::]":
			host = "127.0.0.1"
		}
		return net.JoinHostPort(host, port)
	}
	// ":7701" and bare "7701" both mean loopback:port.
	port := strings.TrimPrefix(v, ":")
	if port == "" {
		return ""
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// peerIdentitySnapshot builds the identity pushed in peer_hello.
func (s *Server) peerIdentitySnapshot() *peerIdentity {
	s.uplinkMu.RLock()
	id, name, advertised, bind := s.serverID, s.serverName, s.advertisedAddr, s.addr
	s.uplinkMu.RUnlock()

	host, _ := os.Hostname()
	if name == "" {
		name = host
	}
	if id == "" {
		id = "server-" + host
	}
	return &peerIdentity{
		ID:         id,
		Name:       name,
		Version:    s.version,
		ListenAddr: dialableAddr(advertised, bind),
	}
}

// dialableAddr returns an address a client can dial. The bind string is not
// that address: "localhost:7700" or ":7700" names the listener, not the
// server (P21-02 D3).
func dialableAddr(advertised, bind string) string {
	if advertised != "" {
		return advertised
	}
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return bind
	}
	ip := net.ParseIP(host)
	if host == "" || host == "localhost" || (ip != nil && (ip.IsLoopback() || ip.IsUnspecified())) {
		if h, err := os.Hostname(); err == nil && h != "" {
			return net.JoinHostPort(h, port)
		}
	}
	return bind
}

// uplinkSnapshot reads the relay's status. A cached snapshot is returned
// alongside the error when the relay cannot be reached, so the caller can
// render state=down with the last known peer.
func (s *Server) uplinkSnapshot() (string, *uplinkRelayStatus, error) {
	s.uplinkMu.RLock()
	addr, client, last := s.uplinkAddr, s.uplinkClient, s.uplinkLast
	s.uplinkMu.RUnlock()
	if addr == "" {
		return "", nil, nil
	}
	if client == nil {
		client = &http.Client{Timeout: uplinkStatusTimeout}
	}

	ctx, cancel := context.WithTimeout(context.Background(), uplinkStatusTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/api/v1/relay/status", nil)
	if err != nil {
		return addr, last, fmt.Errorf("relay status request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return addr, last, fmt.Errorf("relay status at %s: %w", addr, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return addr, last, fmt.Errorf("relay status at %s: HTTP %d", addr, resp.StatusCode)
	}

	var st uplinkRelayStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return addr, last, fmt.Errorf("relay status at %s: decode: %w", addr, err)
	}
	s.uplinkMu.Lock()
	s.uplinkLast = &st
	s.uplinkMu.Unlock()
	return addr, &st, nil
}

// uplinkAddrSnapshot is a small accessor for the topology edge id fallback.
func (s *Server) uplinkConfiguredAddr() string {
	s.uplinkMu.RLock()
	defer s.uplinkMu.RUnlock()
	return s.uplinkAddr
}
