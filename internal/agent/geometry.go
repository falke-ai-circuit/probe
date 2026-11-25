package agent

// P14-01 (contract P10-07 v3.1 §9.1): window-relative geometry and the
// occlusion hit-test.
//
// One path, on purpose. `window` present means x,y are relative to the window's
// rect ORIGIN AS MEASURED AT DISPATCH TIME — the rect is re-read here, not
// carried from the ui-tree answer the caller read a minute ago. A window that
// moved between the two calls must not misplace the click, and the only way to
// guarantee that is to measure the rect in the same breath as the injection.
//
// The hit-test is always on for a request that named a window: the absolute
// point is computed, the platform is asked what is on top there, and if that is
// not the requested window the input is NOT performed. `ran:false` is the
// whole point of the rule — a refusal that still fires the click is worthless.
//
// A request that names no window asserts no intended target (bullet 1: `window`
// absent keeps today's absolute behaviour), so there is no predicate to
// evaluate and no hit-test is run. That is the only path without it, and it is
// a consequence of there being nothing to compare — not a knob.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

const (
	// maxWindowDescent bounds the hit-test walk. Deep menus are not the target
	// of this rule and an unbounded walk against a hostile server is.
	maxWindowDescent = 32
	// foregroundPoll is §8's verify window for _NET_ACTIVE_WINDOW.
	foregroundPoll = 300 * time.Millisecond
)

// windowProbe is the display seam the geometry path needs, and it EXTENDS
// treeDisplay rather than duplicating it: the rect, the client list and the WM
// check the tree publishes are the same observations this rule reads. A second
// geometry path is the defect this contract exists to prevent, so a backend
// that can answer ui-tree can answer this.
type windowProbe interface {
	treeDisplay
	// PointIn translates a root-space point into win's coordinate space and
	// names the child of win that contains it (0 when none does). This is
	// XTranslateCoordinates, the primitive §9.1 names: it answers for an
	// arbitrary point, which XQueryPointer alone does not (it answers only for
	// where the pointer happens to be, and warping the operator's pointer to
	// ask a question is a side effect a hit-test may not have).
	PointIn(win uint32, x, y int) (int32, int32, uint32, error)
	// RequestForeground asks the WM to activate win (§8: _NET_ACTIVE_WINDOW
	// client message, source=2). It reports that the request was SENT; the
	// caller re-reads _NET_ACTIVE_WINDOW to learn whether it was honoured.
	RequestForeground(win uint32) error
}

// inputTarget is a resolved `window` selector plus the display that owns it.
// The caller closes Probe.
type inputTarget struct {
	Ref    protocol.UITreeContextRef
	Window uint32
	Rect   protocol.UITreeRect
	Probe  windowProbe
}

// inputPoint is the §9.1 answer for a pointer primitive: the absolute context
// point to inject at.
type inputPoint struct {
	inputTarget
	X, Y int
}

// resolveInputTarget opens the context's display and, when a window was named,
// proves the window is in that context BEFORE anything is injected (§9.1's
// 404-before-injection rule). No window named means no display is needed: the
// absolute path is today's behaviour, unchanged.
func (a *Agent) resolveInputTarget(env protocol.Envelope, primitive string, contextID *string, window string) (*inputTarget, *protocol.Envelope) {
	if strings.TrimSpace(window) == "" {
		return &inputTarget{}, nil
	}
	probe, ref, refusal := a.openContextDisplay(env, primitive, contextID, window)
	if refusal != nil {
		return nil, refusal
	}
	id, ok := parseWindowID(window)
	if !ok {
		probe.Close()
		return nil, windowNotFoundRefusal(env, ref, window, "the id is not a hex X window id (expected \"0x…\")")
	}
	// §7.1 first: with no EWMH WM there is no client list, so "not in the tree"
	// would be a lie — the tree does not exist on this display.
	if _, wmRef := wmCheck(probe); wmRef != nil {
		probe.Close()
		return nil, noWindowManagerRefusal(env, ref, wmRef, window)
	}
	ids, listRef := clientList(probe)
	if listRef != nil {
		probe.Close()
		return nil, noWindowManagerRefusal(env, ref, listRef, window)
	}
	if !containsWindow(ids, id) {
		probe.Close()
		return nil, windowNotFoundRefusal(env, ref, window, "the window is not in the resolved context's tree")
	}
	rect, err := windowRectNow(probe, id)
	if err != nil {
		probe.Close()
		return nil, windowNotFoundRefusal(env, ref, window, fmt.Sprintf("the window vanished or could not be measured: %v", err))
	}
	return &inputTarget{Ref: ref, Window: id, Rect: rect, Probe: probe}, nil
}

