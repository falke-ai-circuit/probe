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
	"strings"
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

// clickSpacingJitterBudget is how far ABOVE the expected interval one
// measurement may sit before this test calls it wrong.
//
// The expectation is not what a quiet host measures; it is what the gesture
// OWES: the spacing derived from the window, plus the two wire calls the
// interval spans. Every term after that is scheduler overshoot — the measured
// interval spans three sleeps (release of press 1, the spacing, the press of
// press 2), and a busy host legitimately returns from each of them late.
// Recorded: 113.883 ms against a 100 ms expectation at load ~10 (P14B-03),
// 120 ms against the same expectation at load 15.4 (this unit), and 246 ms
// against a 160 ms expectation on the 400 ms window in the same run. A point
// assertion therefore reports the host's load, not the code's behaviour.
//
// The budget is half the derived spacing, which is the largest value that
// still discriminates. For the 200 ms window the two answers are 60 ms apart:
// derived spacing measures ~100 ms, a hardcoded 120 ms measures ~160 ms. Half
// the spacing (30 ms) sits between the worst recorded overshoot (+20 ms) and
// the nearest wrong answer (-30 ms from the ceiling), so lateness up to 130 ms
// is allowed and nothing wrong is.
func clickSpacingJitterBudget(spacing time.Duration) time.Duration {
	return spacing / 2
}

