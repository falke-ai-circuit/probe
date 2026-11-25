package agent

// P13-03 (contract P10-07 v3.1 §7, §7.1): the window tree.
//
// The enumeration is written against treeDisplay — a seam that speaks in EWMH
// property reads, window attributes and geometry. The linux backend fills it
// from a real X connection (x11.go); the tests fill it with synthetic property
// data, which is how the rules below are proved without a desktop of 500
// windows. Nothing in this file touches a socket.
//
// The load-bearing rule is §7.1: a display with no EWMH window manager is a
// 501 refusal, never `windows: []`. An empty array is only legal after an
// enumeration ran, so the WM check happens BEFORE the client list is read and
// a failed check returns a refusal rather than an empty answer.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// maxTreeWindows is §7's hard cap. It bounds one answer, not the enumeration:
// a desktop with more windows reports `truncated:true` rather than a body that
// grows with the desktop.
const maxTreeWindows = 500

// treeDisplay is the seam. Every method is an observation of the X server, so
// a test can state a display and read the rows back.
type treeDisplay interface {
	Root() uint32
	Pixels() (w, h int)
	Property(win uint32, name string) (propValue, error)
	Attributes(win uint32) (x11Attrs, error)
	Geometry(win uint32) (x11Geometry, error)
	TranslateToRoot(win uint32) (x, y int, err error)
	AtomName(id uint32) (string, error)
	Close() error
}

// treeRefusal is the §7.1 answer: which display, why, and what the caller can
// change. reason is one of the §11 501 reasons; there is no success path from
// here.
type treeRefusal struct {
	Reason  string
	Display string
	Message string
}

// buildUITree enumerates a display against the seam. It returns either the §7
// result or a refusal — never an empty result standing in for a refusal.
func buildUITree(d treeDisplay, ctx protocol.UITreeContextRef, visibleOnly bool, limit int) (protocol.UITreeResult, *treeRefusal) {
	if _, refusal := wmCheck(d); refusal != nil {
		refusal.Display = ctx.Resolved
		return protocol.UITreeResult{}, refusal
	}

	ids, refusal := clientList(d)
	if refusal != nil {
		return protocol.UITreeResult{}, refusal
	}

	active := uint32(0)
	if p, err := d.Property(d.Root(), "_NET_ACTIVE_WINDOW"); err == nil {
		if v := propUint32s(p); len(v) > 0 {
			active = v[0]
		}
	}

	capN := maxTreeWindows
	if limit > 0 && limit < capN {
		capN = limit
	}
	truncated := false
	if len(ids) > capN {
		ids = ids[:capN]
		truncated = true
	}

	rows := make([]protocol.UITreeRow, 0, len(ids))
	for _, id := range ids {
		row, keep := treeRow(d, id, visibleOnly)
		if !keep {
			continue
		}
		rows = append(rows, row)
	}

	// Exactly one foreground, or none when the context has no focus. The
	// active window is marked only if the enumeration returned it: a
	// fabricated single is worse than no answer.
	found := false
	for i := range rows {
		if !found && active != 0 && rows[i].Native.XID == active {
			rows[i].Foreground = true
			found = true
		} else {
			rows[i].Foreground = false
		}
	}

	return protocol.UITreeResult{
		ContextRef:   ctx,
		Units:        protocol.TreeUnitsX11,
		DPIScale:     1.0,
		DPIAwareness: protocol.TreeDPIAwarenessX11,
		Truncated:    truncated,
		Count:        len(rows),
		TreeSource:   protocol.TreeSourceEWMHClientList,
		Windows:      rows,
	}, nil
}

