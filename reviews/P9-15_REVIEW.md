# VERDICT FIX-FIRST /opt/data/tmp/probe119/P9-15_REVIEW.md 4f38ed0c07640198b70137313f20f52c78eda17c 460P/0F/1S

P9-15 independent review. reviewer=independent; I authored none of the three artefacts under grade and inherited no verdict.
Head graded: `4f38ed0c07640198b70137313f20f52c78eda17c` on `r119-build` (`git rev-parse HEAD`, `git status --porcelain` empty at session start and at close). `4f38ed0` is a descendant of both v4 commits (`a8d0738`, `c9538ba`); `git merge-base --is-ancestor` = true.
Contract under grade: `P9-08_CONTRACT_V4.md`, 578 lines, 74,299 B, sha256 `e0fa713228828404f6333bd9634e9dd45e2e6a79a497d2d4c4b3073329248634` — byte-identical to the committed `.workflow/architect/P9-08_CONTRACT_V4.md` (both hashed).
Code leg under grade as code: `4f38ed0` (10 files, +665/−25, plus the three `web/dist` files).

RECIPIENT EXPERIENCE: I ran `go build ./... && go vet ./...` and the four-package suite at `4f38ed0` myself (`build_exit=0`, `vet_exit=0`, 516 RUN blocks, 460 `--- PASS`, 0 `--- FAIL`, 1 `--- SKIP`); I extracted `git archive 4f38ed0` to a clean directory and built it (exit 0) and built the parent `c9538ba` the same way (exit 1, `embed.go:9:12`); I rebuilt `web/dist` from source with node v26.5.1 / vite 5.4.21 and got the committed bundle byte-for-byte; I ran the extracted-method test RED (mutant) and GREEN, and the un-committed call-site test RED at `b32b8c7` / GREEN at `4f38ed0`. I did not execute a live update hop and have no Windows host (A6).

---

## A1 — the delta, F1/F2/F4/F6 (grade the answer to P9-12, not the whole contract)

| finding | P9-12 failure | what v4 changed | opened at 4f38ed0 | verdict |
|---|---|---|---|---|
| **F1** | grant keyed to a recyclable PID + TTL; no start-time/exe binding; R-b condition 3 undecidable | grant carries `procIdentity` (StartTicks/BootTime/ExeDev/ExeIno; Windows CreatedAt/ExePath), captured at registration **and** re-read at kill time (`killAuthValid` cond. 4, `identityEqual`); single-use; ancestry condition **deleted**, replaced by image identity from `/proc/<pid>/exe` (dev/ino + basename ∈ {canonical, canonical.old}) / `QueryFullProcessImageNameW` | `agent_proc.go:44-52` (gate has one input — unchanged), `agent_proc.go:104-106` (sole insertion), `agent.go:227-230` (`spawnedPIDs`), `agent.go:1125-1133` (mirror gate), `messages.go:598-601` (`TaskStopParams` 3 fields — `JobID` is a stated addition), `agent.go:176-178,453-455`, `canary_launch.go:24-31`, `connect.go:54-55`, `server_api.go:471` (`hasCapabilityStrict`), `agent_update.go:47-51` (`.next` normalisation), `canary.go:84-86` (`backupSlot` = `canonical + ".old"`, so the L3/F1 image-name set is the real slot) | **PASS** |
| **F2** | L3 "field 22 × btime" is not a time and names no CLK_TCK; `ProcessInfo` wire fields unnamed; `connPID` called the superseded PID | L3 is `started_at_unix = btime + starttime/CLK_TCK`, `CLK_TCK = 100` with `AT_CLKTCK` override, used only for ordering; identity is tick equality; four additive wire fields named with JSON tags (`exe_path`, `exe_ino`, `start_time_ticks`, `started_at_unix`); `connPID` corrected to "the live connection — the successor" | `messages.go:219-227` (no PID today), `:587-592` (`pid,name,cpu_percent,memory_mb`), `:594-596`, `agent.go:432-440` (envelope builder), `agent_proc.go:161` (`ps -eo pid,comm,%cpu,%mem`), `:129-157` (tasklist CSV, full name), `server_ws.go:82-89` (plain `json.Unmarshal`, so `omitempty` is additive both ways), `:113-116` (`s.conns[agentID]` overwrite ⇒ `connPID` is the successor — the correction is right), `server_update.go:265-296` (`OldPID` written `:291`, `NotifiedAt` `:293`, record literal `:289-294`) | **PASS** |
| **F4** | "epoch advanced" read as "swap completed", so a swap dying after `rename1` skipped the restore | branch is on the **step** of the last line, with an explicit table: `start`/`rename1` ⇒ restore candidate under the lease, `rename2` ⇒ sha-verify then escalate, `done` ⇒ skip; test **(e)** crash-after-rename1 added | `canary.go:149-175` (two renames, no lock), `:126-139` (`restoreBackup` unlocked today), `:97-120` (`archiveExistingBackup`), `journal.go:9-10,67-85,89-107` (line format + event-name rule) | **PASS** on the delta; one residual (N3) |
| **F6** | one condition in two codes (409 `duplicate_request` vs 202 `replayed:true`); `kind:"unreadable_journal"` unobservable server-side | **one** duplicate rule keyed to job state (non-terminal + terminal-success ⇒ 202 `replayed:true`; terminal-failure ⇒ 409 `UPDATE_PREVIOUSLY_FAILED` unless `new_attempt:true`); `duplicate_request` removed everywhere; `unreadable_journal` deleted and replaced by `restart_mid_transition` / `agent_report_missing` | `server_api.go:303,309` (both codes exist), `:480-490` (flat `{code,message,detail}` writer), `idempotency.go:11-19` (exactly three states — no fourth name exists, so `UPDATE_PREVIOUSLY_FAILED` is justified), `api_v1.go:186-204` (body lift) | **FIX-FIRST** — duplicate rule CLOSED; the replacement kinds rest on fields that do not exist (N1) |

