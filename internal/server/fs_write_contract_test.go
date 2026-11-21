package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// fsEchoAgent is an in-process stand-in for a connected agent. It answers an
// fs-write (TypeFileSave) envelope the way the real agent does: it base64-decodes
// the params.Data string and reports the number of bytes written. That makes the
// server-side contract visible in a test without a live agent: if Data is empty
// because the caller used the wrong field name, the echo agent reports written:0
// and the request looks successful.
type fsEchoAgent struct {
	srv *Server
}

func (c fsEchoAgent) WriteJSON(v interface{}) error {
	env, ok := v.(protocol.Envelope)
	if !ok {
		return nil
	}
	var params protocol.FSParams
	if env.Params != nil {
		_ = json.Unmarshal(env.Params, &params)
	}
	decoded, _ := base64.StdEncoding.DecodeString(params.Data)
	body, _ := json.Marshal(protocol.FSWriteResult{Path: params.Path, Written: len(decoded)})

	c.srv.pendingMu.Lock()
	ch := c.srv.pendingReqs[env.ID]
	c.srv.pendingMu.Unlock()
	if ch != nil {
		ch <- protocol.Envelope{ID: env.ID, Type: protocol.TypeFileSaveResult, Result: body}
	}
	return nil
}

func (c fsEchoAgent) WriteMessage(int, []byte) error { return nil }
func (c fsEchoAgent) Close() error                   { return nil }

// newFSWriteTestServer builds a server with one connected echo agent, so the
// fs-write handler reaches the agent instead of failing with "not connected".
func newFSWriteTestServer(t *testing.T) (*Server, func(), string) {
	t.Helper()
	srv := NewServer("", "test-token", "")
	srv.mux = http.NewServeMux()
	srv.requireAPIAuth = false
	srv.registerRoutes()

	const agentID = "agent-fs-write-test"
	srv.mu.Lock()
	srv.conns[agentID] = fsEchoAgent{srv: srv}
	srv.connWriteMu[agentID] = &sync.Mutex{}
	srv.mu.Unlock()

	return srv, func() { srv.registry.Stop() }, agentID
}

func postFSWrite(t *testing.T, srv *Server, agentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/fs-write", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	return rec
}

// TestFSWrite_RejectsWrongFieldName is the regression for the silent-failure
// defect: a caller that sends {path, content} instead of the protocol field
// {path, data} must be rejected at the contract boundary instead of being
// accepted with written:0. Before the fix this returned 200 with
// {"ok":true,"data":{"path":...,"written":0}}.
func TestFSWrite_RejectsWrongFieldName(t *testing.T) {
	srv, cleanup, agentID := newFSWriteTestServer(t)
	defer cleanup()

	rec := postFSWrite(t, srv, agentID, `{"path":"C:\\tmp\\payload.bin","content":"aGVsbG8="}`)

	t.Logf("POST /api/v1/agents/%s/fs-write {path,content} -> status=%d body=%s",
		agentID, rec.Code, rec.Body.String())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong field name must be rejected with 400, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data") {
		t.Fatalf(`error message must name the expected field "data", got: %s`, body)
	}
	if strings.Contains(body, `"ok":true`) {
		t.Fatalf("rejection must not present as a success response: %s", body)
	}
}

// TestFSWrite_ExplicitEmptyWriteSucceeds pins the other half of the contract:
// an explicit empty payload ({"data":""}) is a legitimate zero-byte write and
// must still succeed — and it must be distinguishable from the rejection above
// by status and body.
func TestFSWrite_ExplicitEmptyWriteSucceeds(t *testing.T) {
	srv, cleanup, agentID := newFSWriteTestServer(t)
	defer cleanup()

	rec := postFSWrite(t, srv, agentID, `{"path":"C:\\tmp\\empty.bin","data":""}`)

	t.Logf("POST /api/v1/agents/%s/fs-write {path,data:\"\"} -> status=%d body=%s",
		agentID, rec.Code, rec.Body.String())

	if rec.Code != http.StatusOK {
		t.Fatalf("explicit empty write must succeed with 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK   bool `json:"ok"`
		Data struct {
			Path    string `json:"path"`
			Written int    `json:"written"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected ok=true, got: %s", rec.Body.String())
	}
	if resp.Data.Written != 0 {
		t.Fatalf("explicit empty write must report written:0, got %d", resp.Data.Written)
	}
}

// TestFSWrite_RejectsNonStringData covers the third silent-empty path: a null
// or non-string data field decodes to "" and would otherwise look like an
// intentional empty write.
func TestFSWrite_RejectsNonStringData(t *testing.T) {
	srv, cleanup, agentID := newFSWriteTestServer(t)
	defer cleanup()

	rec := postFSWrite(t, srv, agentID, `{"path":"C:\\tmp\\payload.bin","data":null}`)

	t.Logf("POST /api/v1/agents/%s/fs-write {path,data:null} -> status=%d body=%s",
		agentID, rec.Code, rec.Body.String())

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-string data must be rejected with 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
