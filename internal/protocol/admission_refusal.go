package protocol

// P22-03 — an admission refusal is a frame, not an HTTP status.
//
// A WebSocket client cannot read a 429: the handshake fails before a frame
// exists, and gorilla reports every non-101 answer as ErrBadHandshake
// ("websocket: bad handshake"). An agent refused for policy reasons therefore
// looks identical to one refused for a bad token or a TLS mismatch, and the
// reason never leaves the refusing process.
//
// The refusal below instead rides on a socket that has already upgraded. It is
// the protocol's own error envelope — a shape every agent already decodes —
// with a stable code and the numbers the decision was made from.

import (
	"encoding/json"
	"strings"
)

// CodeForwardMaxAgents is the wire code for the max_agents refusal. The
// forwarding path names the same refusal (server.RefusalLinkMaxAgents); this is
// the single spelling both sides agree on.
const CodeForwardMaxAgents = "FORWARD_MAX_AGENTS"

// AdmissionRefusal builds the typed refusal frame: the stable code, the human
// reason (which quotes the limit and the link), and limit/current as structured
// detail so a reader does not have to parse prose.
func AdmissionRefusal(code, reason string, limit, current int) Envelope {
	return Envelope{
		Type: "error",
		Error: &ErrorInfo{
			Code:    code,
			Message: reason,
			Detail: map[string]interface{}{
				"limit":   limit,
				"current": current,
			},
		},
	}
}

// IsForwardRefusalCode reports whether code belongs to the forwarding path's
// named refusals. The family shares the FORWARD_ prefix, so a new refusal code
// is classified without a second table to keep in sync.
func IsForwardRefusalCode(code string) bool {
	return strings.HasPrefix(code, "FORWARD_")
}

// DetailInt reads an integer out of ErrorInfo.Detail. A detail value that
// travelled over the wire decodes as float64; the normalisation lives here so
// every caller does not re-derive it.
func (e *ErrorInfo) DetailInt(key string) (int, bool) {
	if e == nil || e.Detail == nil {
		return 0, false
	}
	switch v := e.Detail[key].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}
