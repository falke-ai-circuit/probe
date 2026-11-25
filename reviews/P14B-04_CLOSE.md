# P14B-04 — close record (R5: a composited window is a 501, not a 500)

**Unit:** P14B-04 (workflow wf_probe1)
**Commit:** SELF — code, test and this record ship in one commit (brief: one commit, pushed, tree clean); a commit
cannot name its own sha, so the sha is in the close report and re-resolvable with
`git log -1 --format=%H -- reviews/P14B-04_CLOSE.md`.
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: SELF` (HEAD is the commit that
carries this file; `git diff --stat HEAD -- '*.go'` is empty once read).
**Scope:** `internal/agent/capture_linux.go` (detection gate + compositor query), `internal/agent/capture.go`
(typed error + 501 mapper), `internal/protocol/messages.go` (`ReasonNotExposed`, added), new
`internal/agent/p14b04_capture_composited_test.go`. No §9 code, no update path, no deploy pin, contract untouched.
Artefact: `/opt/data/tmp/probe119/P14B-04_R5.md` (committed as `reviews/P14B-04_R5.md`).

## What changed

A composited window is now refused **before any pixel is read**: `capture_linux.go:124-126` asks
`captureCompositorSelection = "_NET_WM_CM_S0"` (`GetSelectionOwner`, `:149-166`) and the window's map state
(`:134-147`); when a compositing manager holds the selection **and** the window is viewable, the backend returns
`captureCompositedError` (`capture.go:97-114`) and the handler answers **501 `PRIMITIVE_UNSUPPORTED`
reason:`NOT_EXPOSED`** (`capture.go:483-505`, code→501 via `internal/server/strict.go:270-271`), detail
`{composited:true, compositor:"_NET_WM_CM_S0", source_available:"compositor",
required:"XCompositeNameWindowPixmap", context, target, window, ran:false}` and no result body.

The pre-fix answer was **500 `INTERNAL_ERROR` `stage:"capture"`** at `capture.go:439-449` from the X error at
`capture_linux.go:139` — and, when the server still served the redirected drawable, **200 `window_direct`** with
redirection undetected. Both are quoted from a pre-fix `git archive e5d7440` run in the artefact §5.

## Contract

Authority quoted, not guessed: `docs/contracts/P10-07_CONTRACT_V3_1.md:612` ("`XGetImage(window)`; redirected windows
need `XCompositeNameWindowPixmap` → `source:"compositor"`"), `:642` (501 `PRIMITIVE_UNSUPPORTED`, `NOT_EXPOSED` in the
reason enum), `:649` ("501 = the code or the environment is not there"). The contract was **not** edited. Its defects
are recorded in the artefact §7: it never says what a build without the compositor path answers, and
`source:"compositor"` is unreachable in this build (the mirror of R5's class).

## Proof

- Detection and answer sites cited by file:line; the composited 500 is **not** P14-03's `stage:"decode"` site.
- Red before: pre-fix tree answers `200 window_direct` for the composited viewable window and
  `INTERNAL_ERROR / stage:capture` for the failing read; `TestP1404RedAssert501` FAILs at `e5d7440`.
- Mutation red: gate removed in a throwaway copy → committed test FAILs with the `200 window_direct` body.
- Green after: `TestP1404B04CompositedWindowIsNotExposed` PASS; uncomposited Xvfb capture test unchanged PASS.
- **UNPROVEN-ON-HOST:** a real compositor's `GetImage` BadMatch cannot be produced here — Xvfb accepts
  `CompositeRedirectWindow` and still returns a frame; `NameWindowPixmap` answers BadIDChoice 14 in both states.
  The condition produced is the EWMH composited seat (selection held) + viewable window. Deterministic halves
  covered: end-to-end 501 on Xvfb, the detail map, the unmapped-window negative, the screen-capture negative.

## Suite

`GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1 -v` → **759 pass / 0 fail / 2 skip** (629 top-level +
130 subtests), exit 0; baseline `e5d7440` 758 → +1 (this test). Skips unchanged
(`TestDownloadURL_LiveCrossProcessProof`, `TestV1_CreateTransfer`). `go build ./...` and
`go vet ./internal/agent/ ./internal/protocol/` clean. Log: `/opt/data/tmp/probe119/P14B-04_suite_v.log`.

## Open item

`XCompositeNameWindowPixmap` (the `source:"compositor"` cure) is not implemented — this unit fixes the label and makes
the documented refusal reachable, not the capability. The compositor error path itself stays UNPROVEN-ON-HOST.