// wmCheck reads _NET_SUPPORTING_WM_CHECK on the root window before anything
// else. Absent, zero, or pointing at a window the server no longer has means
// there is no EWMH-compliant WM: Xvfb publishes no client list and no stacking
// order, so an empty answer would be indistinguishable from an empty desktop.
func wmCheck(d treeDisplay) (uint32, *treeRefusal) {
	p, err := d.Property(d.Root(), "_NET_SUPPORTING_WM_CHECK")
	if err != nil {
		if err == errPropAbsent {
			return 0, &treeRefusal{
				Reason:  protocol.ReasonNoWindowManager,
				Message: "_NET_SUPPORTING_WM_CHECK is not set on the root window",
			}
		}
		return 0, &treeRefusal{
			Reason:  protocol.ReasonNoBackend,
			Message: fmt.Sprintf("could not read _NET_SUPPORTING_WM_CHECK: %v", err),
		}
	}
	vals := propUint32s(p)
	if len(vals) == 0 || vals[0] == 0 {
		return 0, &treeRefusal{
			Reason:  protocol.ReasonNoWindowManager,
			Message: "_NET_SUPPORTING_WM_CHECK names no window",
		}
	}
	if _, err := d.Attributes(vals[0]); err != nil {
		if _, isX := err.(*x11Error); isX {
			return 0, &treeRefusal{
				Reason:  protocol.ReasonNoWindowManager,
				Message: fmt.Sprintf("_NET_SUPPORTING_WM_CHECK points at window 0x%x, which no longer exists", vals[0]),
			}
		}
		return 0, &treeRefusal{
			Reason:  protocol.ReasonNoBackend,
			Message: fmt.Sprintf("could not verify the WM check window: %v", err),
		}
	}
	return vals[0], nil
}

// clientList reads the stacking order the WM publishes. _NET_CLIENT_LIST_STACKING
// is the z-order §7 asks for; _NET_CLIENT_LIST is the same set in map order. A
// WM that publishes neither has no clients to enumerate, which is a successful
// empty enumeration — the WM check already passed, so this is not the §7.1
// case.
func clientList(d treeDisplay) ([]uint32, *treeRefusal) {
	for _, name := range []string{"_NET_CLIENT_LIST_STACKING", "_NET_CLIENT_LIST"} {
		p, err := d.Property(d.Root(), name)
		if err == errPropAbsent {
			continue
		}
		if err != nil {
			return nil, &treeRefusal{
				Reason:  protocol.ReasonNoBackend,
				Message: fmt.Sprintf("could not read %s: %v", name, err),
			}
		}
		return propUint32s(p), nil
	}
	return nil, nil
}

// popupTypes are the window types §7 excludes: they are not top-level windows
// an operator addresses, they are transient surfaces of one. Override-redirect
// windows are excluded separately, from the window attributes.
var popupTypes = map[string]bool{
	"_NET_WM_WINDOW_TYPE_POPUP_MENU":    true,
	"_NET_WM_WINDOW_TYPE_DROPDOWN_MENU": true,
	"_NET_WM_WINDOW_TYPE_TOOLTIP":       true,
	"_NET_WM_WINDOW_TYPE_NOTIFICATION":  true,
	"_NET_WM_WINDOW_TYPE_COMBO":         true,
	"_NET_WM_WINDOW_TYPE_DND":           true,
}

// kindOf maps the EWMH type atom onto §7's closed kind enum. A window with no
// type is a normal window (EWMH's own default).
func kindOf(d treeDisplay, id uint32) (kind string, excluded bool) {
	p, err := d.Property(id, "_NET_WM_WINDOW_TYPE")
	if err != nil {
		return protocol.WindowKindNormal, false
	}
	for _, atomID := range propUint32s(p) {
		name, err := d.AtomName(atomID)
		if err != nil {
			continue
		}
		if popupTypes[name] {
			return "", true
		}
		switch name {
		case "_NET_WM_WINDOW_TYPE_DIALOG":
			return protocol.WindowKindDialog, false
		case "_NET_WM_WINDOW_TYPE_UTILITY":
			return protocol.WindowKindUtility, false
		case "_NET_WM_WINDOW_TYPE_SPLASH":
			return protocol.WindowKindSplash, false
		case "_NET_WM_WINDOW_TYPE_TOOLBAR":
			return protocol.WindowKindToolbar, false
		case "_NET_WM_WINDOW_TYPE_MENU":
			return protocol.WindowKindMenu, false
		case "_NET_WM_WINDOW_TYPE_DESKTOP":
			return protocol.WindowKindDesktop, false
		case "_NET_WM_WINDOW_TYPE_DOCK":
			return protocol.WindowKindDock, false
		}
	}
	return protocol.WindowKindNormal, false
}

