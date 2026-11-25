//go:build linux

package agent

// P14-02 (§9.3, §9.4) against a REAL X server. Xvfb on this host has no window
// manager, so the window-scoped paths stay UNPROVEN-ON-HOST — but the keymap
// primitive and the fake-input wire need no WM at all: they are server requests
// and the server's own answers are the evidence.
//
// The load-bearing assertion is the READ-BACK: the borrowed keycode is read
// before the string and again after it, and the two maps must be identical. A
// "restore" that wrote the wrong symbols would leave the display visibly
// broken for every app and still pass an assertion that only counted calls.

import (
	"testing"
)

func TestP1402X11WireKeymapIsRestored(t *testing.T) {
	num := startBareXvfb(t)
	x, err := x11Dial(num)
	if err != nil {
		t.Fatalf("dial :%d: %v", num, err)
	}
	defer x.Close()

	if _, err := x.xtestMajorOp(); err != nil {
		t.Skipf("XTEST is not exposed by this X server, so §9.4 cannot be exercised here: %v", err)
	}

	spare, err := x.SpareKeycode()
	if err != nil {
		t.Fatalf("no spare keycode on a bare Xvfb: %v", err)
	}
	_, before, err := x.keymapFor(spare, 1)
	if err != nil {
		t.Fatalf("read keymap before: %v", err)
	}

	syms, kc, perr := planText(x, "aé日")
	if perr != nil {
		t.Fatalf("planText: %v", perr)
	}
	if kc != spare {
		t.Fatalf("planText borrowed keycode %d, SpareKeycode said %d", kc, spare)
	}
	if err := emitText(x, syms, kc); err != nil {
		t.Fatalf("emitText through the wire: %v", err)
	}

	_, after, err := x.keymapFor(spare, 1)
	if err != nil {
		t.Fatalf("read keymap after: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("keymap width changed: before %v after %v", before, after)
	}
	for i := range before {
		if len(before[i]) != len(after[i]) {
			t.Fatalf("keymap[%d] width changed: before %v after %v", i, before[i], after[i])
		}
		for j := range before[i] {
			if before[i][j] != after[i][j] {
				t.Fatalf("keymap NOT restored: keycode %d keysym[%d] was 0x%x, is 0x%x", spare, j, before[i][j], after[i][j])
			}
		}
	}
	if len(x.saved) != 0 {
		t.Fatalf("connection still remembers borrowed keycodes after restore: %v", x.saved)
	}
}

// TestP1402X11WireFakeInputAccepted proves the X server ACCEPTS the wheel and
// key events (§9.3's button 4/5, §9.4's keycode press) — a BadValue or BadWindow
// surfaces through the sync after each event. It does NOT prove what an
// application received: there is no window manager, no focused surface and no
// event receiver on this substrate, so the EFFECT is UNPROVEN-ON-HOST.
func TestP1402X11WireFakeInputAccepted(t *testing.T) {
	num := startBareXvfb(t)
	x, err := x11Dial(num)
	if err != nil {
		t.Fatalf("dial :%d: %v", num, err)
	}
	defer x.Close()
	if _, err := x.xtestMajorOp(); err != nil {
		t.Skipf("XTEST is not exposed by this X server: %v", err)
	}
	if err := x.Pointer(1, 1); err != nil {
		t.Fatalf("XTest motion rejected: %v", err)
	}
	for _, b := range []string{"4", "5"} {
		if err := x.Button(b, true); err != nil {
			t.Fatalf("button %s press rejected: %v", b, err)
		}
		if err := x.Button(b, false); err != nil {
			t.Fatalf("button %s release rejected: %v", b, err)
		}
	}
	spare, err := x.SpareKeycode()
	if err != nil {
		t.Fatalf("no spare keycode: %v", err)
	}
	if err := x.Key(spare, true); err != nil {
		t.Fatalf("key press rejected: %v", err)
	}
	if err := x.Key(spare, false); err != nil {
		t.Fatalf("key release rejected: %v", err)
	}
}
