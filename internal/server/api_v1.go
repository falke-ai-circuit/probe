package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// ---------------------------------------------------------------------------
// Consistent response format
// ---------------------------------------------------------------------------

// APIResponse is the uniform response envelope for all v1 API endpoints.
// Every endpoint returns this structure — successes set OK=true with Data,
// failures set OK=false with Error.
type APIResponse struct {
	OK    bool        `json:"ok"`
	Data  interface{} `json:"data,omitempty"`
	Error *APIError   `json:"error,omitempty"`
}

// APIError is the error detail returned inside a v1 APIResponse.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeJSON writes a success APIResponse with the given status code and data.
func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(APIResponse{OK: true, Data: data})
}

// writeError writes an error APIResponse with the given status code, error
// code, and human-readable message.
func writeError(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(APIResponse{
		OK:    false,
		Error: &APIError{Code: code, Message: message},
	})
}

// ---------------------------------------------------------------------------
// UUID request ID generation
// ---------------------------------------------------------------------------

// generateRequestID generates a 32-character hex request ID using crypto/rand
// (16 bytes → 32 hex chars). Falls back to a time-based ID on the unlikely
// chance that crypto/rand fails.
func generateRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// v1 auth helper
// ---------------------------------------------------------------------------

// v1CheckAuth performs API auth + RBAC check for v1 endpoints. Returns the
// authenticated operator (may be nil in legacy token mode) and true if the
// request should proceed. On failure it writes a v1 error response and
// returns false.
func (s *Server) v1CheckAuth(w http.ResponseWriter, r *http.Request, action string) (*Operator, bool) {
	// First try operator auth (bearer token from /api/v1/login).
	// This handles RBAC mode where operators log in with username/password.
	op, ok := s.checkOperatorAuth(r, action)
	if ok {
		return op, true
	}

	// If checkOperatorAuth returned a non-nil operator, the operator was
	// authenticated but denied by RBAC. Return 403 immediately — do NOT
	// fall through to legacy token or auth-optional paths.
	if op != nil {
		s.auditDenied(op.ID, action, op)
		writeError(w, http.StatusForbidden, "FORBIDDEN", "permission denied")
		return nil, false
	}

	// Operator auth failed (no operator matched). If requireAPIAuth is set
	// and the token isn't a valid server connection token either, reject.
	if s.requireAPIAuth && !s.isValidToken(r.Header.Get("Authorization")) {
		s.auditDenied("", action, op)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid token")
		return nil, false
	}

	// If requireAPIAuth is false, allow through (auth optional).
	if !s.requireAPIAuth {
		return nil, true
	}

	s.auditDenied("", action, op)
	writeError(w, http.StatusForbidden, "FORBIDDEN", "permission denied")
	return nil, false
}

// errorCodeFromStatus maps an HTTP status code to a v1 error code string.
func errorCodeFromStatus(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusMethodNotAllowed:
		return "METHOD_NOT_ALLOWED"
	case http.StatusServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	case http.StatusGatewayTimeout:
		return "TIMEOUT"
	case http.StatusInternalServerError:
		return "INTERNAL_ERROR"
	default:
		return fmt.Sprintf("HTTP_%d", code)
	}
}

// ---------------------------------------------------------------------------
// Legacy handler wrapper — captures existing handler output and reformats
// it as a v1 APIResponse. This avoids duplicating business logic.
// ---------------------------------------------------------------------------

// v1WrapAgentHandler wraps a legacy agent handler (which takes an agentID
// parameter) and reformats its output as a v1 APIResponse. The action
// parameter is the RBAC action name.
func (s *Server) v1WrapAgentHandler(action string, h func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := r.PathValue("id")
		op, ok := s.v1CheckAuth(w, r, action)
		if !ok {
			return
		}
		// Set the operator in the request context so the legacy handler
		// can use operatorIDFromRequest for audit logging.
		r = r.WithContext(context.WithValue(r.Context(), operatorContextKey{}, op))
		rec := httptest.NewRecorder()
		h(rec, r, agentID)
		reformatV1(rec, w)
	}
}

// reformatV1 reads the output captured by a ResponseRecorder from a legacy
// handler and writes it as a v1 APIResponse.
func reformatV1(rec *httptest.ResponseRecorder, w http.ResponseWriter) {
	body := rec.Body.Bytes()
	if rec.Code >= 400 {
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = http.StatusText(rec.Code)
		}
		writeError(w, rec.Code, errorCodeFromStatus(rec.Code), msg)
		return
	}
	// Success: parse the JSON body and wrap it.
	var data interface{}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 0 {
		if err := json.Unmarshal(body, &data); err != nil {
			// Not valid JSON — wrap as a string.
			data = trimmed
		}
	}
	writeJSON(w, rec.Code, data)
}

