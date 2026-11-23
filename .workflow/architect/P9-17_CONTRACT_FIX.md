# P9-17 — CONTRACT FIX: closes the P9-15 FIX-FIRST review (contract only, no code)

STATE: profile=architect · gateway=running · role=contract-fix · tree=/opt/data/projects/PROBE_UPGRADE/v119wt · branch=r119-build · head at work=`70de7fa03f879e0cdc48dc9048a5d25c69121211` (code head graded by P9-15 = `4f38ed0`; only `Makefile` + `internal/version/version.go` landed since — no citation target moved).

| field | value |
|---|---|
| artefact | `/opt/data/tmp/probe119/P9-17_CONTRACT_FIX.md` (canonical copy `.workflow/architect/P9-17_CONTRACT_FIX.md`) |
| contract delivered | `P9-08_CONTRACT_V5.md` — `/opt/data/tmp/probe119/` + `.workflow/architect/`; v4 (sha256 `e0fa7132…8634`) is preserved byte-identical, untouched |
| reviewed_by | **architect (P9-17)** — self-verified by script; the framing inputs are P9-15's two independent reviews |
| verdict | **FIX-FIRST CLOSED** — F1 job scope, F6 kinds, citation drift and the torn-tail epoch are closed. Ratification remains **NOT-READY-until-re-review**: a re-review of v5 is owed, and ratification is Goran's |
| code changed | **none** — no `.go`, handler or test edit; `git status` clean before and after |

## 1. What was opened vs what was left alone

P9-15's delta graded F2 and F4's winner rule PASS, and P9-12 graded F3/F5 PASS. None of those three regions was re-opened. The four surviving items were closed in one pass, and every closure is a sentence or a table an implementer can act on — not a reword.

## 2. Citation resolver — verbatim output (acceptance quote 1)

Rule: every backticked `file.go:N` / `file.go:N-M` site with an explicit file, plus bare `:N` and comma-list continuations inherited from the preceding file in the same line; each basename resolved against `git ls-tree -r 70de7fa`; span bounds-checked against the blob at that revision; a probe asserts the literal the contract says the span contains.

```
$ python3 /opt/data/tmp/probe119/P9-17_cite_audit.py P9-08_CONTRACT_V5.md HEAD
contract : /opt/data/tmp/probe119/P9-08_CONTRACT_V5.md
rev      : 70de7fa03f879e0cdc48dc9048a5d25c69121211
refs total (explicit file:line sites) : 169
continuation refs (:NN inherited)     : 110
distinct files    : 21
distinct spans    : 141
out-of-range      : 0
mismatched        : 0   (probes 81/279 refs; 26 distinct spans)
VERDICT: PASS
```

Baseline, same script, the graded v4 at the same head — the drift is reproduced, not inherited:

```
$ python3 /opt/data/tmp/probe119/P9-17_cite_audit.py P9-08_CONTRACT_V4.md HEAD
refs total (explicit file:line sites) : 143
continuation refs (:NN inherited)     : 93
distinct files    : 19
distinct spans    : 125
out-of-range      : 1
   OOR  doc:58 internal/upgrade/rollback.go:93-116  (span 93-116 outside 115 lines)
mismatched        : 0   (probes 61/236 refs; 21 distinct spans)
VERDICT: FAIL
```

Counting-rule reconciliation, stated rather than smoothed: the reviewers' regex finds **145** citing sites on v4; my resolver finds 143 under a stricter rule. The two it does not count are `agent.go:227-230` and `messages.go:587-592`, both written mid-group behind text my parser does not enter. All 145 resolve in range, none is stale. V5 is at 169 because the fix **adds** 26 refs (the F6 field table, the torn-tail rule, the F1 scope proof) while re-anchoring the 8 drifted spans and correcting one out-of-range span. None of the three numbers is borrowed.

Honest finding of my own, not raised by either reviewer: `rollback.go:93-116` was **past EOF** at the head (file is 115 lines) — the construct is `RestoreAndRelaunch`, now cited `rollback.go:93-112`.

## 3. F1 — the canary-grant job scope, in one sentence (acceptance quote 2)

> **A `Source:"canary_argv"` grant authorises the kill of exactly one process — the identity it captured at startup — and is not scoped to a job id, because the argv that creates it carries none (`canary_launch.go:24-31` launches `connect --config <path> --canary --canary-old-pid <n>`); therefore the `job_id` comparison (condition 3) is enforced only for `Source:"server_adopt"` grants, and a `job_id` arriving on a `proc_kill` handled under a canary grant is neither required nor compared and can never produce `job_mismatch`.**

