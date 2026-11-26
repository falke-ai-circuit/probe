package server

// P17-02 (spec §7) — peer audit events and the boundary redaction.
//
// The key list is enforced at the ONE place plaintext could enter the audit
// stream: auditPeer, before the entry reaches AuditLogger.Log. The default-
// deny list is the rule; allowlisting plaintext keys is forbidden (§7.1).
// Token never; upstream URL kept (§7.2); refusals kept in full (§7.3);
// counts and transitions kept (§7.4); token_fp kept (§7.5); redaction
// failure writes {"redaction_error":true} instead of the entry (§7.6).

import "regexp"

// auditRedactKeys is the §7.1 default-deny list: any Params key matching one
// of these is replaced BEFORE the entry is written.
var auditRedactKeys = regexp.MustCompile(`(?i)token|secret|password|credential|authorization`)

// redactParams applies the key list recursively. Returns (clean, ok): ok is
// false when redaction itself failed (§7.6) — the caller then writes the
// fail-closed marker instead of a possibly-redacted entry.
func redactParams(params any) (any, bool) {
	switch v := params.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, val := range v {
			if auditRedactKeys.MatchString(k) {
				// The fingerprint vocabulary (§7.5): tokens are replaced by
				// their fp so an auditor can match credentials formed the
				// link without holding the credential. Secret-typed keys
				// that already carry an fp keep it (shape below).
				if s, isStr := val.(string); isStr && isTokenFPShape(s) {
					out[k] = s
					continue
				}
				out[k] = "<redacted>"
				continue
			}
			clean, ok := redactParams(val)
			if !ok {
				return nil, false
			}
			out[k] = clean
		}
		return out, true
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			clean, ok := redactParams(item)
			if !ok {
				return nil, false
			}
			out = append(out, clean)
		}
		return out, true
	default:
		return params, true
	}
}

// isTokenFPShape matches the 8-hex fingerprint convention so a Params value
// that is already an fp survives the key-name redaction (§7.5 keeps fp).
func isTokenFPShape(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// auditPeer writes one peer.* audit event. agentID carries the peer id (§7):
// reason and params pass through the boundary redaction; a redaction failure
// lands as the fail-closed marker, never as unredacted content and never as
// silence (§7.6).
func (s *Server) auditPeer(peerID, action, reason string, params map[string]any) {
	if s.audit == nil {
		return
	}
	if params == nil {
		params = map[string]any{}
	}
	cleaned, ok := redactParams(params)
	if !ok {
		// §7.6 fail closed: opaque entry beats a leaking one, silence is
		// also a defect.
		params = map[string]any{"redaction_error": true}
	} else if m, isMap := cleaned.(map[string]any); isMap {
		params = m
	}
	entry := AuditEntry{
		AgentID:  peerID,
		Action:   action,
		Result:   "success",
		Params:   params,
		ErrorMsg: reason,
	}
	if reason != "" {
		entry.Result = "error"
	}
	s.audit.Log(entry)
}