// ---------------------------------------------------------------------------
// Generic forward handler — for endpoints that have no existing server-side
// HTTP handler but are supported by the agent protocol (fs-list, fs-move,
// fs-mkdir, fs-delete).
// ---------------------------------------------------------------------------

// v1Forward creates a handler that reads the request body (if readBody is
// true), forwards a command to the agent via forwardToAgent, and wraps the
// result in a v1 APIResponse.
func (s *Server) v1Forward(action, msgType string, readBody bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := r.PathValue("id")
		if _, ok := s.v1CheckAuth(w, r, action); !ok {
			return
		}
		var params interface{}
		if readBody {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeError(w, http.StatusBadRequest, "BAD_REQUEST", "failed to read body")
				return
			}
			r.Body.Close()
			if len(body) > 0 {
				params = json.RawMessage(body)
			}
		}
		resp, err := s.forwardToAgent(agentID, msgType, params)
		if err != nil {
			if strings.Contains(err.Error(), "timed out") {
				writeError(w, http.StatusGatewayTimeout, "TIMEOUT", err.Error())
			} else {
				writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
			}
			return
		}
		// forwardToAgent returns a map with "error" key when the agent
		// reports an error — surface that as a v1 error.
		if m, ok := resp.(map[string]interface{}); ok {
			if errMsg, hasErr := m["error"]; hasErr {
				writeError(w, http.StatusServiceUnavailable, "AGENT_ERROR", fmt.Sprintf("%v", errMsg))
				return
			}
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

// registerV1Routes registers all /api/v1/ routes on the server's mux using
// Go 1.22 ServeMux method+path patterns. Existing /api/ routes are kept for
// backward compatibility.
func (s *Server) registerV1Routes() {
	// Server-level endpoints
	s.mux.HandleFunc("GET /api/v1/health", s.handleV1Health)
	s.mux.HandleFunc("GET /api/v1/audit", s.handleV1AuditQuery)

	// Operator management
	s.mux.HandleFunc("GET /api/v1/operators", s.handleV1ListOperators)
	s.mux.HandleFunc("POST /api/v1/operators", s.handleV1CreateOperator)
	s.mux.HandleFunc("DELETE /api/v1/operators/{id}", s.handleV1DeleteOperator)

	// Agent endpoints — read
	s.mux.HandleFunc("GET /api/v1/agents", s.handleV1ListAgents)
	s.mux.HandleFunc("GET /api/v1/agents/{id}", s.handleV1GetAgent)
	s.mux.HandleFunc("DELETE /api/v1/agents/{id}", s.handleV1DeleteAgent)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/health", s.handleV1AgentHealth)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/audit", s.handleV1AgentAudit)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/capabilities", s.handleV1GetAgentCapabilities)

	// Agent endpoints — redeploy
	s.mux.HandleFunc("POST /api/v1/agents/{id}/redeploy", s.handleV1RedeployAgent)

	// Agent endpoints — commands (all POST)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/exec",
		s.v1WrapAgentHandler("exec", s.handleAgentExec))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/fs-list",
		s.v1Forward("fs-list", protocol.TypeFSList, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/fs-read",
		s.v1WrapAgentHandler("fs-read", s.handleAgentFSRead))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/fs-write",
		s.v1WrapAgentHandler("fs-write", s.handleAgentFSWrite))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/fs-stat",
		s.v1WrapAgentHandler("fs-stat", s.handleAgentFSStat))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/fs-hash",
		s.v1WrapAgentHandler("fs-hash", s.handleAgentFSHash))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/fs-move",
		s.v1Forward("fs-move", protocol.TypeFSMove, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/fs-mkdir",
		s.v1Forward("fs-mkdir", protocol.TypeFSMkdir, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/fs-delete",
		s.v1Forward("fs-delete", protocol.TypeFileRemove, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/proc-list",
		s.v1WrapAgentHandler("proc-list", s.handleAgentProcList))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/proc-kill",
		s.v1WrapAgentHandler("proc-kill", s.handleAgentProcKill))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/proc-start",
		s.v1WrapAgentHandler("proc-start", s.handleAgentProcStart))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/capture",
		s.v1WrapAgentHandler("capture", s.handleAgentCapture))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/tunnel",
		s.v1WrapAgentHandler("tunnel", s.handleAgentTunnel))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/tunnel-close",
		s.v1WrapAgentHandler("tunnel-close", s.handleAgentTunnelClose))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/mitm-start",
		s.v1WrapAgentHandler("mitm-start", s.handleAgentMitmStart))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/mitm-stop",
		s.v1WrapAgentHandler("mitm-stop", s.handleAgentMitmStop))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/mitm-traffic",
		s.v1WrapAgentHandler("mitm-traffic", s.handleAgentMitmTraffic))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/debug-attach",
		s.v1WrapAgentHandler("debug-attach", s.handleAgentDebugAttach))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/debug-detach",
		s.v1WrapAgentHandler("debug-detach", s.handleAgentDebugDetach))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/debug-read-mem",
		s.v1WrapAgentHandler("debug-read-mem", s.handleAgentDebugReadMem))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/debug-modules",
		s.v1WrapAgentHandler("debug-modules", s.handleAgentDebugModules))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/debug-mem-query",
		s.v1WrapAgentHandler("debug-mem-query", s.handleAgentDebugMemQuery))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/update",
		s.v1WrapAgentHandler("update", s.handleAgentUpdate))

	// Phase 7: New capability endpoints (all POST)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/socks5-start",
		s.v1Forward("socks5-start", protocol.TypeSocks5Start, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/socks5-stop",
		s.v1Forward("socks5-stop", protocol.TypeSocks5Stop, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/port-forward",
		s.v1Forward("port-forward", protocol.TypePortForward, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/port-scan",
		s.v1Forward("port-scan", protocol.TypePortScan, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/net-connections",
		s.v1Forward("net-connections", protocol.TypeNetConnections, false))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/autostart-enable",
		s.v1Forward("autostart-enable", protocol.TypeAutostartEnable, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/autostart-disable",
		s.v1Forward("autostart-disable", protocol.TypeAutostartDisable, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/autostart-status",
		s.v1Forward("autostart-status", protocol.TypeAutostartStatus, false))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/file-search",
		s.v1Forward("file-search", protocol.TypeFileSearch, true))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/sysinfo",
		s.v1Forward("sysinfo", protocol.TypeSysInfo, false))

	// Enrollment + revocation (Phase 3)
	s.mux.HandleFunc("POST /api/v1/enroll", s.handleV1Enroll)
	s.mux.HandleFunc("POST /api/v1/enrollment-tokens", s.handleV1CreateEnrollmentToken)
	s.mux.HandleFunc("GET /api/v1/enrollment-tokens", s.handleV1ListEnrollmentTokens)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/revoke", s.handleV1RevokeAgent)
	s.mux.HandleFunc("GET /api/v1/agents/revoked", s.handleV1ListRevokedAgents)

	// Agent builder (Phase 4)
	s.mux.HandleFunc("POST /api/v1/builds", s.handleV1CreateBuild)
	s.mux.HandleFunc("GET /api/v1/builds", s.handleV1ListBuilds)
	s.mux.HandleFunc("GET /api/v1/builds/{id}", s.handleV1GetBuild)
	s.mux.HandleFunc("GET /api/v1/builds/{id}/download", s.handleV1DownloadBuild)
	s.mux.HandleFunc("DELETE /api/v1/builds/{id}", s.handleV1DeleteBuild)

	// VirusTotal scan (build-level)
	s.mux.HandleFunc("POST /api/v1/builds/{id}/vt-scan", s.handleV1VTScan)
	s.mux.HandleFunc("GET /api/v1/builds/{id}/vt-scan", s.handleV1GetVTScan)

	// Build profiles (Phase 4)
	s.mux.HandleFunc("GET /api/v1/profiles", s.handleV1ListProfiles)
	s.mux.HandleFunc("POST /api/v1/profiles", s.handleV1CreateProfile)
	s.mux.HandleFunc("GET /api/v1/profiles/{id}", s.handleV1GetProfile)
	s.mux.HandleFunc("DELETE /api/v1/profiles/{id}", s.handleV1DeleteProfile)

	// Task scheduler (Phase 5)
	s.mux.HandleFunc("POST /api/v1/tasks", s.handleV1CreateTask)
	s.mux.HandleFunc("GET /api/v1/tasks", s.handleV1ListTasks)
	s.mux.HandleFunc("GET /api/v1/tasks/{id}", s.handleV1GetTask)
	s.mux.HandleFunc("DELETE /api/v1/tasks/{id}", s.handleV1CancelTask)

	// File transfers (resumable chunked)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/transfer", s.handleV1CreateTransfer)
	s.mux.HandleFunc("GET /api/v1/transfers", s.handleV1ListTransfers)
	s.mux.HandleFunc("GET /api/v1/transfers/{id}", s.handleV1GetTransfer)
	s.mux.HandleFunc("POST /api/v1/transfers/{id}/pause", s.handleV1PauseTransfer)
	s.mux.HandleFunc("POST /api/v1/transfers/{id}/resume", s.handleV1ResumeTransfer)
	s.mux.HandleFunc("POST /api/v1/transfers/{id}/verify", s.handleV1VerifyTransfer)

	// File download (v1)
	s.mux.HandleFunc("GET /api/v1/downloads/{filename}", s.handleV1FileDownload)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/file-download", s.handleV1AgentFileDownload)

	// Screen streaming (v1)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/stream-start", s.handleV1StreamStart)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/stream-stop", s.handleV1StreamStop)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/stream-frame", s.handleV1StreamFrame)

	// Input (mouse/keyboard) (v1)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/pointer-click", s.handleV1PointerClick)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/key-press", s.handleV1KeyPress)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/key-combo", s.handleV1KeyCombo)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/text-input", s.handleV1TextInput)

	// Login endpoint (username/password → operator token)
	s.mux.HandleFunc("POST /api/v1/login", s.handleV1Login)

	// Phase 4: Dynamic mode switching & topology
	s.mux.HandleFunc("POST /api/v1/agents/{id}/mode", s.handleV1AgentMode)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/mode", s.handleV1GetAgentMode)
	s.mux.HandleFunc("GET /api/v1/topology", s.handleV1Topology)
}

// ---------------------------------------------------------------------------
// v1 handlers — server-level and registry/audit/operator endpoints
// ---------------------------------------------------------------------------

// handleV1Health returns server health stats in v1 format.
func (s *Server) handleV1Health(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":         "ok",
		"total_agents":   total,
		"active_agents":  active,
		"stale_agents":   stale,
		"uptime_seconds": int64(time.Since(startTime).Seconds()),
	})
}

