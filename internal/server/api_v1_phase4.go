package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// handleV1AgentMode sends a mode_control command to an agent via WebSocket.
// POST /api/v1/agents/{id}/mode
// Body: {"action":"start","mode":"relay","config":{"listen":":7701","upstream":"ws://...","token":"..."}}
func (s *Server) handleV1AgentMode(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, "agent id required", http.StatusBadRequest)
		return
	}

	var req struct {
		Action string          `json:"action"` // "start" or "stop"
		Mode   string          `json:"mode"`   // "serve", "connect", "relay"
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Action == "" || req.Mode == "" {
		http.Error(w, "action and mode are required", http.StatusBadRequest)
		return
	}

	// Build the mode_control protocol message params
	params := mustMarshalRawV1(struct {
		Action string          `json:"action"`
		Mode   string          `json:"mode"`
		Config json.RawMessage `json:"config"`
	}{req.Action, req.Mode, req.Config})

	// Forward to agent and wait for response
	resp, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeModeControl, params, 15*time.Second, "")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":     true,
		"data":   resp,
		"agent":  agentID,
		"action": req.Action,
		"mode":   req.Mode,
	})
}

// handleV1GetAgentMode returns the current mode status for an agent.
// GET /api/v1/agents/{id}/mode
func (s *Server) handleV1GetAgentMode(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, "agent id required", http.StatusBadRequest)
		return
	}

	// P21-01: this handler used to answer a bare 404 "agent not connected" for
	// an agent the list called active — the same disagreement exec had, one
	// surface over. The predicate names the real state instead.
	if dispatchable, _, reason := s.AgentDispatchability(agentID); !dispatchable {
		http.Error(w, reason, http.StatusNotFound)
		return
	}

	// Return cached mode status from session context (if available)
	// The agent sends mode_status on connect and on mode change
	modeStatus := s.sessions.GetMemory(agentID, "mode_status")

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":          true,
		"agent":       agentID,
		"connected":   true,
		"mode_status": modeStatus,
	})
}

