//go:build linux

package agent

// P14-03 (contract P10-07 v3.1 §10), X11 backend.
//
// The pixels come from GetImage on the SAME connection P13-03 opened for the
// window tree and P14-01/02 used for geometry and input. One display, one
// connection: a capture that read a different view of the server than the rect
// the caller was told about would be a second truth about the same screen.
//
// Decoding is deliberately strict. The reply's own length field decides the
// scanline stride, and the pixel format must be a TrueColor layout this build
// decodes (32 or 16 bits per pixel). Anything else is a captureDecodeError —
// the handler turns that into 500 INTERNAL_ERROR stage:"decode". Falling back
// to a 0x0 success would be the measured SS-11 defect the contract names.

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

const (
	x11GetImage = 73 // GetImage
	// x11GetImageWords is the request length in 4-byte units: opcode/format/
	// length, drawable, x/y/w/h, plane-mask = 5 words.
	x11GetImageWords = 5
)

// bitsPerPixel answers the server's pixel size for a drawable depth, from the
// pixmap formats block of the connection setup reply. The fallbacks only run
// when a server omitted the format row, which the protocol forbids.
func (x *x11Conn) bitsPerPixel(depth byte) int {
	if bpp, ok := x.bpp[depth]; ok && bpp > 0 {
		return int(bpp)
	}
	switch depth {
	case 24, 32:
		return 32
	case 16:
		return 16
	case 8:
		return 8
	}
	return 0
}

// Capture implements captureSeam: §10's three targets on one drawable reader.
func (x *x11Conn) Capture(plan capturePlan) (*captureFrame, error) {
	switch plan.Kind {
	case "window":
		return x.captureWindow(plan)
	case "region":
		return x.captureRegion(plan)
	default:
		return x.captureScreen()
	}
}

func (x *x11Conn) captureScreen() (*captureFrame, error) {
	w, h := x.Pixels()
	if w <= 0 || h <= 0 {
		return nil, &captureDecodeError{Backend: "x11",
			Err: fmt.Errorf("connection setup reported a %dx%d screen", w, h)}
	}
	if px := w * h; px > protocol.CaptureMaxPixels {
		return nil, &captureTooLargeError{Px: px}
	}
	return x.image(x.Root(), 0, 0, w, h, protocol.CaptureSourceScreenRegion)
}

func (x *x11Conn) captureRegion(plan capturePlan) (*captureFrame, error) {
	r := plan.Rect
	if r.W <= 0 || r.H <= 0 {
		return nil, &captureDecodeError{Backend: "x11",
			Err: fmt.Errorf("region %dx%d has no pixels", r.W, r.H)}
	}
	if px := r.W * r.H; px > protocol.CaptureMaxPixels {
		return nil, &captureTooLargeError{Px: px}
	}
	// Region pixels are lifted from the SCREEN, so they are labelled
	// screen_region: occlusion affects this answer and the caller must know.
	return x.image(x.Root(), r.X, r.Y, r.W, r.H, protocol.CaptureSourceScreenRegion)
}

func (x *x11Conn) captureWindow(plan capturePlan) (*captureFrame, error) {
	g, err := x.Geometry(plan.Window)
	if err != nil {
		return nil, &capturePlatformError{Backend: "x11", Err: err}
	}
	w, h := g.W, g.H
	dx, dy := 0, 0
	// A rect WITH a window crops inside that window's own drawable: still
	// window_direct, still occlusion-independent.
	if plan.Rect.W > 0 && plan.Rect.H > 0 {
		dx, dy, w, h = plan.Rect.X, plan.Rect.Y, plan.Rect.W, plan.Rect.H
	}
	if w <= 0 || h <= 0 {
		return nil, &captureDecodeError{Backend: "x11",
			Err: fmt.Errorf("window 0x%x measured %dx%d", plan.Window, w, h)}
	}
	if px := w * h; px > protocol.CaptureMaxPixels {
		return nil, &captureTooLargeError{Px: px}
	}
	return x.image(plan.Window, dx, dy, w, h, protocol.CaptureSourceWindowDirect)
}

