# P14-06 — RE-GATE of the P14-05 fix leg (R1 offsets, R2 drain, wire coverage)

**Unit:** P14-06 · **Workflow:** wf_probe1 · **Repo:** `/opt/data/projects/PROBE_UPGRADE/v119wt` · **Branch:** `r119-build`
**Fix under gate:** `ee5e97a4c666330e8450640c7154415a4b02273f` · **Head during verification:** `48bef390b763785e85d53c8c24bb1f66b7482fe3`
**Gate:** `P14-04_REVIEW.md` item 1 (FAIL), residuals R1/R2 · **Independent of both legs:** neither author nor reviewer of `ee5e97a`
**Substrate:** bare `Xvfb` (no WM, `-screen 0 320x240x24 -ac -nolisten tcp -noreset`); Go 1.22.5 at `/tmp/go/bin/go`

Commits `ee5e97a4..HEAD` touching `internal/`: **0**. `git diff --stat ee5e97a4 HEAD -- internal/` → empty.
The bytes judged below are the bytes at head. Blob ids at head:
`x11_point.go 20a21d6e8415c6a14668e8bd435e26a0f865400c`, `x11.go f88181b76ae4810f8fad89168a9f4f1154b43451`,
`p1405_x11_point_test.go b4a77028379fd3ae68b300e09997724c8710bf95`.

---

## V1 — R1 offsets: PASS

`internal/agent/x11_point.go:41-46` (committed blob):

```
41  // Reply layout: [0]=1, [1]=same-screen, [4:8]=length (0 here), [8:12]=child
44  child := binary.LittleEndian.Uint32(reply[8:12])
45  dx := int32(int16(binary.LittleEndian.Uint16(reply[12:14])))
46  dy := int32(int16(binary.LittleEndian.Uint16(reply[14:16])))
```

**Layout confirmed at the protocol, not from the Go code.** I wrote a raw X11 client
(`cache/scratch/p1406_x11_wire_probe.py`, stdlib socket + struct, no Go, no package code) that dials the
Xvfb socket, completes the setup handshake, creates an `InputOutput` window at (10,20) 100x80
override-redirect, and prints the reply bytes:

```
setup ok: idBase=0x00200000 root=0x0000021f screens=1 wid=0x00200001
sync GetInputFocus reply type=1 seq=3/3
TranslateCoordinates(root->root,15,25): reply[0]=1 same_screen=1 len=0 child@[8:12]=0x00200001 dx@[12:14]=15 dy@[14:16]=25  (want child=0x00200001, 15, 25)
  raw header: 0101040000000000010020000f00190000000000000000000000000000000000
TranslateCoordinates(root->win,15,25): child@[8:12]=0x00000000 dx=5 dy=5 (want child=0, 5, 5)
GetWindowAttributes: length word @[4:8]=3 words (44 bytes total), drained tail=12 bytes, map_state@[26]=2 override_redirect@[27]=1
GetGeometry after attributes: reply type=1 seq=7/7 geometry 100x80 (want 100x80)
TranslateCoordinates(root->win,5,8): dx=-5 dy=-12 (want -5,-12: INT16 signed)
```

Decoding the raw header byte-for-byte: `[0]=01` reply, `[1]=01` same-screen, `[2:4]=0004` sequence,
`[4:8]=00000000` length, `[8:12]=01002000` → `0x00200001` child, `[12:14]=0f00` → 15, `[14:16]=1900` → 25.
That is exactly the layout the brief names and exactly the offsets the fix reads. Negative delta
(`dx=-5 dy=-12` for root point (5,8) → window origin (10,20)) confirms the INT16 signed read, which the
old `[8:10]/[10:12]` placement could not express correctly for any window.

**Behaviour reproduced through the package, on Xvfb, with a window I created myself** (throwaway copy
`cache/scratch/p1407_copy`, probe test `zz_p1406_arch_probe_test.go`; window request written in the probe,
not borrowed from `p1405Window`):

```
OBSERVED PointIn(root,15,25)  = dx 15 dy 25 child 0x200001 err <nil> | want 15,25,0x200001
OBSERVED PointIn(win,15,25)   = dx 5 dy 5 child 0x0 err <nil> | want 5,5,0
OBSERVED PointIn(win,5,8)     = dx -5 dy -12 err <nil> | want -5,-12
```

