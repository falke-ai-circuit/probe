package agent

// P13-03 acceptance (contract P10-07 v3.1 §7/§7.1), driven through the
// treeDisplay seam: the enumeration rules are stated as synthetic EWMH
// property data, so a 500-window cap and an override-redirect window are
// proved without a desktop. The real-X-server refusal is in
// p1303_xvfb_test.go.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// --- the seam, filled with stated properties ------------------------------

type fakeWin struct {
	props map[string]propValue
	attrs x11Attrs
	geom  x11Geometry
	posX  int
	posY  int
	dead  bool
}

type fakeDisplay struct {
	rootID uint32
	wins   map[uint32]*fakeWin
	atoms  map[uint32]string
	nextAt int
}

func newFakeDisplay() *fakeDisplay {
	return &fakeDisplay{
		rootID: 0x1,
		wins:   map[uint32]*fakeWin{},
		atoms:  map[uint32]string{},
		nextAt: 0x100,
	}
}

func p32(vals ...uint32) propValue {
	data := make([]byte, 4*len(vals))
	for i, v := range vals {
		data[4*i] = byte(v)
		data[4*i+1] = byte(v >> 8)
		data[4*i+2] = byte(v >> 16)
		data[4*i+3] = byte(v >> 24)
	}
	return propValue{Format: 32, Data: data}
}

func p8(s string) propValue { return propValue{Format: 8, Data: []byte(s)} }

// atom interns a name to a synthetic id, so a test can put the id in a
// _NET_WM_WINDOW_TYPE property and have the builder resolve it back.
func (f *fakeDisplay) atom(name string) uint32 {
	for id, n := range f.atoms {
		if n == name {
			return id
		}
	}
	id := uint32(f.nextAt)
	f.nextAt++
	f.atoms[id] = name
	return id
}

func (f *fakeDisplay) addWindow(id uint32, w *fakeWin) *fakeWin {
	if w.props == nil {
		w.props = map[string]propValue{}
	}
	f.wins[id] = w
	return w
}

// wm wires a display with a live EWMH WM check window and the given client
// list, in stacking order.
func (f *fakeDisplay) wm(active uint32, ids ...uint32) *fakeDisplay {
	f.addWindow(0x2, &fakeWin{attrs: x11Attrs{MapState: x11MapStateViewable}})
	f.wins[f.rootID] = &fakeWin{props: map[string]propValue{
		"_NET_SUPPORTING_WM_CHECK":  p32(0x2),
		"_NET_CLIENT_LIST_STACKING": p32(ids...),
		"_NET_ACTIVE_WINDOW":        p32(active),
	}}
	return f
}

func (f *fakeDisplay) Root() uint32       { return f.rootID }
func (f *fakeDisplay) Pixels() (int, int) { return 1920, 1080 }
func (f *fakeDisplay) Close() error       { return nil }

func (f *fakeDisplay) win(id uint32) (*fakeWin, error) {
	w, ok := f.wins[id]
	if !ok || w.dead {
		return nil, &x11Error{Code: x11ErrorBadWindow, Want: "window"}
	}
	return w, nil
}

func (f *fakeDisplay) Property(id uint32, name string) (propValue, error) {
	w, err := f.win(id)
	if err != nil {
		return propValue{}, err
	}
	p, ok := w.props[name]
	if !ok {
		return propValue{}, errPropAbsent
	}
	return p, nil
}

func (f *fakeDisplay) Attributes(id uint32) (x11Attrs, error) {
	w, err := f.win(id)
	if err != nil {
		return x11Attrs{}, err
	}
	return w.attrs, nil
}

func (f *fakeDisplay) Geometry(id uint32) (x11Geometry, error) {
	w, err := f.win(id)
	if err != nil {
		return x11Geometry{}, err
	}
	return w.geom, nil
}

func (f *fakeDisplay) TranslateToRoot(id uint32) (int, int, error) {
	w, err := f.win(id)
	if err != nil {
		return 0, 0, err
	}
	return w.posX, w.posY, nil
}

func (f *fakeDisplay) AtomName(id uint32) (string, error) {
	name, ok := f.atoms[id]
	if !ok {
		return "", &x11Error{Code: x11ErrorBadAtom, Want: "atom name"}
	}
	return name, nil
}