// TestP1402ConfiguredWindowSetsSpacing is R3's second half: 400 ms is a
// fallback, not a universal. A seat configured at seatWindow must receive
// presses spaced for ITS window, and the emitted gap must sit below ITS
// threshold — the assertion is against the configured value, never a literal.
//
// The interval is compared against a BAND, not a point. The lower edge is the
// defect this test exists for (spacing that was never derived, or no spacing
// at all); the upper edge is scheduler jitter, which a loaded host is allowed
// to spend. Nothing early is ever allowed; only lateness is.
func TestP1402ConfiguredWindowSetsSpacing(t *testing.T) {
	const (
		latency = 20 * time.Millisecond
		// The windows are pinned here, not read from the code: the expectation
		// this test judges must not move when the thing it judges moves.
		window400 = 400 * time.Millisecond // the contract's fallback interval
		window200 = 200 * time.Millisecond // a seat configured below the default
		// The contract's share of the window the gesture's spacing occupies.
		// Pinned for the same reason: a fraction is part of the contract, and a
		// change to it is a contract change that must revisit this test.
		spacingNum = 3
		spacingDen = 10
	)
	seatMS := int(window200 / time.Millisecond)

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

	def := measure("default window", window400)
	seat := measure("configured window", window200)

	// The band. Two edges, two different defects, and only one of them is the
	// code's:
	//
	//   lower = spacing + the two wire calls the interval spans. The measured
	//           gap covers the release of press 1, the spacing, and press 2,
	//           so anything shorter means the spacing was not emitted at all
	//           (an unimplemented spacing, or a count>1 handled as a loop of
	//           single clicks). Time.Sleep never returns early, so this edge
	//           needs no allowance and a shorter measurement is always a bug.
	//   upper = lower + clickSpacingJitterBudget. Lateness is the host's, not
	//           the code's: a loaded scheduler returns from each of the three
	//           sleeps late, and the target still reads the pair as one
	//           double-click. Allowed, bounded, and not read as a defect.
	//
	// Both edges are computed from the window and the pinned fraction, NOT by
	// calling clickSpacing. That distinction is the band's whole strength: an
	// implementation that hardcoded its spacing would otherwise move the
	// expectation with the defect. Measured on a throwaway copy with
	// clickSpacing hardcoded to 120 ms, the edges computed from the function
	// slid to floor=160/ceiling=220 and the 161 ms measurement passed the band,
	// leaving only an ordering assertion to report it. With the expectation
	// computed here, the same copy measures 161 ms against floor=100/ceiling=130
	// and the band is what goes red.
	//
	// The band runs before the threshold checks below so that a spacing which
	// is not derived from the window is reported as the band violation it is,
	// rather than as whichever ordering assertion happens to trip first.
	wantSeat := window200 * spacingNum / spacingDen
	floor := gapMillis(wantSeat + 2*latency)
	ceiling := floor + gapMillis(clickSpacingJitterBudget(wantSeat))
	t.Logf("configured window band: window=%v test-derived spacing=%v (code's clickSpacing=%v) floor=%dms ceiling=%dms (jitter budget %v) measured gap=%dms",
		window200, wantSeat, clickSpacing(window200), floor, ceiling,
		clickSpacingJitterBudget(wantSeat), seat.GapMS)
	if seat.GapMS < floor {
		t.Fatalf("configured gap_ms = %d is below the band's floor %dms (spacing %v + 2×%v wire cost): the spacing was not derived from the configured window, or was not emitted",
			seat.GapMS, floor, wantSeat, latency)
	}
	if seat.GapMS > ceiling {
		t.Fatalf("configured gap_ms = %d is above the band's ceiling %dms (floor %d + %v jitter budget): the emitted interval is not the %v spacing this window derives under the contract's %d/%d fraction (a hardcoded %v spacing measures %dms)",
			seat.GapMS, ceiling, floor, clickSpacingJitterBudget(wantSeat), wantSeat, spacingNum, spacingDen,
			multiClickGap, gapMillis(wantSeat+2*latency)+gapMillis(multiClickGap-wantSeat))
	}

	if seat.GapMS >= seatMS {
		t.Fatalf("gap_ms = %d is not below the seat's own %d ms double-click window: the presses would not read as one double-click", seat.GapMS, seatMS)
	}
	if seat.GapMS >= def.GapMS {
		t.Fatalf("configured gap_ms = %d is not below the default window's %d: the configured threshold did not change the emitted spacing", seat.GapMS, def.GapMS)
	}

	// The 400 ms window gets the same floor and NO ceiling, deliberately. Its
	// floor proves the default window's spacing was emitted; an upper edge
	// there could not discriminate, because a hardcoded 120 ms produces the
	// same interval the 400 ms window derives. The only upper edge that carries
	// information is the 200 ms one above, where the two answers are 60 ms
	// apart — and it is quoted as such: 246 ms was measured against this
	// window's 160 ms expectation at load 15.4, so a ceiling here would report
	// the host again.
	if want := gapMillis(window400*spacingNum/spacingDen + 2*latency); def.GapMS < want {
		t.Fatalf("default gap_ms = %d is below the %dms this window derives (spacing %v + 2×%v wire cost): the fallback window emitted no spacing",
			def.GapMS, want, window400*spacingNum/spacingDen, latency)
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
	// rune that could not be carried, wherever it sits in the string, and a
	// string that begins with a typeable character must still not reach the
	// display as a prefix. Every row states ONE expected codepoint: an "either"
	// that also accepts U+0061 accepts the string's first character, which is
	// the defect this test exists to catch.
	for _, tc := range []struct {
		name string
		text string
		want string
		at   int
	}{
		{"lone astral", "\U0001F600", "U+1F600", 0},
		{"astral mid-string", "a\U0001F600", "U+1F600", 1},
		{"astral last of three", "aé\U0001F600", "U+1F600", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inj := newFakeInput()
			inj.noSpare = true
			a := p1402Agent(inj)
			env := a.handleType(p1401Envelope(t, "ta", map[string]interface{}{
				"context": "x11::0", "window": "0x3c00007", "text": tc.text,
			}))
			if env.Error == nil || env.Error.Code != protocol.ErrPrimitiveUnsupported {
				t.Fatalf("%q: error = %+v, want PRIMITIVE_UNSUPPORTED", tc.text, env.Error)
			}
			if env.Error.Detail["reason"] != protocol.ReasonCodepointUnsupported {
				t.Fatalf("%q: reason = %v, want CODEPOINT_UNSUPPORTED", tc.text, env.Error.Detail["reason"])
			}
			if named := env.Error.Detail["codepoint"]; named != tc.want {
				t.Fatalf("%q: codepoint = %v, want %s (the rune the mapper could not carry)", tc.text, named, tc.want)
			}
			if at := env.Error.Detail["index"]; at != tc.at {
				t.Fatalf("%q: index = %v, want %d", tc.text, at, tc.at)
			}
			if len(inj.events) != 0 {
				t.Fatalf("%q: a partial string was typed before the refusal: %v", tc.text, inj.events)
			}
		})
	}
}