Why this reading and not the other: the argv has no place for a job id (`canary_launch.go:24-31` — verified at HEAD), so a job-scoped canary grant would refuse every canary-hop kill, which is the primary 1.19 path and the exact failure v4's own F1 test list assumes away. The alternative — adding a job id to the argv — would change the launch contract for a comparison the grant does not need, since the grant is already bound to a re-read process identity.

Rest of the contract made consistent: condition 3 rewritten as `server_adopt`-only; the `killAuthorization.JobID` comment says it is always `""` for canary grants and never compared; F1 test 1 names `Source:"server_adopt"` explicitly; **test 8** exercises the canary grant against `proc_kill{job_id:"upd_01HX…"}` → `killed:true` and again with no `job_id` → `killed:true`, with the `server_adopt` mismatch control → `job_mismatch`; **test 9** asserts a job id confers no authority without a grant. F3's invariant I-1 is untouched: the server's order still carries the job id — only the comparison is scoped.

## 4. F6 — the replacement kinds, re-grounded on fields that exist (acceptance quote 3)

v4 claimed its two kinds came from "fields the job row already carries". They did not: `writer_boot_id`, `s.bootID` and `last_report_at` have **0 hits** at HEAD (`git grep -n 'bootID\|writer_boot\|last_report_at' -- '*.go'`), `pendingUpdate` carries no phase and no report timestamp, and `ServerJob.Phase`/`LastReportUTC` belong to the P8-04 detached-job record. All three invented identifiers are deleted. Each replacement field, with the file:line proving it exists:

| field the kind now rests on | exists at HEAD | written at |
|---|---|---|
| `idemStateInFlight = "in_flight"` | `internal/server/idempotency.go:33` | `begin()` record literal `idempotency.go:166-170` |
| `idemStateUnknown = "unknown"` | `internal/server/idempotency.go:35` | `load()` `idempotency.go:106`; `markUnknown` `:208` |
| the restart transition IN_FLIGHT→UNKNOWN at startup | `internal/server/idempotency.go:106` | `load()`, called on first use |
| `begin()` returning the third state on replay | `internal/server/idempotency.go:160` | `case idemStateUnknown:` |
| the persisted store (on disk, survives restart) | `internal/server/idempotency.go:59-64`, `:117-137` | `saveLocked()` body |
| `pendingUpdate` | `internal/server/server_update.go:27-32` | record literal `:289-294` |
| `pendingUpdate.NotifiedAt` | `internal/server/server_update.go:31` | `:293` |
| `AgentUpdateResult` (its absence is the observation) | `internal/protocol/messages.go:798-803` | agent → server |
| 409 `IDEMPOTENCY_STATE_UNKNOWN` | `internal/server/server_api.go:309` | exec arm `:308-313` |

- `restart_mid_transition` = the update's idempotency record reads `unknown`, i.e. it was `in_flight` when the process died and `load()` rewrote it (`idempotency.go:106`). **No boot id is needed**: the record is on disk and the rewrite is the restart detector. This is the honest replacement for the deleted `writer_boot_id != s.bootID`.
- `agent_report_missing` = `pendingUpdate.NotifiedAt` older than the derived `confirm_timeout_s` **and** no `AgentUpdateResult` stored. `NotifiedAt` is the only timestamp required; `last_report_at` is deleted.

The one addition this fix makes is named with its call site: the update start must claim a key — `s.idempotency().begin(agentID, "update:"+artifact_sha256)` at the top of `handleAgentUpdate` (`server_update.go`), completed by `complete` (`idempotency.go:179-190`) or `markUnknown` (`:204-210`). Without that call the store holds no update record and the kind would be as unproducible as the field it replaces; with it, the kind rests on code that already exists and is already tested. No new struct, no new file, no new field.

The duplicate rule's four non-terminal labels are now defined by what makes each true (record exists / result stored / kill ordered / row purged + version), so the vocabulary is decidable from server-held data instead of asserting a phase enum the tree does not have.

## 5. Torn-tail epoch — explicit definition (acceptance quote 4)

