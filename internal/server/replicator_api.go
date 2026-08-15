package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/falke-ai-circuit/probe/internal/replicator"
)

// requireAdminReplica enforces the admin-only gate on top of v1CheckAuth.
// v1CheckAuth("operator-manage") grants both admin and operator; the replicator
// mints new agents, so it is admin-only. A nil operator (legacy token mode) is
// allowed through — the legacy token is the server's own admin token.
func (s *Server) requireAdminReplica(w http.ResponseWriter, r *http.Request) bool {
	op, ok := s.v1CheckAuth(w, r, "operator-manage")
	if !ok {
		return false
	}
	if op != nil && op.Role != RoleAdmin {
		writeError(w, http.StatusForbidden, "FORBIDDEN", "admin role required")
		return false
	}
	return true
}

// handleV1Replicate handles POST /api/v1/replicate — mint a child agent with
// built-in settings. Admin only. Pre-validates all fields before spawning so a
// bad value is a 400, not a silently-dead child.
func (s *Server) handleV1Replicate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}
	if !s.requireAdminReplica(w, r) {
		return
	}
	var req struct {
		Name        string `json:"name"`
		Server      string `json:"server"`
		Token       string `json:"token"`
		Mode        string `json:"mode"`
		Permissions string `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if s.replicator == nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "replicator not configured")
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "name is required")
		return
	}
	if req.Server == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "server is required")
		return
	}
	if !strings.HasPrefix(req.Server, "ws://") && !strings.HasPrefix(req.Server, "wss://") {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "server must start with ws:// or wss://")
		return
	}
	if req.Mode == "" {
		req.Mode = "silent"
	}
	if req.Mode != "silent" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "mode must be 'silent' (interactive is unsupported for replicated agents)")
		return
	}
	if req.Permissions == "" {
		req.Permissions = "full"
	}
	switch req.Permissions {
	case "sandboxed", "standard", "read-only", "full":
	default:
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "permissions must be sandboxed|standard|read-only|full")
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "token is required")
		return
	}

	rec, err := s.replicator.Spawn(req.Name, req.Server, req.Token, req.Mode, req.Permissions)
	if err != nil {
		writeError(w, http.StatusBadRequest, "SPAWN_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, rec)
}

// handleV1ListReplicas handles GET /api/v1/replicas — list all spawn records.
func (s *Server) handleV1ListReplicas(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	if s.replicator == nil {
		writeJSON(w, http.StatusOK, map[string]replicator.Record{})
		return
	}
	writeJSON(w, http.StatusOK, s.replicator.List())
}

// handleV1KillReplica handles DELETE /api/v1/replicas/{name} — terminate a
// replica (by handle, or by pid if orphaned after a restart). Admin only.
func (s *Server) handleV1KillReplica(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.requireAdminReplica(w, r) {
		return
	}
	if s.replicator == nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "replicator not configured")
		return
	}
	if err := s.replicator.Kill(name); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "status": "killed"})
}
