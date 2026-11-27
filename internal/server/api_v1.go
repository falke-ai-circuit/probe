package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Detail  map[string]interface{} `json:"detail,omitempty"`
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
	writeErrorDetail(w, statusCode, code, message, nil)
}

// writeErrorDetail is writeError with structured detail (P8-03: the elevation
// refusal carries the observed principal and the remediation).
func writeErrorDetail(w http.ResponseWriter, statusCode int, code, message string, detail map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(APIResponse{
		OK:    false,
		Error: &APIError{Code: code, Message: message, Detail: detail},
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
		reformatV1WithAgent(rec, w, agentID)
	}
}

// reformatV1WithAgent is reformatV1 with the agent id in scope. P8-10: a
// command refused because the agent lacks the capability is a typed 409, not a
// 503 transport failure (P8-02 §1.3 row "agent lacks the capability"). The
// legacy handler reports it as a plain 503 string; the v1 surface must not.
func reformatV1WithAgent(rec *httptest.ResponseRecorder, w http.ResponseWriter, agentID string) {
	if rec.Code >= 400 && strings.Contains(rec.Body.String(), "lacks required capability") {
		writeErrorDetail(w, http.StatusConflict, "CAPABILITY_MISSING",
			strings.TrimSpace(rec.Body.String()),
			map[string]interface{}{"agent_id": agentID, "ran": false, "exit_code": nil})
		return
	}
	reformatV1(rec, w)
}

// reformatV1 reads the output captured by a ResponseRecorder from a legacy
// handler and writes it as a v1 APIResponse.
func reformatV1(rec *httptest.ResponseRecorder, w http.ResponseWriter) {
	body := rec.Body.Bytes()
	if rec.Code >= 400 {
		// P13-01: a legacy handler that already wrote the v1 envelope (the
		// strict-decode refusals do) must not be re-derived as BAD_REQUEST
		// with the whole envelope as its message.
		var envelope struct {
			OK    bool      `json:"ok"`
			Error *APIError `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Error != nil && envelope.Error.Code != "" {
			msg := envelope.Error.Message
			if msg == "" {
				msg = http.StatusText(rec.Code)
			}
			writeErrorDetail(w, rec.Code, envelope.Error.Code, msg, envelope.Error.Detail)
			return
		}
		// P8-05: a legacy handler that refused with a typed code (e.g.
		// FS_WRITE_EMPTY_DATA) keeps that code; only untyped bodies fall back
		// to the status-derived code.
		var typed struct {
			Code    string                 `json:"code"`
			Message string                 `json:"message"`
			Detail  map[string]interface{} `json:"detail"`
		}
		if json.Unmarshal(body, &typed) == nil && typed.Code != "" {
			msg := typed.Message
			if msg == "" {
				msg = http.StatusText(rec.Code)
			}
			writeErrorDetail(w, rec.Code, typed.Code, msg, typed.Detail)
			return
		}
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
			} else if d := s.dispatchRefusal(agentID, action, err); d.Code != "" {
				// P21-05: one refusal derivation. A route that died answers
				// ROUTE_GONE through every control path, not only exec.
				writeErrorDetail(w, d.Status, d.Code, d.Message, d.Detail)
			} else {
				writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
			}
			return
		}
		// forwardToAgent returns a map with "error" key when the agent
		// reports an error — surface that as a v1 error. F4: the agent's
		// typed code travels with it, so TIMEOUT is 504 TIMEOUT for every
		// fs op instead of collapsing into 503 AGENT_ERROR.
		if m, ok := resp.(map[string]interface{}); ok {
			if errMsg, hasErr := m["error"]; hasErr {
				code := "AGENT_ERROR"
				if c, ok := m["code"].(string); ok && c != "" {
					code = c
				}
				status := http.StatusServiceUnavailable
				if code == protocol.ErrTimeout {
					status = http.StatusGatewayTimeout
				}
				writeError(w, status, code, fmt.Sprintf("%v", errMsg))
				return
			}
		}
		// P8-18 (P8-17 C1): fs-* fields an agent older than 1.19.0 cannot
		// produce are filled here, on every response (no-op for actions the
		// fallback does not know, and a no-op for fields already present).
		// P8-20: the request offset travels with it — next_offset is a value
		// relative to the caller's position, not to zero.
		resp = s.applyFSIntegrityFallback(agentID, action, "", fsReqOffset(params), resp)
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

	// Replicate — export the probe source as a zip (for MANTLE source_zip)
	s.mux.HandleFunc("GET /api/v1/replicate/source", s.handleV1ReplicateSource)

	// Agent endpoints — read
	s.mux.HandleFunc("GET /api/v1/agents", s.handleV1ListAgents)

	// P22-02 (spec P1): one inventory endpoint. servers[0] is this process.
	// There is deliberately no second endpoint for the badge.
	s.mux.HandleFunc("GET /api/v1/servers", s.handleV1Servers)
	s.mux.HandleFunc("GET /api/v1/agents/{id}", s.handleV1GetAgent)
	s.mux.HandleFunc("DELETE /api/v1/agents/{id}", s.handleV1DeleteAgent)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/health", s.handleV1AgentHealth)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/audit", s.handleV1AgentAudit)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/capabilities", s.handleV1GetAgentCapabilities)

	// P13-02 §6: read-only context enumeration. No selector: it reports which
	// contexts exist and what can be done in each, it does not act on one.
	s.mux.HandleFunc("GET /api/v1/agents/{id}/contexts", s.handleV1Contexts)

	// Agent endpoints — redeploy
	s.mux.HandleFunc("POST /api/v1/agents/{id}/redeploy", s.handleV1RedeployAgent)

	// Agent endpoints — commands (all POST)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/exec",
		s.v1WrapAgentHandler("exec", s.handleAgentExec))
	// P8-04: detached job queries and the server's witness records.
	s.mux.HandleFunc("POST /api/v1/agents/{id}/job-status", s.handleV1JobStatus())
	s.mux.HandleFunc("POST /api/v1/agents/{id}/job-log", s.handleV1JobLog())
	s.mux.HandleFunc("GET /api/v1/agents/{id}/jobs", s.handleV1AgentJobs())
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
	// P8-10 (P8-02 §8.3): watch is a server-side long-poll over fs-read.
	// Additive route — no existing command or response shape changes.
	s.mux.HandleFunc("POST /api/v1/agents/{id}/watch", s.handleV1Watch)
	// P8-06 (P8-02 §5): raw-body streaming transport. Additive routes — no
	// existing response shape changes, old clients never call them.
	s.mux.HandleFunc("POST /api/v1/agents/{id}/fs/upload", s.handleV1FSRawUpload)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/fs/download", s.handleV1FSDownload)
	// P8-07 (§6 C-CRED): write-only credential store. PUT writes, GET is
	// metadata-only by type construction.
	s.registerCredentialRoutes()
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
	s.mux.HandleFunc("GET /api/v1/agents/{id}/tunnels",
		s.v1WrapAgentHandler("tunnel", s.handleAgentTunnelList))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/sniff",
		s.v1WrapAgentHandler("sniff", s.handleAgentSniff))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/sniff-stop",
		s.v1WrapAgentHandler("sniff", s.handleAgentSniffStop))
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

	// P9-11: streamed artifact upload into the managed artifacts directory.
	// Additive route — no existing command or response shape changes.
	s.mux.HandleFunc("POST /api/v1/files", s.handleV1FileUpload)

	// Sensors (v1.14.0)
	// sensor GET/PUT use raw route registration (no v1WrapAgentHandler) because
	// the handlers write their own APIResponse envelope directly. (Reviewer #1 fix.)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/sensors", s.handleV1GetAgentSensorsRaw)
	s.mux.HandleFunc("PUT /api/v1/agents/{id}/sensors", s.handleV1SetAgentSensorsRaw)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/sensors/{name}/enable", s.v1WrapSensorHandler("exec", s.handleV1EnableSensor))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/sensors/{name}/disable", s.v1WrapSensorHandler("exec", s.handleV1DisableSensor))
	// Flow runtime (v1.13.0)
	s.mux.HandleFunc("GET /api/v1/flows", s.handleV1ListFlows)
	s.mux.HandleFunc("POST /api/v1/flows", s.handleV1CreateFlow)
	s.mux.HandleFunc("GET /api/v1/flows/{id}", s.v1WrapFlowHandler("list", s.handleV1GetFlow))
	s.mux.HandleFunc("PUT /api/v1/flows/{id}", s.v1WrapFlowHandler("exec", s.handleV1UpdateFlow))
	s.mux.HandleFunc("DELETE /api/v1/flows/{id}", s.v1WrapFlowHandler("exec", s.handleV1DeleteFlow))
	s.mux.HandleFunc("POST /api/v1/flows/{id}/enable", s.v1WrapFlowHandler("exec", s.handleV1EnableFlow))
	s.mux.HandleFunc("POST /api/v1/flows/{id}/disable", s.v1WrapFlowHandler("exec", s.handleV1DisableFlow))
	s.mux.HandleFunc("POST /api/v1/flows/{id}/run-now", s.v1WrapFlowHandler("exec", s.handleV1FlowRunNow))
	s.mux.HandleFunc("POST /api/v1/flows/{id}/assign", s.v1WrapFlowHandler("exec", s.handleV1AssignFlowToAgent))
	s.mux.HandleFunc("POST /api/v1/flows/{id}/unassign", s.v1WrapFlowHandler("exec", s.handleV1UnassignFlowFromAgent))
	s.mux.HandleFunc("GET /api/v1/flow-runs", s.handleV1ListFlowRuns)
	s.mux.HandleFunc("GET /api/v1/flow-runs/{id}", s.v1WrapFlowHandler("list", s.handleV1GetFlowRun))
	s.mux.HandleFunc("GET /api/v1/agents/{id}/flows", s.v1WrapAgentHandler("list", s.handleV1ListAgentFlows))
	s.mux.HandleFunc("GET /api/v1/flow-templates", s.handleV1ListFlowTemplates)
	s.mux.HandleFunc("POST /api/v1/flows/from-template", s.handleV1InstantiateFromTemplate)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/survey", s.handleV1ListAgentSurveyEventsRaw)

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
	s.mux.HandleFunc("POST /api/v1/transfers/{id}/cancel", s.handleV1CancelTransfer)
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

	// P13-01 §5.5 rows 6, 8, 9, 13, 14 — the new selector-bearing routes.
	s.registerStrictSelectorRoutes()

	// Login endpoint (username/password → operator token)
	s.mux.HandleFunc("POST /api/v1/login", s.handleV1Login)

	// Phase 4: Dynamic mode switching & topology
	s.mux.HandleFunc("POST /api/v1/agents/{id}/mode", s.handleV1AgentMode)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/mode", s.handleV1GetAgentMode)
	s.mux.HandleFunc("GET /api/v1/topology", s.handleV1Topology)
	s.mux.HandleFunc("POST /api/v1/reconfigure", s.handleV1ReconfigureAll)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/reconfigure", s.handleV1ReconfigureAgent)
	s.mux.HandleFunc("POST /api/v1/agents/{id}/forward-policy", s.handleV1ForwardPolicy)
	s.mux.HandleFunc("GET /api/v1/agents/{id}/forward-policy", s.handleV1ForwardPolicy)
	s.mux.HandleFunc("GET /api/v1/forward-policies", s.handleV1ListForwardPolicies)

	// Security management endpoints
	s.registerSecurityRoutes()

	// P21-07: relay identity — fingerprints only, and one operator action to
	// rotate one node's credential.
	s.mux.HandleFunc("GET /api/v1/relays", s.handleV1Relays)
	s.mux.HandleFunc("POST /api/v1/relays/{id}/rotate", s.handleV1RotateRelayCredential)

	// P18-04 (§11.4): read-only surface for the effective network/security
	// toggle values. Secrets-free by type construction (P17-03).
	s.registerSettingsRoutes()

	// P21-08: one chain object per path, with per-hop state and the failing
	// hop named. Its own endpoint: topology stays an edge list.
	s.mux.HandleFunc("GET /api/v1/chain", s.handleV1Chain)
}

// ---------------------------------------------------------------------------
// v1 handlers — server-level and registry/audit/operator endpoints
// ---------------------------------------------------------------------------

// handleV1Health returns server health stats in v1 format.
//
// P12-03: the aggregate is derived from each agent's stale flag, not from
// its stored status — an agent that cannot execute counts as not serving.
func (s *Server) handleV1Health(w http.ResponseWriter, r *http.Request) {
	agents := s.registry.ListAgents()
	s.decorateAgentListLiveness(agents)
	total := len(agents)
	active := 0
	stale := 0
	pingTimeouts := 0
	for _, a := range agents {
		if a.Stale {
			stale++
		} else {
			active++
		}
		pingTimeouts += a.PingTimeouts
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":                  "ok",
		"server_version":          s.version,
		"total_agents":            total,
		"active_agents":           active,
		"stale_agents":            stale,
		"ping_timeouts_total":     pingTimeouts,
		"stale_threshold_seconds": int64(staleThreshold.Seconds()),
		"uptime_seconds":          int64(time.Since(startTime).Seconds()),
		// P22-02 §3.1: a four-key projection of the SAME builder that
		// produces servers[0], so the login screen and the authenticated
		// badge cannot disagree. Nothing here reads a request header.
		"identity": s.preAuthIdentity(),
	})
}

// handleV1ListAgents returns all registered agents in v1 format.
func (s *Server) handleV1ListAgents(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	agents := s.registry.ListAgents()
	s.decorateAgentListLiveness(agents)
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
	s.decorateAgentLiveness(&rec)
	writeJSON(w, http.StatusOK, rec)
}

// handleV1DeleteAgent deletes an agent from the registry in v1 format.
// Unlike a disconnect (which calls Unregister and keeps the row as
// "inactive"), an operator DELETE removes the row from memory and from
// registry.json. A live connection is refused rather than silently
// orphaned: the agent would keep running while its row disappeared.
func (s *Server) handleV1DeleteAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "agent id required")
		return
	}
	// P21-05: presence in the conn-map OR a live relay route both mean the
	// agent still owns a transport, and neither may be deleted underneath.
	// (The conn-map check is deliberately a presence check, not the dispatch
	// resolver: deleting a row is unsafe whenever a transport exists, even
	// one the dispatcher would refuse for want of a write mutex.)
	s.mu.RLock()
	_, connected := s.conns[agentID]
	s.mu.RUnlock()
	if !connected {
		if route, found := s.relayRouteFor(agentID); found && route.live() {
			connected = true
		}
	}
	if connected {
		writeError(w, http.StatusConflict, "AGENT_CONNECTED",
			fmt.Sprintf("agent %s is connected; disconnect it before deleting", agentID))
		return
	}
	if !s.registry.Remove(agentID) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("agent %s not found", agentID))
		return
	}
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
	s.decorateAgentLiveness(&rec)
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
//
// Security: login attempts are rate-limited per IP (5 failures in 5 min →
// 15-min lockout). Every attempt is recorded in the audit log.
func (s *Server) handleV1Login(w http.ResponseWriter, r *http.Request) {
	sourceIP := clientIP(r)
	userAgent := r.Header.Get("User-Agent")

	// Check IP lockout from previous failures.
	if s.loginLimiter.isLocked(sourceIP) {
		s.logLoginAttempt("login_failed", "", sourceIP, userAgent, true)
		log.Printf("[security] login blocked from locked IP %s", sourceIP)
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED",
			"too many failed login attempts — try again later")
		return
	}

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
		locked := s.loginLimiter.recordFailure(sourceIP)
		s.logLoginAttempt("login_failed", params.Username, sourceIP, userAgent, locked)
		log.Printf("[security] login failed for %q from %s (unknown user)", params.Username, sourceIP)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		return
	}
	if !op.CheckPassword(params.Password) {
		locked := s.loginLimiter.recordFailure(sourceIP)
		s.logLoginAttempt("login_failed", params.Username, sourceIP, userAgent, locked)
		log.Printf("[security] login failed for %q from %s (bad password)", params.Username, sourceIP)
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid credentials")
		return
	}

	// Success — reset failure counter.
	s.loginLimiter.recordSuccess(sourceIP)
	s.logLoginAttempt("login_success", params.Username, sourceIP, userAgent, false)
	log.Printf("[security] login success for %q from %s", params.Username, sourceIP)

	// Update last-seen.
	s.operators.UpdateLastSeen(op.ID, time.Now().UTC())

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token":    op.Token,
		"operator": op,
	})
}
