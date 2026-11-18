package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers for v1 API
// ---------------------------------------------------------------------------

// newV1TestServer creates a Server with a fresh mux and registered routes,
// ready for httptest. Returns the server and a cleanup function.
func newV1TestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	srv := NewServer("", "test-token", "")
	srv.mux = http.NewServeMux()
	srv.registerRoutes()
	return srv, func() {
		srv.registry.Stop()
	}
}

// ---------------------------------------------------------------------------
// Response format tests
// ---------------------------------------------------------------------------

// TestV1_HealthResponseFormat verifies the v1 health endpoint returns the
// consistent APIResponse format with ok=true.
func TestV1_HealthResponseFormat(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// Use the mux directly via httptest.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Error("expected ok=true")
	}
	if apiResp.Error != nil {
		t.Errorf("expected nil error, got %+v", apiResp.Error)
	}
	// Data should be a map with "status" key.
	data, ok := apiResp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", apiResp.Data)
	}
	if data["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", data["status"])
	}
}

// TestV1_HealthContentType verifies the Content-Type is application/json.
func TestV1_HealthContentType(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	srv.mux.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// Error handling tests
// ---------------------------------------------------------------------------

// TestV1_GetAgentNotFound verifies that a non-existent agent returns a
// v1-formatted error with 404 and NOT_FOUND code.
func TestV1_GetAgentNotFound(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents/nonexistent", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if apiResp.OK {
		t.Error("expected ok=false for not-found agent")
	}
	if apiResp.Error == nil {
		t.Fatal("expected non-nil error")
	}
	if apiResp.Error.Code != "NOT_FOUND" {
		t.Errorf("expected code NOT_FOUND, got %q", apiResp.Error.Code)
	}
	if apiResp.Error.Message == "" {
		t.Error("expected non-empty error message")
	}
}

// TestV1_ExecAgentNotConnected verifies that exec on an unconnected agent
// returns a 503 error with the AGENT_UNREACHABLE code.
func TestV1_ExecAgentNotConnected(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	body := map[string]interface{}{
		"command": "echo hello",
	}
	bodyBytes, _ := json.Marshal(body)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/ghost/exec", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	srv.mux.ServeHTTP(rec, req)

	// The legacy handleAgentExec returns 503 for unconnected agents.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if apiResp.OK {
		t.Error("expected ok=false for unconnected agent")
	}
	if apiResp.Error == nil {
		t.Fatal("expected non-nil error")
	}
}

// TestV1_ExecInvalidJSON verifies that invalid JSON returns a 400 error.
func TestV1_ExecInvalidJSON(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// Register the agent so it passes the connection check won't matter —
	// the JSON parse happens before forwardToAgent.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/test-agent/exec", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if apiResp.OK {
		t.Error("expected ok=false for invalid JSON")
	}
	if apiResp.Error == nil {
		t.Fatal("expected non-nil error")
	}
}

// ---------------------------------------------------------------------------
// Agent list/get tests
// ---------------------------------------------------------------------------

// TestV1_ListAgents verifies the v1 list agents endpoint returns the
// registered agents in the consistent response format.
func TestV1_ListAgents(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("agent-1", "test-1", "1.0", "linux", "amd64", "outbound", nil)
	srv.registry.Register("agent-2", "test-2", "1.0", "windows", "amd64", "inbound", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Error("expected ok=true")
	}

	data, ok := apiResp.Data.([]interface{})
	if !ok {
		t.Fatalf("expected array data, got %T", apiResp.Data)
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(data))
	}
}

// TestV1_GetAgent verifies the v1 get agent endpoint returns a single agent.
func TestV1_GetAgent(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("agent-1", "test-1", "1.0", "linux", "amd64", "outbound", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents/agent-1", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Error("expected ok=true")
	}

	data, ok := apiResp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", apiResp.Data)
	}
	if data["agent_id"] != "agent-1" {
		t.Errorf("expected agent_id=agent-1, got %v", data["agent_id"])
	}
}

