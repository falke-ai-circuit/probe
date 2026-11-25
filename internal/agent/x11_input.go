//go:build linux

package agent

// P14-02 (contract P10-07 v3.1 §9.2, §9.3, §9.4), X11 wire backend.
//
// The events are emitted with XTEST on the SAME connection §9.1 opened to
// measure the rect and run the hit-test. That is deliberate: a click planned on
// one view of the display and emitted through another has two chances to
// disagree about where the window is, and no way to notice.
//
// XTestFakeInput has no reply. Every injection is therefore followed by ONE
// GetInputFocus, which both flushes and reads back: a queued X error (BadValue
// on a detail byte, BadWindow on a stale root) surfaces there instead of
// desynchronising the next reply read.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	x11GetKeyboardMapping    = 101
	x11ChangeKeyboardMapping = 100
	x11GetInputFocus         = 43
	x11QueryExtension        = 98

	xtestFakeInput = 2

	// XTest event types, as the wire spells them.
	xtestKeyPress      = 2
	xtestKeyRelease    = 3
	xtestButtonPress   = 4
	xtestButtonRelease = 5
	xtestMotion        = 6
)

// x11Button maps the primitive's button names onto X button numbers. `4`/`5`
// are the wheel, which is why scroll goes through Button with those names.
func x11Button(name string) (byte, error) {
	switch name {
	case "", "left":
		return 1, nil
	case "middle":
		return 2, nil
	case "right":
		return 3, nil
	case "4", "5", "6", "7":
		return name[0] - '0', nil
	}
	return 0, fmt.Errorf("unknown button %q", name)
}

// xtestMajorOp returns the XTEST major opcode, querying the extension once.
// A server without XTEST cannot fake input at all, and the caller answers
// NO_BACKEND rather than falling back to a shell wrapper with different timing.
func (x *x11Conn) xtestMajorOp() (byte, error) {
	if x.xtest != 0 {
		return x.xtest, nil
	}
	name := "XTEST"
	n := len(name)
	pad := (4 - n%4) % 4
	req := make([]byte, 8+n+pad)
	req[0] = x11QueryExtension
	binary.LittleEndian.PutUint16(req[2:4], uint16((8+n+pad)/4))
	binary.LittleEndian.PutUint16(req[4:6], uint16(n))
	copy(req[8:], name)
	reply, err := x.do(req, "XTEST QueryExtension")
	if err != nil {
		return 0, err
	}
	if reply[8] == 0 {
		return 0, errXTestMissing
	}
	x.xtest = reply[9]
	return x.xtest, nil
}

// sync writes GetInputFocus and reads until its reply, collecting any error
// packets queued by the fake-input writes in front of it. The last queued error
// is returned: an injection that the server rejected must not look like one
// that landed.
func (x *x11Conn) sync() error {
	req := make([]byte, 4)
	req[0] = x11GetInputFocus
	binary.LittleEndian.PutUint16(req[2:4], 1)
	x.seq++
	if _, err := x.c.Write(req); err != nil {
		return err
	}
	var firstErr error
	pkt := make([]byte, 32)
	for i := 0; i < 8; i++ {
		if _, err := io.ReadFull(x.c, pkt); err != nil {
			return err
		}
		switch {
		case pkt[0] == 0: // error packet
			if firstErr == nil {
				firstErr = &x11Error{Code: pkt[1], Want: "fake input"}
			}
		case pkt[0] == 1: // the GetInputFocus reply
			return firstErr
		case pkt[0] >= 2 && pkt[0] <= 35:
			// An event, not a reply: ChangeKeyboardMapping makes the server
			// broadcast MappingNotify to every client, selected or not. It is
			// not an answer to this request, so it is skipped — treating it as
			// one desynchronises the stream.
			continue
		default:
			return fmt.Errorf("x11: unexpected packet type %d while syncing fake input", pkt[0])
		}
	}
	return errors.New("x11: no reply to GetInputFocus after 8 packets")
}

// fakeInput writes one XTestFakeInput request. Coordinates are root-space and
// only meaningful for motion/button events; key events carry the keycode in
// `detail`.
func (x *x11Conn) fakeInput(typ, detail byte, px, py int) error {
	op, err := x.xtestMajorOp()
	if err != nil {
		return err
	}
	// 9 units (36 bytes), and minor opcode 2 is FakeInput: measured against
	// Xvfb — 8 units answers BadLength, 9 is accepted. The extra unit is the
	// trailing pad after deviceid.
	req := make([]byte, 36)
	req[0] = op
	req[1] = xtestFakeInput
	binary.LittleEndian.PutUint16(req[2:4], 9)
	req[4] = typ
	req[5] = detail
	// time = CurrentTime (0)
	binary.LittleEndian.PutUint32(req[12:16], x.root)
	binary.LittleEndian.PutUint16(req[16:18], uint16(int16(px)))
	binary.LittleEndian.PutUint16(req[18:20], uint16(int16(py)))
	req[28] = 0 // deviceid: the current core pointer/keyboard
	x.seq++
	if _, err := x.c.Write(req); err != nil {
		return err
	}
	return nil
}

// Pointer is XTest motion to root coordinates.
func (x *x11Conn) Pointer(px, py int) error {
	if err := x.fakeInput(xtestMotion, 0, px, py); err != nil {
		return err
	}
	return x.sync()
}

// Button emits one press or release. A wheel notch is a press+release of
// button 4 (up) or 5 (down) — the mapping §9.3 states.
func (x *x11Conn) Button(name string, press bool) error {
	b, err := x11Button(name)
	if err != nil {
		return err
	}
	t := byte(xtestButtonRelease)
	if press {
		t = xtestButtonPress
	}
	if err := x.fakeInput(t, b, 0, 0); err != nil {
		return err
	}
	return x.sync()
}

