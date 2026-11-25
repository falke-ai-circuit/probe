# P14-02 — §9.2 multi-click, §9.3 scroll sign, §9.4 unicode text

Commit: `8cdbb18` on `r119-build` (pushed, tree clean)
Unit: P14-02 · workflow wf_probe1 · owner coder

## 1. What was built, and where

| item | code | seam |
|---|---|---|
| §9.2 multi-click | `internal/agent/input_pointer.go` (`emitClickGesture`), `internal/agent/agent.go` (`handleClick`) | the display connection §9.1 already opens; `multiClicker` for the Windows plan |
| §9.3 scroll | `internal/agent/input_pointer.go` (`scrollButton`, `emitScroll`, `handlePointerScroll`), `internal/agent/x11_input.go` | same display; `resolveInputPoint` reused unchanged |
| §9.4 unicode | `internal/agent/input_pointer.go` (`planText`, `emitText`, `keysymFor`), `internal/agent/x11_input.go` (`SpareKeycode`, `RemapKeycode`, `RestoreKeymap`) | same display |
| results | `internal/protocol/input_geometry.go` (`ClickResult`, `ScrollResult`, `TextResult`, `UNIT_NOT_SUPPORTED`, `CODEPOINT_UNSUPPORTED`) | |

One geometry path: a pointer or text primitive goes through `resolveInputTarget` /
`resolveInputPoint` (P14-01, `23cf971`) and then injects on the SAME connection
that measured the rect and ran the hit-test. No second window lookup, no second
rect computation. The injection half is an OPTIONAL interface (`inputSeam`) on
that connection; a display client that cannot inject falls back to the platform
layer exactly as before, and a primitive with no injector answers `NO_BACKEND`
rather than inventing a path.

`pointer-scroll` moved out of `agent_strict.go` (where it was a `NO_BACKEND`
stub) into `input_pointer.go`. No other `NO_BACKEND` route was touched.

## 2. Acceptance, each with the test that fails without it