// TestV1_DeleteAgent verifies the v1 delete agent endpoint unregisters an agent.
func TestV1_DeleteAgent(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("agent-1", "test-1", "1.0", "linux", "amd64", "outbound", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/agents/agent-1", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Error("expected ok=true")
	}
	data, ok := apiResp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", apiResp.Data)
	}
	if data["removed"] != "agent-1" {
		t.Errorf("expected removed=agent-1, got %v", data["removed"])
	}

	// Verify the row is gone entirely, not merely flipped to inactive.
	if agents := srv.registry.ListAgents(); len(agents) != 0 {
		t.Fatalf("expected 0 agents after delete, got %d (%+v)", len(agents), agents)
	}
}

// TestV1_AgentHealth verifies the v1 agent health endpoint.
func TestV1_AgentHealth(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("agent-1", "test-1", "1.0", "linux", "amd64", "outbound", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents/agent-1/health", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Error("expected ok=true")
	}
	data, ok := apiResp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", apiResp.Data)
	}
	if data["agent_id"] != "agent-1" {
		t.Errorf("expected agent_id=agent-1, got %v", data["agent_id"])
	}
}

// TestV1_AgentHealthNotFound verifies 404 for unknown agent health.
func TestV1_AgentHealthNotFound(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents/ghost/health", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var apiResp APIResponse
	json.Unmarshal(rec.Body.Bytes(), &apiResp)
	if apiResp.OK {
		t.Error("expected ok=false")
	}
	if apiResp.Error == nil || apiResp.Error.Code != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND error, got %+v", apiResp.Error)
	}
}

// ---------------------------------------------------------------------------
// Operator management tests
// ---------------------------------------------------------------------------

// TestV1_ListOperators verifies the v1 list operators endpoint.
func TestV1_ListOperators(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// When no operators are configured, the server falls back to legacy
	// token auth, so v1CheckAuth allows the request through. We create
	// one operator to have data to list.
	srv.operators.Create("alice", RoleAdmin, "tok-1")
	srv.operators.Create("bob", RoleViewer, "tok-2")

	// Set an operator token so we can authenticate.
	// In legacy mode (no operators initially), v1CheckAuth allows through.
	// But after creating operators, we need to authenticate.
	// We'll use the token via Authorization header.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/operators", nil)
	req.Header.Set("Authorization", "Bearer tok-1")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Error("expected ok=true")
	}
	data, ok := apiResp.Data.([]interface{})
	if !ok {
		t.Fatalf("expected array data, got %T", apiResp.Data)
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 operators, got %d", len(data))
	}
}

// TestV1_CreateOperator verifies the v1 create operator endpoint.
func TestV1_CreateOperator(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// Create an admin operator first, then use its token to create more.
	admin, _ := srv.operators.Create("admin", RoleAdmin, "admin-tok")

	body := map[string]interface{}{
		"name": "new-op",
		"role": "viewer",
	}
	bodyBytes, _ := json.Marshal(body)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/operators", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer admin-tok")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Error("expected ok=true")
	}
	data, ok := apiResp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", apiResp.Data)
	}
	if data["name"] != "new-op" {
		t.Errorf("expected name=new-op, got %v", data["name"])
	}
	if data["role"] != "viewer" {
		t.Errorf("expected role=viewer, got %v", data["role"])
	}

	// Verify the operator was created.
	if srv.operators.Get(admin.ID) == nil {
		t.Error("admin operator missing")
	}
	// Verify new-op exists in the manager.
	ops := srv.operators.List()
	found := false
	for _, op := range ops {
		if op.Name == "new-op" {
			found = true
		}
	}
	if !found {
		t.Error("new-op not found in operator list")
	}
}

// TestV1_DeleteOperator verifies the v1 delete operator endpoint.
func TestV1_DeleteOperator(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	admin, _ := srv.operators.Create("admin", RoleAdmin, "admin-tok")
	target, _ := srv.operators.Create("target", RoleViewer, "target-tok")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/operators/"+target.ID, nil)
	req.Header.Set("Authorization", "Bearer admin-tok")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Error("expected ok=true")
	}
	if srv.operators.Get(target.ID) != nil {
		t.Error("operator should be deleted")
	}
	_ = admin
}

