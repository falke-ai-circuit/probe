//go:build linux

package agent

// P14-01 (§9.1): the two X calls the hit-test needs, added to the same
// connection the tree uses.
//
//   - PointIn is TranslateCoordinates(root → win) at the caller's point. Its
//     reply names the child of `win` that contains the point, which is exactly
//     the descent step a hit-test needs for an ARBITRARY point. XQueryPointer
//     cannot answer that question — it reports where the pointer is, and warping
//     the operator's pointer to ask about somewhere else would be a side effect
//     a hit-test must not have.
//   - RequestForeground is the §8 X11 foreground: an EWMH `_NET_ACTIVE_WINDOW`
//     client message with source=2. It sends; it does not verify. The caller
//     re-reads the root property, because a WM that ignores the message is a
//     denial, not a success.

import (
	"encoding/binary"
	"fmt"
)

// PointIn implements windowProbe: the point in win's coordinate space plus the
// child of win containing it (0 = none).
func (x *x11Conn) PointIn(win uint32, px, py int) (int32, int32, uint32, error) {
	req := make([]byte, 16)
	req[0] = 40 // TranslateCoordinates
	binary.LittleEndian.PutUint16(req[2:4], 4)
	binary.LittleEndian.PutUint32(req[4:8], x.root) // src: the point is in root space
	binary.LittleEndian.PutUint32(req[8:12], win)   // dst
	binary.LittleEndian.PutUint16(req[12:14], uint16(int16(px)))
	binary.LittleEndian.PutUint16(req[14:16], uint16(int16(py)))
	reply, err := x.do(req, "translate coordinates")
	if err != nil {
		return 0, 0, 0, err
	}
	if reply[1] != 1 {
		return 0, 0, 0, nil // different screens: nothing of interest here
	}
	// Reply layout: [0]=1, [1]=same-screen, [4:8]=length (0 here), [8:12]=child
	// of dst containing the point, [12:14]/[14:16]=x/y in dst's space (INT16).
	// Reading the child out of [4:8] reads the length word, which is always 0.
	child := binary.LittleEndian.Uint32(reply[8:12])
	dx := int32(int16(binary.LittleEndian.Uint16(reply[12:14])))
	dy := int32(int16(binary.LittleEndian.Uint16(reply[14:16])))
	return dx, dy, child, nil
}

// RequestForeground implements windowProbe. SendEvent has no reply, so the
// request is written directly and the sequence counter advanced the way do()
// would: the next reply is then matched on the wire, not off by one.
func (x *x11Conn) RequestForeground(win uint32) error {
	const (
		netActiveWindow = "_NET_ACTIVE_WINDOW"
		substructure    = 0x00080000 | 0x00100000 // SubstructureNotify | SubstructureRedirect
	)
	atomID, err := x.atom(netActiveWindow)
	if err != nil {
		return err
	}
	req := make([]byte, 44)
	req[0] = 25 // SendEvent
	req[1] = 0  // propagate = false
	binary.LittleEndian.PutUint16(req[2:4], 11)
	binary.LittleEndian.PutUint32(req[4:8], x.root)
	binary.LittleEndian.PutUint32(req[8:12], substructure)
	// ClientMessage event: type=33, format=32, window, message-type atom, data.
	req[12] = 33
	req[13] = 32
	binary.LittleEndian.PutUint32(req[16:20], win)
	binary.LittleEndian.PutUint32(req[20:24], atomID)
	binary.LittleEndian.PutUint32(req[24:28], 2) // source: pager/application (2 per §8)
	x.seq++
	if _, err := x.c.Write(req); err != nil {
		return fmt.Errorf("send _NET_ACTIVE_WINDOW: %w", err)
	}
	return nil
}
