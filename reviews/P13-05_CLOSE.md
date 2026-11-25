# P13-05 — close

**Commit:** `495df34ebb82e6c78c5acdc73b3a02fb39569e93` (`r119-build`, pushed, tree clean)
**Review:** reviewed_by: `PENDING` (no independent verdict on this fix leg — P13-04's FAIL is the finding this unit answers, not a verdict on it) · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `495df34ebb82e6c78c5acdc73b3a02fb39569e93`
**Parent verification (orchestrator, this close):** re-ran the whole suite 3 times at `495df34` → 700 pass / 0 fail / 2 skip every run, matching the unit's 10/10. Read in the tree: `func (p ExecParams) Selector()` at `internal/protocol/messages.go:303`; harness `xvfbDisplayFree` `:36`, `x11AnswersProperty` `:56`, `stopXvfb` `:87`, `-noreset` in the start args `:122`; and the E1/E2 tables carry an `/exec` row each (`internal/server/p1301_strict_test.go:167,201`).
**Scope:** `internal/protocol/messages.go` (`ExecParams.Selector()`),
`internal/server/p1301_strict_test.go` (E1/E2 tables over pointer-click + exec),
`internal/agent/p1303_xvfb_test.go` (harness only; assertions untouched)

Artefact: `/opt/data/tmp/probe119/P13-05_REGATE.md`

## F1 — closed

`ExecParams` now implements `SelectorCarrier`; §5.1 validation, E1 and E2 fire on `/exec`.
Sweep: 10/10 param types carrying `Context`/`Session` implement it, exec was the only gap.
Non-vacuous: with the method removed, both exec subtests fail with `200` where `409`/`502` is
required; both pointer-click subtests still pass, so the test is route-discriminating.

## F2 — closed

Display scanned and exclusively owned (`90..179`, no pid arithmetic), readiness requires an
answer to a `_NET_SUPPORTING_WM_CHECK` property read, cleanup unlinks the socket and lock and
waits for the socket to vanish. Residual cause found by re-running: without `-noreset`, the
server resets as the readiness client disconnects and closes its listening socket, so the
follow-up dial hit EOF under load — `-noreset` removes that window.

## Re-gate

`go vet ./...` exit 0 · `GOOS=windows go build ./...` exit 0 · whole suite 10 run / 10 clean
(`700 pass / 0 fail / 2 skip` each) · Xvfb test alone 12 run / 12 pass.

## Open item

WM-present path remains UNPROVEN-ON-HOST (no WM binary on this host) — carried from P13-03.
