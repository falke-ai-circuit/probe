package server

// P19-04 — per-agent token rotation, surfaced.
//
// The mechanism already existed (server_token.go: StartTokenRotation,
// InitiateTokenRotation, rotatedTokens) but ran DIM: --token-ttl was 0 in the
// shipped config and no API surface could issue a rotation on demand. This
// file is the surface.
//
// Three rules copied from the units that hardened neighbouring seams:
//
//   - P17-03 (§6): a token never appears in ANY response body, old or new.
//     The rotate response carries fingerprints only (token_fp convention).
//     The plaintext travels exactly one hop: server → agent socket via
//     InitiateTokenRotation, which already writes it to zero other sinks.
//
//   - P19-03 B2 loudness bar: every state change is LOUD. Rotate issued,
//     delivered, failed — each writes an audit line (fp-only params) and a
//     server log line. A silent rotation is a finding against this unit.
//
//   - P19-01 refusal discipline: refusals are typed (stable code, HTTP status
//     named), never a bare 500 and never a silent no-op.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// Typed refusal codes. Stable strings quoted in tests and operator docs.
const (
	// RefusalAgentTokenRotateConnected = the rotation command could not be
	// delivered because the agent has no live route (503). Rotation is a
	// per-connection act: there is no socket to push the new token down.
	RefusalAgentTokenRotateConnected = "AGENT_NOT_CONNECTED"
	// RefusalAgentTokenRotateDelivery = the socket accepted nothing (502).
	// The old token stays accepted (grace, see rotatedTokens); rotating
	// again after the agent reconnects is the recovery path.
	RefusalAgentTokenRotateDelivery = "ROTATE_DELIVERY_FAILED"
)

// AgentTokenRotationResult is what the rotate endpoint answers with. It
// carries fingerprints only — the plaintext never rides an HTTP response.
type AgentTokenRotationResult struct {
	AgentID       string `json:"agent_id"`
	TokenFP       string `json:"token_fp"`
	PreviousFP    string `json:"previous_token_fp,omitempty"`
	Expiry        string `json:"expiry,omitempty"`
	Delivered     bool   `json:"delivered"`
	Transport     string `json:"transport"` // LivenessSourceWS or LivenessSourceRelay
	RotatedAt     string `json:"rotated_at"`
}

// registerAgentTokenRoutes wires the rotation surface onto the credentials
// API family. The route lives under /api/v1/credentials (P8-07 family):
// an agent token IS a credential, and the credentials file is where the
// write-only discipline already lives.
func (s *Server) registerAgentTokenRoutes() {
	s.mux.HandleFunc("POST /api/v1/credentials/agents/{agent}/rotate", s.handleV1RotateAgentToken)
}

