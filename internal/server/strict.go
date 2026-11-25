package server

// P13-01 — contract P10-07 v3.1 §5.5 phase-1 enforcement substrate (E1, E2,
// E3a, E3c). No context entry, no platform backend: this file decides, before
// a single byte reaches an agent, whether a request is well-formed.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// featureContextSelector is the advertised feature the E1 gate requires. It is
// checked with hasCapabilityStrict, never HasCapability: an agent that
// advertises nothing must be refused, not treated as having everything — that
// permissive fallback is the silent-pass this unit exists to close (§5.5 E1,
// contract line 729).
const featureContextSelector = "context.selector"

// Route allow-lists, verbatim from the §5.5 table, defined once in protocol so
// the server (E3a) and the agent (E3b) cannot drift. They are the route's own
// key set, not the type's: four routes share protocol.InputParams, so only the
// route list can reject a known-but-wrong field (§5.5).
var (
	allowExec         = protocol.AllowExec
	allowCapture      = protocol.AllowCapture
	allowStreamStart  = protocol.AllowStreamStart
	allowStreamStop   = protocol.AllowStreamStop
	allowStreamFrame  = protocol.AllowStreamFrame
	allowPointerMove  = protocol.AllowPointerMove
	allowPointerClick = protocol.AllowPointerClick
	allowPointerScroll = protocol.AllowPointerScroll
	allowPointerDrag  = protocol.AllowPointerDrag
	allowKeyPress     = protocol.AllowKeyPress
	allowKeyCombo     = protocol.AllowKeyCombo
	allowTextInput    = protocol.AllowTextInput
	allowUITree       = protocol.AllowUITree
	allowUIWindow     = protocol.AllowUIWindow
)

// decodeStrict is E3a: one helper per handler. It reads the body once (so a
// handler can never re-read a half-consumed stream), applies the three checks
// in protocol.ParseStrictBytes, and writes the typed refusal itself. ok=false
// means the response has already been written and the caller must return
// immediately — before forwardToAgent.
func decodeStrict[T any](w http.ResponseWriter, r *http.Request, allowed []string) (T, bool) {
	var zero T
	var body []byte
	if r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, maxStrictBodyBytes))
		if err != nil {
			writeErrorDetail(w, http.StatusBadRequest, protocol.ErrInvalidParams,
				"failed to read body", map[string]interface{}{"reason": protocol.ReasonTypeMismatch, "ran": false})
			return zero, false
		}
		body = b
		r.Body.Close()
	}
	params, serr := protocol.ParseStrictBytes[T](body, allowed)
	if serr != nil {
		writeStrictRefusal(w, serr)
		return zero, false
	}
	// §5.1: conflict and range are decided where the selector is parsed, before
	// any dispatch. A selector that cannot name a context is not a selector.
	if sel, ok := any(params).(protocol.SelectorCarrier); ok {
		ctx, sess := sel.Selector()
		if verr := protocol.ValidateSelector(ctx, sess); verr != nil {
			writeStrictRefusal(w, verr)
			return zero, false
		}
	}
	return params, true
}

// maxStrictBodyBytes bounds the read: a body larger than any of these routes
// documents is not a request this substrate accepts, and reading it into
// memory unbounded is a defect of its own.
const maxStrictBodyBytes = 1 << 20

// writeStrictRefusal renders a StrictError as the §5.5 refusal shape:
// 400 INVALID_PARAMS, detail.ran=false, and the reason from the closed enum.
func writeStrictRefusal(w http.ResponseWriter, serr *protocol.StrictError) {
	detail := map[string]interface{}{
		"reason": serr.Reason,
		"ran":    false,
	}
	if serr.Field != "" {
		detail["field"] = serr.Field
	}
	if len(serr.Allowed) > 0 {
		detail["allowed"] = serr.Allowed
	}
	if serr.Want != "" {
		detail["want"] = serr.Want
	}
	if serr.Got != "" {
		detail["got"] = serr.Got
	}
	writeErrorDetail(w, http.StatusBadRequest, protocol.ErrInvalidParams, serr.Error(), detail)
}

// refuseQueryParams is the §5.6/§6 rule for a GET that cannot carry a
// selector: the query string is where an operator would put one, so a key that
// appears there is UNKNOWN_FIELD — refused, never silently dropped. Returns
// false when the refusal has been written and the handler must return.
func refuseQueryParams(w http.ResponseWriter, r *http.Request, allowed []string) bool {
	if r.URL.RawQuery == "" {
		return true
	}
	field := strings.SplitN(r.URL.RawQuery, "=", 2)[0]
	writeErrorDetail(w, http.StatusBadRequest, protocol.ErrInvalidParams,
		fmt.Sprintf("this route accepts no query parameters; %q was not read", field),
		map[string]interface{}{
			"reason":  protocol.ReasonUnknownField,
			"field":   field,
			"allowed": allowed,
			"ran":     false,
		})
	return false
}

// selectorPresent reports whether the params carry a selector at all. Absent =
// the agent's own context = today's behaviour byte-for-byte (§5.1 invariant).
func selectorPresent(params interface{}) bool {
	sel, ok := params.(protocol.SelectorCarrier)
	if !ok {
		return false
	}
	ctx, sess := sel.Selector()
	return ctx != nil || sess != nil
}