// TestV1_DeleteOperatorNotFound verifies 404 for deleting a non-existent operator.
func TestV1_DeleteOperatorNotFound(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.operators.Create("admin", RoleAdmin, "admin-tok")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/operators/no-such-op", nil)
	req.Header.Set("Authorization", "Bearer admin-tok")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var apiResp APIResponse
	json.Unmarshal(rec.Body.Bytes(), &apiResp)
	if apiResp.OK {
		t.Error("expected ok=false")
	}
	if apiResp.Error == nil || apiResp.Error.Code != "NOT_FOUND" {
		t.Errorf("expected NOT_FOUND, got %+v", apiResp.Error)
	}
}

// ---------------------------------------------------------------------------
// Audit log tests
// ---------------------------------------------------------------------------

// TestV1_AuditQuery verifies the v1 audit query endpoint.
func TestV1_AuditQuery(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// Set up an audit logger with a temp file.
	dir := t.TempDir()
	srv.audit = NewAuditLogger(dir + "/audit.jsonl")

	srv.audit.Log(AuditEntry{AgentID: "a1", OperatorID: "op1", Action: "exec", Result: "success"})
	srv.audit.Log(AuditEntry{AgentID: "a2", OperatorID: "op2", Action: "fs-read", Result: "success"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/audit", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Error("expected ok=true")
	}
	data, ok := apiResp.Data.([]interface{})
	if !ok {
		t.Fatalf("expected array data, got %T", apiResp.Data)
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 audit entries, got %d", len(data))
	}
}

// TestV1_AgentAudit verifies the v1 per-agent audit endpoint.
func TestV1_AgentAudit(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	dir := t.TempDir()
	srv.audit = NewAuditLogger(dir + "/audit.jsonl")

	srv.audit.Log(AuditEntry{AgentID: "a1", Action: "exec", Result: "success"})
	srv.audit.Log(AuditEntry{AgentID: "a2", Action: "fs-read", Result: "success"})
	srv.audit.Log(AuditEntry{AgentID: "a1", Action: "fs-write", Result: "success"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents/a1/audit", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !apiResp.OK {
		t.Error("expected ok=true")
	}
	data, ok := apiResp.Data.([]interface{})
	if !ok {
		t.Fatalf("expected array data, got %T", apiResp.Data)
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 audit entries for a1, got %d", len(data))
	}
}

// ---------------------------------------------------------------------------
// Request ID tests
// ---------------------------------------------------------------------------

// TestGenerateRequestID verifies that generateRequestID returns a 32-char
// hex string and that successive calls produce different IDs.
func TestGenerateRequestID(t *testing.T) {
	id1 := generateRequestID()
	id2 := generateRequestID()

	if len(id1) != 32 {
		t.Errorf("expected 32-char ID, got %d chars: %q", len(id1), id1)
	}
	// Should be hex.
	for _, c := range id1 {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("expected hex char, got %q in %q", c, id1)
			break
		}
	}
	if id1 == id2 {
		t.Error("expected different IDs on successive calls")
	}
}

// ---------------------------------------------------------------------------
// OpenAPI spec tests
// ---------------------------------------------------------------------------

// TestOpenAPI_ServedCorrectly verifies that /openapi.json returns a valid
// OpenAPI 3.0 document with the expected structure.
func TestOpenAPI_ServedCorrectly(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/openapi.json", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var spec map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if spec["openapi"] != "3.0.3" {
		t.Errorf("expected openapi 3.0.3, got %v", spec["openapi"])
	}

	info, ok := spec["info"].(map[string]interface{})
	if !ok {
		t.Fatal("expected info object")
	}
	if info["title"] == "" {
		t.Error("expected non-empty title")
	}

	paths, ok := spec["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("expected paths object")
	}

	// Verify a sample of expected paths exist.
	expectedPaths := []string{
		"/api/v1/health",
		"/api/v1/agents",
		"/api/v1/agents/{id}",
		"/api/v1/agents/{id}/exec",
		"/api/v1/agents/{id}/fs-read",
		"/api/v1/agents/{id}/fs-write",
		"/api/v1/agents/{id}/fs-list",
		"/api/v1/agents/{id}/fs-move",
		"/api/v1/agents/{id}/fs-mkdir",
		"/api/v1/agents/{id}/fs-delete",
		"/api/v1/agents/{id}/tunnel",
		"/api/v1/agents/{id}/mitm-start",
		"/api/v1/agents/{id}/debug-attach",
		"/api/v1/agents/{id}/debug-read-mem",
		"/api/v1/agents/{id}/update",
		"/api/v1/agents/{id}/health",
		"/api/v1/agents/{id}/audit",
		"/api/v1/operators",
		"/api/v1/operators/{id}",
		"/api/v1/audit",
	}
	for _, p := range expectedPaths {
		if _, ok := paths[p]; !ok {
			t.Errorf("expected path %q in spec", p)
		}
	}

	// Verify the APIResponse schema is in components.
	components, ok := spec["components"].(map[string]interface{})
	if !ok {
		t.Fatal("expected components object")
	}
	schemas, ok := components["schemas"].(map[string]interface{})
	if !ok {
		t.Fatal("expected schemas object")
	}
	if _, ok := schemas["APIResponse"]; !ok {
		t.Error("expected APIResponse schema")
	}
	if _, ok := schemas["APIError"]; !ok {
		t.Error("expected APIError schema")
	}
}