// iconicOf reads WM_STATE: IconicState is 3. A window without WM_STATE is not
// iconic — it is unmanaged, which the visible test already excludes.
func iconicOf(d treeDisplay, id uint32) bool {
	p, err := d.Property(id, "WM_STATE")
	if err != nil || p.Format != 32 {
		return false
	}
	vals := propUint32s(p)
	return len(vals) > 0 && vals[0] == 3
}

// treeRow builds one §7 row, or reports that the window is not a top-level
// window this enumeration should return.
func treeRow(d treeDisplay, id uint32, visibleOnly bool) (protocol.UITreeRow, bool) {
	attrs, err := d.Attributes(id)
	if err != nil {
		return protocol.UITreeRow{}, false // vanished between list and read
	}
	if attrs.OverrideRedirect {
		return protocol.UITreeRow{}, false
	}
	kind, excluded := kindOf(d, id)
	if excluded {
		return protocol.UITreeRow{}, false
	}
	iconic := iconicOf(d, id)
	visible := attrs.MapState == x11MapStateViewable
	if visibleOnly && !visible && !iconic {
		return protocol.UITreeRow{}, false
	}

	g, err := d.Geometry(id)
	if err != nil {
		return protocol.UITreeRow{}, false
	}
	x, y, err := d.TranslateToRoot(id)
	if err != nil {
		return protocol.UITreeRow{}, false
	}
	rect := protocol.UITreeRect{X: x, Y: y, W: g.W, H: g.H}
	// Outer rect: a reparenting WM stores the frame around the client, so the
	// client's own geometry under-reports the window the operator sees.
	if p, err := d.Property(id, "_NET_FRAME_EXTENTS"); err == nil {
		if v := propUint32s(p); len(v) >= 4 {
			left, right, top, bottom := int(v[0]), int(v[1]), int(v[2]), int(v[3])
			rect.X -= left
			rect.Y -= top
			rect.W += left + right
			rect.H += top + bottom
		}
	}

	row := protocol.UITreeRow{
		ID:     fmt.Sprintf("0x%x", id),
		IDKind: protocol.WindowIDKindXID,
		Native: protocol.UITreeNative{XID: id},
		Title:  titleOf(d, id),
		Class:  classOf(d, id),
		Rect:   rect,
		Iconic: iconic,
		// visible is the map state, not "not iconic": a window can be viewable
		// while the WM is switching it out.
		Visible: visible,
		Kind:    kind,
	}
	if p, err := d.Property(id, "_NET_WM_PID"); err == nil {
		if v := propUint32s(p); len(v) > 0 {
			row.OwnerPID = int(v[0])
		}
	}
	return row, true
}

// titleOf returns the full title. §7 is explicit: no truncation. _NET_WM_NAME
// is UTF-8 by definition; WM_NAME is the fallback for a client that never set
// the EWMH one.
func titleOf(d treeDisplay, id uint32) string {
	if p, err := d.Property(id, "_NET_WM_NAME"); err == nil {
		return propText(p)
	}
	if p, err := d.Property(id, "WM_NAME"); err == nil {
		return propText(p)
	}
	return ""
}

// classOf returns WM_CLASS's class (the second NUL-separated field), normalised.
// The instance name is the first field and is not what §7 asks for.
func classOf(d treeDisplay, id uint32) string {
	p, err := d.Property(id, "WM_CLASS")
	if err != nil {
		return ""
	}
	parts := strings.Split(propText(p), "\x00")
	if len(parts) >= 2 {
		return strings.TrimSpace(parts[1])
	}
	return strings.TrimSpace(parts[0])
}

// displayNum parses the display index out of a context id (\"x11::0\") or a
// DISPLAY string (\":0.0\").
func displayNum(s string) (int, bool) {
	if i := strings.Index(s, "::"); i >= 0 {
		n, err := strconv.Atoi(strings.TrimSpace(s[i+2:]))
		return n, err == nil
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		rest := s[i+1:]
		if dot := strings.Index(rest, "."); dot >= 0 {
			rest = rest[:dot]
		}
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		return n, err == nil
	}
	return 0, false
}
