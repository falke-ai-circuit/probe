package server

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// fixtureSecret is a stand-in value. The real one never appears in a test, a
// log or an artefact — this string is what the greps search for.
const fixtureSecret = "FIXTURE-SW-PASSWORD-7c1f"

// fakeAgent is an in-process Conn that answers the server's forwarders. It
// models the agent's contract: it decrypts a delivered handle with
// sha256(token|agent_id) and reports the exec result.
type fakeAgent struct {
	t      *testing.T
	srv    *Server
	id     string
	token  string
	mu     sync.Mutex
	sealed []string
	values []string
	execs  int
	// execReply lets a test force a typed agent error.
	execReply map[string]interface{}
	// jobLog is what the fake spools; the server must redact it.
	jobLog string
}

func (f *fakeAgent) WriteMessage(int, []byte) error { return nil }
func (f *fakeAgent) Close() error                   { return nil }

func (f *fakeAgent) WriteJSON(v interface{}) error {
	raw, _ := json.Marshal(v)
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch env.Type {
	case protocol.TypeCredentialDeliver:
		var d protocol.CredentialDelivery
		json.Unmarshal(env.Params, &d)
		f.sealed = append(f.sealed, d.Sealed)
		sealed, _ := base64.StdEncoding.DecodeString(d.Sealed)
		plain, err := openSealedRaw([]byte(f.token+"|"+d.AgentID), sealed)
		if err != nil {
			f.t.Errorf("fake agent: could not unseal handle: %v", err)
			return nil
		}
		f.values = append(f.values, string(plain))
	case protocol.TypeExec:
		f.execs++
		resp := protocol.NewResult(env.ID, protocol.TypeExecResult, protocol.ExecResult{
			// The agent echoes the secret into its own stdout: the response
			// redactor must strip it before the operator sees anything.
			Stdout:   "logged in with " + fixtureSecret,
			ExitCode: 0,
		})
		if f.execReply != nil {
			resp = protocol.NewError(env.ID, "CREDENTIAL_EXPIRED", "credential handle expired")
		}
		f.deliver(env.ID, resp)
	case protocol.TypeJobLog:
		f.deliver(env.ID, protocol.NewResult(env.ID, protocol.TypeJobLogResult, map[string]interface{}{
			"job_id": "job-1",
			"lines":  []string{"device said: " + f.jobLog},
		}))
	}
	return nil
}

func (f *fakeAgent) deliver(reqID string, env protocol.Envelope) {
	f.srv.pendingMu.Lock()
	ch, ok := f.srv.pendingReqs[reqID]
	f.srv.pendingMu.Unlock()
	if ok {
		ch <- env
	}
}

// openSealedRaw decrypts with the raw key sha256(token|agent_id) — the same
// derivation sealForAgent uses. crypto.E2E hashes its input again, so it is not
// a stand-in here.
func openSealedRaw(material []byte, sealed []byte) ([]byte, error) {
	key := sha256.Sum256(material)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
}

func newCredTestServer(t *testing.T) (*Server, *fakeAgent, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PROBE_SECRET_STORE", filepath.Join(dir, "secrets.json"))
	t.Setenv("PROBE_SECRET_KEY", strings.Repeat("ab", 32))
	auditPath := filepath.Join(dir, "audit.jsonl")

	srv := NewServer("", "test-token", filepath.Join(dir, "registry.json"))
	srv.mux = http.NewServeMux()
	srv.registerRoutes()
	srv.audit = NewAuditLogger(auditPath)
	srv.SetE2E("test-token", true)

	f := &fakeAgent{t: t, srv: srv, id: "agent-1", token: "test-token", jobLog: "noise " + fixtureSecret}
	srv.registry.Register("agent-1", "agent-1", "1.19.0", "linux", "amd64", "silent",
		[]string{"exec", "credential"})
	srv.mu.Lock()
	srv.conns["agent-1"] = f
	srv.connWriteMu["agent-1"] = &sync.Mutex{}
	srv.mu.Unlock()

	t.Cleanup(func() { srv.registry.Stop() })
	return srv, f, auditPath
}

