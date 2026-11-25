package server

// P13-02 acceptance, server side: the §6 envelope and its row fields, the
// GET-only strict layer (a query parameter is UNKNOWN_FIELD), the `input` RBAC
// scope refusing a viewer, and a selector refusal arriving as 409 with the
// reason the row reported.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// contextsPayload is a two-row enumeration as an agent would answer it: an X11
// seat whose activity was observed, and the uid context that cannot know.
func contextsPayload() map[string]interface{} {
	features := func(tree bool, ptr []interface{}, region bool) map[string]interface{} {
		return map[string]interface{}{
			"window.tree":     tree,
			"window.control":  []interface{}{},
			"input.pointer":   ptr,
			"input.text":      ptr,
			"capture.window":  false,
			"capture.region":  region,
		}
	}
	return map[string]interface{}{
		"agent_context": map[string]interface{}{"id": "x11::0", "kind": "x11"},
		"contexts": []interface{}{
			map[string]interface{}{
				"id": "x11::0", "kind": "x11", "name": ":0", "user": "flk", "uid": 1000,
				"active": true, "active_known": true, "agent_own": true,
				"addressable": true, "addressable_reason": "agent_own",
				"idle_s": -1, "logon_time": "",
				"surface":  map[string]interface{}{"pixels": "1920x1080", "displays": 1, "dpi_scale": 1.0},
				"features": features(false, []interface{}{"click"}, true),
				"refusals": []interface{}{
					map[string]interface{}{
						"feature": "window.tree", "code": "PRIMITIVE_UNSUPPORTED",
						"reason": "NO_WINDOW_MANAGER",
					},
				},
			},
			map[string]interface{}{
				"id": "uid:1000", "kind": "uid", "name": "flk", "user": "flk", "uid": 1000,
				"active": nil, "active_known": false, "agent_own": true,
				"addressable": false, "addressable_reason": "kind_has_no_surface",
				"idle_s": -1, "logon_time": "",
				"surface":  map[string]interface{}{"pixels": "", "displays": 0, "dpi_scale": 0},
				"features": features(false, []interface{}{}, false),
				"refusals": []interface{}{},
			},
		},
	}
}

func getContexts(srv *Server, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", path, nil)
	srv.mux.ServeHTTP(rec, req)
	return rec
}

