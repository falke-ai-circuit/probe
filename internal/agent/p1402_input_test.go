package agent

// P14-02 acceptance (contract P10-07 v3.1 §9.2, §9.3, §9.4), driven through the
// injection seam the display connection exposes. The seam is where the events
// actually leave the process, so the assertions are on the emitted sequence and
// on the timestamps between events — not on a constant the response echoed.
//
// The two tests that would pass with a broken implementation are called out:
//   - §9.2: a count>1 that re-used the single-click path would emit TWO moves
//     (one per primitive call). The move count is asserted to be 1.
//   - §9.3: a test that only checks "delta -1 did something" survives a flipped
//     sign. The button NUMBER is asserted (4 for up), so a flipped mapping is
//     red, not green.

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// --- the injection seam -----------------------------------------------------

// fakeInput records every wire event the gesture emits, with the time it was
// emitted, and models the keymap §9.4 borrows and restores. `latency` is what
// one Button call costs at the wire; the event is stamped when the call
// returns, i.e. when it is actually on the wire, so a gesture measured through
// this fake sees the wire cost in its inter-press interval.
type fakeInput struct {
	spare    byte
	noSpare  bool
	latency  time.Duration
	bound    map[byte]uint32
	events   []string
	times    []time.Time
	restores int
}

func newFakeInput() *fakeInput {
	return &fakeInput{spare: 0xfb, bound: map[byte]uint32{}}
}

func (f *fakeInput) record(ev string) {
	f.events = append(f.events, ev)
	f.times = append(f.times, time.Now())
}

func (f *fakeInput) Pointer(x, y int) error {
	f.record(fmt.Sprintf("move:%d,%d", x, y))
	return nil
}

func (f *fakeInput) Button(name string, press bool) error {
	edge := "up"
	if press {
		edge = "down"
	}
	if f.latency > 0 {
		time.Sleep(f.latency)
	}
	f.record("btn" + name + ":" + edge)
	return nil
}

func (f *fakeInput) Key(keycode byte, press bool) error {
	edge := "up"
	if press {
		edge = "down"
	}
	sym := f.bound[keycode]
	f.record(fmt.Sprintf("key:%d:0x%x:%s", keycode, sym, edge))
	return nil
}

func (f *fakeInput) SpareKeycode() (byte, error) {
	if f.noSpare {
		return 0, errNoSpareKeycode
	}
	return f.spare, nil
}

func (f *fakeInput) RemapKeycode(keycode byte, sym uint32) error {
	f.bound[keycode] = sym
	f.record(fmt.Sprintf("remap:%d:0x%x", keycode, sym))
	return nil
}

func (f *fakeInput) RestoreKeymap() error {
	for kc := range f.bound {
		delete(f.bound, kc)
	}
	f.restores++
	f.record("restore")
	return nil
}

// fakeInputProbe is a tree display that can also inject: the two halves of the
// same connection, which is what the production seam is.
type fakeInputProbe struct {
	*fakeProbe
	*fakeInput
}

func (f *fakeInputProbe) Close() error { return nil }

// p1402Agent builds an agent whose resolved window IS the injecting display.
func p1402Agent(inj *fakeInput) *Agent {
	p := newFakeProbe()
	const target = 0x3c00007
	p.winAt(0x2, p.rootID, 0, 0, 1, 1) // WM check window
	p.winAt(target, p.rootID, 100, 50, 300, 200)
	p.wm(target, target) // and it owns the focus, so text-input passes the gate
	fp := &fakeInputProbe{fakeProbe: p, fakeInput: inj}
	a := &Agent{plat: &recPlatform{events: &[]string{}}}
	a.openDisplay = func(int) (windowProbe, error) { return fp, nil }
	return a
}

func p1402ClickResult(t *testing.T, env protocol.Envelope) protocol.ClickResult {
	t.Helper()
	if env.Error != nil {
		t.Fatalf("click refused: %s (%v)", env.Error.Message, env.Error.Detail)
	}
	var out protocol.ClickResult
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", string(env.Result), err)
	}
	return out
}