None of the four corrections is a reword. F1 and F2 name the mechanism, the constant, the source and the wire names; F4 moves the decision to the step and adds the missing test; F6 collapses the duplicate rule to one rule. The delta as a delta is real.

### N1 (blocking) — F6's replacement kinds cite three identifiers that exist nowhere

`P9-08_CONTRACT_V4.md:394` says the two kinds "are derived from fields the job row **already** carries", then `:398-399` uses `job.writer_boot_id`, `s.bootID`, `job.last_report_at`, and `job.phase ∈ {dispatched, adopted, awaiting_ack, promoted}`. At `4f38ed0`:

```
git grep -n 'bootID\|writer_boot\|last_report_at' -- '*.go'   → 0 hits
```

The only server job record is `ServerJob` (`internal/server/jobs.go:28-40`): it has `Phase` (`:34`) but the tree writes it as `"running"` (`:69`) and its `Status` vocabulary is `open|recovery_pending|recovery_required|resolved` — none of the four states F6 keys the kind to. Update state today is only `pendingUpdate{OldAgentID,OldPID,NewVersion,NotifiedAt}` (`server_update.go:27-32`), which has no phase, no boot id and no report timestamp. `LastReportUTC` is a `string` (`jobs.go:38`), not `last_report_at`. So the kind is exactly as unproducible as the `unreadable_journal` it replaced: an implementer greps for `writer_boot_id` and finds nothing, and the doc calls it pre-existing. Fix: label the three fields + the phase enum as additive, name the write site (the phase transition) and the boot-id source, or map them onto `ServerJob.Phase`/`LastReportUTC` explicitly.

### N2 (blocking for ratification, hygiene) — 15 of 145 citations no longer resolve at the graded head

My audit (A2) finds every ref was correct at the contract's anchor `b32b8c7`, but `4f38ed0` (P9-14) inserted 26 lines above the body of `server_ws.go` (new `updatePromotionKillDelay` + `completeUpdatePromotion`) and 6 lines in `embed.go`. Consequence at head: 10 refs are shifted +26 in `server_ws.go` (e.g. `:93` "is `agentID := info.Name`" is now `:119`; `:113-116` conn store is now `:139-142`; `:82-89` info unmarshal is now `:108-115`), 4 spans were deleted by the extraction (`:140-148`, `:120-139` ×2, `:130-139`), and `embed.go:9` is now `:15`. This is drift, not fabrication — the same class P9-12 A4 #6 blocked on.

