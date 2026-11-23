package protocol

// P8-07 (P8-02 §6 C-CRED) — credential-reference exec.
//
// A credential is referenced by NAME in the exec request; the VALUE travels in
// a separate envelope delivered immediately before the exec, encrypted under a
// per-agent key. The exec params are logged by design (server_api.go), so the
// value must never be a field there.

const (
	// TypeCredentialDeliver carries one single-use credential handle from the
	// server to a specific agent. It is sent as its own WS frame, never as an
	// exec param, and answered with TypeCredentialResult.
	TypeCredentialDeliver = "credential_deliver"
	// TypeCredentialResult acknowledges a delivery. It never echoes the value.
	TypeCredentialResult = "credential_result"
)

// Error codes for the credential path (mapped to HTTP by the server).
const (
	ErrCredentialNotFound     = "NOT_FOUND"
	ErrCredentialCapMissing   = "CAPABILITY_MISSING"
	ErrCredentialChannelInsec = "CREDENTIAL_CHANNEL_INSECURE"
	ErrCredentialExpired      = "CREDENTIAL_EXPIRED"
	ErrCredentialUnknown      = "CREDENTIAL_UNKNOWN"
)

// CredentialDelivery is the body of a TypeCredentialDeliver envelope.
//
// Sealed is the credential VALUE encrypted with AES-256-GCM under the key
// sha256(shared_token || agent_id), base64 encoded. The handle itself is an
// opaque single-use identifier; it is not a secret, but it is short-lived and
// bound to one agent so a replay from another agent cannot resolve it.
type CredentialDelivery struct {
	Handle       string `json:"handle"`
	Name         string `json:"name"`
	AgentID      string `json:"agent_id"`
	JobID        string `json:"job_id"`
	Mode         string `json:"mode"` // "stdin" (default) | "env"
	Sealed       string `json:"sealed"`
	ExpiresAtUTC string `json:"expires_at_utc"`
}