// simpleWindow is a normal, viewable, titled window at the origin.
func (f *fakeDisplay) simpleWindow(id uint32, title string) *fakeWin {
	return f.addWindow(id, &fakeWin{
		props: map[string]propValue{
			"_NET_WM_NAME":        p8(title),
			"_NET_WM_WINDOW_TYPE": p32(f.atom("_NET_WM_WINDOW_TYPE_NORMAL")),
			"WM_STATE":            p32(1),
		},
		attrs: x11Attrs{MapState: x11MapStateViewable},
		geom:  x11Geometry{W: 300, H: 200},
	})
}

func ctxRef() protocol.UITreeContextRef {
	return protocol.UITreeContextRef{
		Requested: "x11::0", Resolved: "x11::0", Kind: "x11", User: "flk", Source: "selector",
	}
}

// TestP1303Shape is acceptance item 1: every §7 field is present with the
// contract's value, including the context_ref echo and the fixed units.
func TestP1303Shape(t *testing.T) {
	f := newFakeDisplay()
	w := f.simpleWindow(0x3c00007, "…/Control Panel")
	w.props["WM_CLASS"] = p8("probe\x00Qt51516QWindowIcon\x00")
	w.props["_NET_WM_PID"] = p32(2640)
	w.geom = x11Geometry{W: 1038, H: 118}
	f.wm(0x3c00007, 0x3c00007)

	res, refusal := buildUITree(f, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal on a live WM display: %+v", refusal)
	}
	if res.Units != "px" || res.DPIAwareness != "x11_native" || res.TreeSource != "ewmh_client_list" {
		t.Fatalf("units/awareness/source = %q/%q/%q", res.Units, res.DPIAwareness, res.TreeSource)
	}
	if res.Count != 1 || len(res.Windows) != 1 || res.Truncated {
		t.Fatalf("count=%d windows=%d truncated=%v", res.Count, len(res.Windows), res.Truncated)
	}
	if res.ContextRef != ctxRef() {
		t.Errorf("context_ref = %+v, want %+v", res.ContextRef, ctxRef())
	}
	row := res.Windows[0]
	if row.ID != "0x3c00007" || row.IDKind != "xid" || row.Native.XID != 0x3c00007 {
		t.Errorf("id row = %q/%q/%#x", row.ID, row.IDKind, row.Native.XID)
	}
	if row.Title != "…/Control Panel" {
		t.Errorf("title = %q", row.Title)
	}
	if row.Class != "Qt51516QWindowIcon" {
		t.Errorf("class = %q, want the WM_CLASS class field", row.Class)
	}
	if row.Rect != (protocol.UITreeRect{X: 0, Y: 0, W: 1038, H: 118}) {
		t.Errorf("rect = %+v", row.Rect)
	}
	if row.OwnerPID != 2640 {
		t.Errorf("owner_pid = %d", row.OwnerPID)
	}
	if !row.Visible || row.Iconic || !row.Foreground {
		t.Errorf("visible/iconic/foreground = %v/%v/%v", row.Visible, row.Iconic, row.Foreground)
	}
	if row.Kind != protocol.WindowKindNormal {
		t.Errorf("kind = %q", row.Kind)
	}
}

