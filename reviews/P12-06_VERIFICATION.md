VERDICT PASS 5 of 5 /opt/data/tmp/probe119/P12-06_VERIFICATION.md e46023f 550 top-level PASS / 0 FAIL / 2 SKIP (627 incl. 77 subtests)

# P12-06 — independent verification of P12-05 (F1-F4)

reviewed_by: architect (P12-06) · verdict: PASS 5/5 · verdict_sha_of_head: e46023f1cd074ce524f30d82eba54d95775cb350
repo: /opt/data/projects/PROBE_UPGRADE/v119wt, branch r119-build · fix commits 557178b (content) + aad8938 (shipped-config test)
date: 2026-11-24 · toolchain go1.22.5 (/tmp/go/bin/go)

Method: every verdict below is resolved against the committed blob (`git show <sha>:<path>`), never
against P12-05's prose. Prove-the-negatives were re-run by this leg on its own archive of 790aa41
(`/opt/data/tmp/probe119/p1206-base`, `git archive 790aa41`), with probe tests written by this leg —
P12-05's baseline harness is not in the tree and was not trusted. Adversarial probes ran on a separate
`git archive e46023f` copy (`/opt/data/tmp/probe119/p1206-wt`); neither copy touches the repo.

## Verdict table

| item | verdict | cited evidence (blob) |
|---|---|---|
| F1a both default lists + tests that assert | **PASS** | `557178b:internal/agent/capabilities_elevation.go:15-20` (`CapUpdate = "update"`), `:28-31` (`defaultCapabilities` ends `CapExecSystem, CapCredential, CapUpdate`); `557178b:cmd/probe/config.go:193-199` (shipped default list literal `"update"`); `557178b:internal/server/capability_inventory.go:32-35` (`knownCapabilities` contains `"update"`); tests `557178b:internal/agent/p1205_fix_test.go:19-42`, `aad8938:cmd/probe/p1205_capability_default_test.go:17-28` |
| F1b pre-send refusal = typed non-2xx, no `update_in_progress` on that path | **PASS** (item as written) | `557178b:internal/server/server_api.go:772-792` (typed `ForwardRefusal` + two codes), `:816-844` (both pre-send checks return `*ForwardRefusal`); `557178b:internal/server/server_update.go:262-282` (`errors.As` → 403 `CAPABILITY_MISSING` / 503 `AGENT_NOT_CONNECTED`, `delivered:false`, `return` before the 200), `:290-296` (200 only on the post-send branch); tests `557178b:internal/server/p1205_update_refusal_test.go:17-64` (403 + body must not contain `update_in_progress`), `:66-105` (503) |
| F2 published record names canonical agent + executable, survives heartbeat | **PASS** (record) | `557178b:internal/agent/statusfile.go:17-34` (publisher owns `State`, `identity()` rewrites Name/Executable), `:37-43` (heartbeat writes the same `p.st`), `:133-141` (`republishStatusIdentity`); `557178b:internal/agent/canary.go:106-118` (`promoteToCanonical`: clear `CanaryMode`, `TrimSuffix` `-canary`, republish), `:197-219` (`swapAndKillOld` returns the canonical path = `TrimSuffix(exe, ".next")`); test `557178b:internal/agent/p1205_fix_test.go:48-110`, heartbeat re-read at `:95-102` |
| F3 recovered → ROLLBACK_RECOVERED, unrecovered → ROLLBACK_FAILED, both pinned | **PASS** | `557178b:internal/upgrade/canonical.go:24-30` (`CodeRollbackRecovered = "ROLLBACK_RECOVERED"`); recovered sites `557178b:internal/upgrade/swap.go:267` (SW-22), `:285` (SW-23), `:299` (SW-24); unrecovered sites `:263-265`, `:282-283`, `:295-297` still `CodeRollbackFailed`; tests `557178b:internal/upgrade/p1205_rollback_code_test.go:85-97` (recovered → `ROLLBACK_RECOVERED`, step SW-24), `:101-107` (restore also fails → `ROLLBACK_FAILED`) |
| F4 failed swap exits (code 3, conn closed, no reconnect); live shape UNPROVEN | **PASS** (unit) / live shape UNPROVEN as required | `557178b:internal/agent/canary.go:57-61` (`runCanaryCommit` calls `abandonFailedCanary` on swap error), `:80-81` (`canaryExitCode = 3`), `:88-90` (`canaryExit` seam), `:129-135` (log, `conn.Close()`, `canaryExit(3)`, `CanaryMode` left true); no-reconnect branch `557178b:internal/agent/agent.go:385-388`; test `557178b:internal/agent/p1205_fix_test.go:129-181` (swap errors, exit called with 3, conn closed, `CanaryMode` true); UNPROVEN label present in `P12-05_FIX.md:80` and `P12-05_CLOSE.md:36` |

5 of 5 verified. No FAIL row. Four residuals are recorded below; none contradicts an acceptance item as
worded, and none is patched (verifier does not fix).

## Prove-the-negative re-run at 790aa41 (this leg's own probes)

Archive `git archive 790aa41`, probe tests written by this leg, `go test -run P1206Base -v`:

    internal/agent   BASE agent.defaultCapabilities advertised: [exec filesystem process tunnel mitm debug
                     capture input clipboard exec.system credential]  (no "update")     --- PASS
    cmd/probe        BASE shipped default client capabilities: [exec filesystem process tunnel mitm debug
                     capture input clipboard]  (no "update")                            --- PASS
    internal/server  BASE refusal status=200 body={"message":"agent_update command sent...","status":
                     "update_in_progress","version":"1.19.2"}                           --- PASS (the defect)
    internal/upgrade BASE recovered-branch code=ROLLBACK_FAILED step=SW-24 err=SW-24: ROLLBACK_FAILED:
                     rename new into place: forced rename failure (rolled back, sha fe3350f3... size 17
                     verified)                                                          --- PASS (the defect)

    $ git show 790aa41:internal/agent/canary.go | grep -c 'canaryExit\|abandonFailedCanary'  -> 0