// TestOpenAPI_SpecStructure verifies the openapiSpec() function returns a
// valid structure with the correct number of paths.
func TestOpenAPI_SpecStructure(t *testing.T) {
	spec := openapiSpec()
	paths := spec["paths"].(map[string]interface{})

	// We expect 28+ paths (25 agent command + server + operators + audit + openapi).
	if len(paths) < 25 {
		t.Errorf("expected at least 25 paths, got %d", len(paths))
	}
}

// ---------------------------------------------------------------------------
// Backward compatibility test
// ---------------------------------------------------------------------------

// TestV1_LegacyRoutesStillWork verifies that the old /api/agents and
// /api/agent/ routes still work alongside the v1 routes.
func TestV1_LegacyRoutesStillWork(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("agent-1", "test-1", "1.0", "linux", "amd64", "outbound", nil)

	// Legacy list agents — should return raw JSON array, not v1 format.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/agents", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Legacy format: raw JSON array (no ok/data wrapper).
	var rawAgents []interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &rawAgents); err != nil {
		t.Fatalf("unmarshal legacy: %v (body: %s)", err, rec.Body.String())
	}
	if len(rawAgents) != 1 {
		t.Fatalf("expected 1 agent in legacy response, got %d", len(rawAgents))
	}
}

// ---------------------------------------------------------------------------
// Method routing test
// ---------------------------------------------------------------------------

// TestV1_MethodRouting verifies that Go 1.22 ServeMux patterns correctly
// route GET vs POST to the right handlers.
func TestV1_MethodRouting(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("agent-1", "test-1", "1.0", "linux", "amd64", "outbound", nil)

	// GET /api/v1/agents should work (list).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents", nil)
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/v1/agents: expected 200, got %d", rec.Code)
	}

	// POST /api/v1/agents should be 405 (method not allowed — only GET registered).
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/v1/agents", nil)
	srv.mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/v1/agents: expected 405, got %d", rec2.Code)
	}

	// DELETE /api/v1/agents/{id} should work.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("DELETE", "/api/v1/agents/agent-1", nil)
	srv.mux.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("DELETE /api/v1/agents/agent-1: expected 200, got %d", rec3.Code)
	}

	// GET /api/v1/agents/{id}/exec should be 405 (only POST registered).
	rec4 := httptest.NewRecorder()
	req4 := httptest.NewRequest("GET", "/api/v1/agents/agent-1/exec", nil)
	srv.mux.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/v1/agents/{id}/exec: expected 405, got %d", rec4.Code)
	}
}

// ---------------------------------------------------------------------------
// RBAC test
// ---------------------------------------------------------------------------

// TestV1_RBACViewerDenied verifies that a viewer-role operator cannot
// perform an exec command (returns 403).
func TestV1_RBACViewerDenied(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// Create a viewer operator.
	_, _ = srv.operators.Create("viewer", RoleViewer, "viewer-tok")

	body := map[string]interface{}{
		"command": "echo hello",
	}
	bodyBytes, _ := json.Marshal(body)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/test/exec", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer viewer-tok")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (body: %s)", rec.Code, rec.Body.String())
	}

	var apiResp APIResponse
	json.Unmarshal(rec.Body.Bytes(), &apiResp)
	if apiResp.OK {
		t.Error("expected ok=false")
	}
	if apiResp.Error == nil || apiResp.Error.Code != "FORBIDDEN" {
		t.Errorf("expected FORBIDDEN, got %+v", apiResp.Error)
	}
}