### N3 (should-fix) — F4 does not define the torn-tail epoch line

The step table has four rows (`no line`/`start`, `rename1`, `rename2`, `done`). A crash during the `O_APPEND` of `step=rename1` can leave a partial last line; `ParseJournal` rejects it, and no row says what the watchdog does with an unparseable tail — restore (the correct action, `canonical` may be absent) or skip. Left undefined, an implementer can skip, which re-opens the exact crash-after-rename1 harm the step table was written to close. Fix: one sentence — an unparseable tail is treated as the last complete line, and a missing `canonical` with a parseable `step=rename1` history restores.

### N4 (note) — F1 identity check is TOCTOU by microseconds

`identityEqual(identityOf(pid))` then `kill -<sig> <pid>` (`agent_proc.go:54-75` shells out; there is no `pidfd`). A PID recycled between the check and the signal would be killed. Window is the fork of a child process on a 4M-PID space — I grade it negligible, not a blocker; `pidfd_open` would close it if the implementer wants it closed.

---

## A2 — citation audit, my own counts (the child's 145/0/0 is not evidence)

Method: my own extractor over the document text — regex for `file.ext:NN` / `:NN-MM` (comma lists split), 145 occurrences; each basename resolved against `git ls-tree -r 4f38ed0`; each span bounds-checked against that file at head; then the same span re-read at `b32b8c7` to separate drift from fabrication.

| metric | my count |
|---|---|
| refs extracted | **145** |
| distinct files | **19** |
| distinct spans (path:range) | **77** |
| unresolved files | **0** |
| out-of-range spans at 4f38ed0 | **0** |
| refs in files unchanged since b32b8c7 | 127 (all resolve) |
| refs stale at 4f38ed0 | **15** (10 × `server_ws.go` +26, 4 deleted by the P9-14 extraction, 1 × `embed.go` +6) — N2 |
| fabricated spans at b32b8c7 (text never said it) | **0** — each stale span's quoted text exists at the anchor |
| load-bearing spans I opened and read myself | **34**, all say what the contract says they say |

The contract's own figure (145 / 0 out-of-range / 0 mismatched / 57 probes) is arithmetically right **at `b32b8c7`**; it is not right at the head a ratification would bind, and I did not re-run their `p908_v4_cite_audit.py` — I re-derived the counts independently.

---

## A3 — commit 4f38ed0 as code

**A3.1 Purge on both completion paths — PASS.** `server_ws.go:47-61`: the completion half was extracted; the kill is attempted at `:50`, the error branch only logs (`:51`), and `s.purgeCanaryRegistryRow(agentID)` runs at `:60`, after the if/else. The only call site is `server_ws.go:162`, `go s.completeUpdatePromotion(agentID, oldPID)`, inside the `pendingUpdates` hit — no second completion path exists in `handleWebSocket`. Helper is `server_update.go:44` (delete by `agentID+"-canary"`, canonical key untouched).

**A3.2 Does the new test bite? — PASS on the logic, GAP on the wiring.** `p914_update_purge_test.go:85-112` registers canonical + `-canary`, installs `failingWriteConn`, calls `completeUpdatePromotion` and asserts the canary row is gone and the canonical row survives. Anti-tautology by mutation (my run, clean extract of `4f38ed0`): moving the purge back **only** into the success branch makes exactly this test fail —

```
--- FAIL: TestP914CompletionPurgesCanaryOnKillError
    p914_update_purge_test.go:107: kill error left the canary registry row behind: the purge is still inside the success branch
mutant_exit=1
```

