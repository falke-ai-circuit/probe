package agent

// P14-02 (contract P10-07 v3.1 §9.2, §9.3, §9.4).
//
// ONE injection seam, and it is the display connection §9.1 already opened:
// `windowProbe` answers where the pointer must land, and the same connection
// emits the event. A second connection would be a second view of the same
// display, which is how a click lands somewhere the hit-test never checked.
//
// The seam is an OPTIONAL interface on purpose. The probes that predate this
// unit (the synthetic EWMH fixtures, the Windows `wts` column) do not inject;
// a pointer primitive that names no display still goes through the platform
// layer exactly as before. A probe that cannot inject is not guessed at: the
// primitive answers NO_BACKEND rather than inventing a second path.
//
// The three rules implemented here, each in one place:
//
//   - §9.2 a count>1 click is ONE gesture: down,up,down,up with a measured gap
//     ≤ half the platform's double-click interval. A loop of independent
//     `pointer-click` calls is not equivalent and is not what this does.
//   - §9.3 `delta` is notches, negative = up. The backend maps; it never
//     reinterprets. There is no pixel wheel here, so `unit:"px"` refuses.
//   - §9.4 text is typed by remapping a spare keycode, and the map is restored
//     after the string. A string that cannot be typed IN FULL is a refusal,
//     never a prefix (the whole string is validated before the first remap).

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"unicode/utf8"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

const (
	// defaultDoubleClick is the contract's FALLBACK multi-click interval. It is
	// not universal: X11 has no request that reads the seat's own value back
	// (XGetPointerControl answers acceleration, not the multi-click time), so
	// 400 ms is only what answers when nothing configured the agent. A seat
	// configured lower (200 ms is common) would never read two presses spaced
	// for a 400 ms window as a double-click, so the effective interval is a
	// per-agent field (Agent.clickWindow) and the spacing derives from it.
	defaultDoubleClick = 400 * time.Millisecond

	// multiClickFraction is the share of the EFFECTIVE double-click window the
	// gesture's spacing occupies: 30%, comfortably inside §9.2's 50% ceiling.
	multiClickFractionNum = 3
	multiClickFractionDen = 10

	// multiClickGap is what an UNCONFIGURED agent spaces its presses by: 30% of
	// the contract default. It is computed from the same fraction, so it cannot
	// drift; a configured seat gets clickSpacing(window) instead.
	multiClickGap = defaultDoubleClick * multiClickFractionNum / multiClickFractionDen

	// maxClickCount bounds what "one click gesture" can mean. Three is a
	// triple-click (same mechanism); beyond that the app is selecting a range
	// and the caller should say so instead of asking for N clicks.
	maxClickCount = 3

	// maxScrollNotches bounds one request so a large delta cannot become an
	// unbounded burst of wheel events against a hostile server.
	maxScrollNotches = 100
)

// clickSpacing is the inter-press spacing for a double-click window: 30% of it,
// in integer nanoseconds so the value is exact. Spacing is DERIVED from the
// window, never a fixed duration — a seat configured below the default would
// otherwise receive presses outside its own double-click window.
func clickSpacing(window time.Duration) time.Duration {
	if window <= 0 {
		window = defaultDoubleClick
	}
	return window * multiClickFractionNum / multiClickFractionDen
}

// clickWindowFor resolves the effective double-click interval from a configured
// millisecond value. Zero or negative means "not configured": the contract
// default answers, which is the value this build judged itself against before
// the window was configurable.
func clickWindowFor(ms int) time.Duration {
	if ms <= 0 {
		return defaultDoubleClick
	}
	return time.Duration(ms) * time.Millisecond
}

// effectiveClickWindow is the interval this agent judges the gesture against:
// its configured window, or the contract default when none was configured.
func (a *Agent) effectiveClickWindow() time.Duration {
	if a != nil && a.clickWindow > 0 {
		return a.clickWindow
	}
	return defaultDoubleClick
}

// errNoSpareKeycode is §9.4's keycode range being full: there is no key to
// borrow for the remap, so the codepoint cannot be carried.
var errNoSpareKeycode = errors.New("no spare keycode")

// errXTestMissing is a display that cannot fake input at all: 501, not a
// fallback with different timing.
var errXTestMissing = errors.New("the X server does not expose the XTEST extension")

