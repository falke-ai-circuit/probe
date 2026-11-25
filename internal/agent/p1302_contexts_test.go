package agent

// P13-02 acceptance, agent side: per-context features, the no-window-manager
// refusal, the nullable `active`, the absence of a fabricated `wts` row, and
// the selector gate refusing with the SAME reason the row reported.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// testProbe is a stated host: one X11 display owned by our uid, one wayland
// seat when asked for, and no i/o beyond what the test passes in.
func testProbe(wm wmState, active *bool) hostProbe {
	return hostProbe{
		EUID:     1000,
		Username: "flk",
		AgentPID: 4242,
		Display:  ":0",
		XSockets: []x11SocketInfo{{Num: 0, OwnerUID: 1000, OwnerKnown: true}},
		XInfo: func(int) (wmState, *bool, string) {
			return wm, active, "1920x1080"
		},
		Tools: map[string]bool{"xdotool": true, "import": true, "xprop": true, "xdpyinfo": true},
		LookupUser: func(uid int) string {
			if uid == 1000 {
				return "flk"
			}
			return ""
		},
	}
}

func mustRow(t *testing.T, res protocol.ContextsResult, id string) protocol.ContextRow {
	t.Helper()
	for _, r := range res.Contexts {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %q in %+v", id, res.Contexts)
	return protocol.ContextRow{}
}

// TestP1302NoWindowManagerIsAdvertised is acceptance item 5: the caller learns
// at enumeration that window.tree is unavailable HERE, with the §13 entry that
// names the cause, instead of from a 501 mid-flow.
func TestP1302NoWindowManagerIsAdvertised(t *testing.T) {
	res := buildContexts(testProbe(wmAbsent, nil))
	row := mustRow(t, res, "x11::0")
	if row.Features.WindowTree {
		t.Fatalf("window.tree = true on a display with no window manager")
	}
	want := protocol.ContextRefusal{
		Feature: "window.tree",
		Code:    protocol.ErrPrimitiveUnsupported,
		Reason:  "NO_WINDOW_MANAGER",
	}
	found := false
	for _, r := range row.Refusals {
		if r == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("refusals = %+v, want an entry %+v", row.Refusals, want)
	}
}

// TestP1302ActiveIsNullWhenUnknown is acceptance item 2: the uid context cannot
// know the foreground activity, so it reports null + active_known:false. The
// x11 row in the same answer reports the boolean the platform actually gave —
// the two paths are distinguishable, not the same path relabelled.
func TestP1302ActiveIsNullWhenUnknown(t *testing.T) {
	known := true
	res := buildContexts(testProbe(wmPresent, &known))

	uidRow := mustRow(t, res, "uid:1000")
	if uidRow.Active != nil {
		t.Fatalf("uid context active = %v, want nil (unknown)", *uidRow.Active)
	}
	if uidRow.ActiveKnown {
		t.Fatal("uid context active_known = true; it cannot know")
	}
	if uidRow.IdleS != -1 || uidRow.LogonTime != "" {
		t.Fatalf("uid idle_s/logon_time = %d/%q, want -1/\"\"", uidRow.IdleS, uidRow.LogonTime)
	}

	x11Row := mustRow(t, res, "x11::0")
	if x11Row.Active == nil || !x11Row.ActiveKnown || !*x11Row.Active {
		t.Fatalf("x11 active = %v known=%v, want true/true from the answered probe",
			x11Row.Active, x11Row.ActiveKnown)
	}

	// And the JSON really carries null, not an omitted field a caller would
	// read as false.
	raw, err := json.Marshal(uidRow)
	if err != nil {
		t.Fatalf("marshal uid row: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal uid row: %v", err)
	}
	if v, present := decoded["active"]; !present || v != nil {
		t.Fatalf(`uid row "active" = %v (present=%v), want explicit null`, v, present)
	}
}

// TestP1302FeaturesArePerContext is item 5's other half: the same agent answers
// with different matrices for an X11 seat and its own uid context.
func TestP1302FeaturesArePerContext(t *testing.T) {
	res := buildContexts(testProbe(wmPresent, nil))
	x11 := mustRow(t, res, "x11::0")
	uid := mustRow(t, res, "uid:1000")

	if len(x11.Features.InputPointer) == 0 || len(x11.Features.InputText) == 0 {
		t.Errorf("x11 features lost the xdotool-backed input: %+v", x11.Features)
	}
	if !x11.Features.CaptureRegion {
		t.Errorf("x11 capture.region = false with import present")
	}
	if uid.Features.WindowTree || len(uid.Features.InputPointer) != 0 ||
		len(uid.Features.InputText) != 0 || uid.Features.CaptureRegion || uid.Features.CaptureWindow {
		t.Errorf("uid context claims a surface it does not have: %+v", uid.Features)
	}
	if !uid.Addressable == false || uid.AddressableReason != protocol.AddrKindHasNoSurface {
		t.Errorf("uid addressable=%v reason=%q, want false/kind_has_no_surface",
			uid.Addressable, uid.AddressableReason)
	}
	if !x11.Addressable || x11.AddressableReason != protocol.AddrAgentOwn {
		t.Errorf("agent's own x11 row addressable=%v reason=%q, want true/agent_own",
			x11.Addressable, x11.AddressableReason)
	}
}

// TestP1302NoWindowsRowOnLinux pins the brief's rule: `wts` is `[S]` and must
// be absent, not an empty row.
func TestP1302NoWindowsRowOnLinux(t *testing.T) {
	probe := testProbe(wmPresent, nil)
	probe.WaylandDisplay = "wayland-0"
	res := buildContexts(probe)
	if len(res.Contexts) < 3 {
		t.Fatalf("expected x11 + wayland + uid rows, got %d", len(res.Contexts))
	}
	allowed := map[string]bool{
		protocol.ContextKindX11:     true,
		protocol.ContextKindWayland: true,
		protocol.ContextKindUID:     true,
	}
	for _, r := range res.Contexts {
		if r.Kind == "wts" {
			t.Fatalf("fabricated wts row on linux: %+v", r)
		}
		if !allowed[r.Kind] {
			t.Errorf("unexpected kind %q", r.Kind)
		}
	}
	if res.AgentContext.Kind != protocol.ContextKindX11 {
		t.Errorf("agent_context = %+v, want the x11 seat", res.AgentContext)
	}
}

// TestP1302SelectorGateCarriesTheRowReason is acceptance item 3: discovery and
// enforcement are one predicate. The row says addressable:false with a reason;
// a selector for that same context is refused with that same reason, and the
// §5.4 code derived from it.
func TestP1302SelectorGateCarriesTheRowReason(t *testing.T) {
	res := buildContexts(testProbe(wmAbsent, nil))
	contextsCache.mu.Lock()
	contextsCache.at = time.Now()
	contextsCache.res = res
	contextsCache.err = nil
	contextsCache.mu.Unlock()
	t.Cleanup(func() {
		contextsCache.mu.Lock()
		contextsCache.at = time.Time{}
		contextsCache.mu.Unlock()
	})

	row := mustRow(t, res, "uid:1000")
	if row.Addressable {
		t.Fatalf("fixture row is addressable: %+v", row)
	}

	a := &Agent{}
	refusal := a.selectorGate(protocol.Envelope{
		ID:     "t-1",
		Type:   protocol.TypeUITree,
		Params: json.RawMessage(`{"context":"uid:1000","visible_only":true}`),
	})
	if refusal == nil {
		t.Fatal("selector for an unaddressable context was not refused")
	}
	if refusal.Error == nil || refusal.Error.Code != protocol.ErrContextUnavailable {
		t.Fatalf("refusal code = %+v, want %s", refusal.Error, protocol.ErrContextUnavailable)
	}
	if got := refusal.Error.Detail["addressable_reason"]; got != row.AddressableReason {
		t.Errorf("refusal addressable_reason = %v, want the row's %q", got, row.AddressableReason)
	}
	if got := refusal.Error.Detail["reason"]; got != protocol.ContextUnavailableReason(row.AddressableReason) {
		t.Errorf("refusal reason = %v, want %q", got,
			protocol.ContextUnavailableReason(row.AddressableReason))
	}
	if refusal.Error.Detail["ran"] != false {
		t.Errorf("detail.ran = %v, want false", refusal.Error.Detail["ran"])
	}

	// An addressable selector passes the gate: the gate is not a blanket deny.
	if ok := a.selectorGate(protocol.Envelope{
		ID:     "t-2",
		Type:   protocol.TypeUITree,
		Params: json.RawMessage(`{"context":"x11::0"}`),
	}); ok != nil {
		t.Fatalf("addressable selector refused: %+v", ok.Error)
	}

	// A wts selector has no context on this host: inapplicable, not missing.
	session := a.selectorGate(protocol.Envelope{
		ID:     "t-3",
		Type:   protocol.TypeUITree,
		Params: json.RawMessage(`{"session":2}`),
	})
	if session == nil || session.Error == nil || session.Error.Detail["reason"] != "KIND_NOT_APPLICABLE" {
		t.Fatalf("wts selector = %+v, want KIND_NOT_APPLICABLE", session)
	}
}

// TestP1302HandlerRefusesWithoutBackend keeps the non-enumerable platform
// honest: a 501 with NO_BACKEND, never an empty contexts list.
func TestP1302EnumerateDoesNotInventRows(t *testing.T) {
	// An empty probe still yields the agent's own uid context and nothing else:
	// no sockets, no wayland, no wts.
	res := buildContexts(hostProbe{
		EUID: 1000, Username: "flk", AgentPID: 1,
		XInfo: func(int) (wmState, *bool, string) { return wmUnknown, nil, "" },
		Tools: map[string]bool{},
		LookupUser: func(int) string { return "" },
	})
	if len(res.Contexts) != 1 {
		t.Fatalf("empty host produced %d rows: %+v", len(res.Contexts), res.Contexts)
	}
	if res.Contexts[0].ID != "uid:1000" || res.AgentContext.ID != "uid:1000" {
		t.Fatalf("rows = %+v, want only the uid context", res.Contexts)
	}
}