// Key emits one press or release of a keycode.
func (x *x11Conn) Key(keycode byte, press bool) error {
	t := byte(xtestKeyRelease)
	if press {
		t = xtestKeyPress
	}
	if err := x.fakeInput(t, keycode, 0, 0); err != nil {
		return err
	}
	return x.sync()
}

// readReply reads one 32-byte reply, skipping event packets. MappingNotify is
// broadcast to EVERY client when a keymap changes, selected or not, so a reply
// reader that treats any non-1 packet as fatal desynchronises the moment §9.4
// remaps a keycode.
func (x *x11Conn) readReply(want string) ([]byte, error) {
	x.seq++
	// Caller has already written the request; this only reads.
	for i := 0; i < 16; i++ {
		reply := make([]byte, 32)
		if _, err := io.ReadFull(x.c, reply); err != nil {
			return nil, err
		}
		switch {
		case reply[0] == 0:
			return nil, &x11Error{Code: reply[1], Want: want}
		case reply[0] == 1:
			return reply, nil
		case reply[0] >= 2 && reply[0] <= 35:
			continue
		default:
			return nil, fmt.Errorf("x11: unexpected packet type %d for %s", reply[0], want)
		}
	}
	return nil, fmt.Errorf("x11: no reply to %s after 16 packets", want)
}

// keymapFor reads the keysyms bound to count keycodes starting at first. Each
// keycode gets keysymsPerKeycode entries; 0 is NoSymbol.
func (x *x11Conn) keymapFor(first, count byte) (uint32, [][]uint32, error) {
	req := make([]byte, 8)
	req[0] = x11GetKeyboardMapping
	binary.LittleEndian.PutUint16(req[2:4], 2)
	req[4] = first
	req[5] = count
	if _, werr := x.c.Write(req); werr != nil {
		return 0, nil, werr
	}
	reply, err := x.readReply("GetKeyboardMapping")
	if err != nil {
		return 0, nil, err
	}
	per := int(reply[1])
	total := int(count) * per
	need := total * 4
	buf := make([]byte, need+((4-need%4)%4))
	if need > 0 {
		if _, err := io.ReadFull(x.c, buf); err != nil {
			return 0, nil, err
		}
	}
	out := make([][]uint32, count)
	for i := 0; i < int(count); i++ {
		syms := make([]uint32, per)
		for j := 0; j < per; j++ {
			off := (i*per + j) * 4
			syms[j] = binary.LittleEndian.Uint32(buf[off : off+4])
		}
		out[i] = syms
	}
	return uint32(per), out, nil
}

// SpareKeycode finds a keycode with every keysym unbound. §9.4 says "spare
// discovered by scanning the map", which is exactly this: the map is read, not
// assumed, because a keycode that is free on one layout is not free on another.
func (x *x11Conn) SpareKeycode() (byte, error) {
	if x.minKeycode == 0 || x.maxKeycode < x.minKeycode {
		return 0, errors.New("the display did not report a keycode range")
	}
	count := byte(x.maxKeycode - x.minKeycode + 1)
	per, rows, err := x.keymapFor(x.minKeycode, count)
	if err != nil {
		return 0, err
	}
	if per == 0 {
		return 0, errNoSpareKeycode
	}
	for i := int(count) - 1; i >= 0; i-- {
		free := true
		for _, sym := range rows[i] {
			if sym != 0 {
				free = false
				break
			}
		}
		if free {
			return x.minKeycode + byte(i), nil
		}
	}
	return 0, errNoSpareKeycode
}

// RemapKeycode binds one keysym to one keycode, remembering the symbols it
// replaced so RestoreKeymap can put them back.
func (x *x11Conn) RemapKeycode(keycode byte, sym uint32) error {
	if x.saved == nil {
		x.saved = map[byte][]uint32{}
	}
	if _, seen := x.saved[keycode]; !seen {
		_, rows, err := x.keymapFor(keycode, 1)
		if err != nil {
			return err
		}
		if len(rows) == 1 {
			x.saved[keycode] = rows[0]
		}
	}
	// The wire shape libX11 emits, and the only one this server accepts:
	// byte 1 = number of keycodes, byte 5 = keysyms-per-keycode. A zero in
	// byte 5 is answered with BadLength (measured both ways against Xvfb).
	req := make([]byte, 12)
	req[0] = x11ChangeKeyboardMapping
	req[1] = 1 // one keycode
	binary.LittleEndian.PutUint16(req[2:4], 3)
	req[4] = keycode
	req[5] = 1 // one keysym per keycode
	binary.LittleEndian.PutUint32(req[8:12], sym)
	x.seq++
	if _, err := x.c.Write(req); err != nil {
		return err
	}
	return x.sync()
}

// RestoreKeymap puts every borrowed keycode back to the symbols it had before
// the string. It is idempotent: a connection that borrowed nothing writes
// nothing.
func (x *x11Conn) RestoreKeymap() error {
	var firstErr error
	for kc, syms := range x.saved {
		if len(syms) == 0 {
			continue
		}
		req := make([]byte, 8+4*len(syms))
		req[0] = x11ChangeKeyboardMapping
		req[1] = 1 // one keycode
		binary.LittleEndian.PutUint16(req[2:4], uint16(len(req)/4))
		req[4] = kc
		req[5] = byte(len(syms)) // keysyms-per-keycode, as libX11 sends it
		for i, sym := range syms {
			binary.LittleEndian.PutUint32(req[8+i*4:12+i*4], sym)
		}
		x.seq++
		if _, err := x.c.Write(req); err != nil {
			return err
		}
		if err := x.sync(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	x.saved = nil
	return firstErr
}
