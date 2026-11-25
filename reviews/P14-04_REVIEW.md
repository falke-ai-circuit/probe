# P14-04 — independent review of the P14 change set (P14-01, P14-02, P14-03)

VERDICT FAIL /opt/data/tmp/probe119/P14-04_REVIEW.md 3b32753 736 pass / 0 fail / 2 skip (3/3 runs)

Reviewed commits: `23cf971` (P14-01), `8cdbb18` (P14-02), `fd7d548` (P14-03). Head at review:
`3b32753`; every commit above `fd7d548` is documentation (`reviews/`-only, `git show --stat`), and
`git diff --stat fd7d548 HEAD -- internal/` is empty, so the code judged here is the code at head.
Reviewer: independent (neither author nor sequencer). Contract: `docs/contracts/P10-07_CONTRACT_V3_1.md`
§9.1-9.4, §10, §11.

## Per-item verdict table — 5 of 6 PASS, 1 FAIL

| # | item | verdict | evidence (committed blobs, `git show <sha>:<path>`) |
|---|---|---|---|
| 1 | §9.1 geometry | **FAIL** | The four stated sub-items hold in the seam, and the wire hit-test that answers them does not. *Dispatch-time rect*: `23cf971:internal/agent/geometry.go:243-256` re-reads `Geometry`+`TranslateToRoot` and calls the same `outerRect` the tree uses (`:258`); test `:153-189` moves the window 100,50→140,80 between tree read and dispatch and asserts the click at **150,100**, with the tree rect (110,70) named as the failing alternative. *Occlusion refuses with zero input*: gate at `geometry.go:135-140`, before `plat.Click` at `agent.go:1119-1127`; test `p1401_geometry_test.go:208-245` counts the platform's events (`:240`). *404 before injection*: `geometry.go:103-107` returns before a target exists; test `:272-296` (`:294`). *Focus gate three-way*: `geometry.go:193-227`; test `:301-360` (403/NO_FOCUS_TARGET with no keystroke; order `[foreground, type]`; `FOREGROUND_DENIED` when the WM ignores it). **FAIL is the mechanism**: `23cf971:internal/agent/x11_point.go:41-43` decodes the `TranslateCoordinates` reply four bytes early — child from `reply[4:8]` (the reply's **length** word, always 0), dx/dy from `reply[8:10]`/`[10:12]` (the child word). Correct offsets are child `8:12`, dx `12:14`, dy `14:16`; `translateToRoot` in the same file uses the correct ones (`x11.go:262-274`). Measured on Xvfb (`E1`): raw reply `child@8=0x200001 dx@12=15 dy@14=25`; `PointIn(root,15,25)` returned `child=0x0 dx=1 dy=32`; `occludingWindow(requested=0x200001, 15,25)` returned `hit=0x21f` (the root) with `path=[root]`. Since `child` is always 0 the descent never leaves the root, `len(path)==1` (`geometry.go:171-175`) returns the root as the covering window, and **every window-scoped pointer primitive on a real X display answers OCCLUDED and injects nothing**. Companion defect on the same path: `x11.go:317-327` reads 32 bytes of a 44-byte `GetWindowAttributes` reply (length=3), leaving 12 bytes in the stream; measured `geometry#1 ok / attributes ok / geometry#2 "x11 error 0"`. `occludingWindow` calls `Attributes` at every descent step (`geometry.go:160-165`), so the fix for the offsets alone would desync the connection. With both corrected in a throwaway copy the descent works: `path=[root, 0x200001]`, `hit=0`. |
| 2 | §9.2 multi-click | **PASS** | `8cdbb18:internal/agent/input_pointer.go:114-135` emits **one** `Pointer` then `count` × (press, release), spacing by `multiClickGap` and measuring `presses[1]-presses[0]` (`:131-133`); `agent.go:1136-1143` returns `gap_ms = gapMillis(measured)`. Test `p1402_input_test.go:150-201`: sequence exactly `move,down,up,down,up`, `countEvents(move)==1` (`:170-172`) — a loop of single clicks emits two moves and fails — gap `<= defaultDoubleClick/2` (`:176-178`, 200 ms) and `gap_ms == gapMillis(gap)` (`:182-184`); control `count=1` has no `gap_ms` (`:191-201`). Two residuals, R3. |
| 3 | §9.3 scroll sign | **PASS** | `input_pointer.go:171-179` `scrollButton` derives the button from the sign rule (`notch<0`→`"4"`), `:184-205` emits one press/release pair per notch; `x11Button` maps `"4"`→4 (`internal/agent/x11_input.go:42-54`). `unit:"px"` is refused **before** the display is opened (`:366-386`; resolver only at `:408`), so no wheel event exists; test `p1402_input_test.go:263-278` asserts `len(events)==0`. Test `:223-260` asserts the button *number* per delta. Falsification reproduced (below). |
| 4 | §9.4 unicode | **PASS (one named defect)** | Whole string planned before any state change: `input_pointer.go:242-279` maps every rune and borrows the keycode first; `emitText` is the only remap site and defers `RestoreKeymap` (`:285-302`), so a refusal emits no prefix. Astral with no spare keycode refuses 501 naming U+1F600 and types nothing (test `:325-353`). Restore is read back on a real server: `p1402_x11_test.go:19-71` compares the borrowed keycode's map before/after and asserts `x.saved` empty — **ran, not skipped** (`--- PASS: TestP1402X11WireKeymapIsRestored 0.11s`). Defect R6: `codepointUnsupported` (`:306-319`) names the **first rune of the text**, so `"a\U0001F600"` reports U+0061, and the test explicitly accepts that (`:344` allows `U+0061`) while its comment claims "the codepoint that could not be carried". |
| 5 | §10 capture | **PASS** | `fd7d548:internal/agent/capture.go:278` hashes `frame.Pix`; encoding happens only at `:317-329`, so encode-after-hash is structural. Falsification reproduced (below): hashing the encoded bytes reddens acceptance 1 with `sha256:6d2186b2…`, the digest the unit reported. `hash_only` key absence is `omitempty` on `messages.go:563-564` and the test asserts **key absence** in the decoded JSON (`p1403_capture_test.go:158-197`), not emptiness. `validateCaptureFrame` `:334-345` → 500 `stage:"decode"` `:412-423`; test `:199-228` (3 subtests: zero dims, short buffer, nil frame). `changed:null` without baseline and `since` not poisoning the cache: `:280-302` (`put` only when `since==""`), test `:230-281` (`:272-277`). Boundary `>` at `:245-247` against `CaptureMaxPixels=8294400` (`messages.go:581`), test `:283-322` (3840×2160 accepted with one backend call, 3840×2161 refused with `max_px` and zero backend calls). Source honesty: `capture_linux.go:72,86,108` (`screen`/`region`→`screen_region`, `window`→`window_direct`); real-Xvfb test `:327-390` decodes the png and reads pixel (5,5) `#3366cc`. |
| 6 | suite | **PASS** | `GOTOOLCHAIN=local go test ./... -count=1` at `3b32753`, run 3× from the clean worktree: **736 pass / 0 fail / 2 skip, exit 0, each run**. Skips: `TestDownloadURL_LiveCrossProcessProof`, `TestV1_CreateTransfer` — the two the units name. Logs: `/opt/data/tmp/probe119/P14-04_run{1,2,3}.log`. This matches P14-03's 736 and P14-02's +13 / P14-01's +14 deltas. |

