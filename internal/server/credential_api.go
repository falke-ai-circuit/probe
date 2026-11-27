package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-07 (P8-02 §6 C-CRED) — HTTP surface and exec plumbing.
//
// Two rules shape this file:
//   * PUT writes, GET never reads. The metadata type has no value field, so a
//     handler cannot accidentally return one.
//   * The exec path resolves the value only to seal it for one agent. After
//     sealing, the plaintext seeds the redactor for the duration of the call
//     and is dropped.

func (s *Server) registerCredentialRoutes() {
	s.mux.HandleFunc("PUT /api/v1/credentials/{name}", s.handleV1PutCredential)
	s.mux.HandleFunc("GET /api/v1/credentials/{name}", s.handleV1GetCredential)
	s.mux.HandleFunc("GET /api/v1/credentials", s.handleV1ListCredentials)
	// P19-04: per-agent auth-token rotation on the credentials family.
	// POST only — a rotation is a state change; no GET ever rotates.
	s.registerAgentTokenRoutes()
}

// handleV1PutCredential stores a value. The response echoes the name and
// metadata only — never the value that was just written.
func (s *Server) handleV1PutCredential(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	name := r.PathValue("name")
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body")
		return
	}
	store, err := s.credentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	if strings.TrimSpace(body.Value) == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "value is required")
		return
	}
	s.redactor.Add(body.Value)
	err = store.Put(name, body.Value)
	s.redactor.Remove(body.Value)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", s.redactor.Redact(err.Error()))
		return
	}
	meta, _ := store.Meta(name)
	writeJSON(w, http.StatusOK, meta)
}

