package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-03 / Amendment A-1: a 200 means "the agent returned a result envelope".
// Every state where no command outcome exists is a non-200 typed error. These
// tests fail if either half of that regresses.

// echoConn answers with a canned result payload (a successful exec envelope).
type echoConn struct {
	srv    *Server
	result map[string]interface{}
	once   sync.Once
}

func (c *echoConn) WriteJSON(v interface{}) error {
	env, ok := v.(protocol.Envelope)
	if !ok {
		return nil
	}
	c.srv.pendingMu.Lock()
	ch := c.srv.pendingReqs[env.ID]
	c.srv.pendingMu.Unlock()
	if ch == nil {
		return nil
	}
	raw, _ := json.Marshal(c.result)
	ch <- protocol.Envelope{ID: env.ID, Type: protocol.TypeExecResult, Result: raw}
	return nil
}
func (c *echoConn) WriteMessage(int, []byte) error { return nil }
func (c *echoConn) Close() error                   { return nil }

// detailErrConn answers with a typed agent error carrying detail.
type detailErrConn struct {
	srv    *Server
	code   string
	msg    string
	detail map[string]interface{}
}

func (c *detailErrConn) WriteJSON(v interface{}) error {
	env, ok := v.(protocol.Envelope)
	if !ok {
		return nil
	}
	c.srv.pendingMu.Lock()
	ch := c.srv.pendingReqs[env.ID]
	c.srv.pendingMu.Unlock()
	if ch != nil {
		ch <- protocol.Envelope{
			ID:   env.ID,
			Type: protocol.TypeError,
			Error: &protocol.ErrorInfo{
				Code:    c.code,
				Message: c.msg,
				Detail:  c.detail,
			},
		}
	}
	return nil
}
func (c *detailErrConn) WriteMessage(int, []byte) error { return nil }
func (c *detailErrConn) Close() error                   { return nil }

func attachConn(srv *Server, id string, conn interface {
	WriteJSON(interface{}) error
	WriteMessage(int, []byte) error
	Close() error
}) {
	srv.mu.Lock()
	srv.conns[id] = conn
	srv.connWriteMu[id] = &sync.Mutex{}
	srv.mu.Unlock()
}

func postExec(t *testing.T, srv *Server, agentID string, body string) (*httptest.ResponseRecorder, APIResponse) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/exec", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	var resp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", rec.Body.String(), err)
	}
	return rec, resp
}