// TestV1_RBACViewerAllowed verifies that a viewer-role operator can
// perform a read-only action (fs-list).
func TestV1_RBACViewerAllowed(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	_, _ = srv.operators.Create("viewer", RoleViewer, "viewer-tok")

	// fs-list is read-only, so viewer should pass RBAC — but the agent
	// isn't connected, so we expect a 503 AGENT_UNREACHABLE (not 403).
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/test/fs-list", strings.NewReader(`{"path":"."}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer viewer-tok")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("viewer should be allowed fs-list, got 403 (body: %s)", rec.Body.String())
	}
	// Expect 503 (agent not connected) — the important thing is NOT 403.
	var apiResp APIResponse
	json.Unmarshal(rec.Body.Bytes(), &apiResp)
	if apiResp.Error != nil && apiResp.Error.Code == "FORBIDDEN" {
		t.Error("viewer should not be forbidden from fs-list")
	}
}

// ---------------------------------------------------------------------------
// Context test — ensure operator is set in context for wrapped handlers
// ---------------------------------------------------------------------------

// TestV1_OperatorContextSet verifies that the v1 wrapper sets the operator
// in the request context so that legacy handlers can use operatorIDFromRequest.
func TestV1_OperatorContextSet(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	op, _ := srv.operators.Create("admin", RoleAdmin, "admin-tok")

	// We'll test this by using a wrapped handler that checks the context.
	wrapped := false
	checkHandler := func(w http.ResponseWriter, r *http.Request, agentID string) {
		ctxOp := operatorFromContext(r)
		if ctxOp != nil && ctxOp.ID == op.ID {
			wrapped = true
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
	}

	// Register a test handler using the v1 wrapper.
	srv.mux.HandleFunc("POST /api/v1/agents/{id}/test-ctx",
		srv.v1WrapAgentHandler("exec", checkHandler))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/test/test-ctx", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer admin-tok")
	srv.mux.ServeHTTP(rec, req)

	if !wrapped {
		t.Error("expected operator to be set in request context")
	}
}

// ---------------------------------------------------------------------------
// Error code mapping test
// ---------------------------------------------------------------------------

// TestErrorCodeFromStatus verifies the error code mapping function.
func TestErrorCodeFromStatus(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "BAD_REQUEST"},
		{http.StatusUnauthorized, "UNAUTHORIZED"},
		{http.StatusForbidden, "FORBIDDEN"},
		{http.StatusNotFound, "NOT_FOUND"},
		{http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED"},
		{http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{http.StatusGatewayTimeout, "TIMEOUT"},
		{http.StatusInternalServerError, "INTERNAL_ERROR"},
		{418, "HTTP_418"}, // unknown code fallback
	}
	for _, tt := range tests {
		got := errorCodeFromStatus(tt.status)
		if got != tt.want {
			t.Errorf("errorCodeFromStatus(%d): got %q, want %q", tt.status, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// writeJSON / writeError unit tests
// ---------------------------------------------------------------------------

// TestWriteJSON verifies the writeJSON helper produces correct output.
func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusOK, map[string]interface{}{"foo": "bar"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("expected application/json, got %q", rec.Header().Get("Content-Type"))
	}

	var resp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.OK {
		t.Error("expected ok=true")
	}
	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", resp.Data)
	}
	if data["foo"] != "bar" {
		t.Errorf("expected foo=bar, got %v", data["foo"])
	}
}

// TestWriteError verifies the writeError helper produces correct output.
func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, http.StatusBadRequest, "BAD_REQUEST", "missing field")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}

	var resp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.OK {
		t.Error("expected ok=false")
	}
	if resp.Error == nil {
		t.Fatal("expected non-nil error")
	}
	if resp.Error.Code != "BAD_REQUEST" {
		t.Errorf("expected code BAD_REQUEST, got %q", resp.Error.Code)
	}
	if resp.Error.Message != "missing field" {
		t.Errorf("expected message 'missing field', got %q", resp.Error.Message)
	}
}

// ---------------------------------------------------------------------------
// fs-* endpoint tests (forward handler)
// ---------------------------------------------------------------------------