The gap: the test calls the extracted method, so nothing committed exercises the `handleWebSocket` wiring the P9-12 finding named. I ran the coder's un-committed call-site test myself, same file unchanged against both trees: pristine `b32b8c7` → `--- FAIL: TestP914CompletionPurgesCanaryWhenKillFails (12.09s)`; `4f38ed0` → `--- PASS (3.01s)` driving a real WebSocket connect. So the fix is provable — the proof just is not in the tree (`/opt/data/tmp/p914-red/internal/server/p914_code_leg_test.go`, which that worktree now has applied: it is dirty, `M server_ws.go`, so its "RED" log must be read as the 16:05 run, not as its current state). Fold that file in.

**A3.3 ManagedPath census — 4/4, 0 residual literals — PASS.** My greps at head:
`grep -rn 'FilesDir() *+' --include=*.go` → 0 hits (comments included; also 0 hits for `filepath.Join(s.FilesDir(), <user name>)` outside `artifacts.go`). The four named sites now call the constructor: `api_v1_transfer.go:232,260`, `server_download.go:59,83`. Remaining `FilesDir()` uses are the directory passed as a directory (`builder_api.go:324`, `server_update.go:173`) or inside `artifacts.go` itself (`:164` the constructor, `:207` ledger, `:483` temp upload, `:606` generated name). `/tmp/probe-files` at head appears only in `p911_hygiene_test.go:15` (prose) and `:117` (the rejection fixture) — 0 hits in `web/dist`.

**A3.4 Committing web/dist changes what a clean checkout builds — PASS, verified both directions and for staleness.**
- `git archive c9538ba` → no `web/dist`, `go build ./...` → `embed.go:9:12: pattern all:web/dist: no matching files found`, exit 1.
- `git archive 4f38ed0` → `web/dist` present in the commit, `go build ./...` exit 0, `go vet ./...` exit 0, no untracked files needed.
- Staleness: I rebuilt in that clean extract with the tree's own toolchain (`node v26.5.1`, `npm run build`, vite 5.4.21, `tsc && vite build`, exit 0) and got `index-BiOCVBVG.js` md5 `b5b61f5609b98e922b499f868f6976f1` — identical to the committed blob. The bundle carries no `/tmp/probe-files` and calls `createTransfer(e,"download",T)` / `createTransfer(e,"upload",T)` (3 args), matching the current source.
- Residual risk, not a defect: `web/dist` is build output with no CI staleness gate (`P9-14_CODE_LEG.md` open item 1). Correct as designed; make it mechanical later.

SHAPE reuse `internal/server/server_ws.go:47` — `completeUpdatePromotion` is the extracted body of the old inline goroutine; no second completion path was added.
SHAPE reuse `internal/server/artifacts.go:156` — the four download sites now call the existing `ManagedPath`; no parallel joiner.
SHAPE none — contract v4 adds no code (`git show --stat a8d0738` = 1 file, `c9538ba` = 1 file, both `.workflow/architect/P9-08_CONTRACT_V4.md`).

---

## A4 — suite, my own run (no borrowed numbers)

`PATH=/tmp/go/bin GOFLAGS=-mod=vendor go test ./internal/agent/ ./internal/upgrade/ ./internal/server/ ./internal/protocol/ -count=1 -v` at `4f38ed0`:

| package | PASS | FAIL | SKIP |
|---|---|---|---|
| internal/agent | 94 | 0 | 0 |
| internal/upgrade | 55 | 0 | 0 |
| internal/server | 294 | 0 | 1 |
| internal/protocol | 17 | 0 | 0 |
| **total** | **460** | **0** | **1** |

516 `=== RUN` blocks. `go build ./...` exit 0, `go vet ./...` exit 0. **No discrepancy with 460P/0F/1S.** The skip is the pre-existing `TestV1_CreateTransfer` self-skip. Delta over P9-12's 458 = the two new P9-14 tests, as claimed. Log: `/opt/data/profiles/reviewer/cache/scratch/p915/suite2.log`.

---

## A5 — ratification: NOT-READY (I do not ratify; that is Goran's)

