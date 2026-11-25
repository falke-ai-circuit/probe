package agent

// P13-03: the platform-neutral half of the X11 seam. These are the values a
// treeDisplay answers with — property bytes, window attributes, geometry — and
// they live untagged so the §7 builder (uitree.go) and its synthetic tests
// compile on every platform. The connection that produces them is linux-only
// (x11.go).

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// x11Error is an X protocol error packet (a dead window, a bad atom). It is
// the observation that makes "the WM check window is gone" distinguishable
// from "the property was never set".
type x11Error struct {
	Code byte
	Want string
}

func (e *x11Error) Error() string {
	return fmt.Sprintf("x11 error %d while reading %s", e.Code, e.Want)
}

const (
	x11ErrorBadWindow = 3
	x11ErrorBadAtom   = 5
	x11ErrorBadValue  = 2
)

// propValue is one property read: the format the server used (8/16/32), the
// type atom, and the raw value bytes in the server's byte order.
type propValue struct {
	Format byte
	Type   uint32
	Data   []byte
}

// property reads a property on win. A property that is not set comes back as
// ErrPropAbsent, which is an observation, not a failure.
var errPropAbsent = errors.New("x11 property not set")

// propUint32s decodes a format-32 property (window ids, atom lists, WM_STATE).
func propUint32s(p propValue) []uint32 {
	if p.Format != 32 {
		return nil
	}
	out := make([]uint32, 0, len(p.Data)/4)
	for i := 0; i+4 <= len(p.Data); i += 4 {
		out = append(out, binary.LittleEndian.Uint32(p.Data[i:i+4]))
	}
	return out
}

// propText decodes a text property without truncation. §7 forbids shortening a
// title: a page title is the signal, and a caller that trims it answers a
// different question than the one asked.
func propText(p propValue) string {
	switch p.Format {
	case 8:
		return strings.TrimRight(string(p.Data), "\x00")
	case 16:
		rs := make([]rune, 0, len(p.Data)/2)
		for i := 0; i+2 <= len(p.Data); i += 2 {
			rs = append(rs, rune(binary.LittleEndian.Uint16(p.Data[i:i+2])))
		}
		return strings.TrimRight(string(rs), "\x00")
	}
	return ""
}

// attrs is GetWindowAttributes: what "alive", "mapped" and "override-redirect"
// mean for a window id.
type x11Attrs struct {
	MapState         byte // 0 unmapped, 1 unviewable, 2 viewable
	OverrideRedirect bool
}

const x11MapStateViewable = 2

// geometry is GetGeometry in the window's own parent coordinates.
type x11Geometry struct {
	X, Y, W, H int
}
