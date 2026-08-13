package server

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// ---------------------------------------------------------------------------
// EnrollmentManager tests
// ---------------------------------------------------------------------------

func TestEnrollmentManager_CreateAndValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "enrollment.json")
	em := NewEnrollmentManager(path)

	et, err := em.CreateToken("test-agent", 1*time.Hour)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if et.Token == "" {
		t.Fatal("token is empty")
	}
	if et.AgentName != "test-agent" {
		t.Errorf("AgentName: got %q, want %q", et.AgentName, "test-agent")
	}
	if et.Used {
		t.Error("token should not be used")
	}

	// Validate should succeed.
	validated, err := em.ValidateToken(et.Token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if validated.Token != et.Token {
		t.Errorf("validated token mismatch: got %q, want %q", validated.Token, et.Token)
	}
}

func TestEnrollmentManager_ExpiredToken(t *testing.T) {
	em := NewEnrollmentManager("")

	et, _ := em.CreateToken("test-agent", 1*time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	_, err := em.ValidateToken(et.Token)
	if err == nil {
		t.Error("expected error for expired token")
	}
}

func TestEnrollmentManager_UsedToken(t *testing.T) {
	em := NewEnrollmentManager("")

	et, _ := em.CreateToken("test-agent", 1*time.Hour)
	em.MarkUsed(et.Token)

	_, err := em.ValidateToken(et.Token)
	if err == nil {
		t.Error("expected error for used token")
	}
}

func TestEnrollmentManager_RevokeToken(t *testing.T) {
	em := NewEnrollmentManager("")

	et, _ := em.CreateToken("test-agent", 1*time.Hour)
	if err := em.RevokeToken(et.Token); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}

	_, err := em.ValidateToken(et.Token)
	if err == nil {
		t.Error("expected error for revoked token")
	}
}

func TestEnrollmentManager_RevokeNonExistent(t *testing.T) {
	em := NewEnrollmentManager("")
	err := em.RevokeToken("nonexistent")
	if err == nil {
		t.Error("expected error for non-existent token")
	}
}

func TestEnrollmentManager_ListTokens(t *testing.T) {
	em := NewEnrollmentManager("")

	em.CreateToken("agent-1", 1*time.Hour)
	em.CreateToken("agent-2", 1*time.Hour)

	tokens := em.ListTokens()
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d", len(tokens))
	}
}

func TestEnrollmentManager_Persistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "enrollment.json")

	// Create tokens with first manager.
	em1 := NewEnrollmentManager(path)
	et, _ := em1.CreateToken("persisted-agent", 1*time.Hour)

	// Load with second manager.
	em2 := NewEnrollmentManager(path)
	validated, err := em2.ValidateToken(et.Token)
	if err != nil {
		t.Fatalf("persistence: ValidateToken: %v", err)
	}
	if validated.AgentName != "persisted-agent" {
		t.Errorf("persistence: AgentName: got %q, want %q", validated.AgentName, "persisted-agent")
	}
}

// ---------------------------------------------------------------------------
// CAManager tests
// ---------------------------------------------------------------------------

func TestCAManager_GenerateAndSign(t *testing.T) {
	dir := t.TempDir()
	cm := NewCAManager(dir)

	caPEM := cm.CACertPEM()
	if len(caPEM) == 0 {
		t.Fatal("CA cert PEM is empty")
	}

	// Parse the CA cert.
	block, _ := pem.Decode(caPEM)
	if block == nil {
		t.Fatal("failed to decode CA cert PEM")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	if !caCert.IsCA {
		t.Error("CA cert should have IsCA=true")
	}
	if caCert.Subject.CommonName != "PROBE-CA" {
		t.Errorf("CA CN: got %q, want %q", caCert.Subject.CommonName, "PROBE-CA")
	}

	// Sign an agent cert.
	certPEM, keyPEM, returnedCAPEM, err := cm.SignAgentCert("agent-test-001")
	if err != nil {
		t.Fatalf("SignAgentCert: %v", err)
	}
	if len(certPEM) == 0 {
		t.Error("agent cert PEM is empty")
	}
	if len(keyPEM) == 0 {
		t.Error("agent key PEM is empty")
	}
	if len(returnedCAPEM) == 0 {
		t.Error("returned CA PEM is empty")
	}

	// Verify the agent cert is signed by the CA.
	agentBlock, _ := pem.Decode(certPEM)
	if agentBlock == nil {
		t.Fatal("failed to decode agent cert PEM")
	}
	agentCert, err := x509.ParseCertificate(agentBlock.Bytes)
	if err != nil {
		t.Fatalf("parse agent cert: %v", err)
	}
	if agentCert.Subject.CommonName != "agent-test-001" {
		t.Errorf("agent cert CN: got %q, want %q", agentCert.Subject.CommonName, "agent-test-001")
	}
	if agentCert.IsCA {
		t.Error("agent cert should not be CA")
	}

	// Verify the agent cert chains to the CA.
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	_, err = agentCert.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		t.Fatalf("agent cert does not chain to CA: %v", err)
	}
}

