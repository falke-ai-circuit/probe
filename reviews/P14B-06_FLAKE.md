# P14B-06 — the spacing assertion fails for a wrong window, not for a busy host

**Unit:** P14B-06 (workflow wf_probe1) · branch `r119-build` · file: `internal/agent/p1402_input_test.go`
**Scope:** the spacing assertion in `TestP1402ConfiguredWindowSetsSpacing`. No production file touched (see §5).

## 1. What was asserted, and why it was red on a loaded host

`emitClickGesture` spaces press 2 from press 1 by `clickSpacing(window) = window × 3/10`. The test measured
`times[3] - times[1]` — the interval spanning the release of press 1, the spacing, and press 2 — and compared
`gap_ms` to `spacing + 2×latency` with a ±5 ms allowance.

The expectation is right. The tolerance was not: the measured interval spans **three sleeps**, and a loaded
scheduler returns from each of them late. Recorded failures of that point assertion:

| run | load | window | expectation | measured | verdict |
|---|---|---|---|---|---|
| P14B-03 child | 9.9–11.1 | 200 ms | 100 ms | 113.883 ms (`gap_ms=114`) | red |
| P14B-05 parent suite | 15.66 | 200 ms | 100 ms | (test failed; alone 3/3 PASS at 100.47–100.54) | red |
| this unit, pre-fix | 15.4 | 400 ms | 160 ms | 206.27 / 245.95 / 165.51 ms | (not banded) |
| this unit, pre-fix | 15.4 | 200 ms | 100 ms | 119.99 ms → `gap_ms=120` | red |

Pre-fix failure quoted (`pre_load.log`, load 14.95–15.43):

```
p1402_input_test.go:272: default window: window=400ms spacing=120ms measured gap=206.265961ms gap_ms=206
p1402_input_test.go:272: configured window: window=200ms spacing=60ms measured gap=119.992334ms gap_ms=120
p1402_input_test.go:291: configured gap_ms = 120, want ≈100 (spacing 60ms + 2×20ms wire cost): spacing was not derived from the configured window
--- FAIL: TestP1402ConfiguredWindowSetsSpacing (0.41s)
```

The message is the defect: the spacing **was** derived from the window. The +20 ms is scheduler overshoot on the
three sleeps. Nothing in the gesture changed between the red run and the green ones.

## 2. The band, and what each edge means

```
lower  = gapMillis(spacing + 2×latency)                      // nothing early, ever
upper  = lower + clickSpacingJitterBudget(spacing)           // lateness allowed, bounded
clickSpacingJitterBudget(spacing) = spacing / 2
```

- **Lower edge — the code's defect.** The interval covers release-of-press-1, the spacing, and press 2. Shorter
  than `spacing + 2×latency` means the spacing was never emitted (unimplemented spacing, or `count>1` handled as a
  loop of single clicks). `time.Sleep` never returns early, so this edge carries no allowance.
- **Upper edge — the host's lateness.** A busy host returns from each sleep late and the target still reads the
  pair as one double-click. The budget is half the derived spacing: the largest value that still discriminates.

Both edges are computed from the window and the **pinned** fraction (3/10) *inside the test*, not by calling
`clickSpacing`. That is load-bearing: the first attempt called `clickSpacing(window)` for its expectation, and a
throwaway copy with a hardcoded 120 ms slid its own edges to `floor=160 / ceiling=220` and **passed** the band at a
161 ms measurement — only an ordering assertion reported it. Computed from the window, the same copy is red at the
ceiling (§3).

## 3. The numbers, both windows

| window | expected spacing | floor (spacing + 2×20 ms wire) | jitter budget | ceiling | observed `gap_ms` (post-fix) |
|---|---|---|---|---|---|
| 400 ms | 120 ms | **160 ms** | 60 ms (half of 120) | **220 ms — deliberately not applied** | 160, 161, 161, 168, 160, 162 |
| 200 ms | 60 ms | **100 ms** | 30 ms (half of 60) | **130 ms** | 100, 109, 103, 103, 104, 103 |

The 400 ms window is banded **one-sided on purpose**. A hardcoded 120 ms produces exactly the interval the 400 ms
window derives, so no ceiling there can carry information; and 246 ms was measured against that window's 160 ms
expectation at load 15.4, so a ceiling there would report the host again. The floor still proves the fallback
window emitted its spacing. The only ceiling that discriminates is the 200 ms one, where the derived answer
(100 ms) and a hardcoded 120 ms (160 ms) are 60 ms apart; the budget sits 30 ms below the wrong answer and 10 ms
above the worst recorded lateness on this window (120 ms).

