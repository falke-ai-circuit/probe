//go:build linux

package agent

// P14-05 (§9.1) against a REAL X server: the wire assertions the seam tests
// could not make. The seam never decodes a reply, which is why PointIn shipped
// reading the reply's length word as its child (always 0) and Attributes shipped
// leaving 12 bytes on the connection. Both defects are invisible to a fake
// display and obvious to a bare Xvfb, so these tests talk to the server.
//
// No window manager is involved: the hit-test is a TranslateCoordinates
// descent, which is a server question, not a WM one.

import (
	"encoding/binary"
	"io"
	"testing"
)

// p1405Window creates and maps one override-redirect InputOutput window under
// the root at (wx,wy) with an explicit resource id, because the hit-test needs
// two windows on the same connection and x11TestWindow always reuses idBase|1.
// The GetGeometry read afterwards is the sync: it surfaces a queued X error.
func p1405Window(t *testing.T, x *x11Conn, id uint32, wx, wy, w, h int, bg uint32) uint32 {
	t.Helper()
	req := make([]byte, 40) // 10 words: header + 8 fixed words + 2 values
	req[0] = 1              // CreateWindow
	req[1] = 0              // depth = CopyFromParent
	binary.LittleEndian.PutUint16(req[2:4], 10)
	binary.LittleEndian.PutUint32(req[4:8], id)
	binary.LittleEndian.PutUint32(req[8:12], x.root)
	binary.LittleEndian.PutUint16(req[12:14], uint16(int16(wx)))
	binary.LittleEndian.PutUint16(req[14:16], uint16(int16(wy)))
	binary.LittleEndian.PutUint16(req[16:18], uint16(w))
	binary.LittleEndian.PutUint16(req[18:20], uint16(h))
	binary.LittleEndian.PutUint16(req[20:22], 0)         // border width
	binary.LittleEndian.PutUint16(req[22:24], 1)         // InputOutput
	binary.LittleEndian.PutUint32(req[24:28], 0)         // CopyFromParent visual
	binary.LittleEndian.PutUint32(req[28:32], 0x2|0x200) // CWBackPixel | CWOverrideRedirect
	binary.LittleEndian.PutUint32(req[32:36], bg)
	binary.LittleEndian.PutUint32(req[36:40], 1)
	if _, err := x.c.Write(req); err != nil {
		t.Fatalf("CreateWindow %#x: %v", id, err)
	}

	mapReq := make([]byte, 8)
	mapReq[0] = 8 // MapWindow
	binary.LittleEndian.PutUint16(mapReq[2:4], 2)
	binary.LittleEndian.PutUint32(mapReq[4:8], id)
	if _, err := x.c.Write(mapReq); err != nil {
		t.Fatalf("MapWindow %#x: %v", id, err)
	}
	sync := make([]byte, 4)
	sync[0] = 43 // GetInputFocus
	binary.LittleEndian.PutUint16(sync[2:4], 1)
	if _, err := x.c.Write(sync); err != nil {
		t.Fatalf("sync for %#x: %v", id, err)
	}
	hdr := make([]byte, 32)
	if _, err := io.ReadFull(x.c, hdr); err != nil {
		t.Fatalf("sync reply for %#x: %v", id, err)
	}
	if hdr[0] == 0 {
		t.Fatalf("X error %d creating/mapping window %#x", hdr[1], id)
	}
	if g, err := x.Geometry(id); err != nil || g.W != w || g.H != h {
		t.Fatalf("window %#x measured %+v (err %v), want %dx%d", id, g, err, w, h)
	}
	return id
}

// TestP1405WirePointInNamesChildAndDeltas is the load-bearing one: PointIn must
// read the child from reply[8:12] and the deltas from [12:14]/[14:16]. With the
// pre-fix offsets the child comes from the length word (0) and the deltas from
// the child word, so both assertions below fail on the shipped code.
func TestP1405WirePointInNamesChildAndDeltas(t *testing.T) {
	num := startBareXvfb(t)
	x, err := x11Dial(num)
	if err != nil {
		t.Fatalf("dial :%d: %v", num, err)
	}
	defer x.Close()

	win := p1405Window(t, x, x.idBase|1, 10, 20, 100, 80, 0x3366cc)

	// Root-space question: the point (15,25) is inside the window, so the child
	// of the root containing it must be that window and the delta is untouched.
	dx, dy, child, err := x.PointIn(x.Root(), 15, 25)
	if err != nil {
		t.Fatalf("PointIn(root,15,25): %v", err)
	}
	if child != win {
		t.Errorf("PointIn(root,15,25) child = %#x, want the test window %#x", child, win)
	}
	if dx != 15 || dy != 25 {
		t.Errorf("PointIn(root,15,25) deltas = %d,%d, want 15,25 (the child word is not a delta)", dx, dy)
	}

	// Window-space question: translating the same root point into the window
	// gives its offset inside it, and a leaf has no containing child.
	dx, dy, child, err = x.PointIn(win, 15, 25)
	if err != nil {
		t.Fatalf("PointIn(%#x,15,25): %v", win, err)
	}
	if child != 0 {
		t.Errorf("PointIn(window,15,25) child = %#x, want 0 for a leaf window", child)
	}
	if dx != 5 || dy != 5 {
		t.Errorf("PointIn(%#x,15,25) deltas = %d,%d, want 5,5 (15-10, 25-20)", win, dx, dy)
	}
}

