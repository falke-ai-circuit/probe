package server

// P13-01 — the five new §5.5 rows (6, 8, 9, 13, 14). Each route exists so the
// allow-list can be enforced on it; the platform backends are later units, so
// the agent answers PRIMITIVE_UNSUPPORTED/NO_BACKEND after the strict parse
// rather than reporting a success nobody performed.

import (
	"net/http"
	"strings"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// v1StrictForward builds a handler for a selector-bearing route: auth, E3a
// strict decode against the route's own allow-list, E1 pre-dispatch gate, one
// forward, then the E2 context_ref assertion on the result.
func v1StrictForward[T any](s *Server, action, msgType string, allowed []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID := r.PathValue("id")
		if _, ok := s.v1CheckAuth(w, r, action); !ok {
			return
		}
		params, ok := decodeStrict[T](w, r, allowed)
		if !ok {
			return
		}
		if !s.preDispatchSelector(w, agentID, params) {
			return
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
		if !s.assertContextRef(w, agentID, params, resp) {
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// toString renders an agent error message without importing fmt for one call.
func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// registerStrictSelectorRoutes wires the five new rows. Row 5 (stream-frame) is
// a GET with no body and is enforced inside its own handler.
func (s *Server) registerStrictSelectorRoutes() {
	s.mux.HandleFunc("POST /api/v1/agents/{id}/pointer-move",
		v1StrictForward[protocol.PointerMoveParams](s, "input", protocol.TypePointerMove, allowPointerMove))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/pointer-scroll",
		v1StrictForward[protocol.PointerScrollParams](s, "input", protocol.TypePointerScroll, allowPointerScroll))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/pointer-drag",
		v1StrictForward[protocol.PointerDragParams](s, "input", protocol.TypePointerDrag, allowPointerDrag))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/ui-tree",
		v1StrictForward[protocol.UITreeParams](s, "capture", protocol.TypeUITree, allowUITree))
	s.mux.HandleFunc("POST /api/v1/agents/{id}/ui-window",
		v1StrictForward[protocol.UIWindowParams](s, "capture", protocol.TypeUIWindow, allowUIWindow))
}