## 4. Discrimination: the band still fails when spacing is not derived

Throwaway copy (`/opt/data/tmp/probe119/p14b06_work/mut`, `clickSpacing` → `return 120 * time.Millisecond`), test
file identical to the committed one:

```
p1402_input_test.go:310: configured window: window=200ms spacing=120ms measured gap=160.60443ms gap_ms=161
p1402_input_test.go:350: configured window band: window=200ms test-derived spacing=60ms (code's clickSpacing=120ms) floor=100ms ceiling=130ms (jitter budget 30ms) measured gap=161ms
p1402_input_test.go:358: configured gap_ms = 161 is above the band's ceiling 130ms (floor 100 + 30ms jitter budget): the emitted interval is not the 60ms spacing this window derives under the contract's 3/10 fraction (a hardcoded 120ms spacing measures 160ms)
--- FAIL: TestP1402ConfiguredWindowSetsSpacing (0.40s)
```

Red at the band, not at an ordering assertion. The band is not too wide.

## 5. Is the drift the code or the host?

The host — measured, not assumed. Same gesture code, same binary, same run at load 15.4: the 400 ms window drifted
+46 to +86 ms while the 200 ms window drifted +20 ms. Overshoot tracks contention, not the seam's call pattern; on
an idle host the same test lands 0.34–0.55 ms above the exact floor (`100.343–100.549 ms`), i.e. the sleep is
scheduled on time. `emitClickGesture` sleeps **once per gap** (`if i > 0 { time.Sleep(spacing) }`), not once per
event, so there is no per-press scheduling term to fix. No production file was changed.

## 6. Green on a loaded host (acceptance)

12 spinners on 6 cores, run through the same `go test` binary:

```
load at start: 16.01 12.97 16.26 17/4062 2754255
p1402_input_test.go:350: configured window band: window=200ms test-derived spacing=60ms (code's clickSpacing=60ms) floor=100ms ceiling=130ms (jitter budget 30ms) measured gap=100ms
--- PASS: TestP1402ConfiguredWindowSetsSpacing (0.34s)
p1402_input_test.go:350: ... measured gap=109ms --- PASS (0.35s)
p1402_input_test.go:350: ... measured gap=103ms --- PASS (0.34s)
p1402_input_test.go:350: ... measured gap=103ms --- PASS (0.35s)
p1402_input_test.go:350: ... measured gap=104ms --- PASS (0.37s)
exit=0
load at end: 16.09 13.03 16.26 23/4052 2754321
```

5/5 PASS at load 16.01–16.09, every measurement inside `[100, 130]`. Pre-fix, the same protocol at load 15.4 was
2/3 red.

## 7. Neighbouring assertions (untouched)

- `count=2` still emits exactly one pointer move (`TestP1402DoubleClickIsOneGesture`, unmodified; 0.12 s PASS in the
  suite).
- `count=1` still has `gap_ms == 0` and two events (unmodified).
- `gap_ms == gapMillis(measured)` still asserted inside the measurement closure — the echo is compared to the same
  measurement, not to a nominal.
- `seat < seatMS` and `seat < def` kept, unchanged in text; they now run *after* the band so a non-derived spacing is
  reported as a band violation rather than as an ordering trip.

## 8. Suite

`GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1 -v` (go1.25.0, HEAD `d16bde0` + this change):
**763 pass / 0 fail / 2 skip** (633 top-level + 130 subtests), exit 0, load 7.01 at start / 7.59 at end.
Baseline at `d16bde0` is 763/0/2 — same count, no assertion removed, one assertion replaced.

## Open item

The 200 ms ceiling (130 ms) sits 10 ms above the worst lateness recorded on this unit (120 ms at load 15.4). On a host
loaded well past 16, a single stall longer than 30 ms on that measurement can still push `gap_ms` past the ceiling.
Widening the budget would stop discriminating against a hardcoded 120 ms (which measures 160), so the fix for that
tail is not a wider band: it would be one re-measurement on a late result (a hardcoded spacing is deterministic and
fails both attempts). Not implemented — it is a change of test policy, larger than this unit's brief.