// inputSeam is the injection half of the display connection. Every method is
// one wire event (or one keymap operation); the gesture logic lives above it so
// the emitted sequence is one thing a test can watch.
type inputSeam interface {
	// Pointer moves the pointer to root coordinates.
	Pointer(x, y int) error
	// Button emits ONE press or release of the named button.
	Button(button string, press bool) error
	// Key emits ONE press or release of a keycode.
	Key(keycode byte, press bool) error
	// SpareKeycode returns a keycode with no keysyms bound to it.
	SpareKeycode() (byte, error)
	// RemapKeycode binds one keysym to one keycode.
	RemapKeycode(keycode byte, sym uint32) error
	// RestoreKeymap puts back every keycode this connection remapped.
	RestoreKeymap() error
}

// multiClicker is a platform layer that can emit a whole click gesture. Only
// the Windows backend implements it today (the contract's Windows column is
// planned/unverified, W-6); X11 goes through the seam above.
type multiClicker interface {
	ClickCount(x, y int, button string, count int, gap time.Duration) (int, error)
}

// unicodeTyper is a platform that emits UTF-16 code units as character events
// rather than as key presses. Windows' KEYEVENTF_UNICODE is the only one.
type unicodeTyper interface {
	TypeTextUnits(units []uint16) error
}

// injectorFor answers the seam for a resolved display, if that display can
// inject. A nil probe means "no window was named": the caller keeps the
// platform path it had before this unit.
func injectorFor(t *inputTarget) (inputSeam, bool) {
	if t == nil || t.Probe == nil {
		return nil, false
	}
	inj, ok := t.Probe.(inputSeam)
	return inj, ok
}

// --- §9.2 multi-click -------------------------------------------------------

// emitClickGesture is ONE primitive call emitting `count` press/release pairs
// with the inter-click gap §9.2 requires, spaced inside `window` — the seat's
// effective double-click interval, not a build constant. It returns the gap it
// MEASURED between the first and second press (zero for count==1), so the
// number in the response is an observation of this gesture, not a constant
// echoed back: the stamps are taken AFTER the seam returns, so a seam whose
// wire call costs time widens the measured interval and the echo follows it.
func emitClickGesture(inj inputSeam, x, y int, button string, count int, window time.Duration) (time.Duration, error) {
	if err := inj.Pointer(x, y); err != nil {
		return 0, err
	}
	spacing := clickSpacing(window)
	var presses []time.Time
	for i := 0; i < count; i++ {
		if i > 0 {
			time.Sleep(spacing)
		}
		if err := inj.Button(button, true); err != nil {
			return 0, err
		}
		// Stamped after the press left the seam: the call's own cost is part of
		// the interval the target perceives. A stamp taken before the call would
		// report the sleep and hide the wire, which is what made the old echo
		// indistinguishable from a hardcoded 120.
		presses = append(presses, time.Now())
		if err := inj.Button(button, false); err != nil {
			return 0, err
		}
	}
	if len(presses) > 1 {
		return presses[1].Sub(presses[0]), nil
	}
	return 0, nil
}

// clickRefusalForCount rejects a count outside the gesture's meaning range. It
// is a 400, not a silent clamp: a caller that asked for five clicks and got
// three has been lied to.
func clickRefusalForCount(env protocol.Envelope, count int) *protocol.Envelope {
	allowed := []interface{}{1, 2, 3}
	envl := protocol.NewErrorDetail(env.ID, protocol.ErrInvalidParams,
		fmt.Sprintf("pointer-click: count %d is outside the one-gesture range 1-%d", count, maxClickCount),
		map[string]interface{}{
			"reason":  protocol.ReasonOutOfRange,
			"field":   "count",
			"allowed": allowed,
			"ran":     false,
		})
	return &envl
}

// multiClickUnsupported is the §9.2 Android/Termux rule generalised: a backend
// with no multi-click mechanism answers 501 NO_BACKEND. Two independent clicks
// pretending to be a double-click is exactly the failure mode the contract
// names, so there is no fallback that loops the single-click path.
func multiClickUnsupported(env protocol.Envelope, why string) protocol.Envelope {
	return protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported,
		"pointer-click: "+why,
		map[string]interface{}{
			"reason": protocol.ReasonNoBackend,
			"ran":    false,
		})
}

// --- §9.3 scroll ------------------------------------------------------------

// scrollButton maps the CONVENTION (§9.3: negative = up) onto X11's wheel
// buttons: 4 is up/away from the user, 5 is down. The mapping is derived from
// the sign rule, and the test asserts the resulting button, not this comment.
func scrollButton(notch int) (string, bool) {
	switch {
	case notch < 0:
		return "4", true
	case notch > 0:
		return "5", true
	}
	return "", false
}