// TestV1_FSListAgentNotConnected verifies that fs-list on an unconnected
// agent returns the v1 error format (not legacy plain text).
func TestV1_FSListAgentNotConnected(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/ghost/fs-list", strings.NewReader(`{"path":"."}`))
	req.Header.Set("Content-Type", "application/json")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &apiResp); err != nil {
		t.Fatalf("unmarshal: %v (body: %s)", err, rec.Body.String())
	}
	if apiResp.OK {
		t.Error("expected ok=false")
	}
	if apiResp.Error == nil {
		t.Fatal("expected non-nil error")
	}
}

// TestV1_FSMoveAgentNotConnected verifies fs-move uses the forward handler.
func TestV1_FSMoveAgentNotConnected(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/ghost/fs-move",
		strings.NewReader(`{"from":"a","to":"b"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}

	var apiResp APIResponse
	json.Unmarshal(rec.Body.Bytes(), &apiResp)
	if apiResp.OK {
		t.Error("expected ok=false")
	}
}

// TestV1_FSMkdirAgentNotConnected verifies fs-mkdir uses the forward handler.
func TestV1_FSMkdirAgentNotConnected(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/ghost/fs-mkdir",
		strings.NewReader(`{"path":"/tmp/test"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// TestV1_FSDeleteAgentNotConnected verifies fs-delete uses the forward handler.
func TestV1_FSDeleteAgentNotConnected(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/ghost/fs-delete",
		strings.NewReader(`{"path":"/tmp/test"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Unused import guard: context and time are used in wrapper/forward code.
// ---------------------------------------------------------------------------

var _ = context.WithValue
var _ = time.Now
// ---------------------------------------------------------------------------
// P6-04: DELETE /api/v1/agents/{id} must actually delete the row.
// ---------------------------------------------------------------------------

// TestV1_DeleteAgent_RemovesRowAndPersistedRegistry is the P6-04 regression:
// before the fix Unregister only flipped status to "inactive", so the id
// survived both in GET /api/v1/agents and in registry.json on disk.
func TestV1_DeleteAgent_RemovesRowAndPersistedRegistry(t *testing.T) {
	dir := t.TempDir()
	regPath := dir + "/registry.json"

	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.registry = NewRegistry(regPath)
	defer srv.registry.Stop()

	srv.registry.Register("agent-1", "keep-me-out", "1.0", "linux", "amd64", "outbound", nil)
	srv.registry.Register("agent-2", "bystander", "1.0", "linux", "amd64", "outbound", nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/agents/agent-1", nil)
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	for _, a := range srv.registry.ListAgents() {
		if a.AgentID == "agent-1" {
			t.Fatalf("deleted agent agent-1 still present in registry list (status=%q)", a.Status)
		}
	}
	if got := len(srv.registry.ListAgents()); got != 1 {
		t.Fatalf("expected 1 surviving agent, got %d", got)
	}

	raw, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatalf("read registry file: %v", err)
	}
	if strings.Contains(string(raw), "agent-1") {
		t.Fatalf("registry.json still contains deleted agent id agent-1:\n%s", raw)
	}
	if !strings.Contains(string(raw), "agent-2") {
		t.Fatalf("registry.json lost the unrelated agent agent-2:\n%s", raw)
	}
	t.Logf("registry.json after DELETE (%s): %s", regPath, raw)
}

// TestV1_DeleteAgent_ConnectedRefused verifies a live connection is not
// silently orphaned: the endpoint refuses instead of deleting the row under
// a running agent.
func TestV1_DeleteAgent_ConnectedRefused(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	srv.registry.Register("agent-live", "live", "1.0", "linux", "amd64", "outbound", nil)
	srv.mu.Lock()
	srv.conns["agent-live"] = stubConn{}
	srv.mu.Unlock()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/v1/agents/agent-live", nil)
	srv.mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatalf("expected non-200 for connected agent, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rec.Code)
	}
	if _, err := srv.registry.GetHealth("agent-live"); err != nil {
		t.Fatalf("connected agent row must survive a refused delete: %v", err)
	}
}

// stubConn is a no-op Conn for tests that only need connection presence.
type stubConn struct{}

func (stubConn) WriteJSON(interface{}) error          { return nil }
func (stubConn) WriteMessage(int, []byte) error        { return nil }
func (stubConn) Close() error                          { return nil }