func p1402TextResult(t *testing.T, env protocol.Envelope) protocol.TextResult {
	t.Helper()
	if env.Error != nil {
		t.Fatalf("text-input refused: %s (%v)", env.Error.Message, env.Error.Detail)
	}
	var out protocol.TextResult
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", string(env.Result), err)
	}
	return out
}

func countEvents(events []string, want string) int {
	n := 0
	for _, e := range events {
		if e == want {
			n++
		}
	}
	return n
}

// --- §9.2: count=2 is one gesture ------------------------------------------

func TestP1402DoubleClickIsOneGesture(t *testing.T) {
	inj := newFakeInput()
	a := p1402Agent(inj)

	env := a.handleClick(p1401Envelope(t, "d1", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "x": 10, "y": 20, "button": "left", "count": 2,
	}))
	res := p1402ClickResult(t, env)

	// Order is the whole point: down,up,down,up — and ONE pointer move, because
	// this is one gesture. Two independent pointer-click calls would move twice.
	want := []string{"move:110,70", "btnleft:down", "btnleft:up", "btnleft:down", "btnleft:up"}
	if len(inj.events) != len(want) {
		t.Fatalf("events = %v, want %v", inj.events, want)
	}
	for i := range want {
		if inj.events[i] != want[i] {
			t.Fatalf("events[%d] = %q, want %q (full: %v)", i, inj.events[i], want[i], inj.events)
		}
	}
	if moves := countEvents(inj.events, "move:110,70"); moves != 1 {
		t.Fatalf("pointer moves = %d, want 1: a count>1 click implemented as a loop of single clicks moves once per click", moves)
	}

	// The gap is MEASURED between the two presses the backend emitted.
	gap := inj.times[3].Sub(inj.times[1])
	if gap > defaultDoubleClick/2 {
		t.Fatalf("measured gap %v exceeds half the %v double-click interval", gap, defaultDoubleClick)
	}
	if gap < multiClickGap/2 {
		t.Fatalf("measured gap %v is far below the gesture's own spacing %v: the gap was not emitted, only reported", gap, multiClickGap)
	}
	if res.GapMS <= 0 || res.GapMS != gapMillis(gap) {
		t.Fatalf("gap_ms = %d, measured %d ms: the response must echo the measured gap, not a constant", res.GapMS, gapMillis(gap))
	}
	if res.Count != 2 || !res.Success {
		t.Fatalf("result = %+v, want success count 2", res)
	}

	// Control: count=1 is two events and no gap.
	inj1 := newFakeInput()
	a1 := p1402Agent(inj1)
	env1 := a1.handleClick(p1401Envelope(t, "d2", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "x": 10, "y": 20, "button": "left",
	}))
	res1 := p1402ClickResult(t, env1)
	if len(inj1.events) != 3 || countEvents(inj1.events, "btnleft:down") != 1 {
		t.Fatalf("count=1 events = %v, want one move and one press/release", inj1.events)
	}
	if res1.Count != 1 || res1.GapMS != 0 {
		t.Fatalf("count=1 result = %+v, want count 1 and no gap_ms", res1)
	}
}

// --- R3: the echo must be able to be wrong ---------------------------------

