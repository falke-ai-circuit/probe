package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestServerWithFlows creates a Server with flow manager + minimal auth
// for integration testing the API routes.
func newTestServerWithFlows(t *testing.T) (*Server, *OperatorManager, string) {
	t.Helper()
	dir := t.TempDir()

	// Set up token auth
	token := "test-token-abc123"
	srv := &Server{
		token:          token,
		requireAPIAuth: true,
	}
	srv.loginLimiter = newLoginRateLimiter()
	srv.tasks = NewTaskManager(filepath.Join(dir, "tasks.json"), srv)
	srv.flows = NewFlowManager(filepath.Join(dir, "flows.json"), srv)
	srv.flowDispatcher = NewFlowDispatcher(srv)
	srv.audit = NewAuditLogger(filepath.Join(dir, "audit.jsonl"))

	// Create operator
	srv.operators = NewOperatorManager(filepath.Join(dir, "operators.json"))
	pwdHash, _ := hashPasswordForTest("admin")
	srv.operators.CreateWithPassword("admin", RoleAdmin, pwdHash, "op-token-xyz")

	// Register routes
	srv.mux = http.NewServeMux()
	srv.registerV1Routes()

	return srv, srv.operators, token
}

// hashPasswordForTest is a simple wrapper to avoid importing bcrypt directly
// in the test. Uses bcrypt-cost 4 for speed in tests.
func hashPasswordForTest(password string) (string, error) {
	// We use the same package's operator.SetPassword indirectly through CreateWithPassword.
	// But that wants (name, role, password, token) — we already pass the password.
	// So just return the plaintext — bcrypt hashing happens inside CreateWithPassword.
	// Actually looking at the signature: CreateWithPassword takes plaintext password
	// and hashes it internally. So we just pass it through.
	return password, nil
}

// loginAsAdmin performs an operator login and returns the JWT.
func loginAsAdmin(t *testing.T, baseURL, password string) string {
	t.Helper()
	body := bytes.NewBufferString(fmt.Sprintf(`{"username":"admin","password":%q}`, password))
	resp, err := http.Post(baseURL+"/api/v1/login", "application/json", body)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	var apiResp APIResponse
	json.NewDecoder(resp.Body).Decode(&apiResp)
	data := apiResp.Data.(map[string]interface{})
	token, ok := data["token"].(string)
	if !ok {
		t.Fatalf("login response missing token: %v", data)
	}
	return token
}