Citations above were extracted with `git show <sha>:<path>` and each range's first/last line checked
against the symbol it is cited for; no cited range misses its change.

## Falsifications — re-run in throwaway copies (`git archive <sha> | tar -x`), never the worktree

| unit | claimed | what I did | observed |
|---|---|---|---|
| P14-02 | flipped scroll mapping reddens the sign test | swapped `notch<0`→`"5"` in a copy of `8cdbb18` | `--- FAIL: TestP1402ScrollSignConvention` (3/3 subtests red): `btn4 presses = 0, want 1 (events [move:110,70 btn5:down btn5:up])` |
| P14-01 | the gate tests are route-discriminating | (a) made `occludingWindow` return 0; (b) made the refusal also fire a click before returning | (a) red at `:222` (no observed failure produced); (b) red at `:241` `input was emitted despite OCCLUDED: clicks=[[42 96 left]]` **and** `:295` `input was emitted before the window check: [click]` — the zero-event assertions bite independently |
| P14-03 | a file-hashing implementation reddens acceptance 1 | hashed the encoded base64 instead of the pixels | `--- FAIL: TestP1403PixelHashSurvivesReEncoding` — `hash = "sha256:6d2186b21cab…" want … "sha256:fb64a5f36379…"`; the **same digest the unit printed**, so that record is reproducible, not asserted |

