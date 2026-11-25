//go:build linux

package agent

// P13-03 (contract P10-07 v3.1 §7/§7.1): a minimal X11 protocol client.
//
// The tree primitive needs property reads and geometry that Xlib exposes but
// no CLI wrapper guarantees: `xprop`/`xwininfo` are absent from the measured
// headless substrate (Xvfb with no window manager), and shelling out to them
// would put a subprocess per window in the enumeration path. This file speaks
// the wire protocol directly over the display socket, so the backend depends
// on nothing but the X server itself.
//
// Scope is deliberately small: connection setup, InternAtom, GetProperty,
// GetWindowAttributes, GetGeometry and TranslateCoordinates. No events are
// selected, no resources are created, and every request is read back before
// the next one is sent, so a BadWindow error surfaces as an error rather than
// desynchronising the stream.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// x11Conn is one synchronous connection to a display.
type x11Conn struct {
	c         net.Conn
	seq       uint16
	root      uint32
	width     int
	height    int
	atoms     map[string]uint32
	atomNames map[uint32]string
}

// x11Dial connects to display :num over the abstract or filesystem socket and
// performs the connection handshake. num is the X display number, not a path.
func x11Dial(num int) (*x11Conn, error) {
	name := fmt.Sprintf("/tmp/.X11-unix/X%d", num)
	c, err := net.DialTimeout("unix", name, 2*time.Second)
	if err != nil {
		// Abstract namespace socket: some builds do not create the file.
		c, err = net.DialTimeout("unix", fmt.Sprintf("@/tmp/.X11-unix/X%d", num), 2*time.Second)
		if err != nil {
			return nil, err
		}
	}
	x := &x11Conn{c: c, atoms: map[string]uint32{}, atomNames: map[uint32]string{}}
	if err := x.handshake(num); err != nil {
		c.Close()
		return nil, err
	}
	return x, nil
}

func (x *x11Conn) Close() error { return x.c.Close() }

// handshake sends the connection setup request and parses the reply. Auth is
// MIT-MAGIC-COOKIE-1 from $XAUTHORITY when the server demands one; an X server
// with access control disabled accepts the empty block, which is what the
// Xvfb substrate is started with.
func (x *x11Conn) handshake(num int) error {
	name, data := xauthorityCookie(num)
	req := make([]byte, 12+len(name)+len(data))
	req[0] = 'l' // little-endian
	binary.LittleEndian.PutUint16(req[2:4], 11)
	binary.LittleEndian.PutUint16(req[4:6], 0)
	binary.LittleEndian.PutUint16(req[6:8], uint16(len(name)))
	binary.LittleEndian.PutUint16(req[8:10], uint16(len(data)))
	copy(req[12:], name)
	copy(req[12+len(name):], data)
	if _, err := x.c.Write(req); err != nil {
		return err
	}
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(x.c, hdr); err != nil {
		return err
	}
	if hdr[0] != 1 {
		// Failure reply: reason length then the reason bytes.
		n := int(hdr[1])
		reason := make([]byte, n)
		io.ReadFull(x.c, reason)
		return fmt.Errorf("x11 setup refused: %s", strings.TrimSpace(string(reason)))
	}
	extra := int(binary.LittleEndian.Uint16(hdr[6:8])) * 4
	body := make([]byte, extra)
	if _, err := io.ReadFull(x.c, body); err != nil {
		return err
	}
	if len(body) < 24 {
		return errors.New("x11 setup reply truncated")
	}
	vlen := int(binary.LittleEndian.Uint16(body[16:18]))
	numScreens := int(body[20])
	numFormats := int(body[21])
	// vendor starts at 32: release(4) base(4) mask(4) motion(4) vendorlen(2)
	// maxreq(2) screens(1) formats(1) orders(4) keycodes(4) pad(4).
	off := 32 + ((vlen + 3) &^ 3) + numFormats*8
	if numScreens == 0 || off+24 > len(body) {
		return errors.New("x11 setup reply has no screen")
	}
	x.root = binary.LittleEndian.Uint32(body[off : off+4])
	x.width = int(binary.LittleEndian.Uint16(body[off+20 : off+22]))
	x.height = int(binary.LittleEndian.Uint16(body[off+22 : off+24]))
	return nil
}