// handleV1Topology returns the full relay topology tree.
// GET /api/v1/topology
func (s *Server) handleV1Topology(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	// Build topology from relay sessions + agent registry
	s.relayMu.RLock()
	relays := make([]*relaySession, 0, len(s.relays))
	for _, rs := range s.relays {
		relays = append(relays, rs)
	}
	s.relayMu.RUnlock()

	type TopologyNode struct {
		ID            string         `json:"id"`
		Type          string         `json:"type"` // "server", "relay", "agent"
		Name          string         `json:"name"`
		Version       string         `json:"version,omitempty"`
		Modes         string         `json:"modes,omitempty"`
		Relayed       bool           `json:"relayed,omitempty"`
		Via           string         `json:"via,omitempty"` // P21-04: relay id for a mirrored agent
		Active        bool           `json:"active"`
		ForwardPolicy string         `json:"forward_policy,omitempty"` // Step 11: "relay" or "local"
		Children      []TopologyNode `json:"children,omitempty"`
	}

	type TopologyEdge struct {
		From string `json:"from"`
		To   string `json:"to"`
		Type string `json:"type"` // "direct", "relay", "relayed", "uplink"
		// P21-03: the uplink edge is stateful. A down uplink stays present
		// with a named reason and the last known peer — a failure that
		// renders as "nothing here" is the defect this fixes.
		State       string        `json:"state,omitempty"`
		StateReason string        `json:"state_reason,omitempty"`
		ConnectedAt string        `json:"connected_at,omitempty"`
		Peer        *peerIdentity `json:"peer,omitempty"`
	}

	// Get all agents from registry
	agents := s.registry.ListAgents()

	// Build nodes and edges
	var nodes []TopologyNode
	var edges []TopologyEdge

	// Server node — always active if server is running
	nodes = append(nodes, TopologyNode{
		ID:      "server",
		Type:    "server",
		Name:    s.addr,
		Active:  true,
		Version: s.version,
	})

	// Relay nodes + their agents
	for _, rs := range relays {
		relayNode := TopologyNode{
			ID:     rs.relayID,
			Type:   "relay",
			Name:   rs.relayID,
			Active: true, // relay is connected if session exists
		}
		if rs.metadata != nil {
			relayNode.Modes = fmt.Sprintf("listen=%s", rs.metadata.ListenAddr)
		}

		// Find agents behind this relay
		rs.channelsMu.RLock()
		for _, vc := range rs.channels {
			if vc.agentID != "" {
				// Find agent info from registry
				agentRecord, err := s.registry.GetHealth(vc.agentID)
				agentNode := TopologyNode{
					ID:      vc.agentID,
					Type:    "agent",
					Name:    vc.agentID,
					Relayed: true,
					// P23-09: provenance from the node's own fields. The
					// channel's session IS the relay that carries it, so
					// this is read from the transport, not parsed out of
					// the id and not filled from the server's own name.
					Via:    rs.relayID,
					Active: true, // relayed agent is active if it has a channel
				}
				if err == nil {
					agentNode.Version = agentRecord.Version
				}
				relayNode.Children = append(relayNode.Children, agentNode)
			}
		}
		rs.channelsMu.RUnlock()

		nodes = append(nodes, relayNode)
		edges = append(edges, TopologyEdge{
			From: "server",
			To:   rs.relayID,
			Type: "relay",
		})

		// Add edges for relayed agents
		for _, child := range relayNode.Children {
			edges = append(edges, TopologyEdge{
				From: rs.relayID,
				To:   child.ID,
				Type: "relayed",
			})
		}
	}

	// P21-03: this server's own uplink, read from the relay that carries it.
	// The read is bounded (uplinkStatusTimeout); the previous snapshot is
	// retained on failure so a dead link is degraded and named, not absent.
	if edgeAddr, st, err := s.uplinkSnapshot(); edgeAddr != "" {
		to := "relay:" + edgeAddr
		state := "down"
		reason := ""
		connectedAt := ""
		var peer *peerIdentity
		if st != nil {
			if st.RelayID != "" {
				to = st.RelayID
			}
			connectedAt = st.Upstream.ConnectedAt
			if st.Upstream.Peer != nil {
				p := *st.Upstream.Peer
				peer = &p
			}
			if st.Upstream.State == "up" {
				state = "up"
			} else {
				reason = st.Upstream.LastError
			}
		}
		if err != nil {
			state = "down"
			reason = err.Error()
		} else if state == "down" && reason == "" {
			reason = "relay reports upstream state=down with no error text"
		}

		// A relay that also registered upstream to this server already has a
		// node; a second node with the same id would render as two boxes and
		// split the graph's edges. The edge is still emitted either way.
		known := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			known[n.ID] = true
		}
		if !known[to] {
			uplinkNode := TopologyNode{
				ID:      to,
				Type:    "relay",
				Name:    to,
				Active:  state == "up",
				Version: "",
			}
			if peer != nil {
				uplinkNode.Version = peer.Version
				if peer.Name != "" {
					uplinkNode.Name = peer.Name
				}
			}
			nodes = append(nodes, uplinkNode)
		}
		edges = append(edges, TopologyEdge{
			From:        "server",
			To:          to,
			Type:        "uplink",
			State:       state,
			StateReason: reason,
			ConnectedAt: connectedAt,
			Peer:        peer,
		})
	}

	// Direct agents (not behind a relay)
	s.mu.RLock()
	connMap := make(map[string]bool, len(s.conns))
	for id := range s.conns {
		connMap[id] = true
	}
	s.mu.RUnlock()

	for _, agentInfo := range agents {
		// Skip relayed agents (they start with "relay/")
		if strings.HasPrefix(agentInfo.AgentID, "relay/") {
			continue
		}
		node := TopologyNode{
			ID:      agentInfo.AgentID,
			Type:    "agent",
			Name:    agentInfo.Name,
			Version: agentInfo.Version,
			Active:  connMap[agentInfo.AgentID],
		}
		// Include forward policy if set (Step 11)
		policy := s.GetForwardPolicy(agentInfo.AgentID)
		if policy != "" && policy != "relay" {
			node.ForwardPolicy = policy
		}
		nodes = append(nodes, node)
		edges = append(edges, TopologyEdge{
			From: "server",
			To:   agentInfo.AgentID,
			Type: "direct",
		})
	}

	// P21-04: the read-only mirror. A forwarded agent stays visible to this
	// server's UI as relayed=true with the link that carries it, and the edge
	// is named relayed rather than direct. It has no connection and no write
	// path: it renders state, it does not accept commands.
	for _, m := range s.MirroredAgents() {
		nodes = append(nodes, TopologyNode{
			ID:      m.AgentID,
			Type:    "agent",
			Name:    m.AgentID,
			Relayed: true,
			Via:     m.Via,
			Active:  m.UpstreamLive,
		})
		edges = append(edges, TopologyEdge{
			From: m.Via,
			To:   m.AgentID,
			Type: "relayed",
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true,
		"data": map[string]interface{}{
			"nodes": nodes,
			"edges": edges,
		},
	})
}

