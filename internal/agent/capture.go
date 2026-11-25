//go:build linux || darwin || windows

package agent

// P14-03 (contract P10-07 v3.1 §10): capture.
//
// The handler is platform-neutral; the pixels come from a captureSeam, which on
// linux is the same x11Conn P13-03/P14-01/P14-02 open (x11Dial). A second
// display connection strategy is the defect this seam exists to prevent.
//
// What the contract makes non-negotiable, and where it lives here:
//   - hash is sha256 of the DECODED RGBA8 pixels (stride-normalised to w*4),
//     never of the encoded file (C-8). encodeFrame is called AFTER hashing and
//     cannot influence it.
//   - data/size_bytes are absent when hash_only:true (C-8): the result type's
//     omitempty is the mechanism and the test asserts key absence.
//   - width/height are the pixels actually captured. A backend that cannot
//     parse its own reply returns captureDecodeError, which this handler turns
//     into 500 INTERNAL_ERROR with detail{backend, stage:"decode"} — a 0x0
//     success is never produced.
//   - changed is null when there is no baseline; a `since` hash overrides the
//     stored baseline for one call and does not overwrite it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// captureFrame is raw pixels: RGBA8, one row of exactly W*4 bytes (no stride
// padding — the backend normalises, so the hash is stride-independent).
type captureFrame struct {
	Pix    []byte
	W, H   int
	Source string
}

// capturePlan is what the backend is asked for. Kind is one of
// screen|window|region; Rect is present for a region and, with a window, crops
// inside that window's own drawable.
type capturePlan struct {
	Ref    protocol.UITreeContextRef
	Kind   string
	Window uint32
	Rect   protocol.Rect
}

// captureSeam is the one backend interface §10 needs. It is satisfied by
// *x11Conn on linux; other platforms answer errNoCaptureBackend.
type captureSeam interface {
	Capture(plan capturePlan) (*captureFrame, error)
	Close() error
}

// captureDecodeError means the backend could not parse or validate the bytes it
// read. It is distinct from "the platform refused": this one ran and its output
// was unusable, which §10 maps to 500 INTERNAL_ERROR stage:"decode".
type captureDecodeError struct {
	Backend string
	Err     error
}

func (e *captureDecodeError) Error() string {
	return fmt.Sprintf("%s backend could not decode its own capture: %v", e.Backend, e.Err)
}

// captureTooLargeError is the cost guard firing on a MEASURED surface (a window
// that turned out to be larger than the request implied).
type captureTooLargeError struct{ Px int }

func (e *captureTooLargeError) Error() string {
	return fmt.Sprintf("target is %d px, over the %d px limit", e.Px, protocol.CaptureMaxPixels)
}

// capturePlatformError is a backend failure that is neither a decode nor a
// policy refusal (an X error on the drawable, a missing extension).
type capturePlatformError struct {
	Backend string
	Err     error
}

func (e *capturePlatformError) Error() string {
	return fmt.Sprintf("%s backend failed: %v", e.Backend, e.Err)
}

// captureCompositedError means the window is composited: a compositing manager
// owns the screen's EWMH selection, so the window's pixels live in the
// compositor's offscreen pixmap and are not what a direct drawable read returns.
// §10 names the way to obtain them — XCompositeNameWindowPixmap, labelled
// source:"compositor" — and this build does not implement it. That is a 501
// PRIMITIVE_UNSUPPORTED reason:"NOT_EXPOSED" (§11: the surface is not exposed to
// this build), never a 500 that says the agent broke, and never a 200 whose
// pixels are silently the wrong surface.
type captureCompositedError struct {
	Window     uint32
	Compositor string
}

func (e *captureCompositedError) Error() string {
	return fmt.Sprintf("window 0x%x is composited (%s is owned by a compositing manager): "+
		"its pixels are not exposed to a direct drawable read and this build has no "+
		"XCompositeNameWindowPixmap path", e.Window, e.Compositor)
}

// --- baseline cache ---------------------------------------------------------

// captureBaselineTTL and captureBaselineMax are §10's numbers: TTL 600 s, LRU
// 64. The cache is in-agent and never persisted; an evicted or expired entry is
// "no baseline", which is an honest null rather than a fabricated comparison.
const (
	captureBaselineTTL = 600 * time.Second
	captureBaselineMax = 64
)

type captureBaselineEntry struct {
	Hash string
	At   time.Time
}

type captureBaselineCache struct {
	mu      sync.Mutex
	entries map[string]captureBaselineEntry
	order   []string // least recently used first
}

