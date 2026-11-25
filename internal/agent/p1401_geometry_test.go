package agent

// P14-01 acceptance (contract P10-07 v3.1 §9.1), driven through the treeDisplay
// seam: the rect, the client list and the WM check are the same observations the
// window tree publishes, so geometry and tree cannot diverge here by construction.
//
// Every case that asserts "no input was emitted" counts the platform's own
// events. A refusal that still fires the click would pass every other assertion
// in this file, so the counter is the load-bearing one.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/platform"
	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// --- the stacking seam ------------------------------------------------------

// fakeProbe is fakeDisplay plus the two X answers §9.1 needs: what child of a
// window contains a point, and a foreground request the test can watch.
type fakeProbe struct {
	*fakeDisplay
	children map[uint32][]uint32 // parent -> children, bottom..top
	events   *[]string
	ignoreFG bool // a WM that accepts the message and does nothing
}

func newFakeProbe() *fakeProbe {
	return &fakeProbe{fakeDisplay: newFakeDisplay(), children: map[uint32][]uint32{}, events: &[]string{}}
}

// winAt places a window in root space. Stacking is the order children are added.
func (p *fakeProbe) winAt(id, parent uint32, x, y, w, h int) *fakeWin {
	fw := p.addWindow(id, &fakeWin{
		props: map[string]propValue{
			"_NET_WM_NAME":        p8(fmt.Sprintf("win-0x%x", id)),
			"_NET_WM_WINDOW_TYPE": p32(p.atom("_NET_WM_WINDOW_TYPE_NORMAL")),
			"WM_STATE":            p32(1),
			"_NET_WM_PID":         p32(id + 1000),
		},
		attrs: x11Attrs{MapState: x11MapStateViewable},
		geom:  x11Geometry{W: w, H: h},
		posX:  x,
		posY:  y,
	})
	p.children[parent] = append(p.children[parent], id)
	return fw
}

// move changes a window's origin after the caller read the tree — the exact
// seam §9.1's dispatch-time rule exists for.
func (p *fakeProbe) move(id uint32, x, y int) {
	p.wins[id].posX, p.wins[id].posY = x, y
}

func (p *fakeProbe) PointIn(win uint32, px, py int) (int32, int32, uint32, error) {
	w, err := p.win(win)
	if err != nil {
		return 0, 0, 0, err
	}
	kids := p.children[win]
	// Topmost first: the last child in the list is the one on top.
	for i := len(kids) - 1; i >= 0; i-- {
		c, err := p.win(kids[i])
		if err != nil || c.attrs.MapState != x11MapStateViewable {
			continue
		}
		if px >= c.posX && px < c.posX+c.geom.W && py >= c.posY && py < c.posY+c.geom.H {
			return int32(px - w.posX), int32(py - w.posY), kids[i], nil
		}
	}
	return int32(px - w.posX), int32(py - w.posY), 0, nil
}

func (p *fakeProbe) RequestForeground(win uint32) error {
	*p.events = append(*p.events, "foreground")
	if p.ignoreFG {
		return nil
	}
	p.wins[p.rootID].props["_NET_ACTIVE_WINDOW"] = p32(win)
	return nil
}

// --- the injection seam -----------------------------------------------------

// recPlatform records every input event and the order it happened in, so a test
// can assert both "zero events" and "foreground before keystroke".
type recPlatform struct {
	platform.Platform
	events *[]string
	clicks [][3]interface{}
	texts  []string
	keys   []string
}

func (r *recPlatform) Click(x, y int, button string) error {
	*r.events = append(*r.events, "click")
	r.clicks = append(r.clicks, [3]interface{}{x, y, button})
	return nil
}

func (r *recPlatform) TypeText(text string) error {
	*r.events = append(*r.events, "type")
	r.texts = append(r.texts, text)
	return nil
}

func (r *recPlatform) KeyPress(key string) error {
	*r.events = append(*r.events, "key")
	r.keys = append(r.keys, key)
	return nil
}

