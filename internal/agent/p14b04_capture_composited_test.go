//go:build linux

package agent

// P14B-04 (contract P10-07 v3.1 §10/§11, review R5): a composited window is
// NOT_EXPOSED, never INTERNAL_ERROR.
//
// What this test produces, and what it cannot: a bare Xvfb has no compositor
// process, so the condition is produced the way a compositing manager declares
// itself — the EWMH selection `_NET_WM_CM_S0` is taken by a client, which is the
// server-observable fact that this seat is composited. Xvfb will NOT reproduce
// the compositor's own GetImage error: measured here (probe, P14B-04 §2),
// `CompositeRedirectWindow` is accepted and `GetImage` on the redirected window
// still returns a frame, so the "redirected drawable refuses to be read" half is
// UNPROVEN-ON-HOST and is covered at the mapper level by the answer site the
// pre-fix code used.
//
// The discriminator is two-sided on purpose: a composited seat refuses a
// VIEWABLE window with 501 NOT_EXPOSED, and leaves an unmapped window in its own
// mapping class — otherwise the gate would be a blanket "a compositor exists,
// refuse everything", which would be a wrong refusal with a right number.

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// claimCompositor takes the screen's compositing-manager selection, which is
// what a compositing manager does on startup and the signal this build reads.
func claimCompositor(t *testing.T, x *x11Conn, owner uint32) {
	t.Helper()
	a, err := x.atom(captureCompositorSelection)
	if err != nil {
		t.Fatalf("intern %s: %v", captureCompositorSelection, err)
	}
	req := make([]byte, 16)
	req[0] = 22 // SetSelectionOwner
	binary.LittleEndian.PutUint16(req[2:4], 4)
	binary.LittleEndian.PutUint32(req[4:8], owner)
	binary.LittleEndian.PutUint32(req[8:12], a)
	// time = CurrentTime(0)
	if _, err := x.c.Write(req); err != nil {
		t.Fatalf("SetSelectionOwner: %v", err)
	}
	if err := x.sync(); err != nil {
		t.Fatalf("sync after SetSelectionOwner: %v", err)
	}
	if owned, err := x.compositorOwned(); err != nil || !owned {
		t.Fatalf("%s not held after SetSelectionOwner (owned=%v err=%v)",
			captureCompositorSelection, owned, err)
	}
}

// unmapWindow takes a window out of the viewable state.
func unmapWindow(t *testing.T, x *x11Conn, win uint32) {
	t.Helper()
	req := make([]byte, 8)
	req[0] = 10 // UnmapWindow
	binary.LittleEndian.PutUint16(req[2:4], 2)
	binary.LittleEndian.PutUint32(req[4:8], win)
	if _, err := x.c.Write(req); err != nil {
		t.Fatalf("UnmapWindow: %v", err)
	}
	if err := x.sync(); err != nil {
		t.Fatalf("sync after UnmapWindow: %v", err)
	}
	attrs, err := x.Attributes(win)
	if err != nil || attrs.MapState == x11MapStateViewable {
		t.Fatalf("window 0x%x is still viewable (mapstate=%d err=%v)", win, attrs.MapState, err)
	}
}

func TestP1404B04CompositedWindowIsNotExposed(t *testing.T) {
	num := startBareXvfb(t)
	x, err := x11Dial(num)
	if err != nil {
		t.Fatalf("dial Xvfb :%d: %v", num, err)
	}
	defer x.Close()

	win := x11TestWindow(t, x, 10, 20, 120, 90, 0x3366CC)
	ctx := fmt.Sprintf("x11::%d", num)
	body := fmt.Sprintf(`{"context":%q,"window":"0x%x","format":"png","hash_only":true}`, ctx, win)

	a := &Agent{}
	a.openDisplay = defaultDisplayOpener
	captureBaselines.reset()

	// Control: on this seed there is no compositor, so the same request is an
	// ordinary window_direct capture. Without this leg a blanket refusal would
	// pass the test.
	if owned, err := x.compositorOwned(); err != nil || owned {
		t.Fatalf("seed is already composited (owned=%v err=%v): the control leg proves nothing", owned, err)
	}
	env := runCapture(t, a, body)
	m := captureDataMap(t, env)
	if got := m["source"]; got != protocol.CaptureSourceWindowDirect {
		t.Fatalf("uncomposited control source = %v, want %q", got, protocol.CaptureSourceWindowDirect)
	}

	// The condition: a compositing manager claims the screen. The window is
	// viewable, so this is exactly the case the review measured as a 500.
	claimCompositor(t, x, win)

	env = runCapture(t, a, body)
	if env.Error == nil {
		t.Fatalf("composited window answered a capture (%s); §11 forbids a success for a "+
			"surface this build cannot read", string(env.Result))
	}
	if env.Error.Code != protocol.ErrPrimitiveUnsupported {
		t.Fatalf("code = %q, want %q (501 class)", env.Error.Code, protocol.ErrPrimitiveUnsupported)
	}
	if env.Error.Detail == nil {
		t.Fatal("refusal carries no detail: the operator cannot tell composited from dead")
	}
	if got := env.Error.Detail["reason"]; got != protocol.ReasonNotExposed {
		t.Fatalf("reason = %v, want %q", got, protocol.ReasonNotExposed)
	}
	if got := env.Error.Detail["composited"]; got != true {
		t.Fatalf("detail.composited = %v, want true", got)
	}
	if got := env.Error.Detail["compositor"]; got != captureCompositorSelection {
		t.Fatalf("detail.compositor = %v, want %q", got, captureCompositorSelection)
	}
	if got := env.Error.Detail["required"]; got != "XCompositeNameWindowPixmap" {
		t.Fatalf("detail.required = %v, want the §10 compositor path", got)
	}
	if got := env.Error.Detail["source_available"]; got != protocol.CaptureSourceCompositor {
		t.Fatalf("detail.source_available = %v, want %q", got, protocol.CaptureSourceCompositor)
	}
	if got := env.Error.Detail["ran"]; got != false {
		t.Fatalf("detail.ran = %v, want false: nothing was captured", got)
	}
	if len(env.Result) != 0 {
		t.Fatalf("refusal carries a result body: %s", string(env.Result))
	}
	if msg := env.Error.Message; msg == "" {
		t.Fatal("refusal message is empty")
	}

	// Precision: a composited seat does not swallow the mapping class. An
	// unmapped window is not the compositor's doing, and this refusal must not
	// claim to be about it.
	unmapWindow(t, x, win)
	env = runCapture(t, a, body)
	if env.Error != nil && env.Error.Code == protocol.ErrPrimitiveUnsupported {
		t.Fatalf("unmapped window on a composited seat answered %q/%v: the gate must "+
			"require a viewable window, not merely a compositor", env.Error.Code, env.Error.Detail["reason"])
	}

	// Screen and region reads lift pixels from the screen, so a compositor does
	// not take them away: the refusal is window-scoped.
	env = runCapture(t, a, fmt.Sprintf(`{"context":%q,"format":"png","hash_only":true}`, ctx))
	if env.Error != nil {
		t.Fatalf("full-screen capture refused on a composited seat: %s %s", env.Error.Code, env.Error.Message)
	}
	m = captureDataMap(t, env)
	if got := m["source"]; got != protocol.CaptureSourceScreenRegion {
		t.Fatalf("full-screen source = %v, want %q", got, protocol.CaptureSourceScreenRegion)
	}
}