// handleV1GetCredential returns metadata only. There is no code path here that
// can reach the stored value: CredentialMeta has no value field.
func (s *Server) handleV1GetCredential(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	store, err := s.credentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	meta, ok := store.Meta(r.PathValue("name"))
	if !ok {
		writeError(w, http.StatusNotFound, protocol.ErrCredentialNotFound, "credential not found")
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

// handleV1ListCredentials lists names and metadata.
func (s *Server) handleV1ListCredentials(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "exec"); !ok {
		return
	}
	store, err := s.credentials()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	list := store.List()
	writeJSON(w, http.StatusOK, map[string]interface{}{"credentials": list, "count": len(list)})
}

// credentials returns the lazily-opened store. Opening is lazy so tests and
// unconfigured servers do not touch /var/lib/probe.
func (s *Server) credentials() (*CredentialStore, error) {
	s.credMu.Lock()
	defer s.credMu.Unlock()
	if s.credStore != nil {
		return s.credStore, nil
	}
	st, err := DefaultCredentialStore()
	if err != nil {
		return nil, err
	}
	s.credStore = st
	return st, nil
}

// credentialResult is what the caller sees on a successful credential exec.
type credentialResult struct {
	Name     string `json:"name"`
	Ref      string `json:"ref"`
	Used     bool   `json:"used"`
	Redacted int    `json:"redacted"`
}

// credentialExecError is a typed refusal from the credential pre-flight.
type credentialExecError struct {
	Status  int
	Code    string
	Message string
}

func (e *credentialExecError) Error() string { return e.Message }

// sealCredentialForExec performs every refusal the contract names, before any
// command is dispatched:
//   - unknown name        -> 404 NOT_FOUND
//   - agent lacks cap     -> 409 CAPABILITY_MISSING
//   - E2E inactive        -> 409 CREDENTIAL_CHANNEL_INSECURE
//
// On success it mints a single-use handle bound to agent+job, delivers it in a
// separate encrypted WS envelope immediately before the exec, and seeds the
// redactor. The returned cleanup must run when the call ends.
func (s *Server) sealCredentialForExec(agentID, name, jobID, mode string) (string, func(), *credentialExecError) {
	store, err := s.credentials()
	if err != nil {
		return "", func() {}, &credentialExecError{http.StatusInternalServerError, "INTERNAL_ERROR", err.Error()}
	}
	if _, ok := store.Meta(name); !ok {
		return "", func() {}, &credentialExecError{
			http.StatusNotFound, protocol.ErrCredentialNotFound,
			fmt.Sprintf("credential %q is not stored on this server", name),
		}
	}
	// Capability is strict: an old agent ignores the credential fields and
	// would run the command without a login — a silent false success.
	if rec, herr := s.registry.GetHealth(agentID); herr == nil {
		if !hasCapabilityStrict(rec.Capabilities, "credential") {
			return "", func() {}, &credentialExecError{
				http.StatusConflict, protocol.ErrCredentialCapMissing,
				fmt.Sprintf("agent %s does not implement credential delivery (capability \"credential\" absent)", agentID),
			}
		}
	}
	if s.e2eMgr == nil || !s.e2eMgr.IsActive() {
		return "", func() {}, &credentialExecError{
			http.StatusConflict, protocol.ErrCredentialChannelInsec,
			"credential delivery requires an active E2E channel; refusing to send a secret in the clear",
		}
	}
	value, ok := store.Resolve(name)
	if !ok {
		return "", func() {}, &credentialExecError{
			http.StatusInternalServerError, "INTERNAL_ERROR",
			fmt.Sprintf("credential %q could not be unsealed", name),
		}
	}
	if mode == "" {
		mode = "stdin"
	}
	h, err := s.credHandles.Mint(s.token, name, agentID, jobID, value, handleTTL())
	// Seed the redactor BEFORE anything else can log the value, and keep the
	// value only for as long as the sealing needs it.
	s.redactor.Add(value)
	cleanup := func() { s.redactor.Remove(value) }
	if err != nil {
		cleanup()
		return "", func() {}, &credentialExecError{http.StatusInternalServerError, "INTERNAL_ERROR", err.Error()}
	}
	if err := s.sendCredentialDelivery(agentID, h, mode); err != nil {
		if ch, ok := s.credHandles.Consume(h.Handle); ok {
			_ = ch
		}
		cleanup()
		// P21-01: the refusal names the predicate's reason. A dispatchable
		// agent whose link refused the frame is a delivery failure (502), not
		// an absent agent (503) — the old code answered AGENT_UNAVAILABLE for
		// both, which is the disagreement this unit removes.
		d := s.dispatchRefusal(agentID, "credential-delivery", err)
		return "", func() {}, &credentialExecError{
			d.Status, d.Code,
			"credential handle could not be delivered: " + s.redactor.Redact(d.Message),
		}
	}
	store.Touch(name)
	// The value is no longer needed here: the agent holds the sealed copy.
	return h.Handle, cleanup, nil
}

// sendCredentialDelivery writes the handle envelope as its own WS frame,
// serialized behind the same per-agent write mutex as every other frame.
func (s *Server) sendCredentialDelivery(agentID string, h *credHandle, mode string) error {
	tr, rerr := s.resolveAgentTransport(agentID)
	if rerr != nil {
		return rerr
	}
	conn := tr.Conn
	delivery := protocol.CredentialDelivery{
		Handle:       h.Handle,
		Name:         h.Name,
		AgentID:      agentID,
		JobID:        h.JobID,
		Mode:         mode,
		Sealed:       base64.StdEncoding.EncodeToString(h.Sealed),
		ExpiresAtUTC: h.Expires.UTC().Format("2006-01-02T15:04:05Z"),
	}
	raw, err := json.Marshal(delivery)
	if err != nil {
		return err
	}
	env := protocol.Envelope{
		ID:     h.Handle,
		Type:   protocol.TypeCredentialDeliver,
		Params: raw,
	}
	if tr.NeedsWriteLock {
		tr.WriteMu.Lock()
	}
	err = conn.WriteJSON(env)
	if tr.NeedsWriteLock {
		tr.WriteMu.Unlock()
	}
	if err != nil {
		return err
	}
	// The delivery itself is the only place a sealed value crosses the wire;
	// it is logged as a handle event, never as a payload.
	log.Printf("[credential] delivered handle %s for %q to agent %s (mode=%s, ttl=%s)",
		h.Handle, h.Name, agentID, mode, handleTTL())
	return nil
}

// isCredentialExpired reports the agent's typed refusal for a stale handle.
func isCredentialExpired(resp interface{}) bool {
	m, ok := resp.(map[string]interface{})
	if !ok {
		return false
	}
	code, _ := m["code"].(string)
	return code == protocol.ErrCredentialExpired
}

// redactValue filters an arbitrary value through the redactor and returns the
// parsed result, so response handlers keep their normal shapes.
func (s *Server) redactValue(v interface{}) interface{} {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out interface{}
	if err := json.Unmarshal([]byte(s.redactor.Redact(string(raw))), &out); err != nil {
		return v
	}
	return out
}

// redactBody marshals v, filters it through the redactor, and returns bytes.
// Every response in the credential path leaves through here.
func (s *Server) redactBody(v interface{}) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`{}`)
	}
	return []byte(s.redactor.Redact(string(raw)))
}