// handleV1RotateAgentToken issues an operator-initiated rotation for one
// agent: a fresh token is generated, pushed down the agent's own live
// transport, and stored in rotatedTokens so the agent can reconnect with it.
//
// Grace-vs-cut (P19-02 §5, M-1): the rotated token — the NEWEST token —
// remains accepted. When a newer rotation supersedes it, the previous value
// in rotatedTokens is overwritten and the superseded token is cut at that
// moment. Rationale: a mid-upgrade cut turns every reconnect of an agent
// whose push half-landed into a fleet outage (row M-1's loudness discipline
// exists so an operator SEES that state, not so he dials blind into it).
// Cut-after-one-supersession still bounds an orphaned token's life to one
// rotation interval.
func (s *Server) handleV1RotateAgentToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "credentials:rotate"); !ok {
		return
	}
	agentID := r.PathValue("agent")

	// Resolve the route BEFORE minting: rotation of a dead identity mints
	// nothing (there is nothing to deliver to).
	tr, rerr := s.resolveAgentTransport(agentID)
	if rerr != nil {
		s.auditTokenRotate(agentID, "", false, "", "no route: "+rerr.Error())
		writeErrorDetail(w, http.StatusServiceUnavailable,
			RefusalAgentTokenRotateConnected,
			fmt.Sprintf("agent %s has no live transport; rotation requires the agent socket (reconnect and retry)", agentID),
			map[string]interface{}{"agent_id": agentID})
		return
	}

	newToken := generateToken()
	prevFP := s.agentTokenFPLocked(agentID)

	params := protocol.TokenRotateParams{NewToken: newToken}
	if s.tokenTTL > 0 {
		params.Expiry = time.Now().Add(s.tokenTTL)
	}
	paramData, _ := json.Marshal(params)
	env := protocol.Envelope{
		ID:     fmt.Sprintf("p1904-rotate-%d", time.Now().UnixMilli()),
		Type:   protocol.TypeAuthRefresh,
		Params: paramData,
	}

	// One lock scope over the store: the rotated-slot write replaces the
	// previous rotated token, which is the cut point for the superseded one.
	s.tokenMu.Lock()
	s.rotatedTokens[agentID] = newToken
	s.tokenMu.Unlock()

	// Deliver on the agent's own transport. Protocol framing identical to
	// InitiateTokenRotation (the proactive loop's path); writing via the
	// resolved transport keeps relayed agents servable by the same route.
	var delivered bool
	if werr := writeTokenRotateEnvelope(tr, env); werr != nil {
		log.Printf("[server] P19-04 token rotation DELIVERY FAILED for agent %s via %s: %v", agentID, tr.Source, werr)
		s.auditTokenRotate(agentID, tokenFingerprint(newToken), false, tr.Source, "delivery failed: "+werr.Error())
		writeErrorDetail(w, http.StatusBadGateway,
			RefusalAgentTokenRotateDelivery,
			fmt.Sprintf("rotation command for agent %s was not delivered via %s; token stored, agent may collect on reconnect", agentID, tr.Source),
			map[string]interface{}{"agent_id": agentID, "transport": tr.Source})
		return
	}
	delivered = true

	res := AgentTokenRotationResult{
		AgentID:   agentID,
		TokenFP:   tokenFingerprint(newToken),
		Delivered: delivered,
		Transport: tr.Source,
		RotatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if prevFP != "" {
		res.PreviousFP = prevFP
	}
	if s.tokenTTL > 0 {
		res.Expiry = time.Now().Add(s.tokenTTL).UTC().Format(time.RFC3339)
	}

	log.Printf("[server] P19-04 token rotation issued for agent %s via %s (token_fp=%s previous=%s)",
		agentID, tr.Source, res.TokenFP, res.PreviousFP)
	s.auditTokenRotate(agentID, res.TokenFP, true, tr.Source, fmt.Sprintf("issued via %s (previous_metadata=%s)", tr.Source, res.PreviousFP))
	writeJSON(w, http.StatusOK, res)
}

// agentTokenFPLocked returns the fingerprint of the newest rotated token for
// agentID under tokenMu, or "" when none was stored.
func (s *Server) agentTokenFPLocked(agentID string) string {
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	if rt, ok := s.rotatedTokens[agentID]; ok {
		return tokenFingerprint(rt)
	}
	return ""
}

// writeTokenRotateEnvelope sends the rotate envelope over the agent's own
// transport using the transport's lock discipline (per-agent mutex for direct
// sockets; virtualConn/relay locks itself).
func writeTokenRotateEnvelope(tr resolvedTransport, env protocol.Envelope) error {
	if tr.NeedsWriteLock && tr.WriteMu != nil {
		tr.WriteMu.Lock()
		defer tr.WriteMu.Unlock()
	}
	return tr.Conn.WriteJSON(env)
}

// The protocol framing and audit write live in small helpers so the handler
// above stays a readable path. The helpers carry no state.

// auditTokenRotate writes one audit line for a rotation attempt. Params carry
// fingerprints only — never token material.
func (s *Server) auditTokenRotate(agentID, fp string, ok bool, transport, detail string) {
	if s.audit == nil {
		return
	}
	result := "success"
	if !ok {
		result = "error"
	}
	s.audit.Log(AuditEntry{
		Timestamp: time.Now().UTC(),
		AgentID:   agentID,
		Action:    "credential.rotate",
		Params: map[string]string{
			"token_fp":  fp,
			"transport": transport,
			"detail":    detail,
		},
		Result:   result,
		ErrorMsg: detail,
	})
}