// xauthorityCookie reads the first MIT-MAGIC-COOKIE-1 entry for display num
// from the X authority file. A missing file is not an error: the caller may be
// talking to a server with access control disabled.
func xauthorityCookie(num int) (string, []byte) {
	path := os.Getenv("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", nil
	}
	numStr := fmt.Sprintf("%d", num)
	pos := 0
	readField := func() ([]byte, bool) {
		if pos+2 > len(raw) {
			return nil, false
		}
		n := int(binary.BigEndian.Uint16(raw[pos : pos+2]))
		pos += 2
		if pos+n > len(raw) {
			return nil, false
		}
		v := raw[pos : pos+n]
		pos += n
		return v, true
	}
	for pos < len(raw) {
		if pos+2 > len(raw) {
			return "", nil
		}
		family := binary.BigEndian.Uint16(raw[pos : pos+2])
		pos += 2
		addr, ok := readField()
		if !ok {
			return "", nil
		}
		number, ok := readField()
		if !ok {
			return "", nil
		}
		name, ok := readField()
		if !ok {
			return "", nil
		}
		value, ok := readField()
		if !ok {
			return "", nil
		}
		// FamilyLocal is 256 and FamilyWild is 65535; both match any host.
		if family != 256 && family != 65535 {
			continue
		}
		_ = addr
		if string(number) != numStr || string(name) != "MIT-MAGIC-COOKIE-1" {
			continue
		}
		return string(name), value
	}
	return "", nil
}

// do sends one request and returns the 32-byte reply body. An X error packet
// (reply byte 0) is returned as *x11Error: it is the platform answering
// "that window is gone", not a transport failure.
func (x *x11Conn) do(req []byte, want string) ([]byte, error) {
	x.seq++
	if _, err := x.c.Write(req); err != nil {
		return nil, err
	}
	reply := make([]byte, 32)
	if _, err := io.ReadFull(x.c, reply); err != nil {
		return nil, err
	}
	if reply[0] == 0 {
		return nil, &x11Error{Code: reply[1], Want: want}
	}
	if reply[0] != 1 {
		return nil, fmt.Errorf("x11: unexpected reply type %d for %s", reply[0], want)
	}
	return reply, nil
}

// atom interns a property/type name, cached per connection.
func (x *x11Conn) atom(name string) (uint32, error) {
	if a, ok := x.atoms[name]; ok {
		return a, nil
	}
	n := len(name)
	pad := (4 - n%4) % 4
	req := make([]byte, 8+n+pad)
	req[0] = 16 // InternAtom
	binary.LittleEndian.PutUint16(req[2:4], uint16((8+n+pad)/4))
	binary.LittleEndian.PutUint16(req[4:6], uint16(n))
	copy(req[8:], name)
	reply, err := x.do(req, name)
	if err != nil {
		return 0, err
	}
	a := binary.LittleEndian.Uint32(reply[8:12])
	x.atoms[name] = a
	return a, nil
}

// translateToRoot maps a window's origin into root (virtual screen) pixels.
// §7's rect is in the virtual-screen origin, so negative coordinates are a
// legitimate answer for a display placed above or left of the origin.
func (x *x11Conn) translateToRoot(win uint32) (int, int, error) {
	req := make([]byte, 16)
	req[0] = 40 // TranslateCoordinates
	binary.LittleEndian.PutUint16(req[2:4], 4)
	binary.LittleEndian.PutUint32(req[4:8], win)
	binary.LittleEndian.PutUint32(req[8:12], x.root)
	reply, err := x.do(req, "translate coordinates")
	if err != nil {
		return 0, 0, err
	}
	return int(int16(binary.LittleEndian.Uint16(reply[12:14]))),
		int(int16(binary.LittleEndian.Uint16(reply[14:16]))), nil
}

