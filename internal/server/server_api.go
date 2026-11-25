package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// handleAgentRoute dispatches /api/agent/{id}/... routes.
func (s *Server) handleAgentRoute(w http.ResponseWriter, r *http.Request) {
	if !s.checkAPIAuth(w, r) {
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/agent/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 {
		http.Error(w, "agent ID required", http.StatusBadRequest)
		return
	}

	agentID := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	// Special case: /api/agent/file/{filename} serves files from the download directory.
	// This routes file downloads through the /api/agent/ prefix which Docker proxies forward.
	if agentID == "file" && action != "" {
		s.handleFileDownload(w, r)
		return
	}

	// Special case: /api/agent/{any-id}/download/{filename} serves files from the download directory.
	// This works through Docker proxies that only forward /api/agent/{known-id}/* paths.
	if action != "" && strings.HasPrefix(action, "download/") {
		s.handleFileDownload(w, r)
		return
	}

	// Special case: /api/agent/{any-id}/file-download serves a file specified in the JSON body.
	// This is a 2-level path that works through Docker proxies that only forward 2-level paths.
	if action == "file-download" && r.Method == http.MethodPost {
		s.handleFileDownloadBody(w, r)
		return
	}

	// RBAC: check operator permissions for the requested action and set
	// the operator in the request context for audit logging.
	rbacAction := actionToRBAC(action)
	op, ok := s.checkOperatorAuth(r, rbacAction)
	if !ok {
		s.auditDenied(agentID, rbacAction, op)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), operatorContextKey{}, op))

	switch {
	case action == "exec" && r.Method == http.MethodPost:
		s.handleAgentExec(w, r, agentID)
	case action == "fs-read" && r.Method == http.MethodPost:
		s.handleAgentFSRead(w, r, agentID)
	case action == "fs-write" && r.Method == http.MethodPost:
		s.handleAgentFSWrite(w, r, agentID)
	case action == "fs-stat" && r.Method == http.MethodPost:
		s.handleAgentFSStat(w, r, agentID)
	case action == "fs-hash" && r.Method == http.MethodPost:
		s.handleAgentFSHash(w, r, agentID)
	case action == "capture" && r.Method == http.MethodPost:
		s.handleAgentCapture(w, r, agentID)
	case action == "task-list" && r.Method == http.MethodPost:
		s.handleAgentTaskList(w, r, agentID)
	case action == "proc-list" && r.Method == http.MethodPost:
		s.handleAgentProcList(w, r, agentID)
	case action == "proc-kill" && r.Method == http.MethodPost:
		s.handleAgentProcKill(w, r, agentID)
	case action == "proc-start" && r.Method == http.MethodPost:
		s.handleAgentProcStart(w, r, agentID)
	case action == "tunnel" && r.Method == http.MethodPost:
		s.handleAgentTunnel(w, r, agentID)
	case action == "tunnels" && r.Method == http.MethodGet:
		s.handleAgentTunnelList(w, r, agentID)
	case action == "tunnel-close" && r.Method == http.MethodPost:
		s.handleAgentTunnelClose(w, r, agentID)
	case action == "sniff" && r.Method == http.MethodPost:
		s.handleAgentSniff(w, r, agentID)
	case action == "sniff-stop" && r.Method == http.MethodPost:
		s.handleAgentSniffStop(w, r, agentID)
	case action == "mitm-start" && r.Method == http.MethodPost:
		s.handleAgentMitmStart(w, r, agentID)
	case action == "mitm-stop" && r.Method == http.MethodPost:
		s.handleAgentMitmStop(w, r, agentID)
	case action == "mitm-traffic" && r.Method == http.MethodPost:
		s.handleAgentMitmTraffic(w, r, agentID)
	case action == "debug-attach" && r.Method == http.MethodPost:
		s.handleAgentDebugAttach(w, r, agentID)
	case action == "debug-detach" && r.Method == http.MethodPost:
		s.handleAgentDebugDetach(w, r, agentID)
	case action == "debug-read-mem" && r.Method == http.MethodPost:
		s.handleAgentDebugReadMem(w, r, agentID)
	case action == "debug-modules" && r.Method == http.MethodPost:
		s.handleAgentDebugModules(w, r, agentID)
	case action == "debug-mem-query" && r.Method == http.MethodPost:
		s.handleAgentDebugMemQuery(w, r, agentID)
	case action == "update" && r.Method == http.MethodPost:
		s.handleAgentUpdate(w, r, agentID)
	case action == "health" && r.Method == http.MethodGet:
		s.handleAgentHealth(w, r, agentID)
	// Phase 7: New capability endpoints (forward to agent via v1Forward pattern)
	case action == "socks5-start" && r.Method == http.MethodPost:
		s.handleAgentGenericForward(w, r, agentID, protocol.TypeSocks5Start)
	case action == "socks5-stop" && r.Method == http.MethodPost:
		s.handleAgentGenericForward(w, r, agentID, protocol.TypeSocks5Stop)
	case action == "port-forward" && r.Method == http.MethodPost:
		s.handleAgentGenericForward(w, r, agentID, protocol.TypePortForward)
	case action == "port-scan" && r.Method == http.MethodPost:
		s.handleAgentGenericForward(w, r, agentID, protocol.TypePortScan)
	case action == "net-connections" && r.Method == http.MethodPost:
		s.handleAgentGenericForward(w, r, agentID, protocol.TypeNetConnections)
	case action == "autostart-enable" && r.Method == http.MethodPost:
		s.handleAgentGenericForward(w, r, agentID, protocol.TypeAutostartEnable)
	case action == "autostart-disable" && r.Method == http.MethodPost:
		s.handleAgentGenericForward(w, r, agentID, protocol.TypeAutostartDisable)
	case action == "autostart-status" && r.Method == http.MethodPost:
		s.handleAgentGenericForward(w, r, agentID, protocol.TypeAutostartStatus)
	case action == "file-search" && r.Method == http.MethodPost:
		s.handleAgentGenericForward(w, r, agentID, protocol.TypeFileSearch)
	case action == "sysinfo" && r.Method == http.MethodPost:
		s.handleAgentGenericForward(w, r, agentID, protocol.TypeSysInfo)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// handleListAgents returns the list of registered agents as JSON.
func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if !s.checkAPIAuth(w, r) {
		return
	}
	// RBAC: viewers and above can list agents.
	op, ok := s.checkOperatorAuth(r, "list")
	if !ok {
		s.auditDenied("", "list", op)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), operatorContextKey{}, op))
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	agents := s.registry.ListAgents()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(agents)
}


