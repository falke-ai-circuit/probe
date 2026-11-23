VERDICT FIX-FIRST /opt/data/tmp/probe119/P9-15_REVIEW.crosscheck.md 4f38ed0c07640198b70137313f20f52c78eda17c 460P/0F/1S (+55 subtests, 4 pkgs ok)

# P9-15 — independent review, second writer (cross-check of the filed verdict)

**Why this file exists.** A P9-15 review was already written and committed while I was grading: `reviews/P9-15_REVIEW.md` at `774fb5b` (16:36:29Z), amended by `e9666a0`. That is a double dispatch of the same unit, not a revision of mine — I saw the repo clean at `4f38ed0` at 16:16Z and the review landed mid-session. I did not overwrite it (destroying a filed verdict to file a second one is worse than filing alongside). This document is my own grading, done from the same head, with my own scripts, my own suite run, my own mutation probe; §8 states exactly where it agrees with and where it diverges from the filed review.

| field | value |
|---|---|
| unit | P9-15 · milestone P9 · workflow wf_probe1 |
| artefact | `/opt/data/tmp/probe119/P9-15_REVIEW.crosscheck.md` (committed copy `reviews/P9-15_REVIEW.crosscheck.md`) |
| reviewed_by | **reviewer=independent** — I authored none of the contract, nothing in 4f38ed0, and none of the filed review |
| verdict | **FIX-FIRST** — v4's four corrections are substantive; F2/F4 closed, F1 closed on identity but carries one M1 contradiction, F6 not closed; code leg correct and its test bites |
| verdict_sha_of_head | `4f38ed0c07640198b70137313f20f52c78eda17c` — code head under grade (branch `r119-build`); the only commits after it are review documents |
| suite (mine) | build 0 · vet 0 · **460 top-level PASS / 0 FAIL / 1 SKIP**, 55 subtests, 516 `=== RUN`, 4 packages ok |
| contract graded | `P9-08_CONTRACT_V4.md`, 578 lines, sha256 `e0fa713228828404f6333bd9634e9dd45e2e6a79a497d2d4c4b3073329248634` (= the claimed `e0fa7132…8634`) |
| ratification | **NOT-READY**, and I do not ratify — Goran's decision |
| shape | **SHAPE reuse** `internal/server/server_ws.go:completeUpdatePromotion` (hoisted inline body, no second path) · **SHAPE reuse** `internal/server/artifacts.go:156 ManagedPath` (4 download sites share it, no parallel joiner) · **SHAPE none** for contract v4 |

Toolchain note: `go build` with `/opt/data/go/bin/go` fails compiling the **runtime** (`mallocHeaderSize redeclared`; that GOROOT is a mixed 1.21/1.24 source tree). All numbers below come from `go1.24.4` at GOROOT `/opt/data/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.24.4.linux-amd64`.

RECIPIENT EXPERIENCE: I ran the build, vet and four-package suite at `4f38ed0`; built clean worktrees of `c9538ba` (fails) and `4f38ed0` (builds); rebuilt `web/dist` from source with node v26.5.1 / npm 11.17.0 and diffed it against the committed bundle; and ran the new purge test RED (mutant) and GREEN.

---

## A1 — the delta, graded as an answer to the P9-12 failures

