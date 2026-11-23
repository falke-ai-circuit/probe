package agent

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-07 (P8-02 §6.3) — agent-side credential holding.
//
// The value arrives in its own envelope, sealed under sha256(token||agent_id),
// lives only in a byte slice in this process, and is zeroed once the child that
// needed it has exited. It is never written to disk and never placed on a
// command line: the child reads it from stdin.

// pendingCredential is one live secret waiting for the exec that names it.
type pendingCredential struct {
	Handle  string
	Name    string
	Mode    string
	// JobID is the job claim the server minted this secret for (P8-18). A
	// non-empty claim means the secret may only be opened by the exec that
	// presents the same job_id.
	JobID   string
	Expires time.Time
	value   []byte
}

// credentialZeroHook is a test seam: it is called with the buffer after it has
// been zeroed, so a test can assert the bytes are really gone rather than trust
// a comment.
var credentialZeroHook func([]byte)

// zeroCredential overwrites the buffer in place and then reports the buffer to
// the test hook. Overwriting in place matters: reassigning the slice would
// leave the original bytes in the old backing array.
func zeroCredential(buf []byte) {
	for i := range buf {
		buf[i] = 0
	}
	if credentialZeroHook != nil {
		credentialZeroHook(buf)
	}
}

// scrubSecret removes a live credential from output before it can reach a sink.
// Empty and short values are left alone: replacing a one-character password
// would corrupt every line it appears in without protecting anything.
func scrubSecret(out string, value []byte) string {
	s := string(value)
	if len(s) < 4 || !strings.Contains(out, s) {
		return out
	}
	return strings.ReplaceAll(out, s, "[REDACTED]")
}

// handleCredentialDeliver decrypts and parks a delivered secret. It answers
// with the handle and name only — the ack is not an echo channel.
func (a *Agent) handleCredentialDeliver(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.CredentialDelivery](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	if params.Handle == "" || params.Name == "" || params.Sealed == "" {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "handle, name and sealed are required")
	}
	expires, _ := time.Parse(time.RFC3339, params.ExpiresAtUTC)
	if !expires.IsZero() && time.Now().After(expires) {
		return protocol.NewError(env.ID, protocol.ErrCredentialExpired,
			fmt.Sprintf("credential handle for %q expired before delivery", params.Name))
	}
	sealed, err := base64.StdEncoding.DecodeString(params.Sealed)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrCredentialUnknown, "sealed payload is not base64")
	}
	plain, err := openForAgent(a.cfg.Token, params.AgentID, sealed)
	if err != nil {
		// A handle sealed for another agent does not decrypt here: the key
		// carries the agent id, which is why this is a refusal and not a
		// partial read.
		return protocol.NewError(env.ID, protocol.ErrCredentialUnknown,
			"credential payload could not be unsealed for this agent")
	}
	a.credMu.Lock()
	if a.credPending == nil {
		a.credPending = make(map[string]*pendingCredential)
	}
	old := a.credPending[params.Name]
	a.credPending[params.Name] = &pendingCredential{
		Handle:  params.Handle,
		Name:    params.Name,
		Mode:    params.Mode,
		JobID:   params.JobID,
		Expires: expires,
		value:   plain,
	}
	a.credMu.Unlock()
	if old != nil {
		zeroCredential(old.value)
	}
	return protocol.NewResult(env.ID, protocol.TypeCredentialResult, map[string]interface{}{
		"handle":   params.Handle,
		"name":     params.Name,
		"accepted": true,
	})
}

// takeCredential removes the pending secret for name. The second return is
// true when the entry existed but had expired — the server's re-mint trigger.
func (a *Agent) takeCredential(name string) (*pendingCredential, bool) {
	a.credMu.Lock()
	defer a.credMu.Unlock()
	p, ok := a.credPending[name]
	if !ok {
		return nil, false
	}
	delete(a.credPending, name)
	if !p.Expires.IsZero() && time.Now().After(p.Expires) {
		zeroCredential(p.value)
		return nil, true
	}
	return p, false
}

// openForAgent decrypts a handle sealed under sha256(shared_token||agent_id).
func openForAgent(token, agentID string, sealed []byte) ([]byte, error) {
	h := sha256.Sum256([]byte(token + "|" + agentID))
	block, err := aes.NewCipher(h[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, fmt.Errorf("sealed payload too short")
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
}

// ensure the mutex type is referenced where the Agent struct lives.
var _ sync.Mutex