var captureBaselines = &captureBaselineCache{entries: map[string]captureBaselineEntry{}}

// get returns the stored hash and whether a live entry existed.
func (c *captureBaselineCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if time.Since(e.At) > captureBaselineTTL {
		delete(c.entries, key)
		c.drop(key)
		return "", false
	}
	c.touch(key)
	return e.Hash, true
}

func (c *captureBaselineCache) put(key, hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		c.order = append(c.order, key)
	} else {
		c.touch(key)
	}
	c.entries[key] = captureBaselineEntry{Hash: hash, At: time.Now()}
	for len(c.order) > captureBaselineMax {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
}

func (c *captureBaselineCache) touch(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
}

func (c *captureBaselineCache) drop(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

func (c *captureBaselineCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]captureBaselineEntry{}
	c.order = nil
}

// captureBaselineKey is §10's cache key: (context, target, dpi_scale). The
// target string includes the rect, because two regions of the same screen are
// two different targets.
func captureBaselineKey(ref protocol.UITreeContextRef, kind string, window uint32, rect protocol.Rect) string {
	target := kind
	switch kind {
	case "window":
		target = fmt.Sprintf("window:0x%x", window)
		if rect.W > 0 && rect.H > 0 {
			target += fmt.Sprintf("+%d+%d+%dx%d", rect.X, rect.Y, rect.W, rect.H)
		}
	case "region":
		target = fmt.Sprintf("region:%d,%d,%d,%d", rect.X, rect.Y, rect.W, rect.H)
	}
	return fmt.Sprintf("%s|%s|1.0", ref.Resolved, target)
}

// --- handler ----------------------------------------------------------------

func (a *Agent) handleCapture(env protocol.Envelope) protocol.Envelope {
	params, serr := protocol.ParseCommandStrict[protocol.ScreenParams](env, protocol.AllowCapture)
	if serr != nil {
		return strictRefusal(env, serr)
	}

	format := strings.ToLower(strings.TrimSpace(params.Format))
	if format == "" {
		format = "png"
	}
	if format != "png" && format != "jpeg" {
		return protocol.NewErrorDetail(env.ID, protocol.ErrInvalidParams,
			fmt.Sprintf("format %q is not an encoder this build has", params.Format),
			map[string]interface{}{
				"reason":  "OUT_OF_RANGE",
				"field":   "format",
				"allowed": []string{"png", "jpeg"},
				"ran":     false,
			})
	}

	kind := "screen"
	var winID uint32
	if strings.TrimSpace(params.Window) != "" {
		id, ok := parseWindowID(params.Window)
		if !ok {
			return protocol.NewErrorDetail(env.ID, protocol.ErrInvalidParams,
				fmt.Sprintf("window %q is not a hex X window id (expected \"0x…\")", params.Window),
				map[string]interface{}{
					"reason": "OUT_OF_RANGE",
					"field":  "window",
					"ran":    false,
				})
		}
		winID, kind = id, "window"
	}
	var rect protocol.Rect
	if params.Rect != nil {
		rect = *params.Rect
		if rect.W <= 0 || rect.H <= 0 {
			return protocol.NewErrorDetail(env.ID, protocol.ErrInvalidParams,
				fmt.Sprintf("rect %dx%d has no pixels", rect.W, rect.H),
				map[string]interface{}{"reason": "OUT_OF_RANGE", "field": "rect", "ran": false})
		}
		// §10 cost guard, before any display is opened: a caller-named region
		// that is too large is refused without capturing a single byte, and
		// never downscaled into a smaller "capture" that claims to be it.
		if px := rect.W * rect.H; px > protocol.CaptureMaxPixels {
			return regionTooLarge(env, px)
		}
		if kind == "screen" {
			kind = "region"
		}
	}

	if refusal := a.captureConsentRefusal(env, params.Context); refusal != nil {
		return *refusal
	}

	probe, ref, refusal := a.openContextDisplay(env, "capture", params.Context, params.Window)
	if refusal != nil {
		return *refusal
	}
	defer probe.Close()

	seam, ok := probe.(captureSeam)
	if !ok {
		// A display seam that cannot produce pixels: the honest answer is the
		// §11 501, not a capture synthesised from something else.
		return noBackend(env, "capture")
	}

	frame, err := seam.Capture(capturePlan{Ref: ref, Kind: kind, Window: winID, Rect: rect})
	if err != nil {
		return captureBackendRefusal(env, ref, kind, params.Window, err)
	}
	if derr := validateCaptureFrame(frame); derr != nil {
		return captureDecodeRefusal(env, ref, kind, params.Window, derr)
	}

	hash := hashCapturePixels(frame.Pix)

	// Baseline: a `since` hash overrides the stored one for this call only.
	key := captureBaselineKey(ref, kind, winID, rect)
	baseline := ""
	var changed *bool
	if since := strings.TrimSpace(params.Since); since != "" {
		if !strings.HasPrefix(since, "sha256:") {
			return protocol.NewErrorDetail(env.ID, protocol.ErrInvalidParams,
				fmt.Sprintf("since %q is not a sha256 hash", params.Since),
				map[string]interface{}{"reason": "OUT_OF_RANGE", "field": "since", "ran": false})
		}
		baseline = since
		c := hash != baseline
		changed = &c
	} else if stored, ok := captureBaselines.get(key); ok {
		baseline = stored
		c := hash != stored
		changed = &c
	}
	if strings.TrimSpace(params.Since) == "" {
		// A stateless hash-only poll (a `since` call) must not move the
		// baseline: the next ordinary call still compares against what it saw.
		captureBaselines.put(key, hash)
	}

	out := protocol.CaptureV1Result{
		ContextRef: ref,
		Target:     captureTargetOf(kind, params.Window, rect),
		Format:     format,
		Width:      frame.W,
		Height:     frame.H,
		Hash:       hash,
		Changed:    changed,
		Baseline:   baseline,
		Source:     frame.Source,
		Units:      protocol.CaptureUnitsX11,
		DPIScale:   1.0,
	}
	if !params.HashOnly {
		data, size, eerr := encodeCaptureFrame(frame, format, params.Quality)
		if eerr != nil {
			return protocol.NewErrorDetail(env.ID, protocol.ErrInternal,
				fmt.Sprintf("capture: %s encoding failed: %v", format, eerr),
				map[string]interface{}{"backend": "agent", "stage": "encode", "ran": false})
		}
		out.Data = data
		out.SizeBytes = size
		// C-9: the agent is the only encoder here — the backend hands over raw
		// pixels, so every requested format is produced by the agent.
		out.Detail = map[string]interface{}{"encoded_by": "agent"}
	}
	return protocol.NewResult(env.ID, protocol.TypeCaptureResult, out)
}

