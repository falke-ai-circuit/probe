# P14B-03 — CLOSE

**Commit:** `7d79a88` (code + tests; pushed to `origin/r119-build`)
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: 7d79a88`
**Head-move waiver:** the verdict binds to `7d79a88`; this record and `P14B-03_R6.md` follow in their own commit (an artefact cannot name the sha that contains it — same pattern as P14B-02). `git diff 7d79a88 HEAD -- '*.go'` is empty.
**Scope:** `internal/agent/input_pointer.go`, `internal/agent/agent.go`, `internal/agent/p1402_input_test.go`. Artefact: `/opt/data/tmp/probe119/P14B-03_R6.md` (committed as `reviews/P14B-03_R6.md`).

## What changed

`codepointUnsupported` no longer takes the text and names its first rune. `planText` returns a typed `*codepointFailure` carrying the rune and its index, or `at: -1` when the failure belongs to no rune; `codepointUnsupported(env, why error)` reads it with `errors.As` and omits `codepoint`/`index` when unidentified. Both call sites pass the failure, not the string.

## Evidence

- Red at `be5adb1` with the tightened test, quoted: `"a😀": codepoint = U+0061, want U+1F600`; `"aé😀"` the same; `"abc"` with no spare keycode also reported `U+0061`.
- Green after: `U+1F600` at index 0 / 1 / 2 for the three astral shapes; `codepoint` and `index` absent for `"abc"` + no spare keycode and for invalid UTF-8; control char names `U+0001` at index 1; no shape typed a prefix (events 0 on every refusal).
- Suite: `GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1` → **758 pass / 0 fail / 2 skip** (baseline re-measured at `be5adb1`: 753/0/2; delta +5 = 3 new astral subtests + 2 new tests). `go build ./...`, `go vet ./internal/agent/` clean.
- Toolchain note: `/opt/data/go` is a mixed tree here and fails every package (`mallocHeaderSize redeclared`); `/tmp/go` (go1.22.5, matches `go.mod`) was used for all numbers.
- Flake disclosure: two earlier runs of the same command failed only `TestP1402ConfiguredWindowSetsSpacing` (R3 spacing, untouched) at host load 9.9–11.1 on 6 cores — `measured gap=113.883318ms gap_ms=114, want ≈100`; and once at the 100.5 ms rounding boundary. Passed at baseline, 10/10 alone, green in the reported run. Assertion not touched.

## Done criteria

- `"a\U0001F600"` names `U+1F600`; pre-fix `U+0061` quoted as the red observation. ✔
- No case names a rune the code did not identify; both unidentified paths report absence and each has a test. ✔
- Every row of the table states one expected codepoint — the `|| named != "U+0061"` escape is gone. ✔
- Suite green with the unit's own counts; no existing assertion weakened beyond this unit's repair. ✔

## Open item

`R4b` (inherited): invalid UTF-8 bytes are repaired to U+FFFD by `encoding/json` before the handler sees the string — still silent. Named, not absorbed.