**1. count=2 is one gesture.** `TestP1402DoubleClickIsOneGesture`
(`internal/agent/p1402_input_test.go`). Asserts the emitted sequence is exactly
`move, down, up, down, up`; the gap between the first and second press, MEASURED
from the seam's timestamps, is ≤ 200 ms (half of the 400 ms interval) and ≥ 60 ms
(the gesture's own spacing, so a reported-but-not-emitted gap is red); and
`gap_ms` equals that measurement. The move count is asserted to be **1** — a
count>1 implemented as a loop of single-click calls would move twice, which is
the failure the contract names. Control: count=1 emits `move, down, up`,
`count:1`, no `gap_ms`.

**2. Scroll sign.** `TestP1402ScrollSignConvention`: `delta:-1` must produce
**button 4** (up/away), `delta:+1` → button 5, `delta:-3` → three button-4 pairs.
The assertion is on the button number derived from the convention, not on a
number copied from §9.3's table: flipping the mapping turns -1 and +1 red.
`TestP1402ScrollPixelUnitRefusesWithoutScrolling`: `unit:"px"` → 501
`PRIMITIVE_UNSUPPORTED reason:"UNIT_NOT_SUPPORTED"` and **zero** wheel events.

**3. Unicode.** `TestP1402TextRoundTripsThroughRemap`: `"aé日"` emits keysyms
`0x61, 0xe9, 0x1000000|0x65e5`, two key events each, `RestoreKeymap` called once,
and the fake keymap is empty afterwards. `TestP1402AstralRefusalNamesCodepointAndTypesNothing`:
with no spare keycode, `"\U0001F600"` refuses 501 `CODEPOINT_UNSUPPORTED`
naming `U+1F600`; `"a\U0001F600"` refuses with nothing typed (no prefix).
`TestP1402UTF16IsTranscodedOffWindows`: `encoding:"utf16"` sets
`detail.transcoded:true` off Windows and types U+00E9 (not a split code unit).
`TestP1402UnknownEncodingIsRefused`: `latin1` → 400 `OUT_OF_RANGE`.

Against a REAL X server (Xvfb, no WM needed for these):
`TestP1402X11WireKeymapIsRestored` reads the borrowed keycode's keymap BEFORE the
string, types `"aé日"` through the wire, reads it back, and asserts the two maps
are identical and `x11Conn.saved` is empty — a restore of the wrong symbols is
red here. `TestP1402X11WireFakeInputAccepted` proves the server accepts the
motion, button 4/5 and key events (each injection is followed by a GetInputFocus
sync that surfaces a queued X error).

## 3. Wire findings that are load-bearing (measured, not assumed)

- `XTestFakeInput` is minor opcode 2 with **9 units / 36 bytes**. 8 units is
  answered `BadLength`; measured both ways against Xvfb.
- `XChangeKeyboardMapping` must carry the keysyms-per-keycode byte at offset 5
  (`req[5]`), matching the bytes libX11 itself writes. Sending 0 there is
  answered `BadLength` even with an otherwise correct 12-byte request. Capture
  method: `XChangeKeyboardMapping` via libX11 succeeded and changed the map;
  an LD_PRELOAD `write`/`writev` logger recorded libX11's bytes
  (`64010300fa01000061000000…`); feeding that literal to this client succeeded
  (a `MappingNotify` event came back) while the same request with offset 5 = 0
  failed. `RestoreKeymap` writes the same shape with the saved width.
- `MappingNotify` is broadcast to every client on a keymap change, selected or
  not. The reply readers (`sync`, `readReply`) therefore SKIP event packets;
  treating one as a reply desynchronises the stream (observed as "unexpected
  packet type 34").

## 4. Deviations and unproven paths, stated for the review

- **§9.4 `encoding:"utf16"` reading.** The wire carries a JSON string, so Go's
  decoder is where code units are assembled before probe sees anything: an
  unpaired surrogate never reaches the agent as a rune. What this unit
  implements is: on Windows the text is emitted as UTF-16 code units
  (`SendInput`/`KEYEVENTF_UNICODE`, one event per unit, surrogate pair = two
  events); everywhere else it is typed as UTF-8 codepoints and
  `detail.transcoded:true` says the caller's encoding was not the emission
  encoding. "Probe must not re-encode" is honoured to the extent the wire
  allows; the decode happens in the JSON layer, not here. Flagged, not hidden.
- **`gap_ms` is measured at the emitter**, between the two press writes, not
  from server timestamps: reading server event times would require selecting
  ButtonPress on a window, and this substrate has no WM and no focused surface.
  The number is an observation of this gesture, not the constant.
- **Live-desktop assertions are UNPROVEN ON THIS HOST.** No window manager:
  window-scoped clicks/scrolls/text go through the fake seam and the Xvfb wire
  tests, but no application received a real click or a real glyph. What is
  proven is that the server ACCEPTS every request (no X error) and that the
  keymap round-trips. The EFFECT is unproven.
- **Windows multi-click and UTF-16 emission are UNPROVEN**: pure-Go
  `SendInput`/`mouse_event` implementations that compile under
  `GOOS=windows GOARCH=amd64 go build ./...`, never run. The contract marks that
  column `[S]` (W-6/W-8).
- **count range is 1..3** (triple-click is the same gesture; beyond that the
  caller is selecting a range). Outside that: 400 `OUT_OF_RANGE` with
  `detail.allowed`.
- **Text/scroll with no `window`**: the display is opened for INJECTION only
  (no rect, no hit-test — there is no named target to test), matching §9.1
  bullet 1. Click with no window and `count=1` keeps the platform path untouched
  (`TestP1401WindowAbsentKeepsAbsolutePoint` still passes and still asserts no
  display is opened); `count>1` with no window opens the display for the gesture.

## 5. Verification

- `go test ./... -count=1 -v` at `8cdbb18`: **727 pass / 0 fail / 2 skip**
  (baseline `23cf971`: 714/0/2; +13 = 11 new tests + 2 wire tests, the scroll
  test counting its three subtests).
- `go vet ./...`: clean.
- `GOOS=windows GOARCH=amd64 go build ./...`: clean.
- `git status -sb` after push: `## r119-build...origin/r119-build`, clean.