func TestP1402FailedBorrowNamesNoRune(t *testing.T) {
	// Every rune here is carryable on its own; what ran out is the borrowed
	// keycode for the whole string. No character can be blamed, so the refusal
	// must carry no codepoint at all rather than naming the first one.
	inj := newFakeInput()
	inj.noSpare = true
	a := p1402Agent(inj)
	env := a.handleType(p1401Envelope(t, "tn", map[string]interface{}{
		"context": "x11::0", "window": "0x3c00007", "text": "abc",
	}))
	if env.Error == nil || env.Error.Code != protocol.ErrPrimitiveUnsupported {
		t.Fatalf("error = %+v, want PRIMITIVE_UNSUPPORTED", env.Error)
	}
	if env.Error.Detail["reason"] != protocol.ReasonCodepointUnsupported {
		t.Fatalf("reason = %v, want CODEPOINT_UNSUPPORTED", env.Error.Detail["reason"])
	}
	if named, ok := env.Error.Detail["codepoint"]; ok {
		t.Fatalf("codepoint = %v, want the field absent: the failure is not rune-specific", named)
	}
	if idx, ok := env.Error.Detail["index"]; ok {
		t.Fatalf("index = %v, want the field absent: the failure is not rune-specific", idx)
	}
	if strings.Contains(env.Error.Message, "U+") {
		t.Fatalf("message %q names a codepoint the mapper did not identify", env.Error.Message)
	}
	if len(inj.events) != 0 {
		t.Fatalf("a partial string was typed before the refusal: %v", inj.events)
	}
}