func (r *recPlatform) KeyCombo(keys []string) error {
	*r.events = append(*r.events, "combo")
	return nil
}

func p1401Agent(p *fakeProbe) (*Agent, *recPlatform) {
	rec := &recPlatform{events: p.events}
	a := &Agent{plat: rec}
	a.openDisplay = func(num int) (windowProbe, error) { return p, nil }
	return a, rec
}

func p1401Envelope(t *testing.T, id string, body map[string]interface{}) protocol.Envelope {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return protocol.Envelope{ID: id, Params: raw}
}

// p1401Failure decodes the §9.1 observed-failure result.
func p1401Failure(t *testing.T, env protocol.Envelope) protocol.InputFailure {
	t.Helper()
	if env.Error != nil {
		t.Fatalf("expected an observed failure (200 ok:false), got error %q: %s", env.Error.Code, env.Error.Message)
	}
	var out protocol.InputFailure
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("unmarshal result %s: %v", string(env.Result), err)
	}
	return out
}

// --- acceptance 1: window-relative resolution at dispatch time --------------

func TestP1401WindowRelativeUsesCurrentRect(t *testing.T) {
	p := newFakeProbe()
	const target = 0x3c00007
	p.winAt(0x2, p.rootID, 0, 0, 1, 1) // WM check window
	p.winAt(target, p.rootID, 100, 50, 300, 200)
	p.wm(target, target)
	a, rec := p1401Agent(p)

	// The caller reads the tree FIRST — origin (100,50).
	tree, refusal := buildUITree(p, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("tree refused: %+v", refusal)
	}
	if tree.Windows[0].Rect.X != 100 || tree.Windows[0].Rect.Y != 50 {
		t.Fatalf("tree rect = %+v, want origin 100,50", tree.Windows[0].Rect)
	}

	// The window then MOVES before dispatch. A resolver that reused the tree's
	// rect would click at 110,70 and miss.
	p.move(target, 140, 80)

	env := a.handleClick(p1401Envelope(t, "c1", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "x": 10, "y": 20, "button": "left",
	}))
	if env.Error != nil {
		t.Fatalf("click refused: %s (%v)", env.Error.Message, env.Error.Detail)
	}
	if len(rec.clicks) != 1 {
		t.Fatalf("clicks = %d, want 1", len(rec.clicks))
	}
	if rec.clicks[0][0] != 150 || rec.clicks[0][1] != 100 {
		t.Fatalf("click at %v, want 150,100 (current rect origin 140,80 + 10,20) — the tree rect 100,50 would have produced 110,70",
			rec.clicks[:1])
	}
}

// TestP1401WindowAbsentKeepsAbsolutePoint is bullet 1's control: without
// `window` the point is absolute and no display is opened at all.
func TestP1401WindowAbsentKeepsAbsolutePoint(t *testing.T) {
	rec := &recPlatform{events: &[]string{}}
	a := &Agent{plat: rec, openDisplay: func(int) (windowProbe, error) {
		t.Fatal("absolute input must not open a display to place the point")
		return nil, nil
	}}
	env := a.handleClick(p1401Envelope(t, "c2", map[string]interface{}{"x": 42, "y": 96, "button": "left"}))
	if env.Error != nil {
		t.Fatalf("absolute click refused: %s", env.Error.Message)
	}
	if len(rec.clicks) != 1 || rec.clicks[0][0] != 42 || rec.clicks[0][1] != 96 {
		t.Fatalf("clicks = %v, want one click at 42,96", rec.clicks)
	}
}

// --- acceptance 2: occlusion refuses, and emits nothing --------------------