// TestP1402GapEchoTracksMeasuredInterval is R3 (§9.2). The seam's Button call
// costs real time here, so the interval the target perceives is the gesture's
// spacing PLUS that cost. An implementation that echoed a constant — the
// nominal spacing, or a hardcoded 120 — disagrees with the stamps the seam
// booked, so this assertion can tell an echo from a constant. The pre-R3 test
// could not: every term in it derived from the same injected constant.
func TestP1402GapEchoTracksMeasuredInterval(t *testing.T) {
	const latency = 25 * time.Millisecond // what ONE Button wire call costs
	inj := newFakeInput()
	inj.latency = latency
	a := p1402Agent(inj)

	env := a.handleClick(p1401Envelope(t, "g1", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "x": 10, "y": 20, "button": "left", "count": 2,
	}))
	res := p1402ClickResult(t, env)

	// times: [0]=move [1]=down#1 [2]=up#1 [3]=down#2 [4]=up#2, stamped when the
	// event reached the wire.
	measured := inj.times[3].Sub(inj.times[1])
	window := a.effectiveClickWindow()
	nominal := clickSpacing(window)
	t.Logf("R3 numbers: window=%v nominal spacing=%v injected latency=%v/press measured gap=%v gap_ms=%d measured_ms=%d",
		window, nominal, latency, measured, res.GapMS, gapMillis(measured))

	if measured < nominal+2*latency-5*time.Millisecond {
		t.Fatalf("measured gap %v is not materially above the nominal spacing %v with %v/press latency: the injected wire cost never reached the interval, so this test could not tell an echo from a constant", measured, nominal, latency)
	}
	if res.GapMS != gapMillis(measured) {
		t.Fatalf("gap_ms = %d but the seam booked %d ms (%v): the response is not echoing the interval this gesture took", res.GapMS, gapMillis(measured), measured)
	}
	if res.GapMS == gapMillis(nominal) {
		t.Fatalf("gap_ms = %d equals the nominal spacing %v: the echo is a constant, not a measurement", res.GapMS, nominal)
	}
	if measured > window/2 {
		t.Fatalf("measured gap %v exceeds half the effective %v window: the pair would not read as one double-click", measured, window)
	}
}

// TestP1402ConfiguredWindowSetsSpacing is R3's second half: 400 ms is a
// fallback, not a universal. A seat configured at seatWindow must receive
// presses spaced for ITS window, and the emitted gap must sit below ITS
// threshold — the assertion is against the configured value, never a literal.
func TestP1402ConfiguredWindowSetsSpacing(t *testing.T) {
	const latency = 20 * time.Millisecond
	seatWindow := 200 * time.Millisecond // a seat configured below the default
	seatMS := int(seatWindow / time.Millisecond)

	measure := func(name string, window time.Duration) protocol.ClickResult {
		inj := newFakeInput()
		inj.latency = latency
		a := p1402Agent(inj)
		a.clickWindow = window
		env := a.handleClick(p1401Envelope(t, name, map[string]interface{}{
			"context": "x11::0", "window": "0x3c00007", "x": 10, "y": 20, "button": "left", "count": 2,
		}))
		res := p1402ClickResult(t, env)
		measured := inj.times[3].Sub(inj.times[1])
		t.Logf("%s: window=%v spacing=%v measured gap=%v gap_ms=%d", name, window, clickSpacing(window), measured, res.GapMS)
		if res.GapMS != gapMillis(measured) {
			t.Fatalf("%s: gap_ms = %d, seam booked %d ms: echo is not the measurement", name, res.GapMS, gapMillis(measured))
		}
		return res
	}

	def := measure("default window", defaultDoubleClick)
	seat := measure("configured window", seatWindow)

	if seat.GapMS >= seatMS {
		t.Fatalf("gap_ms = %d is not below the seat's own %d ms double-click window: the presses would not read as one double-click", seat.GapMS, seatMS)
	}
	if seat.GapMS >= def.GapMS {
		t.Fatalf("configured gap_ms = %d is not below the default window's %d: the configured threshold did not change the emitted spacing", seat.GapMS, def.GapMS)
	}
	// The spacing itself moved with the window, not only the echo: spacing plus
	// the two wire costs the measured gap must span.
	if want := gapMillis(clickSpacing(seatWindow)) + 2*gapMillis(latency); seat.GapMS < want-5 || seat.GapMS > want+5 {
		t.Fatalf("configured gap_ms = %d, want ≈%d (spacing %v + 2×%v wire cost): spacing was not derived from the configured window", seat.GapMS, want, clickSpacing(seatWindow), latency)
	}
}