func (x *x11Conn) property(win uint32, name string) (propValue, error) {
	propAtom, err := x.atom(name)
	if err != nil {
		return propValue{}, err
	}
	req := make([]byte, 24)
	req[0] = 20 // GetProperty
	binary.LittleEndian.PutUint16(req[2:4], 6)
	binary.LittleEndian.PutUint32(req[4:8], win)
	binary.LittleEndian.PutUint32(req[8:12], propAtom)
	// type = AnyPropertyType (0), offset 0, length 1<<16 items.
	binary.LittleEndian.PutUint32(req[20:24], 1<<16)
	reply, err := x.do(req, name)
	if err != nil {
		return propValue{}, err
	}
	format := reply[1]
	typ := binary.LittleEndian.Uint32(reply[8:12])
	nItems := int(binary.LittleEndian.Uint32(reply[16:20]))
	data := make([]byte, 0)
	if nItems > 0 && typ != 0 {
		unit := 1
		switch format {
		case 16:
			unit = 2
		case 32:
			unit = 4
		}
		need := nItems * unit
		buf := make([]byte, need+((4-need%4)%4))
		if _, err := io.ReadFull(x.c, buf); err != nil {
			return propValue{}, err
		}
		data = buf[:need]
	}
	if typ == 0 {
		return propValue{}, errPropAbsent
	}
	return propValue{Format: format, Type: typ, Data: data}, nil
}

func (x *x11Conn) attributes(win uint32) (x11Attrs, error) {
	req := make([]byte, 8)
	req[0] = 3 // GetWindowAttributes
	binary.LittleEndian.PutUint16(req[2:4], 2)
	binary.LittleEndian.PutUint32(req[4:8], win)
	reply, err := x.do(req, "window attributes")
	if err != nil {
		return x11Attrs{}, err
	}
	return x11Attrs{MapState: reply[26], OverrideRedirect: reply[27] != 0}, nil
}

func (x *x11Conn) geometry(win uint32) (x11Geometry, error) {
	req := make([]byte, 8)
	req[0] = 14 // GetGeometry
	binary.LittleEndian.PutUint16(req[2:4], 2)
	binary.LittleEndian.PutUint32(req[4:8], win)
	reply, err := x.do(req, "geometry")
	if err != nil {
		return x11Geometry{}, err
	}
	return x11Geometry{
		X: int(int16(binary.LittleEndian.Uint16(reply[12:14]))),
		Y: int(int16(binary.LittleEndian.Uint16(reply[14:16]))),
		W: int(binary.LittleEndian.Uint16(reply[16:18])),
		H: int(binary.LittleEndian.Uint16(reply[18:20])),
	}, nil
}

// --- treeDisplay implementation -------------------------------------------
//
// The seam methods are exported so the untagged tree builder can depend on an
// interface instead of this concrete connection: the tests drive the same
// interface with synthetic EWMH property data.

// Root is the screen's root window: the window EWMH properties live on.
func (x *x11Conn) Root() uint32 { return x.root }

// Pixels is the screen size from connection setup: the display's own answer,
// with no xdpyinfo subprocess.
func (x *x11Conn) Pixels() (int, int) { return x.width, x.height }

func (x *x11Conn) Property(win uint32, name string) (propValue, error) {
	return x.property(win, name)
}

func (x *x11Conn) Attributes(win uint32) (x11Attrs, error) { return x.attributes(win) }

func (x *x11Conn) Geometry(win uint32) (x11Geometry, error) { return x.geometry(win) }

func (x *x11Conn) TranslateToRoot(win uint32) (int, int, error) { return x.translateToRoot(win) }

// AtomName resolves a type atom back to its name so the tree builder can map
// _NET_WM_WINDOW_TYPE values onto §7's closed kind enum.
func (x *x11Conn) AtomName(id uint32) (string, error) {
	if name, ok := x.atomNames[id]; ok {
		return name, nil
	}
	req := make([]byte, 8)
	req[0] = 17 // GetAtomName
	binary.LittleEndian.PutUint16(req[2:4], 2)
	binary.LittleEndian.PutUint32(req[4:8], id)
	if _, err := x.c.Write(req); err != nil {
		return "", err
	}
	reply := make([]byte, 32)
	if _, err := io.ReadFull(x.c, reply); err != nil {
		return "", err
	}
	if reply[0] == 0 {
		return "", &x11Error{Code: reply[1], Want: "atom name"}
	}
	n := int(binary.LittleEndian.Uint16(reply[8:10]))
	buf := make([]byte, n+((4-n%4)%4))
	if _, err := io.ReadFull(x.c, buf); err != nil {
		return "", err
	}
	name := string(buf[:n])
	x.atomNames[id] = name
	return name, nil
}