// TestP1405WireAttributesDoesNotDesync reads a GetWindowAttributes reply (44
// bytes: 32 header + 12 declared by its length word = 3) and then issues a
// second request on the same connection. Leaving the 12 bytes unread makes the
// following read start mid-reply: measured on the shipped code as
// geometry#1 ok / attributes ok / geometry#2 "x11 error 0".
func TestP1405WireAttributesDoesNotDesync(t *testing.T) {
	num := startBareXvfb(t)
	x, err := x11Dial(num)
	if err != nil {
		t.Fatalf("dial :%d: %v", num, err)
	}
	defer x.Close()

	win := p1405Window(t, x, x.idBase|1, 10, 20, 100, 80, 0x3366cc)

	if g, err := x.Geometry(win); err != nil || g.W != 100 || g.H != 80 {
		t.Fatalf("geometry#1 = %+v (err %v), want 100x80", g, err)
	}
	attrs, err := x.Attributes(win)
	if err != nil {
		t.Fatalf("attributes: %v", err)
	}
	if attrs.MapState != x11MapStateViewable {
		t.Errorf("attributes MapState = %d, want viewable (%d)", attrs.MapState, x11MapStateViewable)
	}
	if !attrs.OverrideRedirect {
		t.Errorf("attributes lost OverrideRedirect for an override-redirect window")
	}
	g, err := x.Geometry(win)
	if err != nil {
		t.Fatalf("geometry#2 after attributes: %v (the connection desynchronised)", err)
	}
	if g.W != 100 || g.H != 80 {
		t.Errorf("geometry#2 = %+v after attributes, want 100x80", g)
	}
	// A property read is the third reply on the connection: it exercises the
	// property tail drain too, which do() now supplies instead of the wire.
	if _, err := x.Property(x.Root(), "_NET_CLIENT_LIST"); err != nil && err != errPropAbsent {
		t.Errorf("property read after attributes: %v", err)
	}
}

// TestP1405WireOccludingWindowDescends is §9.1's end-to-end: the descent must
// leave the root, the requested window on top must not be refused, and a
// covering child must be named as the hit.
func TestP1405WireOccludingWindowDescends(t *testing.T) {
	num := startBareXvfb(t)
	x, err := x11Dial(num)
	if err != nil {
		t.Fatalf("dial :%d: %v", num, err)
	}
	defer x.Close()

	lower := p1405Window(t, x, x.idBase|1, 10, 20, 100, 80, 0x3366cc)
	upper := p1405Window(t, x, x.idBase|2, 40, 40, 40, 40, 0xcc6633)

	// The requested window is the only thing at this point: not occluded, and
	// the path must show the descent happened (len > 1) rather than stopping at
	// the root, which is what the pre-fix child==0 answer produced.
	hit, path, err := occludingWindow(x, upper, 60, 60)
	if err != nil {
		t.Fatalf("occludingWindow(upper,60,60): %v", err)
	}
	if hit != 0 {
		t.Errorf("occludingWindow(upper,60,60) hit = %#x, want 0 (the requested window is on top)", hit)
	}
	if len(path) < 2 {
		t.Fatalf("descent path = %v, want the root plus at least one child", path)
	}
	if path[len(path)-1] != upper {
		t.Errorf("descent path = %v, want it to end at the requested window %#x", path, upper)
	}

	// A point covered by the upper window: the hit is that window, not the root.
	hit, path, err = occludingWindow(x, lower, 60, 60)
	if err != nil {
		t.Fatalf("occludingWindow(lower,60,60): %v", err)
	}
	if hit != upper {
		t.Errorf("occludingWindow(lower,60,60) hit = %#x, want the covering window %#x (path %v)", hit, upper, path)
	}
	if len(path) != 2 || path[0] != x.Root() || path[1] != upper {
		t.Errorf("descent path = %v, want [%#x %#x]", path, x.Root(), upper)
	}
}