// resolveInputPoint is resolveInputTarget plus the geometry and the occlusion
// rule. It is the ONE place a pointer primitive turns window-relative x,y into
// an absolute point.
func (a *Agent) resolveInputPoint(env protocol.Envelope, primitive string, contextID *string, window string, x, y int) (*inputPoint, *protocol.Envelope) {
	if strings.TrimSpace(window) == "" {
		return &inputPoint{X: x, Y: y}, nil
	}
	t, refusal := a.resolveInputTarget(env, primitive, contextID, window)
	if refusal != nil {
		return nil, refusal
	}
	p := &inputPoint{inputTarget: *t, X: t.Rect.X + x, Y: t.Rect.Y + y}
	hit, _, err := occludingWindow(t.Probe, t.Window, p.X, p.Y)
	if err != nil {
		t.Probe.Close()
		envl := protocol.NewErrorDetail(env.ID, protocol.ErrInternal,
			fmt.Sprintf("%s: could not ask the platform what is on top at %d,%d: %v", primitive, p.X, p.Y, err),
			map[string]interface{}{"reason": protocol.ReasonNoBackend, "ran": false})
		return nil, &envl
	}
	if hit != 0 {
		envl := occludedFailure(env, primitive, t.Probe, t.Ref, t.Window, p.X, p.Y, hit)
		t.Probe.Close()
		return nil, &envl
	}
	return p, nil
}

// occludingWindow answers "which window is on top at root point (x,y)", or 0
// when it is the requested window. It walks the way the X server does: from the
// root, translateInto the point, descend into the child that contains it, and
// stop at the first window with no containing child. A child that is not
// viewable does not receive input, so the walk stops rather than descending
// into an unmapped window's geometry.
func occludingWindow(d windowProbe, requested uint32, px, py int) (uint32, []uint32, error) {
	path := []uint32{d.Root()}
	cur := d.Root()
	for i := 0; i < maxWindowDescent; i++ {
		_, _, child, err := d.PointIn(cur, px, py)
		if err != nil {
			return 0, path, err
		}
		if child == 0 {
			break
		}
		attrs, err := d.Attributes(child)
		if err != nil || attrs.MapState != x11MapStateViewable {
			break
		}
		path = append(path, child)
		cur = child
	}
	for _, w := range path {
		if w == requested {
			return 0, path, nil // the requested window is on the descent: not occluded
		}
	}
	if len(path) == 1 {
		// Nothing is over the root at this point: the desktop is what would
		// receive the click, and the desktop is not the requested window.
		return d.Root(), path, nil
	}
	// A reparenting WM puts its frame between the root and the client, so the
	// top-level window at the point is often the frame. The client is the first
	// window on the descent that the WM manages (WM_STATE), which is the window
	// the caller can address.
	for _, w := range path[1:] {
		if _, err := d.Property(w, "WM_STATE"); err == nil {
			return w, path, nil
		}
	}
	return path[1], path, nil
}

// focusGate is §9.1's typing rule: a named window must own the focus before a
// keystroke or a character is injected. It foregrounds ONLY when the caller sent
// `focus:true` — stealing focus during typing is a silent context switch — and
// it re-reads _NET_ACTIVE_WINDOW afterwards instead of assuming the WM obeyed.
func focusGate(env protocol.Envelope, primitive string, t *inputTarget, focus bool) *protocol.Envelope {
	if t == nil || t.Window == 0 {
		return nil
	}
	active := activeWindow(t.Probe)
	if active == t.Window {
		return nil
	}
	detail := map[string]interface{}{
		"intended": fmt.Sprintf("0x%x", t.Window),
		"active":   fmt.Sprintf("0x%x", active),
		"ran":      false,
	}
	if !focus {
		envl := protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveRefused,
			fmt.Sprintf("%s: window 0x%x does not own the focus; send focus:true to foreground it, or type into the focused window",
				primitive, t.Window),
			mergeDetail(detail, map[string]interface{}{"reason": protocol.ReasonNoFocusTarget}))
		return &envl
	}
	if err := t.Probe.RequestForeground(t.Window); err != nil {
		envl := foregroundDenied(env, primitive, t.Ref, t.Window, detail, fmt.Sprintf("the WM request could not be sent: %v", err))
		return &envl
	}
	deadline := time.Now().Add(foregroundPoll)
	for time.Now().Before(deadline) {
		if activeWindow(t.Probe) == t.Window {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	envl := foregroundDenied(env, primitive, t.Ref, t.Window, detail, "the WM did not make the window active within 300 ms")
	return &envl
}

// activeWindow reads _NET_ACTIVE_WINDOW, 0 when no window owns the focus.
func activeWindow(d treeDisplay) uint32 {
	p, err := d.Property(d.Root(), "_NET_ACTIVE_WINDOW")
	if err != nil {
		return 0
	}
	if v := propUint32s(p); len(v) > 0 {
		return v[0]
	}
	return 0
}

// windowRectNow measures the rect at dispatch time. It is the SAME rect ui-tree
// publishes (outerRect), because a resolver with its own idea of the window's
// origin would place clicks differently from the tree the caller read.
func windowRectNow(d treeDisplay, id uint32) (protocol.UITreeRect, error) {
	g, err := d.Geometry(id)
	if err != nil {
		return protocol.UITreeRect{}, err
	}
	x, y, err := d.TranslateToRoot(id)
	if err != nil {
		return protocol.UITreeRect{}, err
	}
	return outerRect(d, id, g, x, y), nil
}

// outerRect is the ONE rect computation: a reparenting WM stores the frame
// around the client, so the client's own geometry under-reports the window the
// operator sees. §7's row and §9.1's resolution both call this.
func outerRect(d treeDisplay, id uint32, g x11Geometry, x, y int) protocol.UITreeRect {
	rect := protocol.UITreeRect{X: x, Y: y, W: g.W, H: g.H}
	if p, err := d.Property(id, "_NET_FRAME_EXTENTS"); err == nil {
		if v := propUint32s(p); len(v) >= 4 {
			left, right, top, bottom := int(v[0]), int(v[1]), int(v[2]), int(v[3])
			rect.X -= left
			rect.Y -= top
			rect.W += left + right
			rect.H += top + bottom
		}
	}
	return rect
}

// parseWindowID accepts the id shape ui-tree emits ("0x3c00007") and a bare
// decimal, and nothing else: a malformed id is a 404, never a guess at what the
// caller meant.
func parseWindowID(s string) (uint32, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 0, 32)
	if err != nil || v == 0 {
		return 0, false
	}
	return uint32(v), true
}