// validateCaptureFrame is the "no 0-sized success" rule (§10, measured SS-11).
func validateCaptureFrame(f *captureFrame) error {
	if f == nil {
		return errCaptureNoFrame
	}
	if f.W <= 0 || f.H <= 0 {
		return fmt.Errorf("backend reported a %dx%d capture", f.W, f.H)
	}
	if len(f.Pix) != f.W*f.H*4 {
		return fmt.Errorf("backend reported %dx%d (%d bytes expected) but produced %d", f.W, f.H, f.W*f.H*4, len(f.Pix))
	}
	return nil
}

var errCaptureNoFrame = fmt.Errorf("backend produced no frame")

func captureTargetOf(kind, window string, rect protocol.Rect) protocol.CaptureTarget {
	t := protocol.CaptureTarget{Kind: kind}
	switch kind {
	case "window":
		t.Window = window
		t.IDKind = protocol.WindowIDKindXID
		if rect.W > 0 && rect.H > 0 {
			r := rect
			t.Rect = &r
		}
	case "region":
		r := rect
		t.Rect = &r
	}
	return t
}

// hashCapturePixels is sha256 over the decoded RGBA8 bytes. The frame is
// stride-normalised by the backend (W*4), so the same pixels from any encoder
// or any server stride hash identically.
func hashCapturePixels(pix []byte) string {
	sum := sha256.Sum256(pix)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// encodeCaptureFrame encodes raw pixels with an agent-side stdlib encoder.
func encodeCaptureFrame(f *captureFrame, format string, quality int) (string, int64, error) {
	img := image.NewRGBA(image.Rect(0, 0, f.W, f.H))
	copy(img.Pix, f.Pix)
	var buf bytes.Buffer
	var err error
	switch format {
	case "jpeg":
		q := quality
		if q <= 0 {
			q = 90
		}
		if q > 100 {
			q = 100
		}
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: q})
	default:
		err = png.Encode(&buf, img)
	}
	if err != nil {
		return "", 0, err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), int64(buf.Len()), nil
}

// --- refusals ---------------------------------------------------------------

func regionTooLarge(env protocol.Envelope, px int) protocol.Envelope {
	return protocol.NewErrorDetail(env.ID, protocol.ErrInvalidParams,
		fmt.Sprintf("requested region is %d px, over the %d px limit; it was refused, not downscaled", px, protocol.CaptureMaxPixels),
		map[string]interface{}{
			"reason": protocol.ReasonRegionTooLarge,
			"max_px": protocol.CaptureMaxPixels,
			"px":     px,
			"ran":    false,
		})
}