// emitScroll moves the pointer onto the target and emits one press+release per
// notch. `delta` is passed through; the sign convention is applied here and
// nowhere else.
func emitScroll(inj inputSeam, x, y, delta int) (int, error) {
	if err := inj.Pointer(x, y); err != nil {
		return 0, err
	}
	notches := delta
	if notches < 0 {
		notches = -notches
	}
	button, ok := scrollButton(delta)
	if !ok {
		return 0, nil
	}
	for i := 0; i < notches; i++ {
		if err := inj.Button(button, true); err != nil {
			return i, err
		}
		if err := inj.Button(button, false); err != nil {
			return i, err
		}
	}
	return notches, nil
}

// scrollUnitOK says whether this build can honour the unit. `px` is refused on
// EVERY backend here — a wheel cannot be moved by a pixel, and a partial scroll
// is worse than a refusal (§9.3).
func scrollUnitOK(unit string) (string, bool) {
	switch unit {
	case "", "notch", "notches", "line":
		return "notch", true
	case "px", "pixel", "pixels":
		return "", false
	}
	return "", false
}

// --- §9.4 unicode text ------------------------------------------------------

// keysymFor maps a rune onto the keysym that types it. Latin-1 is its own
// keysym; everything above 0xFF is spelled 0x01000000+codepoint, which is the
// Unicode keysym range the X server carries. \n, \r and \t keep their control
// keysyms because that is what a text field receives for them.
func keysymFor(r rune) uint32 {
	switch r {
	case '\n', '\r':
		return 0xff0d
	case '\t':
		return 0xff09
	}
	if r < 0x100 {
		return uint32(r)
	}
	return 0x01000000 | uint32(r)
}

// planText is the whole-string validation §9.4 demands: the string is mapped to
// keysyms BEFORE anything is remapped or typed, so a codepoint that cannot be
// carried refuses the request with no prefix on the display.
func planText(inj inputSeam, text string) ([]uint32, byte, error) {
	if !utf8.ValidString(text) {
		bad := 0
		for i := 0; i < len(text); {
			r, size := utf8.DecodeRuneInString(text[i:])
			if r == utf8.RuneError && size == 1 {
				bad = i
				break
			}
			i += size
		}
		return nil, 0, fmt.Errorf("invalid UTF-8 at byte %d: the string cannot be typed in full", bad)
	}
	runes := []rune(text)
	syms := make([]uint32, 0, len(runes))
	for _, r := range runes {
		switch {
		case r < 0x20 && r != '\n' && r != '\r' && r != '\t':
			return nil, 0, fmt.Errorf("codepoint U+%04X has no keysym: the string cannot be typed in full", r)
		case r >= 0xD800 && r <= 0xDFFF:
			return nil, 0, fmt.Errorf("codepoint U+%04X is a lone surrogate: the string cannot be typed in full", r)
		case r > 0x10FFFF:
			return nil, 0, fmt.Errorf("codepoint U+%04X is outside Unicode", r)
		}
		syms = append(syms, keysymFor(r))
	}
	// One keycode is borrowed for the whole string, so it must exist before the
	// first remap: this is the "keycode range cannot carry it" branch, and it
	// names the first codepoint that would have needed the key.
	kc, err := inj.SpareKeycode()
	if err != nil {
		if len(runes) == 0 {
			return syms, 0, nil
		}
		return nil, 0, fmt.Errorf("codepoint U+%04X: %v", runes[0], err)
	}
	return syms, kc, nil
}

// emitText types a validated keysym plan through one borrowed keycode and puts
// the keymap back afterwards. The restore runs even when a keystroke fails: a
// borrowed keycode left mapped is a keymap change visible to every app on the
// display.
func emitText(inj inputSeam, syms []uint32, kc byte) error {
	if len(syms) == 0 {
		return nil
	}
	defer inj.RestoreKeymap()
	for _, sym := range syms {
		if err := inj.RemapKeycode(kc, sym); err != nil {
			return err
		}
		if err := inj.Key(kc, true); err != nil {
			return err
		}
		if err := inj.Key(kc, false); err != nil {
			return err
		}
	}
	return nil
}

// codepointUnsupported is §9.4's refusal: 501, the codepoint named, the whole
// string not typed.
func codepointUnsupported(env protocol.Envelope, text, why string) protocol.Envelope {
	var named string
	for _, r := range text {
		named = fmt.Sprintf("U+%04X", r)
		break
	}
	return protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported,
		"text-input: "+why,
		map[string]interface{}{
			"reason":    protocol.ReasonCodepointUnsupported,
			"codepoint": named,
			"ran":       false,
		})
}

// textEncodingOK normalises the caller's encoding. Only utf8 (the default) and
// utf16 are defined by §9.4; anything else is a parameter error, not a guess.
func textEncodingOK(enc string) (string, bool) {
	switch enc {
	case "", "utf8", "utf-8":
		return "utf8", true
	case "utf16", "utf-16":
		return "utf16", true
	}
	return "", false
}

