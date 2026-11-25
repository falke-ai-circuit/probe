package server

// P13-02 — contract P10-07 v3.1 §6: GET /api/v1/agents/{id}/contexts.
//
// Read-only enumeration. The route carries NO selector: it does not act on a
// context, it reports which contexts exist and what can be done in each. That
// is why a query parameter is refused rather than interpreted (§5.6's rule for
// a body-less GET) — an operator who writes ?context= means to act, and the
// acting routes are the ones that take a selector.
//
// The server does not compute addressability. Each row arrives from the agent
// already classified by protocol.ClassifyContext, the predicate the selector
// gate enforces; re-deriving it here would be the second predicate the
// contract forbids.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// handleV1Contexts answers the §6 envelope. RBAC: the `input` scope, because a
// logged-on-user list is operator-activity data (a viewer token gets 403).
func (s *Server) handleV1Contexts(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "input"); !ok {
		return
	}
	// §5.6: a GET with no body cannot carry a selector, so the key an operator
	// would put in the query string is refused instead of silently dropped.
	if !refuseQueryParams(w, r, protocol.AllowContextsQuery) {
		return
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeContexts, nil)
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			writeError(w, http.StatusGatewayTimeout, protocol.ErrTimeout, err.Error())
		} else {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
		}
		return
	}
	if m, isMap := resp.(map[string]interface{}); isMap {
		if errMsg, hasErr := m["error"]; hasErr {
			code := "AGENT_ERROR"
			if c, isStr := m["code"].(string); isStr && c != "" {
				code = c
			}
			detail, _ := m["detail"].(map[string]interface{})
			writeErrorDetail(w, forwardedErrorStatus(code), code, toString(errMsg), detail)
			return
		}
	}

	// Normalise through the typed struct: a result that is not the §6 shape is
	// a protocol breach, never a pass-through of whatever the agent sent.
	raw, err := json.Marshal(resp)
	if err != nil {
		writeError(w, http.StatusBadGateway, protocol.ErrProtocolViolation, "contexts result is not serialisable: "+err.Error())
		return
	}
	var result protocol.ContextsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeErrorDetail(w, http.StatusBadGateway, protocol.ErrProtocolViolation,
			"agent answered contexts with a body that is not the §6 shape: "+err.Error(),
			map[string]interface{}{"ran": false})
		return
	}
	if result.Contexts == nil {
		// A host with no contexts still answers a list; null would be a
		// different (and unreadable) statement.
		result.Contexts = []protocol.ContextRow{}
	}

	writeJSON(w, http.StatusOK, protocol.ContextsResponse{
		AgentContext:   result.AgentContext,
		GeneratedAtUTC: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		Contexts:       result.Contexts,
	})
}