func TestCAManager_Persistence(t *testing.T) {
	dir := t.TempDir()
	cm1 := NewCAManager(dir)
	caPEM1 := cm1.CACertPEM()

	// Second manager should load the same CA.
	cm2 := NewCAManager(dir)
	caPEM2 := cm2.CACertPEM()

	if string(caPEM1) != string(caPEM2) {
		t.Error("CA cert should be the same after reload")
	}
}

// ---------------------------------------------------------------------------
// Capability checking tests
// ---------------------------------------------------------------------------

func TestCapabilityForCommand(t *testing.T) {
	tests := []struct {
		msgType string
		want    string
	}{
		{protocol.TypeExec, "exec"},
		{protocol.TypeExecPTY, "exec"},
		{protocol.TypeFSList, "filesystem"},
		{protocol.TypeFSRead, "filesystem"},
		{protocol.TypeFileSave, "filesystem"},
		{protocol.TypeCapture, "capture"},
		{protocol.TypePointerClick, "input"},
		{protocol.TypeProcKill, "process"},
		{protocol.TypeTunnelOpen, "tunnel"},
		{protocol.TypeMitmStart, "mitm"},
		{protocol.TypeDebugAttach, "debug"},
		{protocol.TypeClipboardRead, "clipboard"},
		{protocol.TypeOpenLink, "desktop"},
		{protocol.TypePing, ""},
		{protocol.TypePong, ""},
		{protocol.TypeHealth, ""},
		{"unknown_type", ""},
	}

	for _, tt := range tests {
		got := capabilityForCommand(tt.msgType)
		if got != tt.want {
			t.Errorf("capabilityForCommand(%q): got %q, want %q", tt.msgType, got, tt.want)
		}
	}
}

func TestHasCapability_BackwardCompat(t *testing.T) {
	// Empty capabilities = all capabilities (backward compat).
	if !HasCapability(nil, "exec") {
		t.Error("nil capabilities should allow all")
	}
	if !HasCapability([]string{}, "exec") {
		t.Error("empty capabilities should allow all")
	}
}

func TestHasCapability_Present(t *testing.T) {
	caps := []string{"exec", "filesystem", "capture"}
	if !HasCapability(caps, "exec") {
		t.Error("should have exec capability")
	}
	if !HasCapability(caps, "filesystem") {
		t.Error("should have filesystem capability")
	}
}

func TestHasCapability_Absent(t *testing.T) {
	caps := []string{"exec", "filesystem"}
	if HasCapability(caps, "capture") {
		t.Error("should not have capture capability")
	}
}

func TestHasCapability_EmptyRequired(t *testing.T) {
	// Empty required capability = always allowed.
	if !HasCapability([]string{"exec"}, "") {
		t.Error("empty required capability should always pass")
	}
}

