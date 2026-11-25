# P14-01 — CLOSE

**Commit:** `23cf971` (branch `r119-build`, pushed to origin, tree clean)
**Review:** reviewed_by: `PENDING` (no independent verdict — review leg owed) · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `23cf971d1373b2ab9aeedc38982666ecb6a845f6`
**Parent verification (orchestrator, this close):** whole suite re-run at `23cf971` → **714 pass / 0 fail / 2 skip**, matching the unit (baseline 700, delta +14 = its 10 agent + 4 server tests). The reuse requirement is true structurally, not just asserted: `outerRect` has one definition (`internal/agent/geometry.go:258`) and is called by both paths (`geometry.go:252`, `uitree.go:269`); `clientList` likewise one definition (`uitree.go:166`) called from `geometry.go:98` and `uitree.go:65`. Commit is 13 files, all in scope. Two deviations are flagged in the artefact §3 and are the review leg's to rule on, not this close's: the `XQueryPointer` → `XTranslateCoordinates` substitution (argued as avoiding pointer warp) and the reading that a request naming no window has no predicate to hit-test.
**Scope:** contract §9.1 only — window-relative geometry at dispatch time, the
always-on occlusion hit-test, 404 before injection, the typing focus gate.
§9.2/§9.3/§9.4 (P14-02) and §10 capture untouched.

## Delivered

`window` present → the point is `rect.origin + x,y` measured **at dispatch
time** through the same `treeDisplay` seam `ui-tree` uses (`outerRect` is now
shared: one rect computation, one client-list read). Occlusion is decided
before injection by asking the platform what is on top at the computed absolute
point; when it is not the requested window the input is not performed and the
answer is §8.1's `200 ok:false reason:"OCCLUDED"` with `hit{id,title,owner_pid}`,
`detail{intended,point{x,y}}`, `ran:false`. A window absent from the resolved
context's tree is `404 WINDOW_NOT_FOUND` before any injection; typing into an
unfocused window foregrounds only with `focus:true`, else `403
PRIMITIVE_REFUSED reason:"NO_FOCUS_TARGET"`, and a foreground the WM ignores is
`FOREGROUND_DENIED` with no keystroke.

## Verification

- `go test ./... -count=1`: **714 pass / 0 fail / 2 skip** (baseline `495df34`:
  700 pass / 0 fail / 2 skip; +14 tests, all new).
- `GOOS=windows GOARCH=amd64 go build ./...`: clean (the first full-suite run
  caught a windows-only compile break in this unit — `noWMRemediation` was
  linux-tagged while `geometry.go` is not — fixed by moving the constant to the
  untagged tree file; re-run green).
- Acceptance 1-5 each have their test, listed with the assertion that would fail
  in `P14-01_GEOMETRY.md` §2. Acceptance 1 moves the rect in the seam between
  tree-read and dispatch (150,100 — not the stale 110,70); acceptance 2 asserts
  the fake backend saw zero events.

## Open item

The X11 wire additions (`PointIn` / `RequestForeground`) are **UNPROVEN ON THIS
HOST**: no window manager here, so the Xvfb harness refuses with
`NO_WINDOW_MANAGER` before those calls are reached, and no live click was
performed. Read `P14-01_GEOMETRY.md` §3-§4 before the review: the
`XQueryPointer` → `XTranslateCoordinates` substitution and the "no window named
= no predicate to hit-test" reading are stated there for a verdict, not buried.
