package server

// P14B-02 / §9.4 R4, server side: the caller's text reaches the agent as the
// caller wrote it. The HTTP handler used to forward the DECODED params, and
// encoding/json has already turned a lone `\ud800` into `\ufffd` by then — so
// the agent could not tell a repair from an honest replacement character. The
// assertion is on the bytes written to the agent, which is the only place the
// difference still exists.

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// p14b02Conn records the params the server writes and answers with the §9.4
// success shape, so the handler runs to completion.
type p14b02Conn struct {
	srv  *Server
	mu   sync.Mutex
	seen []byte
}

func (c *p14b02Conn) WriteJSON(v interface{}) error {
	env, ok := v.(protocol.Envelope)
	if !ok {
		return nil
	}
	c.mu.Lock()
	c.seen = append([]byte(nil), env.Params...)
	c.mu.Unlock()
	c.srv.pendingMu.Lock()
	ch := c.srv.pendingReqs[env.ID]
	c.srv.pendingMu.Unlock()
	if ch != nil {
		raw, _ := json.Marshal(map[string]interface{}{
			"ok": true,
			"context_ref": map[string]interface{}{
				"requested": "x11::0", "resolved": "x11::0", "kind": "x11", "source": "selector",
			},
			"data": map[string]interface{}{
				"success": true,
				"detail":  map[string]interface{}{"transcoded": false, "codepoints": 3},
			},
		})
		ch <- protocol.Envelope{ID: env.ID, Type: protocol.TypeTextInputResult, Result: raw}
	}
	return nil
}

func (c *p14b02Conn) WriteMessage(int, []byte) error { return nil }
func (c *p14b02Conn) Close() error                   { return nil }

func TestP14B02ServerForwardsCallerTextVerbatim(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"lone surrogate", `{"window":"0x3c00007","text":"a\ud800b"}`, `\ud800`},
		{"honest replacement", `{"window":"0x3c00007","text":"a\ufffdb"}`, `\ufffd`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cleanup := newV1TestServer(t)
			defer cleanup()
			conn := &p14b02Conn{srv: srv}
			attachP1301Agent(t, srv, "ag1", "1.21.0", []string{"input"}, conn)
			rec := postP1301(srv, "/api/v1/agents/ag1/text-input", tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(string(conn.seen), tc.want) {
				t.Fatalf("agent received %s, want the caller's %s escape: the server re-encoded the text", conn.seen, tc.want)
			}
		})
	}
}