| # | what P9-12 failed it for | v4's answer, opened at the cited region | verdict |
|---|---|---|---|
| **F1** | grant keyed to a recyclable PID with a TTL — no start-time/exe binding; condition 3 undecidable | grant carries `procIdentity` (StartTicks/ExeDev/ExeIno POSIX, CreatedAt/ExePath Windows) captured at registration and **re-read at kill time** with equality required (`killAuthValid` cond. 4 → `detail.reason:"identity_changed"`); the ancestry condition is **deleted** and replaced by image identity with named sources plus `old_pid >= 2`, `old_pid != os.Getpid()` (§F1:169). Mechanism, constant and source are all named — not a reword. | **CLOSED on identity; one M1 contradiction left → FIX-FIRST** |
| **F2** | L3 was a non-computable product; `ProcessInfo` wire fields unnamed | `started_at_unix = btime + start_time_ticks/CLK_TCK`, `CLK_TCK = 100`, `AT_CLKTCK` (type 17) overrides, seconds used only for ordering (§F2:202); four additive fields named with JSON tags (§F2:236-248) against a struct I re-opened (`messages.go:587-592` = `pid,name,cpu_percent,memory_mb`, so the extension is genuinely additive). Field 22 is starttime; the arithmetic is right. v4 also retracts v3's `connPID` claim and cites the real overwrite site (`server_ws.go` conn map). | **CLOSED** |
| **F4** | winner rule branched on the epoch number, so crash-after-rename1 skipped the restore | the branch is on the **step** of the current epoch's last line (`start`/`rename1` ⇒ restore candidate under the lease, `rename2` ⇒ sha-verify then escalate, `done` ⇒ skip), the restore re-checks `canonical` missing/sha-mismatch **and** `sha(.old)==sha(self)` inside the lease (§F4:324), and test (e) crash-after-rename1 is added with the assertions the old rule failed. The two renames are where v4 says (`canary.go:163,167` — verified). | **CLOSED** |
| **F6** | duplicate rule stated twice; `kind:"unreadable_journal"` unobservable server-side | the duplicate rule is now one rule keyed to job state (non-terminal + `done` ⇒ 202 same `update_id` + `replayed:true`; terminal-failure ⇒ 409 `UPDATE_PREVIOUSLY_FAILED` unless `new_attempt:true`), `duplicate_request` is gone from every enum; `unreadable_journal` is replaced by two kinds. **But the replacement kinds rest on identifiers that do not exist and the document says they do** — see below. | **FIX-FIRST** |

**F1 — the contradiction (M1).** `killAuthorization.JobID` is `""` for the argv-sourced canary grant (§F1:131) and condition 3 refuses unless "the job ids match" whenever the command carries `job_id` (§F1:150). The kill order in the designed flow is server-issued and job-scoped. Strictly read, **every canary-hop kill returns `job_mismatch`** — the primary 1.19 path; charitably read ("require it for auth-sourced kills" exempts `canary_argv`), it works. The document does not say which, and its own F1 test list never exercises a canary grant against a job-scoped order (§F1:182 presumes a grant that matches `job_id:J`). This is the same defect class P9-12 failed v3 for: one rule two readings. One sentence fixes it — either the canary grant takes its job id from argv, or condition 3 is explicitly skipped for `Source=="canary_argv"` and the test table gains that case.

**F6 — the E4 claim (M1 for ratification).** §F6:394 says the two kinds "are derived from fields the job row **already** carries". At `4f38ed0`:

```
git grep -n 'bootID\|writer_boot\|last_report_at' -- '*.go'   → 0 hits
```

`ServerJob` (`internal/server/jobs.go:28-40`) has `Phase` (written `"running"` at `:69`) and `LastReportUTC string` (`:38`) — not `job.writer_boot_id`, not `s.bootID`, not `job.last_report_at`, and the phase enum F6 keys on (`dispatched|adopted|awaiting_ack|promoted`) is not the tree's vocabulary. So the replacement kinds are as unproducible as the kind they replaced, and the sentence asserting they pre-exist is a claim about the tree that the tree contradicts. The fix is cheap (label the three fields and the enum additive, name the write site / boot-id source, or map onto `ServerJob.Phase` + `LastReportUTC`), but until it is made F6 is not closed and the doc cannot be consumed as-is.