// image reads a ZPixmap from drawable and converts it to RGBA8 with a stride of
// exactly w*4, which is the normalisation §10's hash is defined over.
func (x *x11Conn) image(drawable uint32, dx, dy, w, h int, source string) (*captureFrame, error) {
	if w <= 0 || h <= 0 {
		return nil, &captureDecodeError{Backend: "x11", Err: fmt.Errorf("%dx%d is not a pixel surface", w, h)}
	}
	if w > 65535 || h > 65535 {
		return nil, &captureDecodeError{Backend: "x11",
			Err: fmt.Errorf("%dx%d exceeds the X protocol's 16-bit geometry", w, h)}
	}
	req := make([]byte, 20)
	req[0] = x11GetImage
	req[1] = 2 // ZPixmap
	binary.LittleEndian.PutUint16(req[2:4], x11GetImageWords)
	binary.LittleEndian.PutUint32(req[4:8], drawable)
	binary.LittleEndian.PutUint16(req[8:10], uint16(int16(dx)))
	binary.LittleEndian.PutUint16(req[10:12], uint16(int16(dy)))
	binary.LittleEndian.PutUint16(req[12:14], uint16(w))
	binary.LittleEndian.PutUint16(req[14:16], uint16(h))
	binary.LittleEndian.PutUint32(req[16:20], 0xffffffff) // AllPlanes
	if _, err := x.c.Write(req); err != nil {
		return nil, &capturePlatformError{Backend: "x11", Err: err}
	}
	reply := make([]byte, 32)
	if _, err := io.ReadFull(x.c, reply); err != nil {
		return nil, &capturePlatformError{Backend: "x11", Err: err}
	}
	if reply[0] == 0 {
		return nil, &capturePlatformError{Backend: "x11", Err: &x11Error{Code: reply[1], Want: "GetImage"}}
	}
	if reply[0] != 1 {
		return nil, &capturePlatformError{Backend: "x11",
			Err: fmt.Errorf("unexpected GetImage reply type %d", reply[0])}
	}
	depth := reply[1]
	replyLen := int(binary.LittleEndian.Uint32(reply[4:8])) * 4
	bpp := x.bitsPerPixel(depth)
	if bpp != 32 && bpp != 16 {
		return nil, &captureDecodeError{Backend: "x11",
			Err: fmt.Errorf("depth %d at %d bits per pixel is not a TrueColor layout this build decodes", depth, bpp)}
	}
	buf := make([]byte, replyLen)
	if _, err := io.ReadFull(x.c, buf); err != nil {
		return nil, &capturePlatformError{Backend: "x11", Err: err}
	}
	// The server's own reply length is the stride authority: a backend that
	// pasted its own idea of the stride here would mis-read every row after
	// the first and still report a plausible width.
	if replyLen%h != 0 {
		return nil, &captureDecodeError{Backend: "x11",
			Err: fmt.Errorf("reply length %d is not a whole number of %d rows", replyLen, h)}
	}
	stride := replyLen / h
	if min := (w*bpp + 7) / 8; stride < min {
		return nil, &captureDecodeError{Backend: "x11",
			Err: fmt.Errorf("stride %d is narrower than %d bytes for %d px at %d bpp", stride, min, w, bpp)}
	}

	pix := make([]byte, w*h*4)
	switch bpp {
	case 32:
		// TrueColor 24/32bpp little-endian: bytes are B,G,R,X.
		for y := 0; y < h; y++ {
			row := buf[y*stride:]
			out := pix[y*w*4:]
			for px := 0; px < w; px++ {
				in := row[px*4:]
				o := px * 4
				out[o] = in[2]
				out[o+1] = in[1]
				out[o+2] = in[0]
				out[o+3] = 255
			}
		}
	case 16:
		// RGB565: depth 15/16 servers.
		for y := 0; y < h; y++ {
			row := buf[y*stride:]
			out := pix[y*w*4:]
			for px := 0; px < w; px++ {
				v := binary.LittleEndian.Uint16(row[px*2:])
				o := px * 4
				out[o] = byte((v>>11)&0x1f) << 3
				out[o+1] = byte((v>>5)&0x3f) << 2
				out[o+2] = byte(v&0x1f) << 3
				out[o+3] = 255
			}
		}
	}
	return &captureFrame{Pix: pix, W: w, H: h, Source: source}, nil
}
