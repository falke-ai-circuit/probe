# P13-06 — close

**Commit:** `495df34ebb82e6c78c5acdc73b3a02fb39569e93` (`r119-build`) — the fix leg verified; head
at verification `d9fc1cba13fb53acedf12ec40a0f6ba4fb95124d` (documentation commits only above it,
`git diff --stat 495df34 HEAD` = `reviews/P13-05_CLOSE.md`, `reviews/P13-05_REGATE.md`).
**Review:** reviewed_by: `architect` · verdict: `PASS` (V1-V5 all PASS; F1 closed, F2 harness
fixed and the `-noreset` mechanism reproduced directly) · verdict_sha_of_head:
`d9fc1cba13fb53acedf12ec40a0f6ba4fb95124d`
**Scope:** verification only. No code, no test, no contract touched by this unit; the negative
mutation ran in a `git archive HEAD` copy at
`/opt/data/profiles/architect/cache/scratch/p1306/tree`, never in the worktree.

Artefact: `/opt/data/tmp/probe119/P13-06_VERIFICATION.md`

## What was verified

| item | verdict | basis |
|---|---|---|
| V1 F1 closed | PASS | `ExecParams.Selector()` at `messages.go:303`; `decodeStrict[ExecParams]` → `preDispatchSelector` (`server_api.go:173,177`) → `assertContextRef` (`:433`). My own 6-case probe: exec 409 on 1.19, exec 502 on 1.21 without `context_ref`, exec `context`+`session` → 400 `SELECTOR_CONFLICT`. Method removed → both exec subtests fail 200-vs-409/502, pointer-click stays green. |
| V2 sweep complete | PASS | `go/ast` re-derivation: 10 structs with `Context *string`+`Session *int`, 10 `Selector()` receivers, no mismatch; every carrier reachable through `decodeStrict`; the anonymous struct at `agent/contexts.go:342` is local to `selectorGate`, never a decode type, never type-asserted. |
| V3 F2 harness | PASS | `xvfbDisplayFree`/`x11AnswersProperty`/`waitXvfbReady`/`stopXvfb`/`-noreset` read at `:36-122`. Suite 6/6 clean (700/0/2 each); Xvfb alone 10/10 pass. Mechanism tested directly: 1500 re-dials after last-client disconnect → 30/37/45/18 errors without `-noreset`, 0/0/0/0 with it. |
| V4 assertions | PASS | 501 `PRIMITIVE_UNSUPPORTED` + `NO_WINDOW_MANAGER` + `ran:false` required (`:168-180`), `200 {count:0}` still forbidden (`:195-199`); the fix diff is one hunk (imports + `startBareXvfb`), assertion body byte-identical to `1ad1afb`. |
| V5 nothing else | PASS | `git show --name-only 495df34` = the three named files; `messages.go` +6 lines only; no contract edit, `uitree*.go` untouched. |

## Open item

WM-present path remains **UNPROVEN-ON-HOST** (no WM binary on this host) — confirmed still
labelled at `P13-03_WINDOW_TREE.md:108` and carried by P13-05; not closed, must not be quoted as
proved.