// (a) The refusal reaches the caller as a typed 409, with the observed
// principal and the remediation — never a 200 claiming the command ran.
func TestExecSystemRefusalSurfacesAsTyped409(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.registry.Register("a1", "n1", "1.19.0", "windows", "amd64", "outbound",
		[]string{"exec", "exec.system"})
	attachConn(srv, "a1", &detailErrConn{
		srv:  srv,
		code: protocol.ErrElevationUnavailable,
		msg:  `exec{as:"SYSTEM"} refused: agent principal is "flk"`,
		detail: map[string]interface{}{
			"principal":      "flk",
			"is_system":      false,
			"token_filtered": true,
			"remediation":    "install as service",
		},
	})

	rec, resp := postExec(t, srv, "a1", `{"command":"whoami","as":"SYSTEM"}`)

	if rec.Code == http.StatusOK {
		t.Fatalf("a refused elevation returned 200 — the silent-success defect (body %s)", rec.Body.String())
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if resp.OK || resp.Error == nil {
		t.Fatalf("expected ok:false with an error envelope, got %s", rec.Body.String())
	}
	if resp.Error.Code != protocol.ErrElevationUnavailable {
		t.Fatalf("error code = %q, want %q", resp.Error.Code, protocol.ErrElevationUnavailable)
	}
	if resp.Error.Detail["principal"] != "flk" {
		t.Fatalf("detail.principal = %v, want \"flk\"", resp.Error.Detail["principal"])
	}
	if resp.Error.Detail["remediation"] == nil {
		t.Fatal("refusal detail carries no remediation")
	}
}

// A pre-1.19 agent that does not advertise exec.system is refused BEFORE
// dispatch: dispatching would run the command unelevated and report success.
func TestExecSystemRefusedBeforeDispatchOnOldAgent(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	srv.registry.Register("old", "n1", "1.18.6", "windows", "amd64", "outbound", nil)
	// No conn is attached: if the gate fails to fire, the handler returns 503
	// instead of this 409 — the test would catch it.

	rec, resp := postExec(t, srv, "old", `{"command":"whoami","as":"SYSTEM"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 UNSUPPORTED_AGENT (body %s)", rec.Code, rec.Body.String())
	}
	if resp.Error == nil || resp.Error.Code != protocol.ErrUnsupportedAgent {
		t.Fatalf("error = %+v, want %q", resp.Error, protocol.ErrUnsupportedAgent)
	}
	if resp.Error.Detail["ran"] != false {
		t.Fatalf("detail.ran = %v, want false — the command must not be dispatched", resp.Error.Detail["ran"])
	}
}

// (b) A command that does not run returns an error envelope with the reason,
// never a 200. Two shapes: agent deadline (504) and agent unreachable (503).
func TestExecNotRunNeverReturns200(t *testing.T) {
	cases := []struct {
		name       string
		agentCode  string
		wantStatus int
		wantCode   string
	}{
		{"deadline", protocol.ErrTimeout, http.StatusGatewayTimeout, protocol.ErrTimeout},
		{"refused-before-spawn", protocol.ErrElevationUnavailable, http.StatusConflict, protocol.ErrElevationUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cleanup := newV1TestServer(t)
			defer cleanup()
			srv.registry.Register("a1", "n1", "1.19.0", "linux", "amd64", "outbound",
				[]string{"exec", "exec.system"})
			attachConn(srv, "a1", &detailErrConn{srv: srv, code: tc.agentCode, msg: "no result"})

			rec, resp := postExec(t, srv, "a1", `{"command":"sleep 300","timeout":1}`)

			if rec.Code == http.StatusOK {
				t.Fatalf("%s returned 200 with no result — Amendment A-1 violated (body %s)",
					tc.name, rec.Body.String())
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if resp.OK || resp.Error == nil {
				t.Fatalf("expected ok:false, got %s", rec.Body.String())
			}
			if resp.Error.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", resp.Error.Code, tc.wantCode)
			}
			if resp.Error.Message == "" {
				t.Fatal("error envelope carries no reason")
			}
			if resp.Error.Detail["started_at_utc"] == nil || resp.Error.Detail["tz"] == nil {
				t.Fatalf("error envelope lacks the time fields: %v", resp.Error.Detail)
			}
		})
	}
}

// A command that RAN and failed is still a result (200) with a non-zero exit
// code — 200 ⇔ a result envelope came back, per §1.3. The two states must not
// be conflated in either direction.
func TestExecFailedCommandIs200WithNonZeroExit(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	attachConn(srv, "a1", &echoConn{srv: srv, result: map[string]interface{}{
		"stdout":           "",
		"stderr":           "not found",
		"exit_code":        1,
		"duration_ms":      12,
		"timed_out":        false,
		"started_at_utc":   "2026-11-23T10:14:02.113Z",
		"started_at_local": "2026-11-23T10:14:02.113Z",
		"tz":               "UTC",
		"principal":        "flk",
	}})

	rec, resp := postExec(t, srv, "a1", `{"command":"definitely-not-a-command"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("a command that ran and failed must be 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if !resp.OK {
		t.Fatalf("expected ok:true result envelope, got %s", rec.Body.String())
	}
	data, _ := resp.Data.(map[string]interface{})
	if data["exit_code"] != float64(1) {
		t.Fatalf("exit_code = %v, want 1", data["exit_code"])
	}
}

// (c) Plain exec is unchanged: same request as v1.18.6, same success shape.
//
//	ASSERTION: rec.Code == 200 && resp.OK && data["exit_code"] == 0 &&
//	           data["stdout"] == "hi"
func TestPlainExecResponseUnchanged(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	attachConn(srv, "a1", &echoConn{srv: srv, result: map[string]interface{}{
		"stdout":           "hi",
		"stderr":           "",
		"exit_code":        0,
		"duration_ms":      3,
		"timed_out":        false,
		"started_at_utc":   "2026-11-23T10:14:02.113Z",
		"started_at_local": "2026-11-23T10:14:02.113Z",
		"tz":               "UTC",
		"principal":        "flk",
	}})

	rec, resp := postExec(t, srv, "a1", `{"command":"echo hi","timeout":10,"workdir":"/tmp"}`)

	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("plain exec changed: status=%d ok=%v body=%s", rec.Code, resp.OK, rec.Body.String())
	}
	data, _ := resp.Data.(map[string]interface{})
	if data["exit_code"] != float64(0) || data["stdout"] != "hi" {
		t.Fatalf("plain exec body changed: %v", data)
	}
	// The additive envelope fields must be present for the operator UI.
	for _, k := range []string{"started_at_utc", "started_at_local", "tz", "duration_ms"} {
		if _, ok := data[k]; !ok {
			t.Fatalf("envelope field %q missing on a successful exec: %v", k, data)
		}
	}
}