// authGet makes an authenticated GET request.
func authGet(t *testing.T, baseURL, path, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", baseURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

// authPost makes an authenticated POST request with JSON body.
func authPost(t *testing.T, baseURL, path, token string, body interface{}) (*http.Response, []byte) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", baseURL+path, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

// authPut makes an authenticated PUT request with JSON body.
func authPut(t *testing.T, baseURL, path, token string, body interface{}) (*http.Response, []byte) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT", baseURL+path, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

// authDelete makes an authenticated DELETE request.
func authDelete(t *testing.T, baseURL, path, token string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("DELETE", baseURL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestFlowAPI_LoginAndCreate(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	token := loginAsAdmin(t, ts.URL, "admin")

	steps := []FlowStep{
		{ID: "s1", Type: "command", CommandType: "exec"},
	}
	body := map[string]interface{}{
		"name":        "test-flow",
		"description": "an integration test flow",
		"trigger":     map[string]interface{}{"type": "once"},
		"steps":       steps,
	}
	resp, data := authPost(t, ts.URL, "/api/v1/flows", token, body)
	if resp.StatusCode != 200 {
		t.Fatalf("create flow: status=%d body=%s", resp.StatusCode, data)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(data, &apiResp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, data)
	}
	if !apiResp.OK {
		t.Fatalf("create flow not OK: %+v", apiResp)
	}
	flowData := apiResp.Data.(map[string]interface{})
	if flowData["name"] != "test-flow" {
		t.Errorf("name = %v, want test-flow", flowData["name"])
	}
	flowID := flowData["id"].(string)
	if flowID == "" {
		t.Fatal("empty flow ID")
	}
}

func TestFlowAPI_List(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	// Create two flows
	steps := []FlowStep{{ID: "s1", Type: "command"}}
	for _, name := range []string{"alpha", "beta"} {
		body := map[string]interface{}{"name": name, "trigger": map[string]interface{}{"type": "once"}, "steps": steps}
		resp, data := authPost(t, ts.URL, "/api/v1/flows", token, body)
		if resp.StatusCode != 200 {
			t.Fatalf("create %s: %d %s", name, resp.StatusCode, data)
		}
	}

	resp, data := authGet(t, ts.URL, "/api/v1/flows", token)
	if resp.StatusCode != 200 {
		t.Fatalf("list: %d %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "alpha") {
		t.Errorf("list missing alpha: %s", data)
	}
	if !strings.Contains(string(data), "beta") {
		t.Errorf("list missing beta: %s", data)
	}
}

func TestFlowAPI_Get(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	steps := []FlowStep{{ID: "s1", Type: "command"}}
	body := map[string]interface{}{"name": "get-me", "trigger": map[string]interface{}{"type": "once"}, "steps": steps}
	resp, data := authPost(t, ts.URL, "/api/v1/flows", token, body)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	var apiResp APIResponse
	json.Unmarshal(data, &apiResp)
	flowID := apiResp.Data.(map[string]interface{})["id"].(string)

	resp, data = authGet(t, ts.URL, "/api/v1/flows/"+flowID, token)
	if resp.StatusCode != 200 {
		t.Errorf("get: %d %s", resp.StatusCode, data)
	}
}

func TestFlowAPI_GetNotFound(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	resp, _ := authGet(t, ts.URL, "/api/v1/flows/nonexistent", token)
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestFlowAPI_Update(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	steps := []FlowStep{{ID: "s1", Type: "command"}}
	body := map[string]interface{}{"name": "original", "trigger": map[string]interface{}{"type": "once"}, "steps": steps}
	resp, data := authPost(t, ts.URL, "/api/v1/flows", token, body)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	var apiResp APIResponse
	json.Unmarshal(data, &apiResp)
	flowID := apiResp.Data.(map[string]interface{})["id"].(string)

	// Update name
	update := map[string]interface{}{"name": "renamed"}
	resp, data = authPut(t, ts.URL, "/api/v1/flows/"+flowID, token, update)
	if resp.StatusCode != 200 {
		t.Fatalf("update: %d %s", resp.StatusCode, data)
	}

	// Verify
	resp, data = authGet(t, ts.URL, "/api/v1/flows/"+flowID, token)
	if !strings.Contains(string(data), "renamed") {
		t.Errorf("update not applied: %s", data)
	}
}

func TestFlowAPI_Delete(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	steps := []FlowStep{{ID: "s1", Type: "command"}}
	body := map[string]interface{}{"name": "to-delete", "trigger": map[string]interface{}{"type": "once"}, "steps": steps}
	resp, data := authPost(t, ts.URL, "/api/v1/flows", token, body)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	var apiResp APIResponse
	json.Unmarshal(data, &apiResp)
	flowID := apiResp.Data.(map[string]interface{})["id"].(string)

	resp, _ = authDelete(t, ts.URL, "/api/v1/flows/"+flowID, token)
	if resp.StatusCode != 200 {
		t.Errorf("delete: %d", resp.StatusCode)
	}

	resp, _ = authGet(t, ts.URL, "/api/v1/flows/"+flowID, token)
	if resp.StatusCode != 404 {
		t.Errorf("after delete: %d, want 404", resp.StatusCode)
	}
}

func TestFlowAPI_EnableDisable(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	steps := []FlowStep{{ID: "s1", Type: "command"}}
	body := map[string]interface{}{"name": "toggle", "trigger": map[string]interface{}{"type": "once"}, "steps": steps}
	resp, data := authPost(t, ts.URL, "/api/v1/flows", token, body)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	var apiResp APIResponse
	json.Unmarshal(data, &apiResp)
	flowID := apiResp.Data.(map[string]interface{})["id"].(string)

	// Disable
	resp, _ = authPost(t, ts.URL, "/api/v1/flows/"+flowID+"/disable", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("disable: %d", resp.StatusCode)
	}
	resp, data = authGet(t, ts.URL, "/api/v1/flows/"+flowID, token)
	if !strings.Contains(string(data), `"enabled":false`) {
		t.Errorf("after disable, enabled != false: %s", data)
	}

	// Enable
	resp, _ = authPost(t, ts.URL, "/api/v1/flows/"+flowID+"/enable", token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("enable: %d", resp.StatusCode)
	}
	resp, data = authGet(t, ts.URL, "/api/v1/flows/"+flowID, token)
	if !strings.Contains(string(data), `"enabled":true`) {
		t.Errorf("after enable, enabled != true: %s", data)
	}
}

func TestFlowAPI_AssignUnassignAgent(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	steps := []FlowStep{{ID: "s1", Type: "command"}}
	body := map[string]interface{}{"name": "to-assign", "trigger": map[string]interface{}{"type": "once"}, "steps": steps}
	resp, data := authPost(t, ts.URL, "/api/v1/flows", token, body)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	var apiResp APIResponse
	json.Unmarshal(data, &apiResp)
	flowID := apiResp.Data.(map[string]interface{})["id"].(string)

	// Assign
	resp, data = authPost(t, ts.URL, "/api/v1/flows/"+flowID+"/assign", token, map[string]string{"agent_id": "test-agent-1"})
	if resp.StatusCode != 200 {
		t.Fatalf("assign: %d %s", resp.StatusCode, data)
	}

	// List flows for agent
	resp, data = authGet(t, ts.URL, "/api/v1/agents/test-agent-1/flows", token)
	if resp.StatusCode != 200 {
		t.Fatalf("list agent flows: %d %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "to-assign") {
		t.Errorf("agent flow list missing assigned flow: %s", data)
	}

	// Unassign
	resp, _ = authPost(t, ts.URL, "/api/v1/flows/"+flowID+"/unassign", token, map[string]string{"agent_id": "test-agent-1"})
	if resp.StatusCode != 200 {
		t.Errorf("unassign: %d", resp.StatusCode)
	}

	// List should be empty
	resp, data = authGet(t, ts.URL, "/api/v1/agents/test-agent-1/flows", token)
	if strings.Contains(string(data), "to-assign") {
		t.Errorf("agent flow list still has unassigned flow: %s", data)
	}
}

func TestFlowAPI_RunNowNoAgent(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	steps := []FlowStep{{ID: "s1", Type: "emit", Signal: "test"}}
	body := map[string]interface{}{"name": "run-test", "trigger": map[string]interface{}{"type": "once"}, "steps": steps}
	resp, data := authPost(t, ts.URL, "/api/v1/flows", token, body)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	var apiResp APIResponse
	json.Unmarshal(data, &apiResp)
	flowID := apiResp.Data.(map[string]interface{})["id"].(string)

	// Run-now without agent_id (should still create a run record)
	resp, data = authPost(t, ts.URL, "/api/v1/flows/"+flowID+"/run-now", token, nil)
	if resp.StatusCode != 200 {
		t.Errorf("run-now: %d %s", resp.StatusCode, data)
	}
}

func TestFlowAPI_Unauthorized(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()

	// No auth header
	resp, _ := http.Get(ts.URL + "/api/v1/flows")
	if resp.StatusCode != 401 {
		t.Errorf("unauth GET: %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	// Bad token
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/flows", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("bad token: %d, want 401", resp.StatusCode)
	}
}

func TestFlowAPI_InvalidJSON(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/flows", strings.NewReader("{garbage"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("invalid JSON: %d, want 400", resp.StatusCode)
	}
}

func TestFlowAPI_ValidationErrors(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	// Empty name
	resp, _ := authPost(t, ts.URL, "/api/v1/flows", token, map[string]interface{}{
		"name":    "",
		"steps":   []FlowStep{{ID: "s1"}},
		"trigger": map[string]interface{}{"type": "once"},
	})
	if resp.StatusCode != 400 {
		t.Errorf("empty name: %d, want 400", resp.StatusCode)
	}

	// Empty steps
	resp, _ = authPost(t, ts.URL, "/api/v1/flows", token, map[string]interface{}{
		"name":    "no-steps",
		"steps":   []FlowStep{},
		"trigger": map[string]interface{}{"type": "once"},
	})
	if resp.StatusCode != 400 {
		t.Errorf("empty steps: %d, want 400", resp.StatusCode)
	}
}

func TestFlowAPI_FullRoundtrip(t *testing.T) {
	srv, _, _ := newTestServerWithFlows(t)
	ts := httptest.NewServer(srv.mux)
	defer ts.Close()
	token := loginAsAdmin(t, ts.URL, "admin")

	// 1. Create
	steps := []FlowStep{
		{ID: "s1", Type: "command", CommandType: "exec"},
		{ID: "s2", Type: "emit", Signal: "done", Next: ""},
	}
	body := map[string]interface{}{
		"name":        "roundtrip",
		"description": "full lifecycle test",
		"trigger":     map[string]interface{}{"type": "recurring", "interval_seconds": 60},
		"steps":       steps,
	}
	resp, data := authPost(t, ts.URL, "/api/v1/flows", token, body)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d %s", resp.StatusCode, data)
	}
	var apiResp APIResponse
	json.Unmarshal(data, &apiResp)
	flowID := apiResp.Data.(map[string]interface{})["id"].(string)

	// 2. Get
	resp, _ = authGet(t, ts.URL, "/api/v1/flows/"+flowID, token)
	if resp.StatusCode != 200 {
		t.Errorf("get: %d", resp.StatusCode)
	}

	// 3. Update description
	resp, _ = authPut(t, ts.URL, "/api/v1/flows/"+flowID, token, map[string]interface{}{"description": "updated"})
	if resp.StatusCode != 200 {
		t.Errorf("update: %d", resp.StatusCode)
	}

	// 4. Disable
	resp, _ = authPost(t, ts.URL, "/api/v1/flows/"+flowID+"/disable", token, nil)
	if resp.StatusCode != 200 {
		t.Errorf("disable: %d", resp.StatusCode)
	}

	// 5. Run-now (no agent — should succeed and create pending run)
	resp, _ = authPost(t, ts.URL, "/api/v1/flows/"+flowID+"/run-now", token, nil)
	if resp.StatusCode != 200 {
		t.Errorf("run-now: %d", resp.StatusCode)
	}

	// 6. List runs
	resp, data = authGet(t, ts.URL, "/api/v1/flow-runs?flow_id="+flowID, token)
	if resp.StatusCode != 200 {
		t.Errorf("list runs: %d %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), `"flow_id":"`+flowID) {
		t.Errorf("runs missing flow_id: %s", data)
	}

	// 7. Delete
	resp, _ = authDelete(t, ts.URL, "/api/v1/flows/"+flowID, token)
	if resp.StatusCode != 200 {
		t.Errorf("delete: %d", resp.StatusCode)
	}

	// 8. Get after delete (should be 404)
	resp, _ = authGet(t, ts.URL, "/api/v1/flows/"+flowID, token)
	if resp.StatusCode != 404 {
		t.Errorf("get after delete: %d, want 404", resp.StatusCode)
	}
}

// helper for compatibility with stdlib testing
var _ = os.Getenv
var _ = filepath.Join