// mustMarshalRawV1 marshals a value to json.RawMessage (for API v1 handlers).
func mustMarshalRawV1(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// handleV1ForwardPolicy sets or gets the forward policy for an agent (Step 11).
// POST /api/v1/agents/{id}/forward-policy
// Body: {"action":"relay|local"}
// GET /api/v1/agents/{id}/forward-policy
func (s *Server) handleV1ForwardPolicy(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, "agent id required", http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodGet {
		policy := s.GetForwardPolicy(agentID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":     true,
			"agent":  agentID,
			"policy": policy,
		})
		return
	}

	if r.Method == http.MethodPost {
		var req struct {
			Action string `json:"action"` // "relay" or "local"
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Action != "relay" && req.Action != "local" {
			http.Error(w, "action must be 'relay' or 'local'", http.StatusBadRequest)
			return
		}

		// Send forward_policy to the agent via WebSocket
		params := mustMarshalRawV1(struct {
			Agent  string `json:"agent"`
			Action string `json:"action"`
		}{agentID, req.Action})

		resp, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeForwardPolicy, params, 10*time.Second, "")
		if err != nil {
			// Agent might not be connected to US (it's local to the server-as-relay node)
			// Still set the policy locally
			s.SetForwardPolicy(agentID, req.Action)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"ok":     true,
				"agent":  agentID,
				"policy": req.Action,
				"note":   "policy set locally (agent not directly connected)",
			})
			return
		}

		s.SetForwardPolicy(agentID, req.Action)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":     true,
			"agent":  agentID,
			"policy": req.Action,
			"result": resp,
		})
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// handleV1ListForwardPolicies returns all forward policies (Step 11).
// GET /api/v1/forward-policies
func (s *Server) handleV1ListForwardPolicies(w http.ResponseWriter, r *http.Request) {
	policies := s.GetForwardPolicies()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":       true,
		"policies": policies,
		"count":    len(policies),
	})
}

// handleV1ReconfigureAll broadcasts a reconfigure command to ALL connected agents.
// POST /api/v1/reconfigure
// Body: {"server_url":"ws://new-server:80/ws","token":"optional-new-token"}
// This enables mass migration when the server IP changes — all agents reconnect
// to the new address, and their local config files are updated.
func (s *Server) handleV1ReconfigureAll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerURL string `json:"server_url"`
		Token     string `json:"token,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.ServerURL == "" {
		http.Error(w, "server_url is required", http.StatusBadRequest)
		return
	}

	params := mustMarshalRawV1(struct {
		ServerURL string `json:"server_url"`
		Token     string `json:"token,omitempty"`
	}{req.ServerURL, req.Token})

	// Get all connected agents
	agents := s.registry.ListAgents()
	results := make([]map[string]interface{}, 0, len(agents))

	for _, agentInfo := range agents {
		// Skip relayed agents and inactive ones
		if strings.HasPrefix(agentInfo.AgentID, "relay/") || agentInfo.Status != "active" {
			continue
		}

		// Send reconfigure with short timeout (fire and forget — agent will disconnect)
		_, err := s.forwardToAgentWithTimeout(agentInfo.AgentID, protocol.TypeReconfigure, params, 5*time.Second, "")

		result := map[string]interface{}{
			"agent":  agentInfo.AgentID,
			"status": "sent",
		}
		if err != nil {
			result["status"] = "error"
			result["error"] = err.Error()
		}
		results = append(results, result)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":      true,
		"data":    results,
		"count":   len(results),
		"new_url": req.ServerURL,
	})
}

// handleV1ReconfigureAgent reconfigures a single agent.
// POST /api/v1/agents/{id}/reconfigure
// Body: {"server_url":"ws://new-server:80/ws","token":"optional-new-token"}
func (s *Server) handleV1ReconfigureAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if agentID == "" {
		http.Error(w, "agent id required", http.StatusBadRequest)
		return
	}

	var req struct {
		ServerURL string `json:"server_url"`
		Token     string `json:"token,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.ServerURL == "" {
		http.Error(w, "server_url is required", http.StatusBadRequest)
		return
	}

	params := mustMarshalRawV1(struct {
		ServerURL string `json:"server_url"`
		Token     string `json:"token,omitempty"`
	}{req.ServerURL, req.Token})

	resp, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeReconfigure, params, 10*time.Second, "")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":    true,
		"data":  resp,
		"agent": agentID,
	})
}
