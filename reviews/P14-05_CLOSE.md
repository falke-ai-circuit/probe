# P14-05 — close

**Unit:** P14-05 · **Workflow:** wf_probe1 · **Branch:** `r119-build`
**Commit:** `ee5e97a4c666330e8450640c7154415a4b02273f` — `P14-05: fix §9.1 wire decode — TranslateCoordinates offsets and the reply-tail drain`
**Review:** reviewed_by: `PENDING` (no verdict — the re-gate is owed) · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `ee5e97a4c666330e8450640c7154415a4b02273f`
**Parent verification (orchestrator, this close):** `GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1` re-run at `ee5e97a` → **739 pass / 0 fail / 2 skip**, matching the unit (baseline 736, delta +3). The three `TestP1405*` wire tests run and pass on a bare Xvfb (`--- PASS` each, 0.04/0.03/0.03 s). Read in the tree: `x11_point.go:43-46` now decodes child `[8:12]`, dx `[12:14]`, dy `[14:16]` with a comment naming the length-word trap; `x11.go:218-251` drains `4*length` inside `do()` so every caller inherits it, and `property()` reads the drained tail at `:326` rather than the wire — the one-drain-in-shared-path choice the brief required, which also avoids the double-read a per-call drain plus a `do()` drain would cause.
**Artefact:** `/opt/data/tmp/probe119/P14-05_FIX.md`
**Suite:** `GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1 -v -timeout 1500s` → **739 pass / 0 fail / 2 skip**, exit 0 (baseline `3b32753`: 736 / 0 / 2; delta +3)

## Done

- R1: `PointIn` reads the `TranslateCoordinates` reply at child `[8:12]`, dx `[12:14]`, dy `[14:16]`;
  the descent leaves the root, so window-scoped pointer primitives no longer answer OCLUDED from a
  stale zero child.
- R2: `do()` drains the reply's declared tail (`4*reply[4:8]`) in the shared read path, so
  `GetWindowAttributes` (44 bytes) cannot desync the connection; `property()` consumes the drained
  tail instead of the wire.
- Wire coverage: `p1405_x11_point_test.go`, 3 tests on a bare Xvfb. All three red at `5689da9`
  (pre-fix blobs) and green at `ee5e97a4`; the red output is in the artefact.
- `occludingWindow` end-to-end on the wire: descent `[root, child]`, covering child named as the hit,
  requested window on top not refused. Function unchanged.

## Not done / open

- R3–R7 (gap_ms echo, lone surrogate, compositor 500-vs-501, codepoint naming, live-desktop
  assertion) remain open, out of this unit's scope.
- No review verdict exists yet: this is a FIX leg; the re-gate is owed.
