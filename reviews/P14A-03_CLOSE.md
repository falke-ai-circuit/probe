# P14A-03 — CLOSE

**Commit:** `b67d2c7cd74db3c85dfaa2bdb971af4b90a49dde` (branch `r119-build`, pushed to origin, tree clean)
**Review:** reviewed_by: `PENDING` · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `b67d2c7cd74db3c85dfaa2bdb971af4b90a49dde`
**Scope:** R2 only — ordering inside `handleAgentUpdate` (`internal/server/server_update.go`) plus one test file (`internal/server/p14a03_refusal_side_effects_test.go`). No refusal code, `ForwardRefusal`, state-machine, deploy-path or release-pin change. Artefact: `reviews/P14A-03_R2.md` (`/opt/data/tmp/probe119/P14A-03_R2.md`).

## Delivered

- Purge moved **after** the frame write: was `server_update.go:244`, now `:319`, no other call site in the handler.
  A refused update mutates no registry row.
- The pre-write arm is now disarmed on refusal: `takeUpdateArtifact` + `clearArtifactInFlight` in the refusal
  branch (`:289-291`). Previously the pin stayed held forever, since `handleAgentUpdateResult` is its only other
  release and a refusal never produces that message.
- The refusal comment no longer claims "nothing was staged". The body declares the truth:
  `delivered=false`, `canary_row_purged=false`, `staged_artifact=<managed path|"">`.
- Census of every pre-write side effect, with site and disposition: artefact §2 (copy `:206`, pin `:230`,
  update-artifact map `:231`, ledger+prune `:232`, pre-send log `:256`; audit rows are inside the forward and
  already error-labelled; no `pendingUpdates` entry exists on this path).

## Verification

- Red before the change, quoted in artefact §3: `a refused update removed the canary registry row … the purge
  still runs before the frame is written` → `--- FAIL: TestP14A03RefusedUpdateKeepsCanaryRow`.
- Green after: `TestP14A03RefusedUpdateKeepsCanaryRow`, `TestP14A03DeliveredUpdateStillDropsCanaryRow`, plus the
  P14A-01 (2), P9-08 (2) and P9-14 (2) tests that own this path — all PASS.
- Suite `GOTOOLCHAIN=local GOFLAGS=-mod=vendor go1.23.12 test ./... -count=1`: exit 0, **743 pass / 0 fail /
  2 skip** (baseline `ef412f9` 741 / 0 / 2 + the two new tests). `go vet ./...` exit 0.
- No existing assertion removed, skipped or relaxed.

## Open item

A refusal still leaves the staged file and its ledger row in the managed directory (now declared via
`detail.staged_artifact`, not denied). Unstaging would delete a file the caller may have asked to live there
(`binary_path` semantics), so it is a declared residue — owner decision if refusal should also unstage.