// TestP1303NoHWND is acceptance item 2 (C-4): the field must be absent from
// the serialised row, not merely absent from the struct. A `hwnd` on an x11
// row is the exact defect v2 shipped.
func TestP1303NoHWND(t *testing.T) {
	f := newFakeDisplay()
	f.simpleWindow(0x400001, "one")
	f.wm(0x400001, 0x400001)
	res, refusal := buildUITree(f, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	body, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "hwnd") {
		t.Fatalf("x11 tree body carries hwnd: %s", body)
	}
	var decoded struct {
		Windows []map[string]interface{} `json:"windows"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := decoded.Windows[0]["hwnd"]; has {
		t.Fatal("decoded x11 row has a hwnd key")
	}
	if _, has := decoded.Windows[0]["native"]; !has {
		t.Fatal("x11 row has no native block")
	}
}

// TestP1303EnumerationRules is acceptance item 3: top-level windows only, in
// stacking order, with the 500 cap reported as truncated.
func TestP1303EnumerationRules(t *testing.T) {
	f := newFakeDisplay()
	f.simpleWindow(0x500001, "first")
	// override-redirect: a menu/tooltip the WM does not manage.
	or := f.simpleWindow(0x500002, "override")
	or.attrs.OverrideRedirect = true
	// popup type: transient, excluded by type.
	popup := f.simpleWindow(0x500003, "popup")
	popup.props["_NET_WM_WINDOW_TYPE"] = p32(f.atom("_NET_WM_WINDOW_TYPE_POPUP_MENU"))
	// unmapped, not iconic: not a window the operator can see or restore.
	unmapped := f.simpleWindow(0x500004, "unmapped")
	unmapped.attrs.MapState = 0
	f.simpleWindow(0x500005, "last")
	f.wm(0x500001, 0x500002, 0x500003, 0x500004, 0x500001, 0x500005)

	res, refusal := buildUITree(f, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	if res.Count != 2 {
		t.Fatalf("count = %d, want the two top-level viewable windows: %+v", res.Count, res.Windows)
	}
	if res.Windows[0].Native.XID != 0x500001 || res.Windows[1].Native.XID != 0x500005 {
		t.Fatalf("stacking order = %#x,%#x; want 0x500001,0x500005",
			res.Windows[0].Native.XID, res.Windows[1].Native.XID)
	}

	// The cap is synthetic: 501 stated windows, not a real desktop.
	big := newFakeDisplay()
	ids := make([]uint32, 0, 501)
	for i := 0; i < 501; i++ {
		id := uint32(0x600000 + i)
		big.simpleWindow(id, fmt.Sprintf("w%d", i))
		ids = append(ids, id)
	}
	big.wm(ids[0], ids...)
	capped, refusal := buildUITree(big, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	if !capped.Truncated {
		t.Fatal("truncated = false with 501 windows, want true")
	}
	if capped.Count != maxTreeWindows || len(capped.Windows) != maxTreeWindows {
		t.Fatalf("count = %d / %d, want the 500 cap", capped.Count, len(capped.Windows))
	}

	// Exactly at the cap is not truncation.
	exact := newFakeDisplay()
	ids = ids[:500]
	exactIDs := make([]uint32, 0, 500)
	for i := 0; i < 500; i++ {
		id := uint32(0x700000 + i)
		exact.simpleWindow(id, "w")
		exactIDs = append(exactIDs, id)
	}
	exact.wm(exactIDs[0], exactIDs...)
	full, refusal := buildUITree(exact, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	if full.Truncated || full.Count != 500 {
		t.Fatalf("500 windows: truncated=%v count=%d, want false/500", full.Truncated, full.Count)
	}
}

// TestP1303Foreground is acceptance item 4: exactly one true when the context
// has focus, none when it does not, and never a fabricated single.
func TestP1303Foreground(t *testing.T) {
	f := newFakeDisplay()
	f.simpleWindow(0x800001, "a")
	f.simpleWindow(0x800002, "b")
	f.wm(0x800002, 0x800001, 0x800002)
	res, refusal := buildUITree(f, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	trues := 0
	for _, r := range res.Windows {
		if r.Foreground {
			trues++
			if r.Native.XID != 0x800002 {
				t.Errorf("foreground on %#x, want 0x800002", r.Native.XID)
			}
		}
	}
	if trues != 1 {
		t.Fatalf("foreground true count = %d, want exactly 1", trues)
	}

	// No focus: _NET_ACTIVE_WINDOW absent.
	none := newFakeDisplay()
	none.simpleWindow(0x810001, "a")
	none.wm(0, 0x810001)
	delete(none.wins[none.rootID].props, "_NET_ACTIVE_WINDOW")
	res, refusal = buildUITree(none, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	for _, r := range res.Windows {
		if r.Foreground {
			t.Fatalf("foreground true on a context with no focus: %+v", r)
		}
	}

	// Active window is not in the client list: no row may be promoted.
	stale := newFakeDisplay()
	stale.simpleWindow(0x820001, "a")
	stale.wm(0x999999, 0x820001)
	res, refusal = buildUITree(stale, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	for _, r := range res.Windows {
		if r.Foreground {
			t.Fatalf("foreground fabricated for an unlisted active window: %+v", r)
		}
	}

	// IconicState: WM_STATE 3, and it survives visible_only as iconic.
	icon := newFakeDisplay()
	w := icon.simpleWindow(0x830001, "icon")
	w.props["WM_STATE"] = p32(3)
	w.attrs.MapState = 0
	icon.wm(0, 0x830001)
	res, refusal = buildUITree(icon, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	if res.Count != 1 || !res.Windows[0].Iconic || res.Windows[0].Visible {
		t.Fatalf("iconic row = %+v", res.Windows)
	}
}

// TestP1303TitleIsNotTruncated is acceptance item 5: the whole title comes
// back. §7 names a truncating caller as a defect.
func TestP1303TitleIsNotTruncated(t *testing.T) {
	long := strings.Repeat("ab", 400) // 800 chars
	f := newFakeDisplay()
	f.simpleWindow(0x900001, long)
	f.wm(0x900001, 0x900001)
	res, refusal := buildUITree(f, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	if res.Windows[0].Title != long {
		t.Fatalf("title len = %d, want %d (untruncated)", len(res.Windows[0].Title), len(long))
	}

	// A UTF-8 title survives byte for byte.
	utf := "caffè — ☕ — naïve"
	g := newFakeDisplay()
	g.simpleWindow(0x900002, utf)
	g.wm(0x900002, 0x900002)
	res, refusal = buildUITree(g, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("refusal: %+v", refusal)
	}
	if res.Windows[0].Title != utf {
		t.Fatalf("utf8 title = %q, want %q", res.Windows[0].Title, utf)
	}
}

// TestP1303NoWindowManagerRefusal is acceptance item 6 at the builder: the
// answer is a refusal with NO_WINDOW_MANAGER, and no empty success stands in
// for it.
func TestP1303NoWindowManagerRefusal(t *testing.T) {
	// Xvfb as measured: a live root, no _NET_SUPPORTING_WM_CHECK.
	bare := newFakeDisplay()
	bare.wins[bare.rootID] = &fakeWin{props: map[string]propValue{}}
	res, refusal := buildUITree(bare, ctxRef(), true, 0)
	if refusal == nil {
		t.Fatalf("no refusal on a display with no WM; result = %+v", res)
	}
	if refusal.Reason != protocol.ReasonNoWindowManager {
		t.Fatalf("reason = %q, want %q", refusal.Reason, protocol.ReasonNoWindowManager)
	}
	if len(res.Windows) != 0 || res.Count != 0 {
		t.Fatalf("a refusal carried a window list: %+v", res)
	}

	// The check window died: pointing at it is not a WM.
	dead := newFakeDisplay()
	dead.wins[dead.rootID] = &fakeWin{props: map[string]propValue{
		"_NET_SUPPORTING_WM_CHECK": p32(0xdead),
	}}
	_, refusal = buildUITree(dead, ctxRef(), true, 0)
	if refusal == nil || refusal.Reason != protocol.ReasonNoWindowManager {
		t.Fatalf("dead check window refusal = %+v, want NO_WINDOW_MANAGER", refusal)
	}

	// A real WM with no clients is legal: 200 count 0, not a refusal.
	empty := newFakeDisplay()
	empty.wm(0)
	res, refusal = buildUITree(empty, ctxRef(), true, 0)
	if refusal != nil {
		t.Fatalf("a WM with no clients was refused: %+v", refusal)
	}
	if res.Count != 0 || len(res.Windows) != 0 || res.Truncated {
		t.Fatalf("empty enumeration = %+v", res)
	}
}

// TestP1303SwitchFlips is acceptance item 7, agent half: one constant, two
// facts. With a WM the context advertises window.tree:true and carries no
// window.tree refusal; without one it advertises false with the §7.1 entry.
func TestP1303SwitchFlips(t *testing.T) {
	withWM := mustRow(t, buildContexts(testProbe(wmPresent, nil)), "x11::0")
	if !withWM.Features.WindowTree {
		t.Fatal("window.tree = false with a WM present, though the §7 backend shipped")
	}
	for _, r := range withWM.Refusals {
		if r.Feature == "window.tree" {
			t.Fatalf("window.tree refusal advertised on a WM display: %+v", r)
		}
	}

	bare := mustRow(t, buildContexts(testProbe(wmAbsent, nil)), "x11::0")
	if bare.Features.WindowTree {
		t.Fatal("window.tree = true with no WM")
	}
	want := protocol.ContextRefusal{
		Feature: "window.tree",
		Code:    protocol.ErrPrimitiveUnsupported,
		Reason:  protocol.ReasonNoWindowManager,
	}
	found := false
	for _, r := range bare.Refusals {
		if r == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("refusals = %+v, want %+v", bare.Refusals, want)
	}
}