> **Complete record:** a line that (i) ends in `\n`, (ii) whose event token is `SWAP_EPOCH`, and (iii) whose `step=` value is one of `start` / `rename1` / `rename2` / `done`. **When the torn tail starts:** at the first byte after the last `\n` in the file — it exists when a write was interrupted (crash or power loss during the single `O_APPEND` write of a record leaves a prefix with no terminator), and it ends when the next append begins (the writer first writes `\n` if the file does not end with one), or when the file stops being written. **Before it:** each complete record is evidence that its step was *reached*; the current epoch's last complete record is the only fact the watchdog may branch on. **Inside it: nothing may be assumed** — a fragment reading `step=done` proves no completion, a fragment reading `step=rename2` proves no second rename. **What the watchdog does:** it discards the tail — and any line whose `step=` value is outside the four names — and branches on the last complete record. Falling back can only move the decision to an *earlier* step, and every earlier row restores or verifies and never skips, so a torn tail cannot cause the skip D4 closed. A torn `step=done` falls back to `step=rename2`, which requires `sha(canonical) == expected` before it skips.

Why this is stronger than "assume the last parseable line": `ParseJournal` keeps a truncated fragment as a line whenever its event name is valid (`journal.go:89-107` rejects only an invalid event name), so "last parseable line" is not a safe rule — a fragment carrying `step=done` would parse. The epoch reader is therefore specified as stricter than `ParseJournal`, with the enum check explicit. Test **(f)** is added: truncate mid-token, assert the preceding complete record governs (restore for `rename1`, `WATCHDOG_SKIPPED reason=swap_completed` for `done`), and assert the next append still parses as its own line.

## 6. Re-anchored spans at the head (N2)

| v4 span | claimed | v5 span |
|---|---|---|
| server_ws.go L82-89 | plain `json.Unmarshal` of `AgentInfo` | `server_ws.go:108-115` |
| server_ws.go L82-116 | decode + conn map | `server_ws.go:108-142` |
| server_ws.go L93 | `agentID := info.Name` | `server_ws.go:119` |
| server_ws.go L113-116 | conn / `connPID` record site | `server_ws.go:139-142` |
| server_ws.go L107 | `registry.Register(...)` | `server_ws.go:133` |
| server_ws.go L120-139 | `delete(pendingUpdates)` before sleep + kill | `server_ws.go:146-165` (delete `:149`) |
| server_ws.go L130-139 | "sleeps 3 s and kills" | `server_ws.go:47-61` (`completeUpdatePromotion`) |
| server_ws.go L140-148 | the purge, **inside the success branch** | `server_ws.go:41-61` — and the state changed: P9-14 hoisted it out of both arms, so the defect this ref names is **already fixed** at head |
| embed.go L9 | `//go:embed` directive | `embed.go:15` |

Historical columns (`v3 ref`, `v2 (as reviewed)`) are labelled as records of what an earlier document said at `b32b8c7`; they are not head claims and carry no probes.

## 7. Residuals — carried, not closed, and said so

| # | item | status |
|---|---|---|
| R1 | `killAuthValid` reads identity then kills by PID (no `pidfd`) | unchanged; accepted in writing as a microsecond window in the contract's own residual paragraph — closing it is implementation, and this unit is read-only |
| R2 | POSIX equality predicate does not say *whose* `ExeIno` the zero test refers to | unchanged (F1's predicate is inside a PASS region); one word owed to the implementer |
| R3 | §F2 L3 rule (i) states a check the server cannot stat | unchanged (F2 region, PASS) |
| R4 | epoch file is append-only with no rotation | unchanged; bounded by one update per agent per lease |
| F | the call-site test (`p914_code_leg_test.go`) is still uncommitted | **fold-forward, not closed here** — this unit is contract-only and commits no test |
| I | nothing in F1-F6 is implemented — `agent_proc.go:44-52` is still the one-input gate, `canary.go` still kills without an order, there is no `killAuth`, no lease, no `writer.go` | unchanged and still true; P9-03..P9-06 remain blocked behind ratification |

## 8. Close record

| field | value |
|---|---|
| artefact | `/opt/data/tmp/probe119/P9-17_CONTRACT_FIX.md` (canonical `.workflow/architect/P9-17_CONTRACT_FIX.md`) |
| contract | `P9-08_CONTRACT_V5.md` — 41 asserted edits over v4, build fails on any anchor mismatch (`P9-17_build_v5.py`) |
| refs checked | **169** explicit file:line sites + 110 continuation refs, 21 files, 141 distinct spans |
| out-of-range | **0** (v4 baseline: 1 — `rollback.go:93-116`, now `93-112`) |
| mismatched | **0** — 81 refs carry an asserted literal, 26 distinct spans; the remainder are bounds-checked only |
| open items | re-review of v5 owed (ratification NOT-READY, Goran's); R1-R4 residuals accepted in writing; call-site test still to fold in; F1-F6 still unimplemented |
| code changed | none |
| verdict | **FIX-FIRST CLOSED / NOT RATIFIED** |
