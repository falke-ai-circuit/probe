# P14-01 — contract §9.1: window-relative geometry + the occlusion hit-test

**Commit:** `23cf971` (branch `r119-build`, pushed, tree clean)
**Review:** `reviewed_by: PENDING — review leg owed`
**Scope:** §9.1 only. No §9.2 multi-click gap, no §9.3 scroll sign, no §9.4
unicode, no capture (§10).

---

## 1. What landed

| file | role |
|---|---|
| `internal/protocol/input_geometry.go` | §8.1 observed-failure wire shape (`ok:false`, `reason`, `hit`, `detail`, `ran:false`) + the closed `ObservedReasons` enum + `WINDOW_NOT_FOUND` / `NO_FOCUS_TARGET` |
| `internal/agent/geometry.go` | the ONE §9.1 path: window membership → dispatch-time rect → absolute point → occlusion → focus gate, plus the refusals |
| `internal/agent/geometry_linux.go` | context → display resolution (explicit `context` wins, else `$DISPLAY`) and the opener seam |
| `internal/agent/geometry_other.go` | non-linux: typed 501 `NO_BACKEND`, never an assumed "nothing on top" |
| `internal/agent/x11_point.go` | `PointIn` (TranslateCoordinates) and `RequestForeground` (`_NET_ACTIVE_WINDOW`, source=2) on the existing X connection |
| `internal/agent/uitree.go` | `outerRect` extracted: the §7 row and §9.1 resolution now share ONE rect computation; `ownerPIDOf` likewise |
| `internal/agent/agent.go` | the four injecting primitives pass through the gate before they touch the platform; `openDisplay` seam |
| `internal/server/strict.go`, `api_v1_transfer.go`, `api_v1_strict.go` | one `writeAgentRefusal` (code → §11 status), 404/403 mapping, and `writeInputOutcome` so a 200-`ok:false` result is not wrapped into `ok:true` |

The reuse requirement (§9.1 names the measured bug; a second lookup path is the
defect) is met structurally: `resolveInputTarget` reads `wmCheck` /`clientList` /
`outerRect` — the functions `buildUITree` already uses. There is no second
client-list read and no second rect arithmetic in the tree.

## 2. Acceptance, each with the test that proves it

All agent-level cases run through the `treeDisplay` seam (the fake states the
display; nothing touches a socket).

1. **Window-relative at dispatch time** —
   `TestP1401WindowRelativeUsesCurrentRect`. The test calls `buildUITree` first
   (origin 100,50), then **moves the window** to 140,80 in the seam, then
   dispatches `x=10,y=20`. The click is recorded at **150,100**. A resolver that
   reused the tree's rect would have produced 110,70 and fails the assertion.
   `TestP1401WindowAbsentKeepsAbsolutePoint` is bullet 1's control: no `window`
   → click at exactly 42,96 and the display opener is never called.
2. **Occlusion refusal with zero input** — `TestP1401OccludedRefusesWithNoInput`.
   Covering window on top at the computed point → `ok:false`,
   `reason:"OCCLUDED"`, `ran:false`, `hit.id` = the covering window,
   `hit.owner_pid` = its `_NET_WM_PID`, `detail.intended` / `detail.point` echo
   the intent, and the platform recorder saw **zero** events
   (`len(clicks)==0 && len(events)==0`). The event counter is the load-bearing
   assertion.
3. **Not occluded fires once** — `TestP1401TopWindowFiresOnce`. Same point,
   requested window on top → exactly one click at 42,96.
4. **404 before injection** — `TestP1401WindowNotInTreeRefusesBeforeInjection`.
   An id absent from `_NET_CLIENT_LIST_STACKING` → `WINDOW_NOT_FOUND` with
   `detail{resolved_context,window,ran:false}` and **zero** platform events.
   `TestP1401OccludedIsA200Result` and `TestP1401RefusalStatusMapping` prove the
   HTTP side: 404, 403 `NO_FOCUS_TARGET`, and the 200-`ok:false` pass-through.
