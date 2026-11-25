# P14-02 — CLOSE

**Commit:** `8cdbb18` (branch `r119-build`, pushed to origin, tree clean)
**Review:** reviewed_by: `PENDING — review leg owed` · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `8cdbb18`
**Scope:** contract §9.2 multi-click gesture, §9.3 scroll sign convention and the
`unit:"px"` refusal, §9.4 unicode typing via keycode remap. §9.1 geometry
(`23cf971`), §10 capture (P14-03) and every `NO_BACKEND` route not named above
are untouched.

## Delivered

- **§9.2** `pointer-click count=2` is one primitive call emitting
  `down,up,down,up` on the display connection §9.1 already opened; the gap
  between presses is 120 ms (30% of the 400 ms X11 interval, inside the 50%
  ceiling) and is ECHOED AS MEASURED in `gap_ms`. `count=1` is unchanged. A
  backend with no multi-click mechanism answers 501 `NO_BACKEND` — never a loop
  of two independent clicks.
- **§9.3** `pointer-scroll` exists: `delta` in notches, negative = up → X11
  button 4, positive → button 5, one press/release per notch; `unit:"px"` is 501
  `UNIT_NOT_SUPPORTED` with no wheel event emitted.
- **§9.4** `text-input` types through `XChangeKeyboardMapping` on a spare keycode
  discovered by scanning the map, `XTestFakeKeyEvent` per codepoint, map restored
  after the string (read back and compared in the wire test). The whole string is
  validated before the first remap: a codepoint the keycode range cannot carry is
  501 `CODEPOINT_UNSUPPORTED` naming it, with no prefix typed. `encoding:"utf16"`
  reports `detail.transcoded:true` off Windows; on Windows it is emitted as
  UTF-16 units.

## Verification

`go test ./... -count=1 -v` → **727 pass / 0 fail / 2 skip** (baseline `23cf971`
714/0/2). `go vet ./...` clean. `GOOS=windows GOARCH=amd64 go build ./...` clean.
Wire tests run against a real Xvfb: keymap read-back identical after the string,
and every faked event accepted (queued X errors surface through the sync).
Details, the two measured wire findings (FakeInput = 9 units; ChangeKeyboardMapping
offset-5 byte) and the four stated deviations are in `P14-02_INPUT.md` §3-§4.

## Open item

Live-desktop effect is **UNPROVEN ON THIS HOST**: no window manager, so no
application received a real double-click, wheel notch or glyph — only the server's
acceptance and the keymap round-trip are measured. Windows `SendInput`/
`mouse_event` paths compile and are unrun (contract column `[S]`). The §9.4
`encoding:"utf16"` reading (JSON decoding happens before the agent sees the
string) is the review leg's to rule on, as P14-01 flagged its own deviations.