What remains, exactly:
1. **N1** — F6: label `writer_boot_id` / `s.bootID` / `last_report_at` and the `phase` enum as additions, name the write site, or re-point the kinds at `ServerJob.Phase`/`LastReportUTC`. Until then the `IDEMPOTENCY_STATE_UNKNOWN` kinds are as unproducible as the kind they replaced.
2. **N2** — re-anchor the 15 stale refs at `4f38ed0` (10 × `server_ws.go` +26, 4 deleted spans, `embed.go:9`→`:15`), or bind the contract to `b32b8c7` in the header and state that the head has moved.
3. **N3** — one sentence on the torn-tail epoch line in F4.
4. **Fold-forward (not a blocker)** — commit the call-site test; it already exists and is RED/GREEN-proven.
5. Still true, unchanged: **nothing in F1-F6 is implemented** — `agent_proc.go:44-52` still has one gate input, `canary.go:203-206` still kills without an order, there is no `writer.go`, no lease, no `AgentInfo.PID`. Ratification is of the design; the implementation unit is still owed and P9-03..P9-06 stay blocked.

---

## A6 — what I could not execute

- **No Windows host.** Every Windows rule (`tasklist` 25-char cap, `LockFileEx`, `QueryFullProcessImageNameW`, `GetProcessTimes`, `PROCESS_QUERY_LIMITED_INFORMATION`) is graded as text against cited source, never as behaviour.
- **No live update hop.** No server leg, no target; `update_adopt` remains specified-only.
- **No fault injection** on lease/epoch: F4 is graded by reading the ordering rule plus its test table; the torn-tail gap (N3) is a rule-level consequence, not an observed crash.
- **No arm64/riscv host**, so O9's `AT_CLKTCK` fallback is unprobed.
- Build-from-committed-content was executed twice (`c9538ba` fail, `4f38ed0` pass) in extracted trees; the UI rebuild was executed once.

---

## Nemesis record (9 attacks, 3 holes, 0 unaddressed)

1. PID recycled inside the TTL → **closed** by re-read identity + single use (F1); residual TOCTOU micro-window (N4).
2. Ancestry predicate still undecidable → **closed**; replaced by a file-identity predicate with a named source, and I checked the backup slot really is `canonical + ".old"` (`canary.go:84-86`) so the accepted name set is not invented.
3. L3 still non-computable → **closed**: `btime + starttime/CLK_TCK`, constant named, tick equality carries identity, seconds only for ordering.
4. Watchdog still skips the restore → **closed** by the step table and test (e); **hole** remains for a torn tail (N3).
5. Duplicate still two codes → **closed** (one rule, 409 arm removed).
6. Replacement 409 kinds still unobservable → **hole** (N1): the fields they rest on are not in the tree and are described as if they were.
7. "The test proves the call site" → **falsified twice**: the committed test calls the extracted method; the call-site test is un-committed (I ran it RED→GREEN).
8. "Committed web/dist is stale build output" → **falsified**: a fresh build from source is byte-identical to the committed blob.
9. "The citations were re-anchored, so they are current" → **falsified at head**: 15 refs drifted when P9-14 landed (N2); 0 fabricated.

## Close record

| field | value |
|---|---|
| artefact | `/opt/data/tmp/probe119/P9-15_REVIEW.md` (repo copy `reviews/P9-15_REVIEW.md`) |
| reviewed_by | reviewer=independent (P9-15) |
| verdict | **FIX-FIRST** — delta: F1 PASS, F2 PASS, F4 PASS, F6 FIX-FIRST; code leg `4f38ed0` PASS with one fold-forward; ratification **NOT-READY** (N1, N2, N3) |
| verdict_sha_of_head | `4f38ed0c07640198b70137313f20f52c78eda17c` (tree under grade; repo head at close is `774fb5b`, differing only by this review file) |
| suite | 460 PASS / 0 FAIL / 1 SKIP (agent 94, upgrade 55, server 294+1S, protocol 17), 516 RUN blocks; build+vet exit 0 at head; clean content of `4f38ed0` builds, `c9538ba` does not (`embed.go:9:12`) |
| open item | N1 the F6 replacement kinds cite `writer_boot_id`/`s.bootID`/`last_report_at`, none of which exist (0 grep hits); N2 15 refs stale at head after P9-14; N3 torn-tail epoch line undefined; fold the call-site test in |