All four P12-05 negative claims reproduce at the ancestor. `790aa41` is an ancestor of HEAD
(`git merge-base --is-ancestor` → yes).

## Adversarial attempt (F1b, the strongest claim)

Two probes in a scratch `git archive e46023f` copy, `internal/server`, each driving the real handler via
the shipped `postUpdate`/`newUpdateTestServer` harness. Output verbatim:

    PROBE B: ws-write-failure status=200 body={"message":"agent_update command sent. The agent will
             download and restart. Check /api/agents for the new version.","status":"update_in_progress",
             "version":"1.19.2"}
    PROBE C: refusal status=403 body={"ok":false,"error":{"code":"CAPABILITY_MISSING","message":"agent
             p1206-side-effects lacks required capability \"update\" for command \"agent_update\"",
             "detail":{"agent_id":"p1206-side-effects","delivered":false}}}
    PROBE C: artifact pinned in flight after refusal = true
    PROBE C: canary registry row PURGED by the refused request: agent p1206-side-effects-canary not found

Probe B (the attempt to make a non-delivery answer 2xx): a live connection whose `WriteJSON` fails —
i.e. the frame never reaches the agent — passes both pre-send checks and falls into the post-send
branch, which answers 200 `update_in_progress` with the text "command sent"
(`server_api.go:885-888` → `server_update.go:290-296`). The refusal path itself is closed; the
undelivered-write path is not. This does not change F1b's verdict, which is scoped to refusals, but it
is the same false-success class still open one line away (R1).

## Residuals (named, not patched)

- **R1 (medium, same class as F1b).** A WS write failure after the pre-send checks still answers 200
  `update_in_progress` ("command sent") for a command that never left the server —
  `server_api.go:885-888` returns a bare `fmt.Errorf`, so `errors.As` fails and the handler takes the
  200 branch. Measured (PROBE B). Suggested shape for the owner: a third typed code
  (`DELIVERY_FAILED`) or treat a write error as pre-send.
- **R2 (low, comment vs code).** `server_update.go:262-267` states "Nothing was staged, nothing will
  restart" on the refusal branch, but the handler has already run `markArtifactInFlight`,
  `setUpdateArtifact`, `registerArtifact` (`:212-223`) and `purgeCanaryRegistryRow` (`:244`) before the
  forward at `:260`. Measured (PROBE C): after a 403, the artifact is still pinned in flight and a
  pre-existing `<agent>-canary` registry row is gone. The HTTP contract is correct; the inline claim is
  not.
- **R3 (low, F3 scope).** SW-25 emits `DIGEST_MISMATCH` on both its recovered and unrecovered branches
  (`swap.go:307`, `:310`, the first carrying "ROLLBACK FAILED:" inside the message), so at that step a
  failed restore is still indistinguishable from a recovered one — the ambiguity F3 removed at
  SW-22/23/24. P12-05 disclosed the code as distinct but not that it is symmetric.
- **R4 (low, F4 second-order).** `abandonFailedCanary` exits via `os.Exit`, which skips the deferred
  `stopStatus()` (`agent.go:339-340`), so the abandoned canary's run-state record survives on disk.
  `lifecycle.ProbeResolved` validates liveness (`status.go:295-298`) and reports `not_running` with the
  reason, and the surviving old agent's next heartbeat rewrites the shared record, so this is a bounded
  reporting window, not a false "running" claim.
- **F2 argv** — unchanged (`--canary --canary-old-pid …`), as declared open in `P12-05_FIX.md:61` and
  `P12-05_CLOSE.md:35`. Not a FAIL under the item's wording.
- **F4 live shape** — "exactly one active agent after a real failed swap" is UNPROVEN (no host run in
  this leg; the brief forbids touching CT100). It stays labelled UNPROVEN; the unit-level exit path is
  what is verified.

## Test-harness observations (not defects of the fix)

- `p1205_fix_test.go:168` calls `abandonFailedCanary` directly instead of driving `runCanaryCommit`,
  so the wiring at `canary.go:59` is not pinned by any test — a regression deleting that call would
  leave the suite green. The code at that line is correct today.
- Both F1a tests assert membership of the constant `CapUpdate`, not the literal `"update"`. The wire
  literal is pinned only server-side (`enrollment.go:463-464`). A change to the constant's value would
  break the gate with both agent-side tests still green.

## Suite (this leg, at e46023f)

    $ /tmp/go/bin/go test ./... -count=1 -json     # exit 0
    550 top-level PASS / 0 FAIL / 2 SKIP ; 627 PASS incl. 77 subtests ; 645 events ; 11 packages
    skips: internal/server TestDownloadURL_LiveCrossProcessProof, TestV1_CreateTransfer

Parent measured 627 incl. subtests at e46023f; P12-05 measured 627 at aad8938. This leg reproduces
627 at e46023f — count identical, no drift across the P16-01 commit.

## Out of scope / blocked

Windows update, Windows service install, reboot survival, logoff survival: no Windows host, no SCM,
PID 1 is s6-svscan (unchanged from P12-01…P12-04). Not passing by omission.