**Residuals in the corrected regions (named, not blocking).**
- R1: `killAuthValid` reads identity then kills **by PID**; `pidfd` appears nowhere in the document (`grep` = 0). Microseconds, not 600 s — the graded defect is answered, but the window exists and should either be closed on Linux or accepted in writing.
- R2: the POSIX equality predicate "`StartTicks` equal and (`ExeIno == 0` || `ExeIno` equal with `ExeDev`)" (§F1.1:198) never says **whose** `ExeIno` the zero test refers to; "live is 0 ⇒ accept" is a fail-open reading. One word needed.
- R3: §F2:252 states L3 rule (i) as `exe_ino == ino(<canonical>.old)`, which the **server** cannot stat; §F2:257 correctly hands the server a wire-only proxy. Two executors, one sentence — say which clause belongs to which.
- R4 (M3): the epoch file is append-only with no rotation, unlike the journal (§F5:354).

---

## A2 — citation audit, my own extraction and my own resolution

Own scripts: `/opt/data/tmp/probe119/P9-15_cite_audit.py` (extract + bounds at head) and `P9-15_drift.py` (same span compared with `b32b8c7`, the contract's declared anchor). I did not run the child's `p908_v4_cite_audit.py`.

| measure | mine | contract §9 claim |
|---|---|---|
| citing sites | **145** | 145 |
| distinct spans (path:start-end) | **80** | 77 (different counting rule; both internally consistent) |
| files cited | **19** | 19 |
| unresolved paths | **0** | 0 |
| out-of-range spans at `4f38ed0` | **0** | 0 |
| spans byte-identical at `b32b8c7` and `4f38ed0` | **72 / 80** | not measured |
| spans that no longer describe the head | **8 / 80** | "0 mismatched" — true only at `b32b8c7` |

The eight stale spans, all moved by `4f38ed0` (each one resolves correctly at `b32b8c7`, which I checked line by line: `:93`=`agentID := info.Name`, `:114`=conn map, `:123`=pendingUpdates delete, `:137`=3 s sleep, `:147`=purge-inside-success — so this is drift, not fabrication):

| cited | claims to show | at 4f38ed0 | now at |
|---|---|---|---|
| `server_ws.go:82-89` | plain `json.Unmarshal` of AgentInfo | reconnect error handling | :108-116 |
| `server_ws.go:82-116` | decode + conn map | auth/reconnect block | :108-142 |
| `server_ws.go:93` (6 citing sites) | `agentID := info.Name` | blank line | :119 |
| `server_ws.go:113-116` | conn/`connPID` record site | unmarshal error path | :139-142 |
| `server_ws.go:120-139` | `delete(pendingUpdates)` + 3 s sleep | `agentID==""` fallback + revoke check | :145-165 |
| `server_ws.go:130-139` | "the place that blindly sleeps 3 s and kills" | session create + conn store | :155-165 |
| `server_ws.go:140-148` | purge inside the success branch | `connWriteMu` store | :166-174 |
| `embed.go:9` | the `//go:embed` directive | P9-14's added comment | :15 |

Consequence: the F1/F2/F3 **code-side anchors sit +26 lines off at the head**, and one state v4 orders changed (the blind sleep with the purge inside the success branch) has already been restructured by 4f38ed0. The A1 verdicts above stand because I opened the regions at the contract's declared anchor; but a contract consumed at the head without re-anchoring puts an implementer in the wrong code.

Load-bearing spans I opened and read myself: `agent_proc.go:44-52` (one-input gate — unchanged), `agent_proc.go:104-106`, `agent.go:227-230`, `agent.go:453-455`, `canary_launch.go:24-31`, `connect.go:52-58`, `server_api.go:468-474` (`hasCapabilityStrict`), `:793-815` (the `Bypass` comment), `:480-490` (`writeTypedErrorDetail`), `api_v1.go:184-206` (`reformatV1`), `idempotency.go:8-20` (three states), `journal.go:7-12` + `:89-107` (`isJournalEventName`), `messages.go:585-601`, `server_update.go:265-296` (`OldPID` `:291`, `NotifiedAt` `:293`), `canary.go:126-139,145-180`, `server_ws.go` (all seven mapped above), `embed.go`. Each says what the contract says it says.

---

## A3 — 4f38ed0 as code

| item | evidence | verdict |
|---|---|---|
| purge on **both** completion paths, kill error included | `server_ws.go:44-66`: `completeUpdatePromotion` sleeps, sends `proc_kill`, logs in the error branch, logs in the success branch, and calls `s.purgeCanaryRegistryRow(agentID)` **after** the if/else — one call, outside both arms. The only entry is `server_ws.go:162` `go s.completeUpdatePromotion(agentID, oldPID)` inside the `pendingUpdates` hit; there is no second completion path. | **PASS** |
| the new test is not a tautology | Mutation probe in a throwaway worktree: moving the purge back inside the success branch makes `TestP914CompletionPurgesCanaryOnKillError` **fail** with "kill error left the canary registry row behind: the purge is still inside the success branch"; unmutated head → both new tests pass, canonical row asserted intact (`p914_update_purge_test.go:106-111`). The failing conn fails the transport, not the capability gate (the test registers `process` first). | **PASS (bites)** |
| "the test the call site lacked" | Only `p914_update_purge_test.go:104,130` reference `completeUpdatePromotion`; no test in the tree calls `handleWebSocket`. The goroutine spawn, the `pending.Promoted` gate and the `pendingUpdates` lookup that lead to the purge are untested — a refactor could drop the call and every test would stay green. | **FINDING (M2)** — the commit message overstates what was pinned |
| `ManagedPath` census 4/4, zero residual literals incl. prose | `grep -rn 'FilesDir() *+ *"/"' --include=*.go` = **0**; four sites — `api_v1_transfer.go:232,260`, `server_download.go:59,83` — each with an `err → 400` arm; both files' comments rewritten to name ManagedPath. `ManagedPath` (`artifacts.go:156-165`) is stricter than the old join (empty/`.`/`..`/slash refused) and both callers already reject `..` and `/`, so no legitimate name regresses. The new `err` arm is unreachable behind those pre-checks (M3). | **PASS** |
| committing `web/dist` changes a clean checkout | Clean worktree at `c9538ba`: `go build ./...` → `embed.go:9:12: pattern all:web/dist: no matching files found`, exit **1**. Same worktree at `4f38ed0`: exit **0**. `git check-ignore web/dist/index.html` → not ignored; 3 blobs tracked. `npm ci && npm run build` from `web/` reproduces `index-BiOCVBVG.js` / `index-DAbJBep9.css`, and `diff -rq` against the committed tree is **identical** — sha256 `665ac68…` for the JS both sides. The committed artefact is the real build, not a stale snapshot. | **PASS (both directions + staleness)** |

Code leg overall: correct, minimal, and the defect P9-12 named is measurably gone. Two nits, neither blocking.

---

## A4 — suite, my own run

`go1.24.4`, `-count=1 -v`, four packages, head `4f38ed0`, log `/opt/data/tmp/probe119/P9-15_suite.log`:

| package | PASS | FAIL | SKIP |
|---|---|---|---|
| internal/agent | 94 | 0 | 0 |
| internal/upgrade | 55 | 0 | 0 |
| internal/server | 294 | 0 | 1 |
| internal/protocol | 17 | 0 | 0 |
| **total** | **460** | **0** | **1** |

516 `=== RUN` (461 top-level + 55 subtests). `go build ./...` exit 0, `go vet ./...` exit 0. **No discrepancy** with the claimed 460P/0F/1S; the +2 over P9-12's 458 are the two P9-14 tests; the skip is the pre-existing `TestV1_CreateTransfer`.

---

## A5 — ratification

**NOT-READY.** What remains, in order:

1. **F1 job scope** (§F1:131 vs :150) — blocking; the canary hop either always refuses or is exempted by inference. One sentence + one test case.
2. **F6 kinds** (§F6:394-399) — blocking for the reason F6 was failed in the first place: the fields cited as pre-existing do not exist (`0` grep hits), and the phase enum is not the tree's. Label them additive and name the write site, or re-point at `ServerJob.Phase`/`LastReportUTC`.
3. **Re-anchor** the 8 drifted spans to the head; update `verdict_sha_of_head` from `b32b8c7`.
4. **Residuals** R1-R4: close or accept in writing.
5. Still true and unchanged: **nothing in F1-F6 is implemented** — `agent_proc.go:44-52` is still the one-input gate, `canary.go` still kills without an order, there is no `killAuth`, no lease, no `writer.go`, no `AgentInfo.PID`. Ratification is of the design; the implementation unit is owed and P9-03..P9-06 stay blocked behind it. This verdict unblocks nothing.

## A6 — what I could not execute

- **No Windows host**: `GetProcessTimes`, `QueryFullProcessImageNameW`, `EqualFold`, the 25-char `tasklist` cap (v4's own O6) and `LockFileEx` are graded as text against cited source, never as behaviour.
- **No live 1.18.6 → 1.19 hop**: no agent was updated, no `proc_kill` reached an OS; `update_adopt` remains specified-only.
- **No fault injection on the lease/epoch**: F4 is graded by rule inspection against the real renames; test (e) is the contract's design, not an executed crash.
- The **code leg was executed** (suite, mutation probe, two clean-checkout builds, npm rebuild); the **UI was not clicked through** — the bundle was verified reproducible and byte-identical, not rendered in a browser.
- This document grades the **code head** `4f38ed0`; the two commits after it are review documents only, so no code claim above depends on them.

---

## 8 — Divergences from the filed review (`774fb5b`, amended by `e9666a0`)

I read it after finishing my own grading. Agreements, on my own evidence: N2 drift (same eight spans; their "15 refs" counts citing sites, mine counts distinct spans), N4 TOCTOU, N1 F6 fabricated-field claim (**confirmed independently, `0` grep hits**), the code-leg verdicts, and every suite number (460/0/1, 516 RUN, per-package split identical). Differences:

1. **F1 is not PASS in my grading.** The filed review grades F1 PASS and does not mention the `canary_argv` grant's empty `JobID` against a job-scoped kill order. That is the one surviving M1 contradiction and it sits on the primary hop.
2. **N3 (torn-tail epoch line) is weaker than filed.** The F4 table's first row is "no line yet, **or** `step=start`" and `ParseJournal` rejects a partial line, so the last *parseable* line after a torn `step=rename1` write is `step=start` — which routes to restore, the safe action. The wording should still say "last parseable line", but the harm the filed review implies (an implementer skipping) is not reachable through the table as written. M2 wording fix, not a hole.
3. **Their A3.2 cites an uncommitted call-site test** (`/opt/data/tmp/p914-red/…`) that is not in the tree. My finding stands as filed there: the wiring is untested in the committed state, so fold that test in.

## Close record

| field | value |
|---|---|
| artefact | `/opt/data/tmp/probe119/P9-15_REVIEW.crosscheck.md` (repo copy `reviews/P9-15_REVIEW.crosscheck.md`) |
| reviewed_by | reviewer=independent (P9-15, second writer — see the duplicate-dispatch note) |
| verdict | **FIX-FIRST** — delta: F2 PASS, F4 PASS, F1 FIX-FIRST (identity closed, job-scope contradiction), F6 FIX-FIRST (kinds cite non-existent fields); code leg `4f38ed0` PASS with one fold-forward; ratification **NOT-READY** |
| verdict_sha_of_head | `4f38ed0c07640198b70137313f20f52c78eda17c` |
| suite | 460 PASS / 0 FAIL / 1 SKIP (94/55/294+1S/17), 516 RUN, build+vet exit 0; clean `c9538ba` fails to build (`embed.go:9:12`), clean `4f38ed0` builds |
| open item | F1 canary-grant job scope; F6 `writer_boot_id`/`s.bootID`/`last_report_at` do not exist in the tree; re-anchor 8 spans to the head; fold the call-site test in; F1-F6 still unimplemented |