func TestP1401OccludedRefusesWithNoInput(t *testing.T) {
	p := newFakeProbe()
	const target, cover = 0x3c00007, 0x3c00011
	p.winAt(0x2, p.rootID, 0, 0, 1, 1)
	p.winAt(target, p.rootID, 0, 0, 300, 200) // bottom
	p.winAt(cover, p.rootID, 0, 0, 400, 300)  // on top, covers the point
	p.wm(target, target, cover)
	a, rec := p1401Agent(p)

	env := a.handleClick(p1401Envelope(t, "c3", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "x": 42, "y": 96, "button": "left",
	}))
	f := p1401Failure(t, env)
	if f.OK || f.Data.Reason != protocol.ReasonOccluded || f.Data.Ran {
		t.Fatalf("failure = %+v, want ok:false reason OCCLUDED ran:false", f)
	}
	if f.Data.Hit == nil || f.Data.Hit.ID != "0x3c00011" {
		t.Fatalf("hit = %+v, want the covering window 0x3c00011", f.Data.Hit)
	}
	if f.Data.Hit.OwnerPID != int(cover)+1000 {
		t.Fatalf("hit.owner_pid = %d, want %d", f.Data.Hit.OwnerPID, int(cover)+1000)
	}
	if f.Data.Detail["intended"] != "0x3c00007" {
		t.Fatalf("detail.intended = %v", f.Data.Detail["intended"])
	}
	pt, _ := f.Data.Detail["point"].(map[string]interface{})
	if pt == nil || pt["x"] != float64(42) || pt["y"] != float64(96) {
		t.Fatalf("detail.point = %v, want the computed absolute 42,96", f.Data.Detail["point"])
	}
	if f.ContextRef.Resolved != "x11::0" {
		t.Fatalf("context_ref.resolved = %q", f.ContextRef.Resolved)
	}
	if len(rec.clicks) != 0 || len(*p.events) != 0 {
		t.Fatalf("input was emitted despite OCCLUDED: clicks=%v events=%v", rec.clicks, *p.events)
	}
}

// --- acceptance 3: not occluded still fires exactly once -------------------

func TestP1401TopWindowFiresOnce(t *testing.T) {
	p := newFakeProbe()
	const target, under = 0x3c00007, 0x3c00011
	p.winAt(0x2, p.rootID, 0, 0, 1, 1)
	p.winAt(under, p.rootID, 0, 0, 400, 300)  // bottom
	p.winAt(target, p.rootID, 0, 0, 300, 200) // upgraded to the top
	p.wm(target, under, target)
	a, rec := p1401Agent(p)

	env := a.handleClick(p1401Envelope(t, "c4", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "x": 42, "y": 96, "button": "left",
	}))
	if env.Error != nil {
		t.Fatalf("click refused: %s (%v)", env.Error.Message, env.Error.Detail)
	}
	if len(rec.clicks) != 1 {
		t.Fatalf("clicks = %d, want exactly 1", len(rec.clicks))
	}
	if rec.clicks[0][0] != 42 || rec.clicks[0][1] != 96 {
		t.Fatalf("click at %v, want 42,96", rec.clicks[0])
	}
}

// --- acceptance 4: 404 before injection ------------------------------------

func TestP1401WindowNotInTreeRefusesBeforeInjection(t *testing.T) {
	p := newFakeProbe()
	p.winAt(0x2, p.rootID, 0, 0, 1, 1)
	p.winAt(0x3c00007, p.rootID, 0, 0, 300, 200)
	p.wm(0x3c00007, 0x3c00007)
	a, _ := p1401Agent(p)

	env := a.handleClick(p1401Envelope(t, "c5", map[string]interface{}{
		"context": "x11::0", "window": "0xdeadbee", "x": 1, "y": 2, "button": "left",
	}))
	if env.Error == nil {
		t.Fatalf("expected a refusal, got %s", string(env.Result))
	}
	if env.Error.Code != protocol.ErrWindowNotFound {
		t.Fatalf("code = %q, want %q", env.Error.Code, protocol.ErrWindowNotFound)
	}
	if env.Error.Detail["resolved_context"] != "x11::0" {
		t.Fatalf("detail.resolved_context = %v", env.Error.Detail["resolved_context"])
	}
	if env.Error.Detail["window"] != "0xdeadbee" {
		t.Fatalf("detail.window = %v", env.Error.Detail["window"])
	}
	if len(*p.events) != 0 {
		t.Fatalf("input was emitted before the window check: %v", *p.events)
	}
}

