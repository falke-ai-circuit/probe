# P12-06 CLOSE — independent verification of P12-05: 5 of 5 PASS, four residuals named, none patched

**Commit:** `e46023f` (repo `/opt/data/projects/PROBE_UPGRADE/v119wt`, branch `r119-build`) — the tree under
verification. Fix commits `557178b` (content) + `aad8938` (shipped-config test); `e46023f` is P16-01's
CI/Dockerfile commit only. Artefact committed by this leg under `reviews/`; that documentation commit is
not part of what was verified.
**Review:** reviewed_by: `PENDING` (the parent applies the verdict row) · verdict: `PASS 5/5` (this unit's own
table) · verdict_sha_of_head: `e46023f1cd074ce524f30d82eba54d95775cb350`
**Scope:** verification only. F1-F4 resolved against committed blobs; prove-the-negatives re-run on a
`git archive 790aa41` copy; adversarial probes on a `git archive e46023f` copy. Nothing patched, nothing
deployed, no live agent or CT100 stack touched. Artefact: `/opt/data/tmp/probe119/P12-06_VERIFICATION.md`.

## Verdict

F1a PASS · F1b PASS · F2 PASS (record) · F3 PASS · F4 PASS at unit level, live shape UNPROVEN and
labelled. No FAIL row. All five of P12-05's cited `git show` ranges contain the change they describe.

## Evidence

- Suite at `e46023f` (`/tmp/go/bin/go test ./... -count=1 -json`, exit 0): **550 top-level PASS / 0 FAIL /
  2 SKIP; 627 PASS incl. 77 subtests; 11 packages**. Same 627 the parent measured here and P12-05 measured
  at `aad8938` — no drift across P16-01. Skips unchanged: `TestDownloadURL_LiveCrossProcessProof`,
  `TestV1_CreateTransfer`.
- Prove-the-negative at `790aa41`, this leg's own probes: base agent caps and shipped default both lack
  `update`; base refusal answers `200 {"status":"update_in_progress"}`; base recovered SW-24 branch says
  `ROLLBACK_FAILED`; `grep -c 'canaryExit\|abandonFailedCanary' internal/agent/canary.go` → 0. All four
  P12-05 negatives reproduce.
- Adversarial (F1b): a WS write failure (`server_api.go:885-888` → 200 at `server_update.go:290-296`)
  still reports "command sent / update_in_progress" for a command that never reached the agent;
  a refused request still pins the artifact in flight and purges a pre-existing canary registry row
  (contradicting the "Nothing was staged" comment at `server_update.go:262-267`).

## Residuals (R1-R4 in the artefact; none is an acceptance FAIL)

R1 undelivered-write → 200 `update_in_progress` (same class as F1b, one line away) · R2 refusal branch
carries side effects the fix's comment denies · R3 SW-25 emits `DIGEST_MISMATCH` on both branches, so
recovered/unrecovered is still ambiguous at that step · R4 `os.Exit` skips the deferred status retire;
bounded, `ProbeResolved` reports `not_running` with a reason and the old agent's heartbeat rewrites the
record.

## Open item

F4's host-level shape — "exactly one active agent after a real failed swap" — remains UNPROVEN and stays
labelled so: it needs the reviewer to repeat P12-04's forced-failure run and read the registry. R1 is the
one residual worth a decision before any milestone claims "no silent false success" on the update route.
