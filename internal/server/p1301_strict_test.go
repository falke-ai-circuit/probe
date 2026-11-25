package server

// P13-01 acceptance, server side: the 14 §5.5 rows refuse an unknown key, the
// duplicate selector is refused, E1 refuses before dispatch, and E2 refuses a
// context-scoped result that omits context_ref.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// resultConn answers every forwarded command with one canned success payload,
// so the post-dispatch assertion (E2) can be exercised without a live agent.
type resultConn struct {
	srv     *Server
	payload map[string]interface{}
}

func (c *resultConn) WriteJSON(v interface{}) error {
	env, ok := v.(protocol.Envelope)
	if !ok {
		return nil
	}
	c.srv.pendingMu.Lock()
	ch := c.srv.pendingReqs[env.ID]
	c.srv.pendingMu.Unlock()
	if ch != nil {
		raw, _ := json.Marshal(c.payload)
		ch <- protocol.Envelope{ID: env.ID, Type: protocol.TypePointerClickResult, Result: raw}
	}
	return nil
}

func (c *resultConn) WriteMessage(int, []byte) error { return nil }
func (c *resultConn) Close() error                   { return nil }

// attachP1301Agent registers an agent record and a live connection. The record
// is what E1 reads: capabilities are advertised, never inferred from version.
func attachP1301Agent(t *testing.T, srv *Server, id, version string, caps []string, conn interface {
	WriteJSON(v interface{}) error
	WriteMessage(int, []byte) error
	Close() error
}) {
	t.Helper()
	srv.registry.Register(id, id, version, "linux", "amd64", "serve", caps)
	srv.mu.Lock()
	srv.conns[id] = conn
	srv.connWriteMu[id] = &sync.Mutex{}
	srv.mu.Unlock()
}

func postP1301(srv *Server, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, bytes.NewReader([]byte(body)))
	srv.mux.ServeHTTP(rec, req)
	return rec
}

func decodeAPIError(t *testing.T, rec *httptest.ResponseRecorder) APIResponse {
	t.Helper()
	var out APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response %q: %v", rec.Body.String(), err)
	}
	return out
}

func wantRefusal(t *testing.T, rec *httptest.ResponseRecorder, status int, code, reason string) map[string]interface{} {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body.String())
	}
	resp := decodeAPIError(t, rec)
	if resp.OK {
		t.Fatalf("ok = true on a refusal: %s", rec.Body.String())
	}
	if resp.Error == nil || resp.Error.Code != code {
		t.Fatalf("code = %+v, want %q", resp.Error, code)
	}
	detail := resp.Error.Detail
	if detail == nil {
		t.Fatalf("detail missing on %q refusal", code)
	}
	if detail["ran"] != false {
		t.Fatalf("detail.ran = %v, want false", detail["ran"])
	}
	if reason != "" && detail["reason"] != reason {
		t.Fatalf("detail.reason = %v, want %q (body %s)", detail["reason"], reason, rec.Body.String())
	}
	return detail
}

