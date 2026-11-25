package server

// P14-01 acceptance, server side (§9.1): the agent's three §9.1 answers reach
// the caller with the status the contract names — 404 for a window that is not
// in the context's tree, 403 for a refused focus target, and HTTP 200 with
// ok:false for an OCCLUDED observed failure (not an error, not a 5xx).

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

func TestP1401RefusalStatusMapping(t *testing.T) {
	cases := []struct {
		name    string
		route   string
		body    string
		payload map[string]interface{}
		status  int
		code    string
		reason  string
	}{
		{
			name:  "window not in tree is 404",
			route: "/pointer-click",
			body:  `{"x":1,"y":2,"window":"0xdeadbee"}`,
			payload: map[string]interface{}{
				"error": "the window is not in the resolved context's tree",
				"code":  protocol.ErrWindowNotFound,
				"detail": map[string]interface{}{
					"resolved_context": "x11::0", "window": "0xdeadbee", "ran": false,
				},
			},
			status: http.StatusNotFound,
			code:   protocol.ErrWindowNotFound,
		},
		{
			name:  "unfocused typing is 403 NO_FOCUS_TARGET",
			route: "/text-input",
			body:  `{"text":"x","window":"0x3c00007"}`,
			payload: map[string]interface{}{
				"error": "window 0x3c00007 does not own the focus",
				"code":  protocol.ErrPrimitiveRefused,
				"detail": map[string]interface{}{
					"reason": protocol.ReasonNoFocusTarget, "intended": "0x3c00007", "ran": false,
				},
			},
			status: http.StatusForbidden,
			code:   protocol.ErrPrimitiveRefused,
			reason: protocol.ReasonNoFocusTarget,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, cleanup := newV1TestServer(t)
			defer cleanup()
			attachP1301Agent(t, srv, "ag1", "1.21.0", []string{"input"},
				&resultConn{srv: srv, payload: tc.payload})
			rec := postP1301(srv, "/api/v1/agents/ag1"+tc.route, tc.body)
			wantRefusal(t, rec, tc.status, tc.code, tc.reason)
		})
	}
}

// TestP1401OccludedIsA200Result is the shape that makes the rule usable: the
// request ran, the input did not, so it is a RESULT — HTTP 200, ok:false — and
// the covering window reaches the caller intact.
func TestP1401OccludedIsA200Result(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	occluded := map[string]interface{}{
		"ok": false,
		"context_ref": map[string]interface{}{
			"requested": "x11::0", "resolved": "x11::0", "kind": "x11", "source": "selector",
		},
		"data": map[string]interface{}{
			"reason": protocol.ReasonOccluded,
			"hit":    map[string]interface{}{"id": "0x3c00011", "title": "SYSTEM LAYOUT", "owner_pid": 3180},
			"detail": map[string]interface{}{"intended": "0x3c00007", "point": map[string]interface{}{"x": 42, "y": 96}},
			"ran":    false,
		},
	}
	attachP1301Agent(t, srv, "ag1", "1.21.0", []string{"input"}, &resultConn{srv: srv, payload: occluded})

	rec := postP1301(srv, "/api/v1/agents/ag1/pointer-click", `{"x":42,"y":96,"window":"0x3c00007"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		OK   bool `json:"ok"`
		Data struct {
			Reason string `json:"reason"`
			Ran    bool   `json:"ran"`
			Hit    struct {
				ID string `json:"id"`
			} `json:"hit"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %s: %v", rec.Body.String(), err)
	}
	if body.OK || body.Data.Reason != protocol.ReasonOccluded || body.Data.Ran {
		t.Fatalf("body = %+v, want ok:false reason OCCLUDED ran:false", body)
	}
	if body.Data.Hit.ID != "0x3c00011" {
		t.Fatalf("hit.id = %q, want the covering window", body.Data.Hit.ID)
	}
}