// transcode reports what §9.4 says must be visible: a caller that supplied
// UTF-16 units learns that the text was decoded to UTF-8 before emission.
// Windows emits the units verbatim, so nothing was transcoded there.
func transcode(enc string) bool {
	return enc == "utf16" && runtime.GOOS != "windows"
}

// gapMillis is the measured gap rounded to the millisecond the response
// reports. It is derived from the gesture's own timestamps, never from the
// constant that spaced it.
func gapMillis(d time.Duration) int {
	return int((d + 500*time.Microsecond) / time.Millisecond)
}

// clickBackendError separates "this display cannot fake input" (a 501 — the
// code is not there) from "the display rejected the event" (a 500).
func clickBackendError(env protocol.Envelope, err error) protocol.Envelope {
	if errors.Is(err, errXTestMissing) {
		return protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported,
			"pointer-click: "+err.Error(),
			map[string]interface{}{"reason": protocol.ReasonNoBackend, "ran": false})
	}
	return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
}

// handlePointerScroll is §5.5 row 8 + §9.3. It replaced the NO_BACKEND stub:
// the sign convention, the notch mapping and the px refusal all live in this
// file so the backend only ever sees "one notch up" or "one notch down".
func (a *Agent) handlePointerScroll(env protocol.Envelope) protocol.Envelope {
	params, serr := protocol.ParseCommandStrict[protocol.PointerScrollParams](env, protocol.AllowPointerScroll)
	if serr != nil {
		return strictRefusal(env, serr)
	}
	unit, unitOK := scrollUnitOK(params.Unit)
	if !unitOK {
		reason := protocol.ReasonUnitNotSupported
		code := protocol.ErrPrimitiveUnsupported
		msg := fmt.Sprintf("pointer-scroll: unit %q is not supported on this backend", params.Unit)
		detail := map[string]interface{}{
			"reason":    reason,
			"unit":      params.Unit,
			"supported": []string{"notch"},
			"ran":       false,
		}
		if params.Unit != "px" && params.Unit != "pixel" && params.Unit != "pixels" {
			// An unknown unit is a parameter error; only "px" is a defined unit
			// this backend cannot honour.
			code = protocol.ErrInvalidParams
			detail["reason"] = protocol.ReasonOutOfRange
			detail["field"] = "unit"
			detail["allowed"] = []string{"notch"}
		}
		return protocol.NewErrorDetail(env.ID, code, msg, detail)
	}
	if params.Delta == 0 {
		return protocol.NewErrorDetail(env.ID, protocol.ErrInvalidParams,
			"pointer-scroll: delta 0 is not a scroll",
			map[string]interface{}{
				"reason":  protocol.ReasonOutOfRange,
				"field":   "delta",
				"ran":     false,
			})
	}
	if params.Delta > maxScrollNotches || params.Delta < -maxScrollNotches {
		return protocol.NewErrorDetail(env.ID, protocol.ErrInvalidParams,
			fmt.Sprintf("pointer-scroll: delta %d is outside one request's range", params.Delta),
			map[string]interface{}{
				"reason":  protocol.ReasonOutOfRange,
				"field":   "delta",
				"allowed": fmt.Sprintf("-100..100 notches"),
				"ran":     false,
			})
	}
	// The point goes through the SAME §9.1 resolver a click uses: a
	// window-relative scroll is placed and hit-tested exactly like a click.
	pt, refusal := a.resolveInputPoint(env, "pointer-scroll", params.Context, params.Window, params.X, params.Y)
	if refusal != nil {
		return *refusal
	}
	if pt.Probe != nil {
		defer pt.Probe.Close()
	}
	inj, ok := injectorFor(&pt.inputTarget)
	if !ok && pt.Probe == nil {
		// No window named: no point to place, but the wheel still has to be
		// emitted somewhere. Open the context's display for the injection only.
		if probe, _, oref := a.openContextDisplay(env, "pointer-scroll", params.Context, params.Window); oref == nil {
			defer probe.Close()
			inj, ok = probe.(inputSeam)
		}
	}
	if !ok {
		return noBackend(env, "pointer-scroll")
	}
	notches, serr2 := emitScroll(inj, pt.X, pt.Y, params.Delta)
	if serr2 != nil {
		return clickBackendError(env, serr2)
	}
	return protocol.NewResult(env.ID, protocol.TypePointerScrollResult, protocol.ScrollResult{
		Success: true, Delta: params.Delta, Unit: unit, Notches: notches,
	})
}