func TestP1402ClickCountOutOfRangeIsRefused(t *testing.T) {
	inj := newFakeInput()
	a := p1402Agent(inj)
	env := a.handleClick(p1401Envelope(t, "d3", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "x": 1, "y": 2, "count": 9,
	}))
	if env.Error == nil || env.Error.Code != protocol.ErrInvalidParams {
		t.Fatalf("error = %+v, want INVALID_PARAMS", env.Error)
	}
	if env.Error.Detail["reason"] != protocol.ReasonOutOfRange {
		t.Fatalf("reason = %v, want OUT_OF_RANGE", env.Error.Detail["reason"])
	}
	if len(inj.events) != 0 {
		t.Fatalf("events emitted for a refused count: %v", inj.events)
	}
}

// --- §9.3: the sign convention, mapped ------------------------------------

func TestP1402ScrollSignConvention(t *testing.T) {
	cases := []struct {
		name      string
		delta     int
		wantBtn   string
		wantPairs int
	}{
		// §9.3: negative = up. On X11 up IS button 4, so this asserts the
		// convention and not the table: flipping the mapping turns this red.
		{"delta -1 scrolls up", -1, "btn4", 1},
		{"delta +1 scrolls down", 1, "btn5", 1},
		{"delta -3 is three notches up", -3, "btn4", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inj := newFakeInput()
			a := p1402Agent(inj)
			env := a.handlePointerScroll(p1401Envelope(t, "s", map[string]interface{}{
				"context": "x11::0", "window": "0x3c00007", "x": 10, "y": 20, "delta": tc.delta,
			}))
			if env.Error != nil {
				t.Fatalf("scroll refused: %s (%v)", env.Error.Message, env.Error.Detail)
			}
			var res protocol.ScrollResult
			if err := json.Unmarshal(env.Result, &res); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := countEvents(inj.events, tc.wantBtn+":down"); got != tc.wantPairs {
				t.Fatalf("%s presses = %d, want %d (events %v) — the sign must map to the convention", tc.wantBtn, got, tc.wantPairs, inj.events)
			}
			if other := countEvents(inj.events, "btn5:down") + countEvents(inj.events, "btn4:down"); other != tc.wantPairs {
				t.Fatalf("wheel presses = %d, want only %d on %s", other, tc.wantPairs, tc.wantBtn)
			}
			if res.Delta != tc.delta || res.Notches != tc.wantPairs || res.Unit != "notch" {
				t.Fatalf("result = %+v, want delta %d / %d notches / notch", res, tc.delta, tc.wantPairs)
			}
		})
	}
}

func TestP1402ScrollPixelUnitRefusesWithoutScrolling(t *testing.T) {
	inj := newFakeInput()
	a := p1402Agent(inj)
	env := a.handlePointerScroll(p1401Envelope(t, "sp", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "x": 10, "y": 20, "delta": -40, "unit": "px",
	}))
	if env.Error == nil || env.Error.Code != protocol.ErrPrimitiveUnsupported {
		t.Fatalf("error = %+v, want PRIMITIVE_UNSUPPORTED", env.Error)
	}
	if env.Error.Detail["reason"] != protocol.ReasonUnitNotSupported {
		t.Fatalf("reason = %v, want UNIT_NOT_SUPPORTED", env.Error.Detail["reason"])
	}
	if len(inj.events) != 0 {
		t.Fatalf("a px scroll moved the wheel partially: %v", inj.events)
	}
}

// --- §9.4: the remap path ---------------------------------------------------

