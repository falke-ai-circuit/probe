package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// handleAgentMitmStart starts a MITM TCP proxy on the agent.
// POST /api/agent/{id}/mitm-start
// {"listen_addr":"127.0.0.3:1516","target_addr":"127.0.0.1:1516","log_path":"C:\\temp\\mitm.log","reuse_addr":true}
func (s *Server) handleAgentMitmStart(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.MitmStartParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeMitmStart, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAgentMitmStop stops a MITM TCP proxy on the agent.
// POST /api/agent/{id}/mitm-stop  {"mitm_id":"mitm-1"}
func (s *Server) handleAgentMitmStop(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.MitmStopParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeMitmStop, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAgentMitmTraffic retrieves captured traffic from a MITM proxy session.
// POST /api/agent/{id}/mitm-traffic  {"mitm_id":"mitm-1"}
func (s *Server) handleAgentMitmTraffic(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.MitmStopParams // reuses MitmID field
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeMitmData, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}