// P27-02 — the uplink-server row model (spec P27-01 §1) and its backing
// reads. This file adds NO state: it projects the peerManager snapshot + the
// servers bootstrap rows into one row shape. Every key is emitted on every
// row (R3); a value that is genuinely unknown is null, never a green default
// (§5.3 never-green-while-dead).

package server

import (
	"net/url"
	"sort"
	"strings"
	"time"
)

// existingPeerManager returns the process-wide manager WITHOUT creating one.
// A read path (GET /servers, /health) must not spawn a peer lifecycle as a
// side effect of being polled.
func (s *Server) existingPeerManager() *peerManager {
	s.peersMu.Lock()
	defer s.peersMu.Unlock()
	return s.peers
}

// uplinkStateOf applies the §1.2 derivation to a runtime view. The ordering
// rule is binding: enabled=false ⇒ disabled wins over any link state (a
// flapping link under a disabled row must not read connected).
func uplinkStateOf(v peerConfigView) string {
	if !v.Enabled {
		return peerStateDisabled
	}
	if v.LinkState == "" {
		// Outside the §4 machine: a peer with no live runtime (manual, not
		// yet dialed; scheduled, parked) is disabled by rule, not down.
		return peerStateDisabled
	}
	return v.LinkState
}

// linkTransport is the §2.3 transport truth, parsed from the upstream URL
// scheme at view time — never from a constant and never from a self-report.
func linkTransport(upstream string) string {
	u, err := url.Parse(strings.TrimSpace(upstream))
	if err != nil {
		return ""
	}
	switch u.Scheme {
	case "wss", "https":
		return "wss"
	case "ws", "http":
		return "ws"
	}
	return ""
}

// p27PeerEntries renders the peers-sourced half of GET /api/v1/servers:
// one row per configured peer, joined to its live runtime. Rows already
// present from the live relay map (same id) are skipped — the socket is the
// truth for those.
func (s *Server) p27PeerEntries(now time.Time, have map[string]bool) []serverEntry {
	pm := s.existingPeerManager()
	if pm == nil {
		return nil
	}
	views := pm.snapshot()
	out := make([]serverEntry, 0, len(views))
	for _, v := range views {
		if have[v.ID] {
			continue
		}
		ty := v.UplinkType
		if ty == "" {
			ty = uplinkTypeAlwaysOn
		}
		direction := "uplink"
		address := v.Upstream
		if v.Listen != "" {
			direction = "inbound"
			address = v.Listen
		}
		inWindow := true
		var sw *scheduleWindow
		if ty == uplinkTypeScheduled {
			sw = &scheduleWindow{Start: v.ScheduleStart, End: v.ScheduleEnd}
			rec := defaultPeerRecord{UplinkType: ty, ScheduleStart: v.ScheduleStart, ScheduleEnd: v.ScheduleEnd}
			inWindow = rec.inWindowNow()
		}
		row := serverEntry{
			ID:             v.ID,
			Name:           v.ID,
			Role:           rolePeer,
			Address:        address,
			Direction:      direction,
			State:          uplinkStateOf(v),
			StateReason:    v.StateDetail.Reason,
			LastSeen:       "",
			LinkState:      &v.LinkState,
			TokenFP:        &v.TokenFP,
			UplinkType:     &ty,
			ScheduleWindow: sw,
			InWindow:       inWindow,
			Enabled:        v.Enabled,
			Encrypted:      encryptedPair{Transport: linkTransport(v.Upstream), Payload: "none"},
			Source:         "peers",
			Transports:     []serverTransport{},
			Counts:         serverCounts{},
			PeerMeta:       &peerMetaRow{},
		}
		if v.StateDetail.PeerVersion != "" {
			pv := v.StateDetail.PeerVersion
			row.PeerVersion = &pv
			row.Version = &pv
		}
		if v.StateDetail.RequiredMin != "" {
			rm := v.StateDetail.RequiredMin
			row.RequiredMin = &rm
		}
		if ma := v.MaxAgents; ma > 0 {
			row.MaxAgents = &ma
		}
		// §5.2: carrying truth exists only for the peer that IS this
		// server's forward target (the forwarder links belong to it).
		row.Carrying = s.carryingForPeer(v.ID)
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].State != out[j].State {
			return out[i].State == peerStateConnected
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// carryingForPeer returns the forwarder counters when this peer is the wired
// forward target, and honest zeros otherwise.
func (s *Server) carryingForPeer(id string) carryingCounts {
	s.forwardMu.Lock()
	f := s.forwarder
	fid := s.forwarderID
	s.forwardMu.Unlock()
	if f == nil || fid != id {
		return carryingCounts{}
	}
	open, registered, last := f.Carrying()
	return carryingCounts{ChannelsOpen: open, AgentsRegistered: registered, LastRefusal: last}
}

// normalizeUplinkRow fills the defaults the row model promises for rows that
// come from the P22-02 paths (local + relay/bootstrap): in_window is true
// when the concept does not apply (§1.1), enabled is true (the row exists),
// and payload is always "none" (§2.3). It never invents a positive state.
func normalizeUplinkRow(e *serverEntry) {
	e.InWindow = true
	e.Enabled = true
	e.Encrypted.Payload = "none"
	if e.Source == "" {
		e.Source = "bootstrap"
	}
	if e.Direction == "" {
		if e.ListenAddr != "" {
			e.Direction = "inbound"
		} else {
			e.Direction = "uplink"
		}
	}
	if e.Address == "" {
		if e.Direction == "inbound" {
			e.Address = e.ListenAddr
		} else if e.PeerMeta != nil && e.PeerMeta.Upstream != nil {
			e.Address = *e.PeerMeta.Upstream
		}
	}
	if e.Encrypted.Transport == "" {
		if e.PeerMeta != nil && e.PeerMeta.Upstream != nil {
			e.Encrypted.Transport = linkTransport(*e.PeerMeta.Upstream)
		}
	}
	// §1.1 bootstrap rows: state from ws_state — up ⇒ connected, else
	// unreachable. Applied only where the P22-02 vocabulary ("up"/"down")
	// is still in use; §4 states pass through untouched.
	switch e.State {
	case "up":
		e.State = peerStateConnected
	case "down":
		e.State = peerStateUnreachable
	}
}