func TestP1402TextRoundTripsThroughRemap(t *testing.T) {
	inj := newFakeInput()
	a := p1402Agent(inj)
	const text = "aé日"
	env := a.handleType(p1401Envelope(t, "tx", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "text": text,
	}))
	res := p1402TextResult(t, env)

	// One borrowed keycode, three keysyms, two events per character.
	wantSyms := []uint32{0x61, 0xe9, 0x01000000 | 0x65e5}
	var gotSyms []uint32
	for _, ev := range inj.events {
		if len(ev) > 4 && ev[:4] == "key:" {
			var kc int
			var sym uint32
			var edge string
			fmt.Sscanf(ev, "key:%d:0x%x:%s", &kc, &sym, &edge)
			if edge == "down" {
				gotSyms = append(gotSyms, sym)
			}
		}
	}
	if len(gotSyms) != len(wantSyms) {
		t.Fatalf("keysyms typed = %v, want %v (events %v)", gotSyms, wantSyms, inj.events)
	}
	for i := range wantSyms {
		if gotSyms[i] != wantSyms[i] {
			t.Fatalf("keysym[%d] = 0x%x, want 0x%x: the string did not round-trip", i, gotSyms[i], wantSyms[i])
		}
	}
	// The keymap is RESTORED: nothing borrowed is left bound.
	if len(inj.bound) != 0 {
		t.Fatalf("keymap left bound after the string: %v", inj.bound)
	}
	if inj.restores != 1 {
		t.Fatalf("RestoreKeymap calls = %d, want 1", inj.restores)
	}
	if res.Detail == nil || res.Detail.CodePoints != 3 || res.Detail.Transcoded {
		t.Fatalf("detail = %+v, want 3 codepoints and transcoded:false for utf8", res.Detail)
	}
}

func TestP1402AstralRefusalNamesCodepointAndTypesNothing(t *testing.T) {
	// The range cannot carry it: no spare keycode. The refusal must NAME the
	// astral codepoint, and a string that begins with a typeable character must
	// still not reach the display as a prefix.
	for _, text := range []string{"\U0001F600", "a\U0001F600"} {
		inj := newFakeInput()
		inj.noSpare = true
		a := p1402Agent(inj)
		env := a.handleType(p1401Envelope(t, "ta", map[string]interface{}{
			"context": "x11::0", "window": "0x3c00007", "text": text,
		}))
		if env.Error == nil || env.Error.Code != protocol.ErrPrimitiveUnsupported {
			t.Fatalf("%q: error = %+v, want PRIMITIVE_UNSUPPORTED", text, env.Error)
		}
		if env.Error.Detail["reason"] != protocol.ReasonCodepointUnsupported {
			t.Fatalf("%q: reason = %v, want CODEPOINT_UNSUPPORTED", text, env.Error.Detail["reason"])
		}
		named := env.Error.Detail["codepoint"]
		if named != "U+1F600" && named != "U+0061" {
			t.Fatalf("%q: codepoint = %v, want the codepoint that could not be carried", text, named)
		}
		if text == "\U0001F600" && named != "U+1F600" {
			t.Fatalf("codepoint = %v, want the astral codepoint named (U+1F600)", named)
		}
		if len(inj.events) != 0 {
			t.Fatalf("%q: a partial string was typed before the refusal: %v", text, inj.events)
		}
	}
}

func TestP1402UTF16IsTranscodedOffWindows(t *testing.T) {
	inj := newFakeInput()
	a := p1402Agent(inj)
	env := a.handleType(p1401Envelope(t, "tu", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "text": "héllo", "encoding": "utf16",
	}))
	res := p1402TextResult(t, env)
	if res.Detail == nil || !res.Detail.Transcoded {
		t.Fatalf("detail = %+v, want transcoded:true off Windows", res.Detail)
	}
	if res.Detail.CodePoints != 5 {
		t.Fatalf("codepoints = %d, want 5", res.Detail.CodePoints)
	}
	// The decoded text is what was typed: é is 0xe9, not a code unit split.
	if countEvents(inj.events, "remap:251:0xe9") != 1 {
		t.Fatalf("é was not typed as U+00E9 (events %v)", inj.events)
	}
}

func TestP1402UnknownEncodingIsRefused(t *testing.T) {
	inj := newFakeInput()
	a := p1402Agent(inj)
	env := a.handleType(p1401Envelope(t, "te", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "text": "x", "encoding": "latin1",
	}))
	if env.Error == nil || env.Error.Code != protocol.ErrInvalidParams {
		t.Fatalf("error = %+v, want INVALID_PARAMS", env.Error)
	}
	if len(inj.events) != 0 {
		t.Fatalf("events emitted for an unknown encoding: %v", inj.events)
	}
}