// preDispatchSelector is E1 plus the §5.1 range/conflict re-check: a
// context-scoped call to an agent that does not advertise context.selector is
// refused 409 UNSUPPORTED_AGENT here, before anything is written to the wire.
// false means the refusal is written and the handler must return.
func (s *Server) preDispatchSelector(w http.ResponseWriter, agentID string, params interface{}) bool {
	sel, ok := params.(protocol.SelectorCarrier)
	if !ok {
		return true
	}
	ctx, sess := sel.Selector()
	if ctx == nil && sess == nil {
		return true
	}
	if serr := protocol.ValidateSelector(ctx, sess); serr != nil {
		writeStrictRefusal(w, serr)
		return false
	}

	version := ""
	var caps []string
	if rec, err := s.registry.GetHealth(agentID); err == nil {
		version = rec.Version
		caps = rec.Capabilities
	}
	if hasCapabilityStrict(caps, featureContextSelector) {
		return true
	}
	requested := ""
	if ctx != nil {
		requested = *ctx
	} else if sess != nil {
		requested = fmt.Sprintf("wts:%d", *sess)
	}
	writeErrorDetail(w, http.StatusConflict, protocol.ErrUnsupportedAgent,
		fmt.Sprintf("agent %s does not advertise %q; it would ignore the selector and act in its own context", agentID, featureContextSelector),
		map[string]interface{}{
			"feature":       featureContextSelector,
			"agent_version": version,
			"requested":     requested,
			"ran":           false,
			"remediation":   "upgrade the agent to a build that advertises context.selector, or send no selector to act in the agent's own context",
		})
	return false
}

// assertContextRef is E2: a context-scoped result must carry context_ref. An
// agent that advertises the selector (1.21+) and omits it is a protocol
// breach — 502, never a success passthrough. A pre-1.21 agent cannot echo the
// field at all, and its call was already refused by E1, so the assertion does
// not invent a failure for it.
func (s *Server) assertContextRef(w http.ResponseWriter, agentID string, params interface{}, resp interface{}) bool {
	if !selectorPresent(params) {
		return true
	}
	m, ok := resp.(map[string]interface{})
	if !ok {
		return true
	}
	if _, isErr := m["error"]; isErr {
		// The agent refused; there is no result envelope to assert on.
		return true
	}
	if _, has := m["context_ref"]; has {
		return true
	}
	version := ""
	if rec, err := s.registry.GetHealth(agentID); err == nil {
		version = rec.Version
	}
	if !agentVersionAtLeast(version, 1, 21) {
		return true
	}
	writeErrorDetail(w, http.StatusBadGateway, protocol.ErrProtocolViolation,
		fmt.Sprintf("agent %s answered a context-scoped request without context_ref; the action's context is unknowable, so the result is refused", agentID),
		map[string]interface{}{
			"agent_version": version,
			"missing":       "context_ref",
			"ran":           false,
		})
	return false
}

// agentVersionAtLeast compares a version string ("1.21.0", "v1.21.0-rc1",
// "1.21.0-dev") against a major/minor floor. Unparseable versions are treated
// as older: an agent that cannot state its version has not earned the newer
// contract's obligations, and E1 already refused its context-scoped calls.
func agentVersionAtLeast(version string, wantMajor, wantMinor int) bool {
	major, minor, ok := parseAgentVersion(version)
	if !ok {
		return false
	}
	if major != wantMajor {
		return major > wantMajor
	}
	return minor >= wantMinor
}

func parseAgentVersion(version string) (int, int, bool) {
	v := strings.TrimSpace(version)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return 0, 0, false
	}
	// Keep only the numeric dotted prefix: "1.21.0-dev" → "1.21".
	end := 0
	for end < len(v) && (v[end] == '.' || (v[end] >= '0' && v[end] <= '9')) {
		end++
	}
	v = v[:end]
	parts := strings.Split(v, ".")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// forwardedErrorStatus maps an agent-side typed error code onto the HTTP status
// the contract assigns it. Anything unlisted keeps today's 503 AGENT_ERROR —
// the mapping is additive, not a reclassification.
func forwardedErrorStatus(code string) int {
	switch code {
	case protocol.ErrTimeout:
		return http.StatusGatewayTimeout
	case protocol.ErrPrimitiveUnsupported:
		return http.StatusNotImplemented
	case "CONTEXT_UNAVAILABLE":
		return http.StatusConflict
	case protocol.ErrInvalidParams:
		return http.StatusBadRequest
	case protocol.ErrWindowNotFound:
		// §9.1: the window is not in the resolved context's tree. 404, and the
		// agent refused before any injection.
		return http.StatusNotFound
	case protocol.ErrPrimitiveRefused:
		// §11 403 class: the platform's security model (or the missing
		// `focus:true`) denied the primitive before it ran.
		return http.StatusForbidden
	default:
		return http.StatusServiceUnavailable
	}
}

// writeAgentRefusal renders an agent error payload with the status its code
// owns (§11). It reports whether the payload was an error at all, so a caller
// keeps one success path: an OCCLUDED answer is a RESULT (HTTP 200, ok:false),
// not an error, and must reach the caller unchanged.
func writeAgentRefusal(w http.ResponseWriter, resp interface{}) bool {
	m, ok := resp.(map[string]interface{})
	if !ok {
		return false
	}
	errMsg, has := m["error"]
	if !has {
		return false
	}
	code := "AGENT_ERROR"
	if c, isStr := m["code"].(string); isStr && c != "" {
		code = c
	}
	detail, _ := m["detail"].(map[string]interface{})
	writeErrorDetail(w, forwardedErrorStatus(code), code, toString(errMsg), detail)
	return true
}

// writeInputOutcome renders a §9 input result. An observed failure (§8.1: HTTP
// 200, ok:false) is a RESULT the caller must see at the top level — wrapping it
// in the ok:true envelope would turn a refusal into a success, which is the
// class of lie this contract exists to kill.
func writeInputOutcome(w http.ResponseWriter, resp interface{}) {
	if m, ok := resp.(map[string]interface{}); ok {
		if v, has := m["ok"]; has {
			if b, isBool := v.(bool); isBool && !b {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(m)
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
