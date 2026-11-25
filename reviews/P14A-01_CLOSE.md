# P14A-01 — CLOSE

**Commit:** `e159de358e65b299f1b0bdca55aecb9930ec1bb2` (branch `r119-build`, pushed to origin, tree clean)
**Review:** reviewed_by: `PENDING` · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `e159de358e65b299f1b0bdca55aecb9930ec1bb2`
**Scope:** R1 only — a WS write failure on the forward path is typed (`DELIVERY_FAILED`) and the update route refuses with 502 instead of 200 in-progress; the audit row for an agent-side typed error no longer records `success`. R2's refusal-branch side effects, the update state machine, canary logic, §9/§10 and the deploy path are untouched. Artefact: `/opt/data/tmp/probe119/P14A-01_R1.md`.

## Delivered

`conn.WriteJSON` failure → `*ForwardRefusal{Code: RefusalDeliveryFailed}` (`server_api.go:795` enum,
`:901-913` return) → `handleAgentUpdate` answers **502** `ok:false code:DELIVERY_FAILED delivered:false`
(`server_update.go:276-279`) instead of `200 {"status":"update_in_progress"}`. Audit at `:917-932` logs
`error` + the agent's code rather than `success` for a command the agent rejected.

## Verification

- Red at `d416411` (worktree, identical test): `undelivered write answered 200: {... "status":"update_in_progress" ...}` → FAIL.
- Green at `e159de3`: both new tests PASS; `TestP14A01DeliveredUpdateAnswersAsBefore` keeps the 200 happy path.
- Suite `GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1`: **741 pass / 0 fail / 2 skip**
  (baseline `d416411` 739/0/2; delta +2 = this unit's two tests). Skips unchanged.

## Open item

R2 — the refusal branch still pins the artifact and purges the canary registry row before any write is
attempted, so the "Nothing was staged" comment is still false for delivery refusals; it is reachable in
one more case now and remains for its own unit. 502 `DELIVERY_FAILED` is a new status row absent from the
P9-01 contract table — flagged for the review leg, not edited into the contract here.