**`translateToRoot` still agrees** — `x11.go:290-291` returns `int16(reply[12:14])`, `int16(reply[14:16])`,
the same field placement, from `x11.go:286`'s `do()` call. Root→window and window→root now disagree only
in the request's `src`/`dst` words (`x11_point.go:30-31` vs `x11.go:284-285`), which is the one legitimate
difference. Pre-fix, the two functions disagreed on the *reply* layout: that was the defect.

---

## V2 — R2 drain: PASS

`x11.go:227-254` is the only reply reader on the `do()` path. Order of operations, cited:

| step | line | observed |
|---|---|---|
| 32-byte header read | 232-235 | `io.ReadFull(x.c, reply)` |
| **error packet first** | 236-238 | `if reply[0] == 0 { return nil, &x11Error{...} }` — before `reply[4:8]` is touched, correct because an error's `[4:8]` is the bad resource, not a length |
| non-reply type refused | 239-241 | `reply[0] != 1` |
| length interpreted | 242 | `extra := 4 * int(binary.LittleEndian.Uint32(reply[4:8]))` |
| **implausible-length guard** | 243-245 | `if extra > 1<<20 { … "implausible reply length" }` — refuses before allocating |
| tail drained | 246-252 | `io.ReadFull(x.c, tail)`; `reply = append(reply, tail...)` |

`property()` reads the drained tail, not the wire: `x11.go:323-327`
(`if len(reply) < 32+need { … "reply carried %d data bytes" }`, then `data = reply[32 : 32+need]`).
The old `io.ReadFull(x.c, buf)` is gone from that function; the only remaining `io.ReadFull` on `x.c` in
`property`'s path is inside `do()`.

**(a) No caller still reads the wire for a tail — no double-drain.** Every `do()` caller at head:

| caller | reply | tail handling |
|---|---|---|
| `x11.go:268` `atom` (InternAtom) | 32 B, length 0 | inherits drain, indexes `[8:12]` |
| `x11.go:286` `translateToRoot` | 32 B | inherits drain |
| `x11.go:306` `property` (GetProperty) | 32 B + data | **slices `reply[32:32+need]`** |
| `x11.go:340` `attributes` (GetWindowAttributes) | 44 B (length 3) | inherits drain, ignores tail, reads `[26]`/`[27]` — correct |
| `x11.go:352` `geometry` (GetGeometry) | 32 B | inherits drain |
| `x11_input.go:71` `xtestMajorOp` (QueryExtension) | 32 B | inherits drain |
| `x11_point.go:34` `PointIn` (TranslateCoordinates) | 32 B | inherits drain |

Three functions read `x.c` directly and each drains its own tail — they are *not* on `do()`, so they
cannot double-read: `AtomName` (`x11.go:400-411`, header + own tail), `readReply`+`keymapFor`
(`x11_input.go:190-235`, only caller is `keymapFor`, which reads its own tail), `image`
(`capture_linux.go:134-155`, header + `4*length`), plus `sync`/`handshake` which read fixed 32-byte
packets. No path reads a tail twice and none assumes a tail was consumed elsewhere.

**(b)+(c) probed live on one connection** (geometry → attributes → geometry → property → geometry, my
probe, Xvfb):

```
OBSERVED geometry#1  -> {X:10 Y:20 W:100 H:80} | err <nil>
OBSERVED attributes  -> mapstate=2 override=true | err <nil>
OBSERVED geometry#2  -> {X:10 Y:20 W:100 H:80} | err <nil>
OBSERVED property    -> fmt=0 type=0x0 data=0 bytes | err x11 property not set
OBSERVED geometry#3  -> {X:10 Y:20 W:100 H:80} | err <nil>
```

The third call survives and the fourth reply still decodes (`errPropAbsent` is the correct answer for an
absent property, not a transport fault). Pre-fix the second geometry returned `x11 error 0` — reproduced
below, which is the measurement that makes this PASS rather than an assertion.

---

## V3 — the wire tests are real, and not vacuous: PASS

`p1405_x11_point_test.go` (new, 197 lines; assertions at `:76`, `:118`, `:158`). They exercise the wire,
not a seam: each test calls `startBareXvfb(t)` (real server, `p1303_xvfb_test.go:110-135`), `x11Dial(num)`
(real socket + handshake, `x11.go:65-81`), creates windows with raw `CreateWindow`/`MapWindow` requests
(`:24-70`), and then drives `x.PointIn`, `x.Attributes`, `x.Geometry`, `x.Property` — the same code path
`occludingWindow` uses (`geometry.go:149-175`, `Attributes` at `:160`). No fake, no injected `windowProbe`.