// --- acceptance 5: the focus gate -----------------------------------------

func TestP1401FocusGate(t *testing.T) {
	newFixture := func() (*fakeProbe, *Agent, *recPlatform) {
		p := newFakeProbe()
		p.winAt(0x2, p.rootID, 0, 0, 1, 1)
		p.winAt(0x3c00007, p.rootID, 0, 0, 300, 200)
		p.winAt(0x3c00011, p.rootID, 0, 0, 300, 200)
		p.wm(0x3c00011, 0x3c00007, 0x3c00011) // another window owns the focus
		a, rec := p1401Agent(p)
		return p, a, rec
	}

	t.Run("no focus:true is a 403 and no keystroke", func(t *testing.T) {
		p, a, rec := newFixture()
		env := a.handleType(p1401Envelope(t, "t1", map[string]interface{}{
			"context": "x11::0", "window": "0x3c00007", "text": "secret",
		}))
		if env.Error == nil || env.Error.Code != protocol.ErrPrimitiveRefused {
			t.Fatalf("error = %+v, want PRIMITIVE_REFUSED", env.Error)
		}
		if env.Error.Detail["reason"] != protocol.ReasonNoFocusTarget {
			t.Fatalf("reason = %v, want %q", env.Error.Detail["reason"], protocol.ReasonNoFocusTarget)
		}
		if len(rec.texts) != 0 || len(*p.events) != 0 {
			t.Fatalf("typed despite the refusal: %v / %v", rec.texts, *p.events)
		}
	})

	t.Run("focus:true foregrounds before typing", func(t *testing.T) {
		p, a, rec := newFixture()
		env := a.handleType(p1401Envelope(t, "t2", map[string]interface{}{
			"context": "x11::0", "window": "0x3c00007", "text": "hello", "focus": true,
		}))
		if env.Error != nil {
			t.Fatalf("typing refused: %s (%v)", env.Error.Message, env.Error.Detail)
		}
		if len(rec.texts) != 1 || rec.texts[0] != "hello" {
			t.Fatalf("texts = %v, want one", rec.texts)
		}
		if len(*p.events) != 2 || (*p.events)[0] != "foreground" || (*p.events)[1] != "type" {
			t.Fatalf("events = %v, want [foreground type] — a keystroke before the foreground is the silent context switch §9.1 forbids", *p.events)
		}
	})

	t.Run("focus:true that the WM ignores is FOREGROUND_DENIED and no keystroke", func(t *testing.T) {
		p, a, rec := newFixture()
		p.ignoreFG = true
		env := a.handleKey(p1401Envelope(t, "t3", map[string]interface{}{
			"context": "x11::0", "window": "0x3c00007", "key": "Return", "focus": true,
		}))
		f := p1401Failure(t, env)
		if f.Data.Reason != protocol.ReasonForegroundDenied || f.Data.Ran {
			t.Fatalf("failure = %+v, want FOREGROUND_DENIED ran:false", f)
		}
		if len(rec.keys) != 0 {
			t.Fatalf("key was pressed despite the denial: %v", rec.keys)
		}
	})
}

// TestP1401ObservedEnumIsClosed keeps §8.1's enum honest: the reasons this unit
// can emit are exactly the ones the contract enumerates.
func TestP1401ObservedEnumIsClosed(t *testing.T) {
	want := map[string]bool{
		protocol.ReasonOccluded:         true,
		protocol.ReasonForegroundDenied: true,
		protocol.ReasonNotSupported:     true,
	}
	if len(protocol.ObservedReasons) != len(want) {
		t.Fatalf("ObservedReasons = %v, want the three §8.1 values", protocol.ObservedReasons)
	}
	for _, r := range protocol.ObservedReasons {
		if !want[r] {
			t.Fatalf("reason %q is not in §8.1's closed enum", r)
		}
	}
}