// TestP1301All14RoutesRefuseUnknownKey is the §5.5 work list as a table: an
// unknown top-level key on any row is a 400, never silently dropped.
func TestP1301All14RoutesRefuseUnknownKey(t *testing.T) {
	cases := []struct {
		row   string
		route string
		body  string
	}{
		{"1 exec", "exec", `{"command":"id","nope":1}`},
		{"2 capture", "capture", `{"nope":1}`},
		{"3 stream-start", "stream-start", `{"nope":1}`},
		{"4 stream-stop", "stream-stop", `{"stream_id":"s-1","nope":1}`},
		{"6 pointer-move", "pointer-move", `{"x":1,"y":2,"nope":1}`},
		{"7 pointer-click", "pointer-click", `{"x":1,"y":2,"nope":1}`},
		{"8 pointer-scroll", "pointer-scroll", `{"x":1,"y":2,"delta":-1,"nope":1}`},
		{"9 pointer-drag", "pointer-drag", `{"from":{"x":1,"y":1},"to":{"x":2,"y":2},"nope":1}`},
		{"10 key-press", "key-press", `{"key":"a","nope":1}`},
		{"11 key-combo", "key-combo", `{"keys":["a"],"nope":1}`},
		{"12 text-input", "text-input", `{"text":"a","nope":1}`},
		{"13 ui-tree", "ui-tree", `{"visible_only":true,"nope":1}`},
		{"14 ui-window", "ui-window", `{"window":"0x1","action":"close","nope":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.row, func(t *testing.T) {
			srv, cleanup := newV1TestServer(t)
			defer cleanup()
			rec := postP1301(srv, "/api/v1/agents/ag1/"+tc.route, tc.body)
			detail := wantRefusal(t, rec, http.StatusBadRequest, protocol.ErrInvalidParams, protocol.ReasonUnknownField)
			if detail["field"] != "nope" {
				t.Fatalf("detail.field = %v, want \"nope\"", detail["field"])
			}
		})
	}

	// Row 5 is a GET with no body; what it must refuse is a query parameter.
	t.Run("5 stream-frame", func(t *testing.T) {
		srv, cleanup := newV1TestServer(t)
		defer cleanup()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/agents/ag1/stream-frame?context=x11::0", nil)
		srv.mux.ServeHTTP(rec, req)
		wantRefusal(t, rec, http.StatusBadRequest, protocol.ErrInvalidParams, protocol.ReasonUnknownField)
	})
}

// TestP1301DuplicateSelectorRefused is the smuggling primitive: the SAME body
// was accepted before this unit (Go kept the last duplicate, silently), and the
// audit row would name one context while the platform acted in the other.
func TestP1301DuplicateSelectorRefused(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	body := `{"context":"x11::0","context":"wts:7","x":1,"y":2}`
	rec := postP1301(srv, "/api/v1/agents/ag1/pointer-click", body)
	detail := wantRefusal(t, rec, http.StatusBadRequest, protocol.ErrInvalidParams, protocol.ReasonDuplicateField)
	if detail["field"] != "context" {
		t.Fatalf("detail.field = %v, want \"context\"", detail["field"])
	}
}

// TestP1301E1RefusesAgentWithoutSelectorFeature proves the pre-dispatch gate
// and its control in one test: with a selector the call is 409 before dispatch;
// without one the same agent is served — so removing the gate fails this.
func TestP1301E1RefusesAgentWithoutSelectorFeature(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	attachP1301Agent(t, srv, "old1", "1.19.0", []string{"input"},
		&resultConn{srv: srv, payload: map[string]interface{}{"success": true}})

	rec := postP1301(srv, "/api/v1/agents/old1/pointer-click", `{"context":"x11::0","x":1,"y":2}`)
	detail := wantRefusal(t, rec, http.StatusConflict, protocol.ErrUnsupportedAgent, "")
	if detail["feature"] != featureContextSelector {
		t.Fatalf("detail.feature = %v, want %q", detail["feature"], featureContextSelector)
	}
	if detail["agent_version"] != "1.19.0" {
		t.Fatalf("detail.agent_version = %v, want \"1.19.0\"", detail["agent_version"])
	}

	// Control: no selector → today's behaviour, byte-for-byte.
	rec = postP1301(srv, "/api/v1/agents/old1/pointer-click", `{"x":1,"y":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("selector-free call to a 1.19 agent = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestP1301E2RefusesMissingContextRef proves the post-dispatch assertion: a
// 1.21+ agent that advertises the selector and answers a context-scoped call
// without context_ref is a 502, never a success passthrough.
func TestP1301E2RefusesMissingContextRef(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	attachP1301Agent(t, srv, "new1", "1.21.0",
		[]string{featureContextSelector, "input"},
		&resultConn{srv: srv, payload: map[string]interface{}{"success": true}})

	rec := postP1301(srv, "/api/v1/agents/new1/pointer-click", `{"context":"wts:7","x":1,"y":2}`)
	detail := wantRefusal(t, rec, http.StatusBadGateway, protocol.ErrProtocolViolation, "")
	if detail["missing"] != "context_ref" {
		t.Fatalf("detail.missing = %v, want \"context_ref\"", detail["missing"])
	}

	// Control: the same call with the echo is a result, not a refusal.
	attachP1301Agent(t, srv, "new2", "1.21.0",
		[]string{featureContextSelector, "input"},
		&resultConn{srv: srv, payload: map[string]interface{}{
			"success":     true,
			"context_ref": map[string]interface{}{"id": "wts:7", "kind": "wts"},
		}})
	rec = postP1301(srv, "/api/v1/agents/new2/pointer-click", `{"context":"wts:7","x":1,"y":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("result carrying context_ref = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestP1301SelectorValidation covers §5.1 where the selector is parsed.
func TestP1301SelectorValidation(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		reason string
		field  string
	}{
		{"conflict", `{"context":"x11::0","session":7,"x":1,"y":2}`, protocol.ReasonSelectorConflict, "context"},
		{"out-of-range", `{"session":70000,"x":1,"y":2}`, protocol.ReasonOutOfRange, "session"},
		{"negative", `{"session":-1,"x":1,"y":2}`, protocol.ReasonOutOfRange, "session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cleanup := newV1TestServer(t)
			defer cleanup()
			rec := postP1301(srv, "/api/v1/agents/ag1/pointer-click", tc.body)
			detail := wantRefusal(t, rec, http.StatusBadRequest, protocol.ErrInvalidParams, tc.reason)
			if detail["field"] != tc.field {
				t.Fatalf("detail.field = %v, want %q", detail["field"], tc.field)
			}
		})
	}
}

// TestP1301StreamStopRow drives §5.5 row 4 / §5.6: an empty id used to stop
// every stream, and a selector is accepted only to be refused.
func TestP1301StreamStopRow(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	rec := postP1301(srv, "/api/v1/agents/ag1/stream-stop", `{}`)
	detail := wantRefusal(t, rec, http.StatusBadRequest, protocol.ErrInvalidParams, protocol.ReasonStreamIDRequired)
	if detail["field"] != "stream_id" {
		t.Fatalf("detail.field = %v, want \"stream_id\"", detail["field"])
	}

	rec = postP1301(srv, "/api/v1/agents/ag1/stream-stop", `{"stream_id":"s-1","context":"x11::0"}`)
	wantRefusal(t, rec, http.StatusBadRequest, protocol.ErrInvalidParams, protocol.ReasonContextNotAccepted)
}

// TestP1301NoSelectorMeansTodayBehaviour is the invariant the contract keeps:
// an absent selector on an old agent is not a new refusal.
func TestP1301NoSelectorMeansTodayBehaviour(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	attachP1301Agent(t, srv, "old2", "1.19.0", []string{"capture", "input"},
		&resultConn{srv: srv, payload: map[string]interface{}{"success": true}})
	for _, route := range []string{"pointer-click", "key-press", "text-input"} {
		body := map[string]string{
			"pointer-click": `{"x":1,"y":2}`,
			"key-press":     `{"key":"a"}`,
			"text-input":    `{"text":"a"}`,
		}[route]
		rec := postP1301(srv, "/api/v1/agents/old2/"+route, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s without a selector = %d, want 200 (body %s)", route, rec.Code, rec.Body.String())
		}
	}
}

// keep imports honest if the table above is edited
var _ = strings.TrimSpace