func doJSON(srv *Server, method, path string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, &buf)
	srv.mux.ServeHTTP(rec, req)
	return rec
}

// A1: PUT then GET. The value is absent from every response, and the on-disk
// form is ciphertext that does not contain the secret.
func TestCredentialStore_WriteOnlyAndSealed(t *testing.T) {
	srv, _, _ := newCredTestServer(t)

	put := doJSON(srv, "PUT", "/api/v1/credentials/sw-enable", map[string]string{"value": fixtureSecret})
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", put.Code, put.Body.String())
	}
	if strings.Contains(put.Body.String(), fixtureSecret) {
		t.Fatal("PUT response echoed the secret")
	}

	get := doJSON(srv, "GET", "/api/v1/credentials/sw-enable", nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d", get.Code)
	}
	if strings.Contains(get.Body.String(), fixtureSecret) {
		t.Fatalf("GET body contains the secret: %s", get.Body.String())
	}
	var resp struct {
		Data CredentialMeta `json:"data"`
	}
	json.Unmarshal(get.Body.Bytes(), &resp)
	if resp.Data.Name != "sw-enable" || resp.Data.CreatedAt == "" {
		t.Fatalf("GET metadata = %+v", resp.Data)
	}

	raw, err := os.ReadFile(os.Getenv("PROBE_SECRET_STORE"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(fixtureSecret)) {
		t.Fatal("store file contains the plaintext secret")
	}
	if !bytes.Contains(raw, []byte(`"ct"`)) {
		t.Fatalf("store file has no ciphertext field: %s", raw)
	}
	var file credFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("store is not valid JSON: %v", err)
	}
	ct := file.Entries["sw-enable"].Ciphertext
	t.Logf("A1 ciphertext shape: %d bytes base64, prefix=%q", len(ct), ct[:12])
}

// A2: the exec succeeds, and the secret appears in none of the four sinks.
func TestCredentialExec_RedactsAllSinks(t *testing.T) {
	srv, f, auditPath := newCredTestServer(t)

	var logBuf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prev)

	if rec := doJSON(srv, "PUT", "/api/v1/credentials/sw-enable", map[string]string{"value": fixtureSecret}); rec.Code != 200 {
		t.Fatalf("PUT = %d", rec.Code)
	}
	rec := doJSON(srv, "POST", "/api/v1/agents/agent-1/exec", map[string]interface{}{
		"command":         "probe-client ssh --host 127.0.0.1 --user enable --credential sw-enable --script /tmp/x.txt",
		"credential":      "sw-enable",
		"credential_mode": "stdin",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("exec status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, fixtureSecret) {
		t.Fatalf("response body leaked the secret: %s", body)
	}
	if !strings.Contains(body, `"credential"`) || !strings.Contains(body, `"ref":"cred_`) {
		t.Fatalf("response is missing data.credential: %s", body)
	}
	if len(f.values) != 1 || f.values[0] != fixtureSecret {
		t.Fatalf("agent did not receive the value (got %d deliveries)", len(f.values))
	}

	// job log sink. The real agent scrubs the child's output at source; this
	// fake deliberately does not, so the server's own filter is what is being
	// tested here. The redactor is seeded exactly as it is for the duration of
	// a credential call.
	srv.redactor.Add(fixtureSecret)
	jl := doJSON(srv, "POST", "/api/v1/agents/agent-1/job-log", map[string]string{"job_id": "job-1"})
	srv.redactor.Remove(fixtureSecret)
	if jl.Code != http.StatusOK {
		t.Fatalf("job-log = %d", jl.Code)
	}
	if strings.Contains(jl.Body.String(), fixtureSecret) {
		t.Fatalf("job log leaked the secret: %s", jl.Body.String())
	}

	// audit sink
	auditRaw, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("audit file: %v", err)
	}
	sinks := map[string]string{
		"server log":   logBuf.String(),
		"audit log":    string(auditRaw),
		"job log":      jl.Body.String(),
		"response body": body,
	}
	for name, s := range sinks {
		if strings.Contains(s, fixtureSecret) {
			t.Errorf("%s contains the secret", name)
		}
	}
	t.Logf("A2 grep hits per sink: server=%d audit=%d joblog=%d response=%d",
		strings.Count(logBuf.String(), fixtureSecret),
		strings.Count(string(auditRaw), fixtureSecret),
		strings.Count(jl.Body.String(), fixtureSecret),
		strings.Count(body, fixtureSecret))
}