func containsWindow(ids []uint32, id uint32) bool {
	for _, w := range ids {
		if w == id {
			return true
		}
	}
	return false
}

func mergeDetail(base, add map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(base)+len(add))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range add {
		out[k] = v
	}
	return out
}

// --- refusals ---------------------------------------------------------------

// windowNotFoundRefusal is §9.1's 404: the window is not in the resolved
// context's tree, so nothing was injected.
func windowNotFoundRefusal(env protocol.Envelope, ref protocol.UITreeContextRef, window, why string) *protocol.Envelope {
	envl := protocol.NewErrorDetail(env.ID, protocol.ErrWindowNotFound,
		fmt.Sprintf("%s: %s", window, why),
		map[string]interface{}{
			"resolved_context": ref.Resolved,
			"window":           window,
			"ran":              false,
		})
	return &envl
}

// noWindowManagerRefusal surfaces §7.1 on the input path: a window-scoped input
// cannot be placed or hit-tested on a display with no client list, and an
// invented "not found" would blame the caller for the environment.
func noWindowManagerRefusal(env protocol.Envelope, ref protocol.UITreeContextRef, r *treeRefusal, window string) *protocol.Envelope {
	envl := protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported,
		fmt.Sprintf("display %s: %s", ref.Resolved, r.Message),
		map[string]interface{}{
			"reason":      r.Reason,
			"context":     protocol.ContextRef{ID: ref.Resolved, Kind: protocol.ContextKindX11},
			"window":      window,
			"ran":         false,
			"remediation": noWMRemediation,
		})
	return &envl
}

// occludedFailure is the §8.1 shape: HTTP 200, ok:false, the covering window
// named, the intended window and the measured point echoed. `ran:false` is
// literal — the caller below never injects.
func occludedFailure(env protocol.Envelope, primitive string, d treeDisplay, ref protocol.UITreeContextRef, intended uint32, px, py int, hit uint32) protocol.Envelope {
	h := protocol.OcclusionHit{
		ID:       fmt.Sprintf("0x%x", hit),
		Title:    titleOf(d, hit),
		OwnerPID: ownerPIDOf(d, hit),
	}
	return protocol.NewInputFailure(env.ID, primitiveResultType(primitive), ref, protocol.ReasonOccluded, &h,
		map[string]interface{}{
			"intended": fmt.Sprintf("0x%x", intended),
			"point":    map[string]interface{}{"x": px, "y": py},
			"ran":      false,
		})
}

// foregroundDenied reports that the platform refused to make the window active.
// It is an observed failure (the request ran, focus-stealing prevention or a
// WM that ignored the message said no), not a 403.
func foregroundDenied(env protocol.Envelope, primitive string, ref protocol.UITreeContextRef, win uint32, detail map[string]interface{}, why string) protocol.Envelope {
	return protocol.NewInputFailure(env.ID, primitiveResultType(primitive), ref, protocol.ReasonForegroundDenied, nil,
		mergeDetail(detail, map[string]interface{}{"why": why}))
}

// primitiveResultType maps a primitive name to its result command, so an
// observed failure travels on the same envelope type as the success it
// replaces. Only the primitives this build injects are mapped: a primitive with
// no backend answers NO_BACKEND before any of this runs.
func primitiveResultType(primitive string) string {
	switch primitive {
	case "pointer-click":
		return protocol.TypePointerClickResult
	case "text-input":
		return protocol.TypeTextInputResult
	case "key-press":
		return protocol.TypeKeyPressResult
	case "key-combo":
		return protocol.TypeKeyComboResult
	}
	return protocol.TypeError
}