// TestP1302ContextsEnvelopeShape is acceptance item 1: exactly the §6 envelope
// and exactly the row fields, with `active` nullable per row.
func TestP1302ContextsEnvelopeShape(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	attachP1301Agent(t, srv, "a1", "1.21.0",
		[]string{"exec", "filesystem", "capture", "input", "ui", "context"},
		&resultConn{srv: srv, payload: contextsPayload()})

	rec := getContexts(srv, "/api/v1/agents/a1/contexts")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var envelope struct {
		OK   bool                   `json:"ok"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal %s: %v", rec.Body.String(), err)
	}
	if !envelope.OK {
		t.Fatalf("ok = false: %s", rec.Body.String())
	}
	keys := make([]string, 0, len(envelope.Data))
	for k := range envelope.Data {
		keys = append(keys, k)
	}
	if len(keys) != 3 {
		t.Fatalf("data keys = %v, want exactly agent_context, generated_at_utc, contexts", keys)
	}
	for _, want := range []string{"agent_context", "generated_at_utc", "contexts"} {
		if _, ok := envelope.Data[want]; !ok {
			t.Fatalf("data.%s missing: %v", want, keys)
		}
	}
	gen, _ := envelope.Data["generated_at_utc"].(string)
	if _, err := time.Parse(time.RFC3339, gen); err != nil {
		t.Errorf("generated_at_utc = %q is not RFC3339: %v", gen, err)
	}
	if !strings.HasSuffix(gen, "Z") {
		t.Errorf("generated_at_utc = %q, want UTC (Z suffix)", gen)
	}

	rows, _ := envelope.Data["contexts"].([]interface{})
	if len(rows) != 2 {
		t.Fatalf("contexts = %d rows, want 2", len(rows))
	}
	required := []string{
		"id", "kind", "name", "user", "uid", "active", "active_known",
		"agent_own", "addressable", "addressable_reason", "surface", "features",
	}
	for i, rawRow := range rows {
		row, ok := rawRow.(map[string]interface{})
		if !ok {
			t.Fatalf("row %d is not an object", i)
		}
		for _, f := range required {
			if _, present := row[f]; !present {
				t.Errorf("row %d missing field %q: %v", i, f, row)
			}
		}
		surface, _ := row["surface"].(map[string]interface{})
		for _, f := range []string{"pixels", "displays", "dpi_scale"} {
			if _, present := surface[f]; !present {
				t.Errorf("row %d surface missing %q", i, f)
			}
		}
		features, _ := row["features"].(map[string]interface{})
		for _, f := range []string{
			"window.tree", "window.control", "input.pointer",
			"input.text", "capture.window", "capture.region",
		} {
			if _, present := features[f]; !present {
				t.Errorf("row %d features missing %q", i, f)
			}
		}
	}

	// The uid row must carry explicit null and active_known:false.
	uid := rows[1].(map[string]interface{})
	if v, present := uid["active"]; !present || v != nil {
		t.Errorf(`uid row "active" = %v (present=%v), want null`, v, present)
	}
	if uid["active_known"] != false {
		t.Errorf("uid active_known = %v, want false", uid["active_known"])
	}
	if !strings.Contains(rec.Body.String(), `"active":null`) {
		t.Errorf("body does not contain an explicit active:null: %s", rec.Body.String())
	}
}

// TestP1302ContextsQueryParameterRefused is acceptance item 6: the route is
// wired into the strict layer, so a query parameter — where an operator would
// put a selector — is refused rather than dropped.
func TestP1302ContextsQueryParameterRefused(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	attachP1301Agent(t, srv, "a1", "1.21.0",
		[]string{"input", "context"},
		&resultConn{srv: srv, payload: contextsPayload()})

	rec := getContexts(srv, "/api/v1/agents/a1/contexts?context=x11::0")
	detail := wantRefusal(t, rec, http.StatusBadRequest, protocol.ErrInvalidParams, protocol.ReasonUnknownField)
	if detail["field"] != "context" {
		t.Errorf("detail.field = %v, want context", detail["field"])
	}
	allowed, ok := detail["allowed"].([]interface{})
	if !ok || len(allowed) != 0 {
		t.Errorf("detail.allowed = %v, want an empty list", detail["allowed"])
	}

	// GET only: the same path with a write verb is not this route.
	postRec := postP1301(srv, "/api/v1/agents/a1/contexts", `{}`)
	if postRec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /contexts = %d, want 405", postRec.Code)
	}
}

// TestP1302ContextsViewerForbidden is item 6's RBAC half: a logged-on-user list
// is operator-activity data, so a viewer token gets 403.
func TestP1302ContextsViewerForbidden(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	attachP1301Agent(t, srv, "a1", "1.21.0",
		[]string{"input", "context"},
		&resultConn{srv: srv, payload: contextsPayload()})
	if _, err := srv.operators.Create("viewer", RoleViewer, "viewer-tok"); err != nil {
		t.Fatalf("create viewer: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/agents/a1/contexts", nil)
	req.Header.Set("Authorization", "Bearer viewer-tok")
	srv.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	resp := decodeAPIError(t, rec)
	if resp.OK {
		t.Fatalf("ok = true for a viewer: %s", rec.Body.String())
	}
	if resp.Error == nil || resp.Error.Code != "FORBIDDEN" {
		t.Fatalf("code = %+v, want FORBIDDEN", resp.Error)
	}
}

// TestP1302UnaddressableSelectorIs409WithRowReason is item 3 end to end: the
// refusal a caller gets from a selector route carries the same reason the
// context's row reported, and the §5.4 code derived from it.
func TestP1302UnaddressableSelectorIs409WithRowReason(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	attachP1301Agent(t, srv, "a1", "1.21.0",
		[]string{"capture", "input", "context", "context.selector"},
		&resultConn{srv: srv, payload: map[string]interface{}{
			"error": "context \"uid:1000\" cannot be addressed (KIND_NOT_APPLICABLE)",
			"code":  protocol.ErrContextUnavailable,
			"detail": map[string]interface{}{
				"reason":             "KIND_NOT_APPLICABLE",
				"addressable_reason": protocol.AddrKindHasNoSurface,
				"field":              "context",
				"ran":                false,
			},
		}})

	rec := postP1301(srv, "/api/v1/agents/a1/ui-tree", `{"context":"uid:1000"}`)
	detail := wantRefusal(t, rec, http.StatusConflict, protocol.ErrContextUnavailable, "KIND_NOT_APPLICABLE")
	if detail["addressable_reason"] != protocol.AddrKindHasNoSurface {
		t.Errorf("detail.addressable_reason = %v, want %q",
			detail["addressable_reason"], protocol.AddrKindHasNoSurface)
	}
}
