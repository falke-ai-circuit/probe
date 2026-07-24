package server

import (
	"encoding/json"
	"net/http"
	"time"
)


// handleHealth returns a server health check with per-server stats.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	agents := s.registry.ListAgents()
	total := len(agents)
	active := 0
	stale := 0
	for _, a := range agents {
		switch a.Status {
		case "active":
			active++
		case "stale":
			stale++
		}
	}
	resp := map[string]interface{}{
		"status":         "ok",
		"server_version": s.version,
		"total_agents":   total,
		"active_agents":  active,
		"stale_agents":   stale,
		"uptime_seconds": int64(time.Since(startTime).Seconds()),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

