# P13-04 — close

**Commit:** `7274ec2` (P13-01), `f243257` (P13-02), `b6a32f7` (P13-03) — reviewed at head `5d37f9f`
(the three commits are ancestors; the commits above `b6a32f7` are documentation)
**Review:** `reviewed_by: reviewer` · verdict **FAIL** · `verdict_sha_of_head: 5d37f9f`
**Scope:** independent review of the P13 change set against `docs/contracts/P10-07_CONTRACT_V3_1.md` §5.1, §5.4,
§5.5 (14 rows), §6, §7, §7.1 — per-item verdicts, adversarial probes, repeated whole-suite runs, `go vet`.
No fixes; no CT100 or live agent touched; worktree-only instrumentation, main tree clean.

## Verdict — 4 of 5 PASS, 1 FAIL

| # | item | verdict |
|---|---|---|
| 1 | §5.5 strict decode, 14 rows | PASS |
| 2 | E1 / E2 enforcement | **FAIL** |
| 3 | §6 contexts | PASS |
| 4 | §7 window tree | PASS |
| 5 | §7.1 refusal | PASS (code) / proof QUARANTINED |

**Blocking finding.** `POST /exec` escapes E1/E2 and §5.1 range/conflict validation: `ExecParams` has
`Context`/`Session` but no `Selector()`, so `preDispatchSelector`/`selectorPresent` classify it as
selector-free (`internal/server/strict.go:130-146`). Probe: `200` with `{"context":"wts:7"}` on a 1.19 agent that
advertises `exec` but not `context.selector`; control `pointer-click` → `409 UNSUPPORTED_AGENT`. `P13-01_STRICT.md`
row 1 claims `E1 yes / E2 yes`.

**Second finding, test-only.** `TestP1303RealXvfbRefusesNoWindowManager` is red in 4 of 8 whole-suite runs
(695/1/2 vs 696/0/2) and 1 of 10 single-test runs. Product code is correct (probe returns the honest `wmUnknown`;
refusal branch proven by the seam test). Cause is the harness: display `90+pid%8` (8-value space), readiness
accepts a socket+handshake rather than a serving server, SIGKILL cleanup leaves sockets and locks. §7.1 is
therefore **not quotable** as a green-gate result until the harness is fixed.

Suite: 695 pass / 1 fail / 2 skip (worst of 8 runs at head); `go vet ./...` exit 0.

Artefact: `/opt/data/tmp/probe119/P13-04_REVIEW.md`.
