package protocol

// P13-03 (contract P10-07 v3.1 §7, §7.1): the window-tree wire shape.
//
// Two honesty rules are encoded here rather than in a backend:
//   - §7.1: a display with no EWMH window manager is a REFUSAL, never an empty
//     list. A zero-window answer is legal only after an enumeration ran
//     (`truncated:false, count:0`); the absence of a client list is a 501 with
//     `reason:"NO_WINDOW_MANAGER"` and `ran:false`.
//   - C-4: `hwnd` is a Windows-compat integer for `wts` rows only. There is no
//     field for it on this row type, and the test asserts its absence from the
//     serialised JSON (omitting a struct field is not the same as proving it
//     cannot be emitted).

const (
	// TreeSourceEWMHClientList is the only enumeration this build performs.
	TreeSourceEWMHClientList = "ewmh_client_list"
	// TreeUnitsX11 is the unit of every rect on an x11 row.
	TreeUnitsX11 = "px"
	// TreeDPIAwarenessX11 says coordinates are X server pixels, not scaled.
	TreeDPIAwarenessX11 = "x11_native"
)

// §7.1 adds NO_WINDOW_MANAGER to the §11 501 reason enum.
const (
	ReasonNoWindowManager = "NO_WINDOW_MANAGER"
	ReasonNoBackend       = "NO_BACKEND"
)

// §7 window kinds. A backend that cannot classify a window says "normal"
// rather than inventing a new word; the enum is closed so a caller can switch
// on it.
const (
	WindowKindNormal  = "normal"
	WindowKindDialog  = "dialog"
	WindowKindUtility = "utility"
	WindowKindSplash  = "splash"
	WindowKindToolbar = "toolbar"
	WindowKindMenu    = "menu"
	WindowKindTooltip = "tooltip"
	WindowKindDesktop = "desktop"
	WindowKindDock    = "dock"
	WindowKindOther   = "other"
	WindowIDKindXID   = "xid"
)

// UITreeRect is an outer rect in context pixels. Negative x/y are legal: the
// virtual-screen origin may sit above or left of a display.
type UITreeRect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// UITreeNative is the platform-native window handle. On X11 it carries the
// xid; on a `wts` row it would carry hwnd (not this build).
type UITreeNative struct {
	XID uint32 `json:"xid"`
}

// UITreeRow is §7's field table. `owner_pid` is omitted when the platform did
// not publish _NET_WM_PID: an unknown owner is not pid 0.
type UITreeRow struct {
	ID         string       `json:"id"`
	IDKind     string       `json:"id_kind"`
	Native     UITreeNative `json:"native"`
	Title      string       `json:"title"`
	Class      string       `json:"class"`
	Rect       UITreeRect   `json:"rect"`
	Iconic     bool         `json:"iconic"`
	Foreground bool         `json:"foreground"`
	OwnerPID   int          `json:"owner_pid,omitempty"`
	Visible    bool         `json:"visible"`
	Kind       string       `json:"kind"`
}

// UITreeContextRef is §7's echo of the selector: what was asked for, what the
// agent resolved it to, and where the resolution came from.
type UITreeContextRef struct {
	Requested string `json:"requested"`
	Resolved  string `json:"resolved"`
	Kind      string `json:"kind"`
	User      string `json:"user"`
	Source    string `json:"source"`
}

// UITreeResult is the §7 `data` object.
type UITreeResult struct {
	ContextRef   UITreeContextRef `json:"context_ref"`
	Units        string           `json:"units"`
	DPIScale     float64          `json:"dpi_scale"`
	DPIAwareness string           `json:"dpi_awareness"`
	Truncated    bool             `json:"truncated"`
	Count        int              `json:"count"`
	TreeSource   string           `json:"tree_source"`
	Windows      []UITreeRow      `json:"windows"`
}

// TypeUITreeResult is the agent→server result command for §7.
const TypeUITreeResult = "ui_tree_result"