// handleAgentRoute dispatches /api/agent/{id}/... routes.


// handleAgentExec executes a shell command ON THE REMOTE AGENT via WebSocket.
// Uses forwardToAgent with a per-command timeout (default 60s, from params).
func (s *Server) handleAgentExec(w http.ResponseWriter, r *http.Request, agentID string) {
	// P13-01 E3a: strict decode against this route's own allow-list before any
	// dispatch. Reads the body once; refuses unknown/duplicate/mistyped keys.
	params, ok := decodeStrict[protocol.ExecParams](w, r, allowExec)
	if !ok {
		return
	}
	if !s.preDispatchSelector(w, agentID, params) {
		return
	}

	if params.Timeout <= 0 {
		params.Timeout = 60
	}

	// P8-03: validate `as` before dispatch. An old agent ignores unknown
	// fields, so an unvalidated "SYSTEM" would run the command as flk and
	// report success — the exact silent lie this unit exists to close.
	wantSystem := strings.EqualFold(strings.TrimSpace(params.As), "SYSTEM")
	if a := strings.TrimSpace(params.As); a != "" && !strings.EqualFold(a, "current") && !wantSystem {
		writeTypedErrorDetail(w, http.StatusBadRequest, protocol.ErrInvalidParams,
			fmt.Sprintf("as must be \"current\" or \"SYSTEM\", got %q", params.As),
			map[string]interface{}{"call": "exec", "agent_id": agentID, "requested": params.As})
		return
	}

	startedAt := time.Now().UTC()
	tzName, tzOff := serverTZ()

	// P8-03: gate on the explicit capability, NOT the permissive "empty means
	// all" rule — an old agent advertises nothing and must be refused in
	// milliseconds rather than dispatched into a silent unelevated run.
	if wantSystem {
		if rec, herr := s.registry.GetHealth(agentID); herr == nil {
			if !hasCapabilityStrict(rec.Capabilities, "exec.system") {
				writeTypedErrorDetail(w, http.StatusConflict, protocol.ErrUnsupportedAgent,
					fmt.Sprintf("agent %s does not implement exec{as:\"SYSTEM\"} (capability exec.system absent); "+
						"it would ignore the field and run the command unelevated", agentID),
					map[string]interface{}{
						"call":           "exec",
						"agent_id":       agentID,
						"version":        rec.Version,
						"requested":      "SYSTEM",
						"capabilities":   rec.Capabilities,
						"started_at_utc": startedAt.Format(time.RFC3339Nano),
						"tz":             tzName,
						"tz_offset_min":  tzOff,
						"ran":            false,
						"exit_code":      nil,
						"remediation":    "upgrade the agent to v1.19+ (which advertises exec.system), then install it as a service so its principal is LocalSystem",
					})
				return
			}
		}
	}

	timeout := time.Duration(params.Timeout) * time.Second

	// P8-07 (§6 C-CRED): a referenced credential is resolved to a sealed,
	// single-use handle here and delivered in its OWN envelope immediately
	// before the exec. The value never enters params (params are audited by
	// design), and the redactor is seeded for the duration of the call.
	var credRef string
	var credHits0 int64
	var credCleanups []func()
	defer func() {
		for _, f := range credCleanups {
			f()
		}
	}()
	if name := strings.TrimSpace(params.Credential); name != "" {
		ref, cleanup, cerr := s.sealCredentialForExec(agentID, name, params.JobID, params.CredentialMode)
		if cerr != nil {
			writeTypedErrorDetail(w, cerr.Status, cerr.Code, cerr.Message, map[string]interface{}{
				"call":       "exec",
				"agent_id":   agentID,
				"credential": name,
				"ran":        false,
				"exit_code":  nil,
			})
			return
		}
		// P8-18 (P8-17 C6): use-time enforcement at the server's single
		// decision point — single-use, TTL and the job/agent binding all live
		// in handleRegistry.Use. A handle minted for job J presented by a call
		// claiming another job is refused here, before the exec is dispatched.
		if _, code := s.credHandles.Use(ref, agentID, params.JobID); code != "" {
			cleanup()
			writeTypedErrorDetail(w, http.StatusConflict, code,
				fmt.Sprintf("credential handle %s is not usable for this call (%s)", ref, code),
				map[string]interface{}{
					"call":       "exec",
					"agent_id":   agentID,
					"credential": name,
					"job_id":     params.JobID,
					"ran":        false,
					"exit_code":  nil,
				})
			return
		}
		credRef = ref
		credHits0 = s.redactor.Hits()
		credCleanups = append(credCleanups, cleanup)
	}

	// P8-10 (P8-02 §8.4): idempotency is claimed AFTER every pre-dispatch
	// refusal (param validation, elevation gate, credential sealing) and
	// immediately BEFORE the command can reach the agent. A claim that never
	// dispatches is aborted below; a claim that dispatches but returns no
	// result is marked unknown and refuses replay rather than re-running.
	idemKey := strings.TrimSpace(params.IdempotencyKey)
	if idemKey != "" {
		if err := validateIdempotencyKey(idemKey); err != nil {
			writeTypedErrorDetail(w, http.StatusBadRequest, protocol.ErrInvalidParams, err.Error(),
				map[string]interface{}{"call": "exec", "agent_id": agentID, "ran": false, "exit_code": nil})
			return
		}
		stored, state, ierr := s.idempotency().begin(agentID, idemKey)
		switch state {
		case "replay":
			var body map[string]interface{}
			if err := json.Unmarshal(stored, &body); err != nil {
				body = map[string]interface{}{}
			}
			body["replayed"] = true
			body["idempotency_key"] = idemKey
			w.Header().Set("Content-Type", "application/json")
			w.Write(s.redactBody(body))
			return
		case "inflight":
			writeTypedErrorDetail(w, http.StatusConflict, "IDEMPOTENCY_IN_FLIGHT",
				"a call with this idempotency_key is still in flight; its result is not available yet",
				map[string]interface{}{"call": "exec", "agent_id": agentID, "idempotency_key": idemKey,
					"ran": false, "exit_code": nil})
			return
		case "unknown":
			writeTypedErrorDetail(w, http.StatusConflict, "IDEMPOTENCY_STATE_UNKNOWN",
				"the command may or may not have run; inspect the node before retrying",
				map[string]interface{}{"call": "exec", "agent_id": agentID, "idempotency_key": idemKey,
					"ran": false, "exit_code": nil})
			return
		}
		if ierr != nil {
			writeTypedErrorDetail(w, http.StatusInternalServerError, protocol.ErrInternal, ierr.Error(),
				map[string]interface{}{"call": "exec", "agent_id": agentID, "ran": false, "exit_code": nil})
			return
		}
	}

	// The key is server-side bookkeeping: it stays out of the params that
	// reach the agent, so an old agent never sees a field it cannot interpret.
	params.IdempotencyKey = ""

	var resp interface{}
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		resp, err = s.forwardToAgentWithTimeout(agentID, protocol.TypeExec, params, timeout, operatorIDFromRequest(r))
		if err != nil || !isCredentialExpired(resp) {
			break
		}
		// §6.3(5): an expired handle is re-minted exactly once. Prompting is
		// not a fallback this server has.
		if attempt == 0 {
			ref, cleanup, cerr := s.sealCredentialForExec(agentID, strings.TrimSpace(params.Credential), params.JobID, params.CredentialMode)
			if cerr != nil {
				break
			}
			credRef = ref
			credCleanups = append(credCleanups, cleanup)
		}
	}
	if err != nil {
		// P8-10: a call that never produced a result must not look replayable
		// as a success. A timeout may have reached the agent (unknown); any
		// other transport failure did not dispatch, so the claim is released.
		if idemKey != "" {
			if strings.Contains(err.Error(), "timed out") {
				s.idempotency().markUnknown(agentID, idemKey)
			} else {
				s.idempotency().abort(agentID, idemKey)
			}
		}
		// Amendment A-1: a call that did not produce a result is never a 200.
		if strings.Contains(err.Error(), "timed out") {
			writeTypedErrorDetail(w, http.StatusGatewayTimeout, "DEADLINE_EXCEEDED",
				fmt.Sprintf("exec did not return within %d ms", params.Timeout*1000),
				map[string]interface{}{
					"call":           "exec",
					"agent_id":       agentID,
					"deadline_ms":    params.Timeout * 1000,
					"started_at_utc": startedAt.Format(time.RFC3339Nano),
					"tz":             tzName,
					"tz_offset_min":  tzOff,
					"timed_out":      true,
					"ran":            false,
					"exit_code":      nil,
				})
			return
		}
		code := "AGENT_UNAVAILABLE"
		status := http.StatusServiceUnavailable
		if strings.Contains(err.Error(), "lacks required capability") {
			code = "CAPABILITY_MISSING"
			status = http.StatusConflict
		}
		writeTypedErrorDetail(w, status, code, err.Error(), map[string]interface{}{
			"call":           "exec",
			"agent_id":       agentID,
			"started_at_utc": startedAt.Format(time.RFC3339Nano),
			"tz":             tzName,
			"tz_offset_min":  tzOff,
			"ran":            false,
			"exit_code":      nil,
		})
		return
	}

	// P8-03/Amendment A-1: an agent refusal is an error envelope, never a 200
	// carrying stderr text. The typed code decides the HTTP status.
	if m, ok := resp.(map[string]interface{}); ok {
		if errMsg, hasErr := m["error"]; hasErr {
			code, _ := m["code"].(string)
			status := http.StatusInternalServerError
			switch code {
			case protocol.ErrElevationUnavailable, protocol.ErrUnsupportedAgent,
				protocol.ErrGuardianUnavailable, "CAPABILITY_MISSING",
				protocol.ErrCredentialExpired:
				status = http.StatusConflict
			case protocol.ErrTimeout:
				status = http.StatusGatewayTimeout
			case protocol.ErrPermissionDenied, "permission_denied":
				status = http.StatusForbidden
			case protocol.ErrInvalidParams:
				status = http.StatusBadRequest
			case "":
				code = protocol.ErrInternal
			}
			detail, _ := m["detail"].(map[string]interface{})
			if detail == nil {
				detail = map[string]interface{}{}
			}
			detail["call"] = "exec"
			detail["agent_id"] = agentID
			detail["started_at_utc"] = startedAt.Format(time.RFC3339Nano)
			detail["tz"] = tzName
			detail["tz_offset_min"] = tzOff
			detail["ran"] = false
			if _, has := detail["exit_code"]; !has {
				detail["exit_code"] = nil
			}
			// P8-10: the agent refused, so the command did not run. Releasing
			// the claim lets a corrected call reuse the key.
			if idemKey != "" {
				s.idempotency().abort(agentID, idemKey)
			}
			writeTypedErrorDetail(w, status, code, fmt.Sprintf("%v", errMsg), detail)
			return
		}
	}

	// P13-01 E2: a context-scoped exec result must carry context_ref. Placed
	// after the agent-error branch (a refusal has no result envelope to assert
	// on) and before the response is recorded or written.
	if !s.assertContextRef(w, agentID, params, resp) {
		return
	}

	s.sessions.AddMemory(agentID, "last_exec", params.Command)
	// P8-04: remember a detached launch as a witness record. Foreground exec
	// responses carry no job_id and are ignored.
	s.recordDetachedJob(agentID, resp)
	// P8-10: the envelope carries `replayed` on every exec response, so a
	// caller never has to infer whether a body came from the agent or the
	// idempotency journal. Store the redacted body before writing it.
	if m, ok := resp.(map[string]interface{}); ok {
		m["replayed"] = false
		if idemKey != "" {
			m["idempotency_key"] = idemKey
		}
	}
	// P8-07: the credential result is metadata — name, handle ref, whether it
	// was used, and how many redactions the call performed. The value is not
	// an input to this struct.
	if credRef != "" {
		if m, ok := resp.(map[string]interface{}); ok {
			m["credential"] = credentialResult{
				Name:     params.Credential,
				Ref:      credRef,
				Used:     true,
				Redacted: int(s.redactor.Hits() - credHits0),
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	// Every response in this path leaves through the redactor, so a handler
	// that echoed the value would still be filtered at the last hop.
	outBody := s.redactBody(resp)
	if idemKey != "" {
		s.idempotency().complete(agentID, idemKey, outBody)
	}
	w.Write(outBody)
}

// hasCapabilityStrict reports whether cap is listed explicitly. It differs
// from HasCapability (empty list = all) on purpose: a pre-1.19 agent never
// advertises exec.system, and dispatching to it would be a silent failure.
func hasCapabilityStrict(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// writeTypedErrorDetail writes an error body carrying both the typed code and
// structured detail. reformatV1 forwards the detail onto the v1 envelope.
func writeTypedErrorDetail(w http.ResponseWriter, status int, code, message string, detail map[string]interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"code":    code,
		"message": message,
		"detail":  detail,
	})
}

// serverTZ declares the server's zone for envelopes whose clock source is the
// server (agent unreachable, refusal before dispatch).
func serverTZ() (string, int) {
	now := time.Now()
	_, off := now.Zone()
	return now.Location().String(), off / 60
}

func mustMarshalRaw(v interface{}) []byte {
	data, _ := json.Marshal(v)
	return data
}


// handleAgentFSRead reads a file ON THE REMOTE AGENT via WebSocket.
func (s *Server) handleAgentFSRead(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.FSParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// P8-06 (P8-02 §4.3.2): the server-side cap. A caller that asks for no
	// limit on a file larger than the cap gets a declared window instead of a
	// whole-file buffer. PROBE_FS_READ_MAX_BYTES=0 restores unlimited.
	if maxBytes := fsReadMaxBytes(); maxBytes > 0 {
		if params.Limit <= 0 || int64(params.Limit) > maxBytes {
			params.Limit = int(maxBytes)
		}
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeFSRead, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// P8-18 (P8-17 C1): the fallback runs on EVERY fs-read response, not only
	// inside `if truncated` — a v1.18.6 agent never sets truncated, so the old
	// branch was dead for exactly the population it existed for. The window
	// digest is derived from the decoded bytes and next_offset from the
	// REQUESTED offset plus that window (P8-20: a pre-1.19 agent does not echo
	// the offset, so it has to be carried in from here).
	resp = s.applyFSIntegrityFallback(agentID, "fs-read", params.Path, params.Offset, resp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}


// handleAgentFSWrite writes a file ON THE REMOTE AGENT via WebSocket.
// Retries up to 3 times on transient failures (timeout, WS write error).
func (s *Server) handleAgentFSWrite(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.FSParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// Retry loop: transient WS failures (timeout, brief disconnect) are common
	// during large chunked transfers. Retry up to 3 times with 1s backoff.
	var resp interface{}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = s.forwardToAgent(agentID, protocol.TypeFileSave, params)
		if err == nil {
			break
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	if lastErr != nil {
		http.Error(w, lastErr.Error(), http.StatusServiceUnavailable)
		return
	}
	// P8-05: an agent refusal is a refusal, not a 200 with a message inside.
	// The typed code is preserved so the caller can branch on it.
	if m, ok := resp.(map[string]interface{}); ok {
		if errMsg, hasErr := m["error"]; hasErr {
			code, _ := m["code"].(string)
			status := http.StatusUnprocessableEntity
			switch code {
			case protocol.ErrFSWriteEmptyData, protocol.ErrFSWriteEmpty:
				status = http.StatusBadRequest
			case protocol.ErrTimeout:
				status = http.StatusGatewayTimeout
			case "":
				code = protocol.ErrInternal
			}
			writeTypedError(w, status, code, fmt.Sprintf("%v", errMsg))
			return
		}
	}
	// P8-18 (P8-17 C1): fs-write integrity for a pre-1.19 agent — digest is
	// read back from the same agent after the write.
	resp = s.applyFSIntegrityFallback(agentID, "fs-write", params.Path, params.Offset, resp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// writeTypedError writes a body carrying both the human message and the typed
// agent error code, so reformatV1 can keep the code on the v1 surface.
func writeTypedError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}

// POST /api/agent/{id}/fs-stat  body: {"path":"C:\	emp\\file.exe"}
func (s *Server) handleAgentFSStat(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.FSParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeFSStat, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	resp = s.applyFSIntegrityFallback(agentID, "fs-stat", params.Path, params.Offset, resp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAgentFSHash returns SHA256 hash of a file on the remote agent.
// POST /api/agent/{id}/fs-hash  body: {"path":"C:\	emp\\file.exe"}
// Returns: {"path":"...","sha256":"abc123...","size":9321472}
func (s *Server) handleAgentFSHash(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.FSParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeFSHash, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	resp = s.applyFSIntegrityFallback(agentID, "fs-hash", params.Path, params.Offset, resp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}


// handleAgentCapture captures display ON THE REMOTE AGENT via WebSocket.
func (s *Server) handleAgentCapture(w http.ResponseWriter, r *http.Request, agentID string) {
	params, ok := decodeStrict[protocol.ScreenParams](w, r, allowCapture)
	if !ok {
		return
	}
	if !s.preDispatchSelector(w, agentID, params) {
		return
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeCapture, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// P14-03: the agent's §10 refusals (REGION_TOO_LARGE 400, decode failure
	// 500, CONSENT_REQUIRED 403) travel as typed errors. Relaying them as a
	// 200 body would report a refused capture as a capture.
	if writeAgentRefusal(w, resp) {
		return
	}
	if !s.assertContextRef(w, agentID, params, resp) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}


// handleAgentTaskList lists processes ON THE REMOTE AGENT via WebSocket.
func (s *Server) handleAgentTaskList(w http.ResponseWriter, r *http.Request, agentID string) {
	resp, err := s.forwardToAgent(agentID, protocol.TypeTaskList, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}


// handleAgentProcList lists processes ON THE REMOTE AGENT via WebSocket (new).
func (s *Server) handleAgentProcList(w http.ResponseWriter, r *http.Request, agentID string) {
	resp, err := s.forwardToAgent(agentID, protocol.TypeProcList, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}


// handleAgentProcKill kills a process ON THE REMOTE AGENT via WebSocket.
func (s *Server) handleAgentProcKill(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.TaskStopParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeProcKill, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}


// handleAgentProcStart starts a process ON THE REMOTE AGENT via WebSocket.
func (s *Server) handleAgentProcStart(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.ProcStartParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeProcStart, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}


// forwardToAgent sends a command to the agent via WebSocket and waits for response.
// This is the generic request-response pattern used by all forwarded handlers.
// Uses a default 120-second timeout. The operatorID is used for audit logging
// (empty string for server-initiated commands).
func (s *Server) forwardToAgent(agentID string, msgType string, params interface{}) (interface{}, error) {
	return s.forwardToAgentWithTimeout(agentID, msgType, params, 120*time.Second, "")
}

// Refusal codes for a command that was refused BEFORE it reached the wire.
// They are typed (ForwardRefusal) so a handler can tell "this never left the
// server" from "sent, the answer did not arrive in time" — the two states have
// opposite meanings to a caller (P12-04 F1: a capability refusal was answered
// HTTP 200 {"status":"update_in_progress"}).
const (
	RefusalCapabilityMissing = "CAPABILITY_MISSING"
	RefusalAgentNotConnected = "AGENT_NOT_CONNECTED"
)

// ForwardRefusal is returned by forwardToAgentWithTimeout when the command was
// never written to the agent: the agent has no live connection, or the registry
// says it does not advertise the capability the command requires. A caller must
// surface it as a failure; the agent did not receive anything and nothing will
// happen on the host.
type ForwardRefusal struct {
	Code    string
	AgentID string
	Message string
	Err     error
}

func (e *ForwardRefusal) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code + ": " + e.AgentID
}

func (e *ForwardRefusal) Unwrap() error { return e.Err }

// forwardToAgentWithTimeout sends a command to the agent via WebSocket and waits
// for response with a custom timeout. This is the core request-response method —
// forwardToAgent is a convenience wrapper with a 120s default. Every forwarded
// command is logged to the audit logger with the operator, action, and result.
// operatorID is the authenticated operator's ID (empty for server-initiated
// commands such as tunnel setup or disconnect-time process kills).
//
// The two pre-send checks return *ForwardRefusal, never a bare error: callers
// that treat errors as "the send timed out, work is in flight" must be able to
// distinguish a refusal from a delivery that started.
func (s *Server) forwardToAgentWithTimeout(agentID string, msgType string, params interface{}, timeout time.Duration, operatorID string) (interface{}, error) {
	start := time.Now()
	s.mu.RLock()
	conn, ok := s.conns[agentID]
	writeMu, wmuOk := s.connWriteMu[agentID]
	s.mu.RUnlock()
	if !ok || !wmuOk {
		s.auditLog(agentID, msgType, params, "error", "agent not connected", start, false, operatorID)
		return nil, &ForwardRefusal{
			Code:    RefusalAgentNotConnected,
			AgentID: agentID,
			Message: fmt.Sprintf("agent %s not connected", agentID),
		}
	}

	// Capability check: verify the agent advertises the required capability
	// for this command type. Agents with no capabilities advertised are
	// treated as having all capabilities (backward compat).
	if rec, err := s.registry.GetHealth(agentID); err == nil {
		if capErr := CheckAgentCapability(agentID, rec.Capabilities, msgType); capErr != nil {
			s.auditLog(agentID, msgType, params, "error", "capability denied: "+capErr.Error(), start, false, operatorID)
			return nil, &ForwardRefusal{
				Code:    RefusalCapabilityMissing,
				AgentID: agentID,
				Message: capErr.Error(),
				Err:     capErr,
			}
		}
	}

	reqID := fmt.Sprintf("%s-%d", msgType, time.Now().UnixMilli())
	var paramData json.RawMessage
	if params != nil {
		paramData, _ = json.Marshal(params)
	}
	env := protocol.Envelope{
		ID:     reqID,
		Type:   msgType,
		Params: paramData,
		// Bypass is intentionally never set by the server-side forwarder.
		// API callers cannot escalate to bypass mode — the server must
		// explicitly set bypass=true only after server-side authorization
		// logic (which does not exist yet). This prevents any client-supplied
		// bypass flag from reaching the agent.
	}

	respCh := make(chan protocol.Envelope, 1)
	s.pendingMu.Lock()
	s.pendingReqs[reqID] = respCh
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pendingReqs, reqID)
		s.pendingMu.Unlock()
	}()

	// Serialize WebSocket writes per-agent to prevent parallel write corruption.
	// Concurrent WriteJSON calls on the same WebSocket connection can interleave
	// frames, corrupting data — this was the root cause of binary upload corruption.
	// For relayed agents (virtualConn), the Conn implementation handles its own
	// locking internally (vc.session.writeMu), so we must NOT double-lock here
	// — Go mutexes are non-reentrant and double-locking causes a self-deadlock.
	if _, isVirtual := conn.(*virtualConn); !isVirtual {
		writeMu.Lock()
	}
	err := conn.WriteJSON(env)
	if _, isVirtual := conn.(*virtualConn); !isVirtual {
		writeMu.Unlock()
	}
	if err != nil {
		s.auditLog(agentID, msgType, params, "error", "send to agent failed: "+err.Error(), start, false, operatorID)
		return nil, fmt.Errorf("send to agent failed: %w", err)
	}

	select {
	case resp := <-respCh:
		if resp.Error != nil {
			s.auditLog(agentID, msgType, params, "success", "", start, false, operatorID)
			// P8-05: the typed code travels with the message so the HTTP
			// layer can refuse with FS_WRITE_EMPTY_DATA instead of a generic
			// 5xx. Callers that only read "error" are unaffected.
			return map[string]interface{}{
				"error":  resp.Error.Message,
				"code":   resp.Error.Code,
				"detail": resp.Error.Detail,
			}, nil
		}
		var result interface{}
		if resp.Result != nil {
			json.Unmarshal(resp.Result, &result)
		}
		s.auditLog(agentID, msgType, params, "success", "", start, false, operatorID)
		return result, nil
	case <-time.After(timeout):
		s.auditLog(agentID, msgType, params, "error", "timed out", start, false, operatorID)
		return nil, fmt.Errorf("agent did not respond within %v (timed out)", timeout)
	}
}

// auditLog writes a single audit entry for a forwarded command. operatorID
// is the authenticated operator (empty for server-initiated actions).
func (s *Server) auditLog(agentID, action string, params interface{}, result, errMsg string, start time.Time, bypass bool, operatorID string) {
	if s.audit == nil {
		return
	}
	s.audit.Log(AuditEntry{
		AgentID:    agentID,
		OperatorID: operatorID,
		Action:     action,
		Params:     params,
		Result:     result,
		ErrorMsg:   errMsg,
		DurationMs: time.Since(start).Milliseconds(),
		Bypass:     bypass,
	})
}


// handleAgentHealth returns the full health record for a single agent.
func (s *Server) handleAgentHealth(w http.ResponseWriter, r *http.Request, agentID string) {
	rec, err := s.registry.GetHealth(agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

// actionToRBAC maps an HTTP route action (e.g. "exec", "fs-read") to the
// RBAC action name used by Operator.CanPerform. The mapping is 1:1 for
// most actions; "health" maps to "health" (viewer-allowed).
func actionToRBAC(action string) string {
	switch action {
	case "exec", "fs-read", "fs-write", "fs-stat", "fs-hash", "capture",
		"task-list", "proc-list", "proc-kill", "proc-start",
		"tunnel", "tunnel-close", "sniff", "sniff-stop",
		"mitm-start", "mitm-stop", "mitm-traffic",
		"debug-attach", "debug-detach", "debug-read-mem", "debug-modules", "debug-mem-query",
		"update", "health",
		// Phase 7: new capability actions
		"socks5-start", "socks5-stop",
		"port-forward", "port-scan",
		"net-connections",
		"autostart-enable", "autostart-disable", "autostart-status",
		"file-search", "sysinfo":
		return action
	default:
		return action
	}
}

// auditDenied logs a "denied" audit entry when an operator is rejected by RBAC.
func (s *Server) auditDenied(agentID, action string, op *Operator) {
	if s.audit == nil {
		return
	}
	entry := AuditEntry{
		AgentID:   agentID,
		Action:    action,
		Result:    "denied",
		ErrorMsg:  "permission denied",
		Timestamp: time.Now().UTC(),
	}
	if op != nil {
		entry.OperatorID = op.ID
	}
	s.audit.Log(entry)
}

// handleAgentGenericForward is a generic handler that reads the request body
// (if present) and forwards a command to the agent. Used by Phase 7 new
// capabilities that don't need custom server-side logic.
func (s *Server) handleAgentGenericForward(w http.ResponseWriter, r *http.Request, agentID string, msgType string) {
	var params interface{}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	if len(body) > 0 {
		params = json.RawMessage(body)
	}
	resp, err := s.forwardToAgent(agentID, msgType, params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