Five further probes were run against the wire on Xvfb (`startBareXvfb` + `x11TestWindow`, no WM needed):
raw `TranslateCoordinates`, `PointIn`, `occludingWindow`, the `Attributes` desync pair, and the same
two with the offsets/drain corrected. Results are item 1's FAIL evidence. One line on scope: the
correction was applied **only** in the throwaway probe copy to prove the fix is sufficient — the
worktree was not modified (P14-04 is review-only).

## The three deviations — rulings

**1. §9.1 `XQueryPointer`+`XTranslateCoordinates` → `TranslateCoordinates` descent: the question is
the right one, the answer as shipped is wrong.**
What the literal pair gives a caller: `XQueryPointer` names the window under the pointer's **current**
position — for the computed point `rect.origin + x,y` it answers a different question unless the
operator's pointer is warped there, which mutates the desktop to run a test. What the descent gives
(once decoded correctly): `XTranslateCoordinates(src=root, dst=W)` at the point returns precisely "the
topmost child of W containing this point"; walking that reply from the root is X's own pick order, so
it answers §9.1's question with no side effect. Same question, better mechanism — **the substitution
is legitimate**. What a caller actually gets from the shipped code: `child` read from the length word,
always 0, so the descent stops at the root and `OCCLUDED` names the root (measured `hit=0x21f`): a
blanket refusal, which a caller can neither act on nor distinguish from a real occlusion. *Unmapped-but-alive*: normally
absent from `_NET_CLIENT_LIST`, so the 404 fires (`geometry.go:103-107`) — nothing is injected; if a WM
keeps it listed, the walk stops at the first non-viewable child (`:160-165`) and names what is
actually on top → `OCCLUDED` with `ran:false`, which is honest (a click there would not reach the
requested window). Neither branch can fire input. Ruling: mechanism ACCEPTED, implementation FAIL.

**2. §9.4 `encoding:"utf16"`: honest about the encoding change, not lossless at the wire — and not
described as lossless.**
Measured (`E1`, `encoding/json`, Go 1.24.4): `"\ud800"` decodes with **no error** to `U+FFFD`;
`"a\ud800b"` → `a U+FFFD b`. So the code units are assembled — and repaired — in the JSON layer
before the agent exists, exactly as the unit argues. Off Windows the string is then typed as UTF-8
codepoints and `detail.transcoded:true` reports that the caller's encoding was not the emission
encoding; on Windows `utf16.Encode([]rune(text))` re-emits units (`agent.go:1218-1229`). For every
string that is valid Unicode the path is lossless and the flag is true. The residual is the input that
is not: an unpaired surrogate is silently replaced and typed, and `transcoded:true` says nothing about
a replacement — so §9.4's "never silently lossy" is not met for that class. It is not a lossy path
**labelled** lossless (no lossless claim is made anywhere), and the unit flagged the wire limit rather
than hiding it. Ruling: ACCEPTED with residual R4 (contract-wide; detectable only by scanning the raw
params before decode).

