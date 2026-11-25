# P14B-01 — CLOSE

**Commit:** `942bcc8` (pushed to `origin/r119-build`, tree clean)
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: 942bcc8`
**Scope:** `internal/agent/input_pointer.go`, `internal/agent/p1402_input_test.go`, `internal/agent/agent.go` (call sites + `Config.DoubleClickMS` + `Agent.clickWindow` + `New()`). Artefact: `/opt/data/tmp/probe119/P14B-01_R3.md`.

Suite: 745 pass / 0 fail / 2 skip (baseline 743/0/2 at 62865d7).

Done:
- echo is falsifiable — press stamps taken after the seam returns; 25 ms/press injected → measured 170.5 ms, `gap_ms` 171, nominal spacing 120 ms.
- hardcoded `return multiClickGap` fails both new tests (quote in the artefact), copy removed.
- configured window 200 ms → spacing 60 ms, `gap_ms` 102 < 200 (asserted against the configured value).
- `count=2` one move, `count=1` no `gap_ms` — kept.

Open item: `Config.DoubleClickMS` has no JSON key; plumbing it needs `cmd/probe/config.go`, outside this unit's file scope.