func TestP1402InvalidUTF8NamesNoRune(t *testing.T) {
	// planText refuses invalid bytes before the rune loop runs, so there is no
	// rune to point at: the refusal must leave the codepoint field out instead
	// of borrowing the first character of a string it never decoded.
	inj := newFakeInput()
	_, _, perr := planText(inj, "a\xffb")
	if perr == nil {
		t.Fatal("planText accepted invalid UTF-8")
	}
	env := codepointUnsupported(p1401Envelope(t, "ti", nil), perr)
	if env.Error == nil || env.Error.Code != protocol.ErrPrimitiveUnsupported {
		t.Fatalf("error = %+v, want PRIMITIVE_UNSUPPORTED", env.Error)
	}
	if env.Error.Detail["reason"] != protocol.ReasonCodepointUnsupported {
		t.Fatalf("reason = %v, want CODEPOINT_UNSUPPORTED", env.Error.Detail["reason"])
	}
	if named, ok := env.Error.Detail["codepoint"]; ok {
		t.Fatalf("codepoint = %v, want the field absent: an invalid byte is not a rune", named)
	}
	if strings.Contains(env.Error.Message, "U+") {
		t.Fatalf("message %q names a codepoint the mapper did not identify", env.Error.Message)
	}
	if len(inj.events) != 0 {
		t.Fatalf("invalid UTF-8 reached the display: %v", inj.events)
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

// --- P14B-02 / R4: a lone surrogate must not become U+FFFD in silence --------
//
// The two bodies below decode to the SAME Go string ("a\ufffdb"): Go's
// encoding/json repairs an unpaired code unit before any caller can see it. So
// the decoded text cannot be the discriminator — only the raw escape can. The
// detector reads env.Params, and these tests assert the two DIFFERENT outcomes
// on bodies that are identical after decode.

// p1402RawEnvelope keeps the caller's bytes verbatim. p1401Envelope marshals a
// map, which cannot express a lone surrogate: the repair would happen in the
// test instead of in the code under test.
func p1402RawEnvelope(id, params string) protocol.Envelope {
	return protocol.Envelope{ID: id, Params: json.RawMessage(params)}
}

// p1402Detail decodes `detail` as a map so a key the result type does not have
// YET is still observable — that is what makes the before-run red rather than a
// compile error.
func p1402Detail(t *testing.T, env protocol.Envelope) map[string]interface{} {
	t.Helper()
	if env.Error != nil {
		t.Fatalf("text-input refused: %s (%v)", env.Error.Message, env.Error.Detail)
	}
	var out struct {
		Detail map[string]interface{} `json:"detail"`
	}
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", string(env.Result), err)
	}
	return out.Detail
}

func TestP14B02LoneSurrogateIsLabelled(t *testing.T) {
	const prefix = `{"context":"x11::0","window":"0x3c00007","text":"a`
	// Identical to the byte after the escape: \ud800 is repaired, \ufffd is
	// the character the caller meant.
	loneEnv := p1402RawEnvelope("tl", prefix+`\ud800b"}`)
	realEnv := p1402RawEnvelope("tf", prefix+`\ufffdb"}`)

	var loneParams, realParams protocol.InputParams
	if err := json.Unmarshal(loneEnv.Params, &loneParams); err != nil {
		t.Fatalf("unmarshal lone body: %v", err)
	}
	if err := json.Unmarshal(realEnv.Params, &realParams); err != nil {
		t.Fatalf("unmarshal fffd body: %v", err)
	}
	if loneParams.Text != realParams.Text {
		t.Fatalf("premise broken: %q != %q — the two bodies are supposed to be identical after decode",
			loneParams.Text, realParams.Text)
	}

	lone := p1402Detail(t, p1402Agent(newFakeInput()).handleType(loneEnv))
	if got := lone["repaired_rune_count"]; got != float64(1) {
		t.Fatalf("a\\ud800 was repaired in silence: detail = %v, want repaired_rune_count 1", lone)
	}
	at, ok := lone["repaired_at"].([]interface{})
	if !ok || len(at) != 1 || at[0] != float64(2) {
		t.Fatalf("repaired_at = %v, want [2] (the escaped unit is the 2nd code unit)", lone["repaired_at"])
	}

	real := p1402Detail(t, p1402Agent(newFakeInput()).handleType(realEnv))
	if got, ok := real["repaired_rune_count"]; ok {
		t.Fatalf("a caller-sent U+FFFD was labelled as a repair (repaired_rune_count=%v): detail = %v", got, real)
	}
}

func TestP14B02AstralPairIsNotLabelled(t *testing.T) {
	cases := map[string]string{
		// The literal body carries the character as UTF-8; the escaped body
		// carries the two code units the caller would write on the wire.
		"literal": "{\"context\":\"x11::0\",\"window\":\"0x3c00007\",\"text\":\"\U0001F600\"}",
		"escaped": `{"context":"x11::0","window":"0x3c00007","text":"\ud83d\ude00"}`,
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			inj := newFakeInput()
			det := p1402Detail(t, p1402Agent(inj).handleType(p1402RawEnvelope("ta", params)))
			if _, ok := det["repaired_rune_count"]; ok {
				t.Fatalf("%s: a valid astral pair was labelled as repaired: %v", name, det)
			}
			if det["codepoints"] != float64(1) {
				t.Fatalf("%s: codepoints = %v, want 1 (one codepoint, two code units)", name, det["codepoints"])
			}
			if countEvents(inj.events, "remap:251:0x101f600") != 1 {
				t.Fatalf("%s: the astral character was not typed as U+1F600: %v", name, inj.events)
			}
		})
	}
}

func TestP14B02CallerReplacementCharacterIsItself(t *testing.T) {
	// U+FFFD is a legitimate character. Typing it must not be re-reported as
	// something probe did to the caller's text.
	inj := newFakeInput()
	env := p1402RawEnvelope("tr", `{"context":"x11::0","window":"0x3c00007","text":"a\ufffdb"}`)
	det := p1402Detail(t, p1402Agent(inj).handleType(env))
	if _, ok := det["repaired_rune_count"]; ok {
		t.Fatalf("caller-sent U+FFFD reported as a repair: %v", det)
	}
	if countEvents(inj.events, "remap:251:0x100fffd") != 1 {
		t.Fatalf("U+FFFD was not typed as U+FFFD: %v", inj.events)
	}
}
