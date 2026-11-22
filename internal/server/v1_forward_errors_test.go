package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// scriptedConn answers every forwarded command with one canned error envelope,
// so the v1 surface mapping can be tested without a live agent.
type scriptedConn struct {
	srv   *Server
	code  string
	msg   string
	delay bool
}

func (c *scriptedConn) WriteJSON(v interface{}) error {
	env, ok := v.(protocol.Envelope)
	if !ok {
		return nil
	}
	c.srv.pendingMu.Lock()
	ch := c.srv.pendingReqs[env.ID]
	c.srv.pendingMu.Unlock()
	if ch != nil {
		ch <- protocol.Envelope{
			ID:    env.ID,
			Type:  protocol.TypeError,
			Error: &protocol.ErrorInfo{Code: c.code, Message: c.msg},
		}
	}
	return nil
}

func (c *scriptedConn) WriteMessage(int, []byte) error { return nil }
func (c *scriptedConn) Close() error                   { return nil }

// TestV1ForwardPreservesAgentErrorCode is P8-12 F4: the agent's typed code must
// survive the v1 surface, and TIMEOUT must be 504 for every fs op rather than
// collapsing into 503 AGENT_ERROR.
func TestV1ForwardPreservesAgentErrorCode(t *testing.T) {
	cases := []struct {
		agentCode  string
		wantStatus int
		wantCode   string
	}{
		{protocol.ErrTimeout, http.StatusGatewayTimeout, protocol.ErrTimeout},
		{protocol.ErrNotFound, http.StatusServiceUnavailable, protocol.ErrNotFound},
		{protocol.ErrFSWriteEmptyData, http.StatusServiceUnavailable, protocol.ErrFSWriteEmptyData},
	}
	for _, tc := range cases {
		t.Run(tc.agentCode, func(t *testing.T) {
			srv, cleanup := newV1TestServer(t)
			defer cleanup()

			conn := &scriptedConn{srv: srv, code: tc.agentCode, msg: "stalled volume"}
			srv.mu.Lock()
			srv.conns["a1"] = conn
			srv.connWriteMu["a1"] = &sync.Mutex{}
			srv.mu.Unlock()

			handler := srv.v1Forward("fs-read", protocol.TypeFSRead, true)
			body := strings.NewReader(`{"path":"/x","deadline_ms":1}`)
			req := httptest.NewRequest("POST", "/api/v1/agents/a1/fs-read", body)
			req.SetPathValue("id", "a1")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var resp APIResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if resp.OK || resp.Error == nil {
				t.Fatalf("expected an error envelope, got %s", rec.Body.String())
			}
			if resp.Error.Code != tc.wantCode {
				t.Fatalf("error code = %q, want %q (agent code was dropped)", resp.Error.Code, tc.wantCode)
			}
		})
	}
}