**Not vacuous — both reverts run in a throwaway copy (`p1407_copy`), never the worktree.**

Revert A — old offsets restored (`child [4:8]`, dx `[8:10]`, dy `[10:12]`):

```
--- FAIL: TestP1405WirePointInNamesChildAndDeltas (0.03s)
    p1405_x11_point_test.go:93: PointIn(root,15,25) child = 0x0, want the test window 0x200001
    p1405_x11_point_test.go:96: PointIn(root,15,25) deltas = 1,32, want 15,25 (the child word is not a delta)
    p1405_x11_point_test.go:109: PointIn(0x200001,15,25) deltas = 0,0, want 5,5 (15-10, 25-20)
--- FAIL: TestP1405WireOccludingWindowDescends (0.03s)
    p1405_x11_point_test.go:177: occludingWindow(upper,60,60) hit = 0x21f, want 0 (the requested window is on top)
    p1405_x11_point_test.go:180: descent path = [543], want the root plus at least one child
FAIL	github.com/falke-ai-circuit/probe/internal/agent	0.087s
```

`deltas = 1,32` is the pre-fix signature the review measured, and `hit = 0x21f` is the root: the descent
never leaves it. Two of three tests discriminate the offsets.

Revert B — the tail drain removed from `do()` (offsets left correct):

```
--- FAIL: TestP1405WireAttributesDoesNotDesync (0.03s)
    p1405_x11_point_test.go:143: geometry#2 after attributes: x11 error 0 while reading geometry (the connection desynchronised)
FAIL	github.com/falke-ai-circuit/probe/internal/agent	0.033s
```

The third test discriminates the drain and not the offsets (it passes under revert A), so the suite is
one test per defect, not three copies of one assertion. Green at head, all three, on a live server:

```
--- PASS: TestP1405WirePointInNamesChildAndDeltas (0.03s)
--- PASS: TestP1405WireAttributesDoesNotDesync (0.07s)
--- PASS: TestP1405WireOccludingWindowDescends (0.05s)
```

`TestP1405WirePointInNamesChildAndDeltas` asserts both a root-space answer (child named, deltas = the
query point) and a window-space answer (leaf child 0, deltas = the offset inside), so a decode that
returned the same word twice for the two fields cannot pass.

---

## V4 — nothing else moved: PASS

`git show --name-only ee5e97a` → 3 files, `+228/−10`:

```
internal/agent/p1405_x11_point_test.go   (new, +197)
internal/agent/x11.go                    (+32/−10 → net +22/−10 region: 3 hunks)
internal/agent/x11_point.go              (+6/−3)
```

- **Occlusion policy unchanged**: `geometry.go` has no hunk in the fix (`git diff --stat ee5e97a^..HEAD -- internal/agent/geometry.go` → empty); `occludingWindow` at `geometry.go:149-175` is byte-identical to `23cf971`. The fix corrects what `PointIn` reports, not what the descent decides.
- **Geometry path unchanged**: same — no hunk under `geometry.go`; the dispatch-time re-read (`geometry.go:243-256`) and `outerRect` are untouched.
- **`input_pointer.go` unchanged**: no hunk (§9.2-9.4 code untouched; nothing in P14-05 could have moved a scroll sign or a multi-click count).
- **§10 capture unchanged**: `capture.go` / `capture_linux.go` have no hunk; `image()` still reads its own reply (`capture_linux.go:134-155`), which is why routing the drain through `do()` did not reach it.
- **No existing assertion weakened**: the fix's only test change is the *addition* of a new file. `p1401_geometry_test.go` has no hunk in the fix and `git log --oneline -- internal/agent/p1401_geometry_test.go` shows exactly one commit, `23cf971` (P14-01) — so acceptance 2's zero-event assertion is the one P14-01 shipped, not a relaxed one. Same for `p1402_*_test.go` / `p1403_*_test.go`: no hunk, no edit. There is nothing under `internal/agent/` to quote because the diff is empty for every file except `x11.go` and `x11_point.go`.
- The only behavioural widening is inside `do()`: a reply's declared tail is now consumed by the shared reader. Every `do()` caller either wants the tail (`property`) or ignores it (the other six); table in V2(a).

---

## V5 — suite: PASS, 739 / 0 / 2

`GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1`, run 4× from the clean worktree
(`git status --porcelain` empty at every run), head `48bef39`:

| run | exit | packages ok | FAIL lines |
|---|---|---|---|
| 1 | 0 | 11 | 0 |
| 2 | 0 | 11 | 0 |
| 3 | 0 | 11 | 0 |
| 4 (`-v`, for counts) | 0 | 11 | 0 |

Counts from the `-v` run: **739 pass / 0 fail / 2 skip** — identical to the parent's 739/0/2 and to
P14-05's claimed delta of +3 over the 736 baseline. Skips are the two the units name:
`TestDownloadURL_LiveCrossProcessProof`, `TestV1_CreateTransfer`. `internal/agent` 2.104s / 1.943s /
2.093s; the three `TestP1405*` tests ran (not skipped) in every `-v` run.

---

## Item-1 ruling

**PASS.** P14-04 item 1's FAIL was mechanism-not-implementation: the substitution (`TranslateCoordinates`
descent instead of `XQueryPointer` warp) was accepted, the reply decode was wrong. Both defects it named
are corrected at the bytes the server sends, the decode is confirmed by a non-Go protocol probe as well as
through the package, the companion desync is gone on a live connection, and the three new tests are red
under each reverted fix. The end-to-end consequence the review measured — `hit=0x21f` (root) and
`path=[root]` for a point plainly inside a child — is now `hit=0`, `path=[root, child]`, with the covering
child named when one sits on top. **§9.1's window-scoped pointer path is no longer a blanket OCCLUDED on a
real X display.**

---

## R3-R7: judgement (not closed by silence)

| # | residual | does it block §9.1? | ruling |
|---|---|---|---|
| R3 | `gap_ms` echo not discriminating (constant 120 ms sleep, `defaultDoubleClick=400` build constant) | **No** | §9.2 leg. Genuinely later work: a test double with a varying sleep, plus making the double-click window a seat knob. Blocks nothing in §9.1. |
| R4 | lone surrogate → `U+FFFD` unlabelled | **No** | §9.4 leg, contract-wide and pre-JSON-layer. Later work. |
| R5 | redirected window → 500 `INTERNAL_ERROR` instead of 501 `NOT_EXPOSED` | **No** | §10 envelope class. Later work. |
| R6 | `codepointUnsupported` names the first rune of the text; the test accepts `U+0061` for `"a\U0001F600"` | **No** | §9.4 reporting accuracy. Later work — but it is a *test that accepts the wrong answer*, so it is the one residual of the five that should be fixed before §9.4 is called done. |
| R7 | no live-desktop assertion | **No for the decode; yes for §9.1-on-a-real-desktop** | This unit's substrate is a bare Xvfb with no WM; a WM/reparenting/compositor path is unproven here and I did not claim one. §9.1's acceptance is met on the substrate that was available; §9.1 *under a WM* remains UNPROVEN-ON-HOST and is later work requiring a seat. Naming it, not closing it. |

One disclosure about R7's scope: the descent asks the server (`TranslateCoordinates`), so a reparenting
WM's frame offsets are the server's answer and not a client-side extents guess — the mechanism is the
right shape for a WM desktop. That is an argument about the design, not evidence of behaviour under a WM,
and I am not scoring it as evidence.

---

## Non-blocking notes (nothing here needs a fix in this unit)

1. `x11.go:397` `AtomName` writes its own request without `x.seq++`, unlike `do()` and the XTEST writers. `x.seq` is never read anywhere in the package (it is write-only housekeeping), so this is not a desync and not a regression from `ee5e97a`; noting it because it is the one wire path that is not `do()`-shaped.
2. The guard bound is `extra > 1<<20`; `GetProperty`'s own maximum ask is 65536 items × 4 B = 256 KiB and `GetImage` bypasses `do()` entirely, so the bound does not truncate a legitimate reply on any path that uses `do()`.
3. `property()`'s short-tail error (`x11.go:324`) fires where the old code would have blocked reading a tail the server never sent — a strictly better failure, and the one guard that keeps a stale/absent tail from being decoded as data.

## How to re-run this gate

```bash
export PATH=/tmp/go/bin:$PATH && cd /opt/data/projects/PROBE_UPGRADE/v119wt
git rev-parse HEAD && git diff --stat ee5e97a4 HEAD -- internal/
python3 /opt/data/profiles/architect/cache/scratch/p1406_x11_wire_probe.py 87        # V1/V2 protocol probe
GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1 -v                       # V5, counts
# V3: copy internal/ + vendor + go.mod into a throwaway dir, revert offsets -> 2 red;
#     remove the do() drain -> the third red.
```