**3. §10 compositor path: refusal acceptable, envelope class wrong.**
`capture_linux.go:89-109` calls `XGetImage` on the window drawable; a redirected (compositor-owned)
window answers an X error, which `capture.go:439-449` renders as 500 `INTERNAL_ERROR`
`detail{stage:"capture"}`. No caller can read that as a captured window: there is no `window_direct`,
no `source`, no pixels. So the essential risk — a frame labelled as the window when it is not — is
handled. Two things are not: the agent cannot *detect* redirection (it reports whatever X error it
gets, so a dead window and a composited one are the same answer), and §11 licenses 501
`PRIMITIVE_UNSUPPORTED reason:"NOT_EXPOSED"` for "this deployment cannot obtain it", while 500 tells
the operator the agent broke. Ruling: ACCEPTABLE FOR THE PHASE as a refusal, mislabelled as to class;
remains the unit's open item (R5).

## Residuals

| # | residual | tier |
|---|---|---|
| R1 | `x11_point.go:41-43` reply offsets — window-scoped pointer input is 100 % OCCLUDED on a real X display | **M1, blocking** |
| R2 | `x11.go:317-327` `GetWindowAttributes` leaves 12 bytes unread — desyncs the connection on the first `Attributes` in the descent (measured) | **M1, blocking (companion)** |
| R3 | `gap_ms` echo is not discriminating (sleep is a constant 120 ms and `gapMillis` rounds, so a hardcoded 120 passes `:182-184`); `defaultDoubleClick=400` is a build constant — a seat configured below 240 ms would not read 120 ms as a double-click | M2 |
| R4 | lone surrogate → `U+FFFD` substitution, unlabelled (`transcoded:true` cannot carry it) | M2 |
| R5 | redirected window → 500 `INTERNAL_ERROR` instead of 501 `NOT_EXPOSED`; redirection undetected | M2 |
| R6 | `codepointUnsupported` names the first rune of the text, and the test accepts the wrong codepoint for `"a\U0001F600"` | M3 |
| R7 | no live-desktop assertion — correctly labelled UNPROVEN-ON-HOST in all three artefacts (P14-01 §4, P14-02 §4, P14-03 open item); this review does not close it | M3 |

## Fix list (P14-01 re-gate)

1. `internal/agent/x11_point.go:41-43` → `child = reply[8:12]`, `dx = reply[12:14]`, `dy = reply[14:16]`.
2. `internal/agent/x11.go:317-327` → drain `4*reply[4:8]` bytes after the 32-byte header (12 for
   `GetWindowAttributes`); do not generalise the drain into `do()` — `property()` consumes its own tail.
3. Add the wire test that would have caught this without a desktop: on `Xvfb`, `CreateWindow` at
   (10,20), assert `PointIn(root,15,25)` names that window and `occludingWindow(requested=that window)`
   returns 0. It fails at `23cf971` and passes with 1+2 (both measured here).
4. (non-blocking) make `gap_ms` discriminating by varying the emitter's sleep in a test double.

## Nemesis gate

Three attacks, all recorded above with their outcome: fake-seam blindness (§9.1 wire — **found the
blocking defect**), constant-gap echo (§9.2 — R3), and a compositor frame labelled as a capture (§10 —
refused, R5). Drift check: request (verify committed blobs, not tables) — done, and that is where the
defect surfaced; implementation (does the substitution answer the contract's question?) — answered
correctly in the seam, incorrectly on the wire; verification (do the unit's tests discriminate the
route?) — inside the seam yes (mutation B2), on the wire nothing exercised `PointIn`, which the unit
itself disclosed as unexecuted. That disclosure was accurate and still under-weighted: the wire was
testable here without a WM, and it is wrong.