// A3: the three typed refusals, each with its status.
func TestCredentialExec_Negatives(t *testing.T) {
	srv, f, _ := newCredTestServer(t)
	if rec := doJSON(srv, "PUT", "/api/v1/credentials/sw-enable", map[string]string{"value": fixtureSecret}); rec.Code != 200 {
		t.Fatalf("PUT = %d", rec.Code)
	}

	// unknown name -> 404 NOT_FOUND
	rec := doJSON(srv, "POST", "/api/v1/agents/agent-1/exec", map[string]interface{}{
		"command": "true", "credential": "nope",
	})
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "NOT_FOUND") {
		t.Fatalf("unknown credential = %d %s", rec.Code, rec.Body.String())
	}

	// E2E inactive -> 409 CREDENTIAL_CHANNEL_INSECURE
	srv.SetE2E("test-token", false)
	rec = doJSON(srv, "POST", "/api/v1/agents/agent-1/exec", map[string]interface{}{
		"command": "true", "credential": "sw-enable",
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "CREDENTIAL_CHANNEL_INSECURE") {
		t.Fatalf("insecure channel = %d %s", rec.Code, rec.Body.String())
	}

	// expired handle -> agent refuses twice -> 409 CREDENTIAL_EXPIRED after one re-mint
	srv.SetE2E("test-token", true)
	f.execReply = map[string]interface{}{"expired": true}
	rec = doJSON(srv, "POST", "/api/v1/agents/agent-1/exec", map[string]interface{}{
		"command": "true", "credential": "sw-enable",
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "CREDENTIAL_EXPIRED") {
		t.Fatalf("expired handle = %d %s", rec.Code, rec.Body.String())
	}
	if len(f.values) != 2 {
		t.Fatalf("expected exactly one re-mint (2 deliveries), got %d", len(f.values))
	}

	// agent lacks the capability -> 409 CAPABILITY_MISSING
	srv.registry.UpdateCapabilities("agent-1", []string{"exec"})
	rec = doJSON(srv, "POST", "/api/v1/agents/agent-1/exec", map[string]interface{}{
		"command": "true", "credential": "sw-enable",
	})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "CAPABILITY_MISSING") {
		t.Fatalf("missing capability = %d %s", rec.Code, rec.Body.String())
	}
}

// The redactor must not be a passthrough with a nicer name.
func TestRedactor_FiltersAndCounts(t *testing.T) {
	r := NewRedactor()
	r.Add(fixtureSecret)
	in := "login " + fixtureSecret + " ok " + fixtureSecret
	out := r.Redact(in)
	if strings.Contains(out, fixtureSecret) || r.Hits() != 2 {
		t.Fatalf("redactor out=%q hits=%d", out, r.Hits())
	}
	r.Remove(fixtureSecret)
	if got := r.Redact(in); got != in {
		t.Fatalf("redactor still holds the value after Remove: %q", got)
	}
}

// A handle sealed for one agent must not open for another: the key carries the
// agent id, so the wrong agent gets an AES-GCM authentication failure.
func TestHandle_SealedPerAgent(t *testing.T) {
	srv, f, _ := newCredTestServer(t)
	h, err := srv.credHandles.Mint("test-token", "sw-enable", "agent-1", "", fixtureSecret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A different agent id derives a different key.
	if _, err := openSealedRaw([]byte("test-token|agent-2"), h.Sealed); err == nil {
		t.Fatal("a handle sealed for agent-1 decrypted under agent-2's key")
	}
	// The intended agent does open it.
	if v, err := openSealedRaw([]byte("test-token|agent-1"), h.Sealed); err != nil || string(v) != fixtureSecret {
		t.Fatalf("handle did not open for its own agent: %v", err)
	}
	_ = f
}
