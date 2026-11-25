# P14B-06 — close record (the spacing assertion bands jitter; it no longer reports the host)

**Unit:** P14B-06 (workflow wf_probe1) · branch `r119-build`
**Commit:** `41aab18d3e0068254d65989ef7505e3cdcde9ebd` — test only. The artefact and this record follow in a
document-only commit on top of it, so the reviewed HEAD stays `41aab18`.
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: 41aab18`
**Scope:** `internal/agent/p1402_input_test.go` — the spacing assertion in
`TestP1402ConfiguredWindowSetsSpacing`, plus the `clickSpacingJitterBudget` helper and the test's own pinned
window/fraction constants. No production file touched: the drift is the host's, measured (§5 of the artefact).
Artefact: `/opt/data/tmp/probe119/P14B-06_FLAKE.md` (committed as `reviews/P14B-06_FLAKE.md`).

## What changed

The point assertion (`gap_ms ≈ spacing + 2×latency ± 5 ms`) is replaced by a band:

```
lower = gapMillis(spacing + 2×latency)                 // the spacing must have been emitted
upper = lower + clickSpacingJitterBudget(spacing)      // spacing/2 of lateness allowed
```

Lower edge = the code's defect (no spacing emitted, or `count>1` as a loop of single clicks); upper edge = the
host's lateness, which a busy scheduler is entitled to spend. Nothing early is allowed.

Both edges are computed from the window and the pinned 3/10 fraction **inside the test**. The first attempt called
`clickSpacing(window)` for its expectation; a throwaway copy hardcoding 120 ms then moved its own edges to
floor=160/ceiling=220 and passed the band at 161 ms, leaving only an ordering assertion to report the defect.
Computed from the window, the same copy is red at the ceiling — quoted in the artefact §4.

## Band numbers

| window | spacing | floor | budget | ceiling | observed `gap_ms` |
|---|---|---|---|---|---|
| 400 ms | 120 ms | 160 ms | 60 ms | **not applied (one-sided)** | 160–168 |
| 200 ms | 60 ms | 100 ms | 30 ms | 130 ms | 100–109 |

The 400 ms window is floored and deliberately not ceiled: a hardcoded 120 ms produces exactly its expected
interval, and 246 ms was measured there at load 15.4, so a ceiling would report the host again. The 200 ms ceiling
is the one that discriminates: derived 100 ms vs hardcoded 160 ms, budget 30 ms, 10 ms above the worst recorded
lateness on this window (120 ms).

## Proof

- **Red before, at load:** 2/3 FAIL pre-fix at load 14.95–15.43, `gap_ms=120` and `gap_ms=109` against a 100 ms
  expectation, both reported as "spacing was not derived from the configured window". Quoted in artefact §1.
- **Green after, on a loaded host:** 5/5 PASS at load 16.01–16.09 (12 spinners on 6 cores), gaps 100/109/103/103/104
  inside `[100, 130]`. Quoted in artefact §6.
- **Still discriminates:** hardcoded 120 ms in a throwaway copy → `gap_ms = 161 is above the band's ceiling 130ms`,
  red at the band. Quoted in artefact §4.
- **Not the code:** same binary and run, the 400 ms window drifted +46…+86 ms while the 200 ms window drifted
  +20 ms; idle runs land 0.34–0.55 ms above the exact floor. `emitClickGesture` sleeps once per gap, not per event.
  No production file changed.
- **Neighbours intact:** one move for `count=2`, no `gap_ms` for `count=1`, `gap_ms == gapMillis(measured)` kept in
  the closure, `seat < seatMS` and `seat < def` kept (now after the band, so a non-derived spacing is reported as a
  band violation rather than as an ordering trip).

## Suite

`GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1 -v` (go1.25.0) → exit 0,
**763 pass / 0 fail / 2 skip** (633 top-level + 130 subtests), load 7.01 at start / 7.59 at end. Baseline at
`d16bde0` is 763/0/2 — same count, one assertion replaced, none removed. `go vet ./internal/agent/` clean;
`gofmt -l` on the touched file empty.

## Open item

The 200 ms ceiling (130 ms) is 10 ms above the worst lateness recorded here (120 ms at load 15.4). A host loaded
well past 16 can still push one measurement past it. Widening the budget would stop discriminating against a
hardcoded 120 ms (which measures 160), so the tail wants a re-measurement on a late result, not a wider band — a
policy change larger than this unit's brief, named and not absorbed.