func captureDecodeRefusal(env protocol.Envelope, ref protocol.UITreeContextRef, kind, window string, err error) protocol.Envelope {
	return protocol.NewErrorDetail(env.ID, protocol.ErrInternal,
		"capture: "+err.Error(),
		map[string]interface{}{
			"backend": "x11",
			"stage":   "decode",
			"context": ref.Resolved,
			"target":  kind,
			"window":  window,
			"ran":     true,
		})
}

func captureBackendRefusal(env protocol.Envelope, ref protocol.UITreeContextRef, kind, window string, err error) protocol.Envelope {
	switch e := err.(type) {
	case *captureTooLargeError:
		return regionTooLarge(env, e.Px)
	case *captureDecodeError:
		detail := map[string]interface{}{
			"backend": e.Backend,
			"stage":   "decode",
			"context": ref.Resolved,
			"target":  kind,
			"window":  window,
			"ran":     true,
		}
		return protocol.NewErrorDetail(env.ID, protocol.ErrInternal, "capture: "+e.Error(), detail)
	case *capturePlatformError:
		return protocol.NewErrorDetail(env.ID, protocol.ErrInternal,
			"capture: "+e.Error(),
			map[string]interface{}{
				"backend": e.Backend,
				"stage":   "capture",
				"context": ref.Resolved,
				"target":  kind,
				"window":  window,
				"ran":     true,
			})
	case *captureCompositedError:
		return compositedRefusal(env, ref, kind, window, e)
	}
	return protocol.NewErrorDetail(env.ID, protocol.ErrInternal, "capture: "+err.Error(),
		map[string]interface{}{"backend": "x11", "stage": "capture", "ran": true})
}

// compositedRefusal is §11's 501 for a window this build cannot obtain: the
// seat is composited, so the pixels live in the compositor's pixmap and §10's
// way to read them (XCompositeNameWindowPixmap, source:"compositor") is not
// implemented here. The reason is NOT_EXPOSED, not NOT_IMPLEMENTED: the surface
// is not exposed to a direct read on THIS seat, and shipping nothing would still
// leave it unobtainable. detail names the compositor selection that was
// observed, so the operator can tell a composited seat from a dead window.
func compositedRefusal(env protocol.Envelope, ref protocol.UITreeContextRef, kind, window string, e *captureCompositedError) protocol.Envelope {
	return protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported,
		fmt.Sprintf("capture: window 0x%x is composited (%s is held by a compositing manager); "+
			"its pixels are not exposed to a direct drawable read and this build has no "+
			"XCompositeNameWindowPixmap path", e.Window, e.Compositor),
		map[string]interface{}{
			"reason":           protocol.ReasonNotExposed,
			"composited":       true,
			"compositor":       e.Compositor,
			"source_available": protocol.CaptureSourceCompositor,
			"required":         "XCompositeNameWindowPixmap",
			"context":          ref.Resolved,
			"target":           kind,
			"window":           window,
			"ran":              false,
		})
}

// captureConsentRefusal answers §10's Wayland row: a Wayland (or Termux) seat
// has no capture without a consent dialog nobody can raise from here, so the
// answer is 403 PRIMITIVE_REFUSED reason CONSENT_REQUIRED — never a 501 that
// suggests shipping code would fix it.
func (a *Agent) captureConsentRefusal(env protocol.Envelope, selector *string) *protocol.Envelope {
	requested := ""
	if selector != nil {
		requested = *selector
	}
	if requested == "" {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") != "" {
			envl := consentRequiredRefusal(env, "wayland", "")
			return &envl
		}
		return nil
	}
	res, err := enumerateContextsCached()
	if err != nil {
		return nil
	}
	for _, row := range res.Contexts {
		if row.ID != requested {
			continue
		}
		if row.Kind == protocol.ContextKindWayland {
			envl := consentRequiredRefusal(env, row.Kind, requested)
			return &envl
		}
		return nil
	}
	return nil
}

func consentRequiredRefusal(env protocol.Envelope, kind, context string) protocol.Envelope {
	return protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveRefused,
		fmt.Sprintf("capture on a %s seat requires a portal consent dialog that this agent cannot raise", kind),
		map[string]interface{}{
			"reason":  protocol.ReasonConsentRequired,
			"context": context,
			"ran":     false,
		})
}