func TestCheckAgentCapability_Allowed(t *testing.T) {
	caps := []string{"exec", "filesystem"}
	err := CheckAgentCapability("agent-1", caps, protocol.TypeExec)
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

func TestCheckAgentCapability_Denied(t *testing.T) {
	caps := []string{"filesystem"}
	err := CheckAgentCapability("agent-1", caps, protocol.TypeExec)
	if err == nil {
		t.Error("expected error for missing exec capability")
	}
}

func TestCheckAgentCapability_BackwardCompat(t *testing.T) {
	// Agent with no caps advertised = all caps allowed.
	err := CheckAgentCapability("agent-1", nil, protocol.TypeExec)
	if err != nil {
		t.Errorf("backward compat: expected nil error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RevokedAgents tests
// ---------------------------------------------------------------------------

func TestRevokedAgents_RevokeAndCheck(t *testing.T) {
	ra := NewRevokedAgents()
	ra.Revoke("agent-1")

	if !ra.IsRevoked("agent-1") {
		t.Error("agent-1 should be revoked")
	}
	if ra.IsRevoked("agent-2") {
		t.Error("agent-2 should not be revoked")
	}
}

func TestRevokedAgents_List(t *testing.T) {
	ra := NewRevokedAgents()
	ra.Revoke("agent-1")
	ra.Revoke("agent-2")

	list := ra.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 revoked, got %d", len(list))
	}
}

func TestServer_IsAgentRevoked(t *testing.T) {
	srv := NewServer("", "token", "")
	srv.revokedAgents.Revoke("bad-agent")

	if !srv.IsAgentRevoked("bad-agent") {
		t.Error("bad-agent should be revoked")
	}
	if srv.IsAgentRevoked("good-agent") {
		t.Error("good-agent should not be revoked")
	}
}

// ---------------------------------------------------------------------------
// HTTP endpoint tests
// ---------------------------------------------------------------------------

func TestV1_Enroll_Success(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// Create an enrollment token.
	et, err := srv.enrollment.CreateToken("test-agent", 1*time.Hour)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	body, _ := json.Marshal(EnrollRequest{Token: et.Token})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/enroll", bytesReader(body))
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Fatal("expected ok=true")
	}

	dataBytes, _ := json.Marshal(apiResp.Data)
	var enrollResp EnrollResponse
	if err := json.Unmarshal(dataBytes, &enrollResp); err != nil {
		t.Fatalf("unmarshal enroll response: %v", err)
	}

	if enrollResp.AgentID == "" {
		t.Error("agent_id should not be empty")
	}
	if enrollResp.Certificate == "" {
		t.Error("certificate should not be empty")
	}
	if enrollResp.PrivateKey == "" {
		t.Error("private_key should not be empty")
	}
	if enrollResp.ServerCA == "" {
		t.Error("server_ca should not be empty")
	}
}

func TestV1_Enroll_InvalidToken(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	body, _ := json.Marshal(EnrollRequest{Token: "invalid-token"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/enroll", bytesReader(body))
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestV1_Enroll_MissingToken(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	body, _ := json.Marshal(EnrollRequest{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/enroll", bytesReader(body))
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestV1_Enroll_TokenUsedOnce(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	et, _ := srv.enrollment.CreateToken("test-agent", 1*time.Hour)

	// First enrollment succeeds.
	body1, _ := json.Marshal(EnrollRequest{Token: et.Token})
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest("POST", "/api/v1/enroll", bytesReader(body1))
	srv.mux.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first enroll: expected 201, got %d", rec1.Code)
	}

	// Second enrollment with same token should fail.
	body2, _ := json.Marshal(EnrollRequest{Token: et.Token})
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/v1/enroll", bytesReader(body2))
	srv.mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("second enroll: expected 403, got %d", rec2.Code)
	}
}

func TestV1_CreateEnrollmentToken(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]interface{}{
		"agent_name": "new-agent",
		"ttl_hours":  48,
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/enrollment-tokens", bytesReader(body))
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var apiResp APIResponse
	json.Unmarshal(rec.Body.Bytes(), &apiResp)
	if !apiResp.OK {
		t.Fatal("expected ok=true")
	}
}

func TestV1_ListEnrollmentTokens(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.enrollment.CreateToken("agent-1", 1*time.Hour)
	srv.enrollment.CreateToken("agent-2", 1*time.Hour)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/enrollment-tokens", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var apiResp APIResponse
	json.Unmarshal(rec.Body.Bytes(), &apiResp)
	if !apiResp.OK {
		t.Fatal("expected ok=true")
	}

	dataBytes, _ := json.Marshal(apiResp.Data)
	var tokens []EnrollmentToken
	json.Unmarshal(dataBytes, &tokens)
	if len(tokens) != 2 {
		t.Errorf("expected 2 tokens, got %d", len(tokens))
	}
}

func TestV1_RevokeAgent(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	body := []byte("{}")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/agent-to-revoke/revoke", bytesReader(body))
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if !srv.IsAgentRevoked("agent-to-revoke") {
		t.Error("agent should be revoked after endpoint call")
	}
}

func TestV1_ListRevokedAgents(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.revokedAgents.Revoke("revoked-1")
	srv.revokedAgents.Revoke("revoked-2")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents/revoked", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var apiResp APIResponse
	json.Unmarshal(rec.Body.Bytes(), &apiResp)
	if !apiResp.OK {
		t.Fatal("expected ok=true")
	}

	dataBytes, _ := json.Marshal(apiResp.Data)
	var result map[string]interface{}
	json.Unmarshal(dataBytes, &result)
	revoked, ok := result["revoked_agents"]
	if !ok {
		t.Fatal("missing revoked_agents field")
	}
	arr, ok := revoked.([]interface{})
	if !ok {
		t.Fatalf("revoked_agents should be array, got %T", revoked)
	}
	if len(arr) != 2 {
		t.Errorf("expected 2 revoked agents, got %d", len(arr))
	}
}

// ---------------------------------------------------------------------------
// AgentInfo capabilities serialization test
// ---------------------------------------------------------------------------

func TestAgentInfo_CapabilitiesSerialization(t *testing.T) {
	info := protocol.AgentInfo{
		Name:         "cap-agent",
		Version:      "0.3.0",
		OS:           "linux",
		Arch:         "amd64",
		Mode:         "outbound",
		Capabilities: []string{"exec", "filesystem", "capture"},
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded protocol.AgentInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.Capabilities) != 3 {
		t.Fatalf("expected 3 capabilities, got %d", len(decoded.Capabilities))
	}
	if decoded.Capabilities[0] != "exec" {
		t.Errorf("capability[0]: got %q, want %q", decoded.Capabilities[0], "exec")
	}
}

func TestAgentInfo_CapabilitiesOmitEmpty(t *testing.T) {
	info := protocol.AgentInfo{
		Name:    "no-caps-agent",
		Version: "0.3.0",
		OS:      "linux",
		Arch:    "amd64",
		Mode:    "outbound",
	}

	data, _ := json.Marshal(info)
	s := string(data)
	if contains(s, "capabilities") {
		t.Errorf("expected capabilities to be omitted when empty, got: %s", s)
	}
}

// ---------------------------------------------------------------------------
// Registry capabilities test
// ---------------------------------------------------------------------------

func TestRegistry_RegisterWithCapabilities(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	r := NewRegistry(path)

	caps := []string{"exec", "filesystem", "capture"}
	r.Register("agent-1", "test", "0.3", "linux", "amd64", "outbound", caps)

	rec, err := r.GetHealth("agent-1")
	if err != nil {
		t.Fatalf("GetHealth: %v", err)
	}

	if len(rec.Capabilities) != 3 {
		t.Fatalf("expected 3 capabilities, got %d", len(rec.Capabilities))
	}
	if rec.Capabilities[0] != "exec" {
		t.Errorf("capability[0]: got %q, want %q", rec.Capabilities[0], "exec")
	}
}

func TestRegistry_RegisterUpdateCapabilities(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(dir)

	// Register without caps — the registry persists exactly what the agent
	// advertises. Agents built from v1.15.1+ configs default their
	// capabilities at config-parse time (cmd/probe/config.go applyDefaults,
	// audit F6); the server must not fabricate capabilities for agents that
	// never advertised any.
	r.Register("agent-1", "test", "0.3", "linux", "amd64", "outbound", nil)
	rec, _ := r.GetHealth("agent-1")
	if len(rec.Capabilities) != 0 {
		t.Error("expected 0 capabilities on first register")
	}

	// Re-register with caps — should update to the explicit list.
	r.Register("agent-1", "test", "0.3", "linux", "amd64", "outbound", []string{"exec"})
	rec, _ = r.GetHealth("agent-1")
	if len(rec.Capabilities) != 1 || rec.Capabilities[0] != "exec" {
		t.Errorf("expected [exec], got %v", rec.Capabilities)
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

// bytesReader wraps a byte slice as an io.Reader for httptest.NewRequest.
// We use this instead of bytes.NewReader to avoid importing bytes in the
// test file — keeps the import list minimal.
func bytesReader(b []byte) *bytesReaderImpl {
	return &bytesReaderImpl{data: b}
}

type bytesReaderImpl struct {
	data []byte
	pos  int
}

func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, os.ErrInvalid
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && stringIndex(s, substr) >= 0))
}

func stringIndex(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}