5. **Focus gate** — `TestP1401FocusGate`, three directions: without `focus:true`
   → 403 `PRIMITIVE_REFUSED reason:"NO_FOCUS_TARGET"` and no text; with
   `focus:true` → event order is exactly `[foreground, type]`; with `focus:true`
   and a WM that ignores the message → 200 `FOREGROUND_DENIED`, no keystroke.

## 3. Deviations and interpretations (explicit, for the review leg)

1. **`XQueryPointer` → `XTranslateCoordinates` descent.** §9.1 names
   `XQueryPointer` + `XTranslateCoordinates`. `XQueryPointer` reports the window
   under the *pointer's current position*, not under an arbitrary point, and the
   only way to make it answer for the computed point is to warp the operator's
   pointer — a side effect a hit-test must not have. The implemented descent is
   `TranslateCoordinates(root → win)` at the computed point, stepping into the
   child the reply names, stopping at the first non-viewable child (an unmapped
   window receives no input, so descending into its geometry would refuse a
   legitimate click). Same question, same answer, no pointer mutation. If the
   review leg requires the literal `XQueryPointer` call, that is a change to
   `PointIn` only.
2. **"Always on, no knob" is scoped by whether there is a predicate.** A request
   that names no `window` asserts no intended target (bullet 1 keeps today's
   absolute behaviour); "the top window is not the requested one" has no
   requested one to compare against. That is the only path without a hit-test,
   and it is a consequence of there being nothing to compare — not an opt-out.
   Flagging it rather than hiding it.
3. **`pointer-move` / `pointer-scroll` / `pointer-drag` / `ui-window` keep
   `NO_BACKEND`.** They have no injection backend in this build (§11 phases 4-5
   for scroll/drag; §9.2-9.4 are P14-02), so applying geometry to them would be
   dead code. They will pass through the same `resolveInputPoint` when their
   backend lands; no second geometry path is being introduced for them.
4. **The observed-failure body carries `context_ref`.** §9.1's example does not
   show it; §11's walkthrough requires every response to name the context it
   acted in, and the server's E2 assertion would otherwise read the answer as a
   protocol violation. Additive field.
5. **Non-X11 contexts** (wayland, uid) and non-linux hosts answer 501
   `NO_BACKEND` on window-scoped input: this build has no geometry backend
   there, and the §11 matrix's per-platform answers are other units. A display
   with no EWMH WM surfaces §7.1's `NO_WINDOW_MANAGER` instead of a fabricated
   "not found".

## 4. UNPROVEN-ON-HOST

This host has no window manager, so:

- **No live click was performed on a real desktop.** Every assertion above is
  stated through the seam, as the brief directs. §9.1's "lands at
  `rect.origin + x,y`" is proved against a stated display, not a running one.
- **The new X11 wire code is unexecuted against a real X server**:
  `PointIn` (TranslateCoordinates opcode 40) and `RequestForeground`
  (SendEvent opcode 25 + `_NET_ACTIVE_WINDOW` client message). The Xvfb harness
  in `p1303_xvfb_test.go` runs without a WM, so a window-scoped input there
  returns 501 `NO_WINDOW_MANAGER` before reaching them. This is a gap the
  review leg should know about, not a claim of live verification.
- Real `_NET_ACTIVE_WINDOW` semantics (focus-stealing prevention honouring
  source=2) are asserted by contract, not measured here.

## 5. Evidence

```
$ go build ./... && GOOS=windows GOARCH=amd64 go build ./...     # exit 0, both
$ go vet ./internal/...                                          # exit 0
$ go test ./... -count=1 -json | <count>                         # pass=714 fail=0 skip=2
$ go test ./internal/agent -run P1401 -v    # 7 top-level + 3 subtests PASS
$ go test ./internal/server -run P1401 -v   # 2 + 2 subtests PASS
$ git status -sb                            # ## r119-build...origin/r119-build (clean)
```

Baseline at `495df34` was 700 pass / 0 fail / 2 skip; this unit adds 14 tests
(10 agent, 4 server) and leaves no failure behind.