// handleV1ListAgents returns all registered agents in v1 format.
func (s *Server) handleV1ListAgents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	agents := s.registry.ListAgents()
	writeJSON(w, http.StatusOK, agents)
}

// handleV1GetAgent returns a single agent's details in v1 format.
func (s *Server) handleV1GetAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	rec, err := s.registry.GetHealth(agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// handleV1DeleteAgent removes (unregisters) an agent in v1 format.
func (s *Server) handleV1DeleteAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	s.registry.Unregister(agentID)
	writeJSON(w, http.StatusOK, map[string]interface{}{"removed": agentID})
}

// handleV1AgentHealth returns a single agent's health record in v1 format.
func (s *Server) handleV1AgentHealth(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "health"); !ok {
		return
	}
	rec, err := s.registry.GetHealth(agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// handleV1AgentAudit returns the audit log for a specific agent in v1 format.
func (s *Server) handleV1AgentAudit(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "audit-read"); !ok {
		return
	}
	results := s.audit.Query(AuditFilter{AgentID: agentID})
	writeJSON(w, http.StatusOK, results)
}

// handleV1AuditQuery queries the audit log with optional filters in v1 format.
func (s *Server) handleV1AuditQuery(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "audit-read"); !ok {
		return
	}
	filter := AuditFilter{
		AgentID:    r.URL.Query().Get("agent_id"),
		OperatorID: r.URL.Query().Get("operator_id"),
		Action:     r.URL.Query().Get("action"),
	}
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			filter.Limit = limit
		}
	}
	if from := r.URL.Query().Get("from"); from != "" {
		if t, err := time.Parse(time.RFC3339, from); err == nil {
			filter.FromTime = t
		}
	}
	if to := r.URL.Query().Get("to"); to != "" {
		if t, err := time.Parse(time.RFC3339, to); err == nil {
			filter.ToTime = t
		}
	}
	results := s.audit.Query(filter)
	writeJSON(w, http.StatusOK, results)
}

// ---------------------------------------------------------------------------
// v1 handlers — operator management
// ---------------------------------------------------------------------------

// handleV1ListOperators returns all operators in v1 format.
func (s *Server) handleV1ListOperators(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}
	ops := s.operators.List()
	writeJSON(w, http.StatusOK, ops)
}

// handleV1CreateOperator creates a new operator in v1 format.
func (s *Server) handleV1CreateOperator(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}
	var params struct {
		Name  string `json:"name"`
		Role  string `json:"role"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	op, err := s.operators.Create(params.Name, params.Role, params.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, op)
}

// handleV1DeleteOperator deletes an operator by ID in v1 format.
func (s *Server) handleV1DeleteOperator(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}
	if !s.operators.Delete(opID) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "operator not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": opID})
}

// handleV1Login authenticates an operator with username/password and returns
// their API token. The request body must contain "username" and "password"
// fields. On success, returns {"ok":true,"data":{"token":"...","operator":{...}}}.
// Returns 401 on invalid credentials.
func (s *Server) handleV1Login(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if params.Username == "" || params.Password == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "username and password are required")
		return
	}

	op := s.operators.GetByName(params.Username)
	if op == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		return
	}
	if !op.CheckPassword(params.Password) {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		return
	}

	// Update last-seen.
	s.operators.UpdateLastSeen(op.ID, time.Now().UTC())

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":    op.Token,
		"operator": op,
	})
}