# VERDICT FIX-FIRST P9-08_CONTRACT_V3.md 867b0e9 458P/0F/1S

P9-12 independent review. reviewer=independent; I authored none of the three artefacts and inherited no verdict — every grade below rests on a region or a command I opened/ran myself. Contract under grade: `/opt/data/tmp/probe119/P9-08_CONTRACT_V3.md`, 440 lines, 50,017 B, sha256 `bb9c7ebfa470f054268842b1a53bf407217ee59d1d3f0eb38d6c1b1ee557770c` (byte-identical to the committed `.workflow/architect/P9-08_CONTRACT_V3.md` at caa0ea9 — `sha256sum` of both).
Commits under grade as code: `f07f241901b061c9ea6d6231f9b5d9ad64c8185c`, `867b0e9fd188076818386d454d0afbf83b5f2a31` (tree `9064db42`).
THREE ARTEFACTS, NOT TWO: at session start `git rev-parse HEAD` = `d32547ae`, not `867b0e9` as briefed. `d32547ae` adds exactly one file, `reviews/P9-12_REVIEW.md`, and nothing else (`git diff --stat 867b0e9 d32547ae` → 1 file, +114). The code tree and the contract are therefore identical to 867b0e9; every claim here was re-derived from the tree at 867b0e9. The pre-existing review is treated as E4 and is cited as evidence nowhere.

RECIPIENT EXPERIENCE: I ran `go build ./... && go vet ./...` and the four-package suite at 867b0e9 (`/opt/data/tmp/probe119/P9-12_suite.log`, 514 RUN blocks); I created a clean worktree at 867b0e9 and its build failed at `embed.go:9:12`; I executed `/opt/data/tmp/v119/bin/probe-client-1186 --version` → `PROBE Client v1.18.6`; I did not execute a live update hop (A5).

---

## A1 — F1-F6, each verdict on a region opened at 867b0e9

Method: 81 `file:line` refs extracted from the contract by script; every file resolved by basename against `git ls-tree -r 867b0e9`; every span bounds-checked against that file at 867b0e9; each load-bearing span read; span text compared byte-wise between 0352f3b and 867b0e9 to separate fabrication from line drift.

**Citation audit: 81/81 files exist, 0 out-of-range spans, 0 fabricated, 4 stale-by-drift refs in 3 regions** — all in `server_update.go`, shifted 7 lines by f07f241: `:18-25` and `:20-25` (now the canary-suffix comment/const; `type pendingUpdate` is `:27-32`), `:133-158` (now inside the sha256 branch; `handleAgentUpdateResult` is `:265-296`, the `OldPID` write `:289`), `:154` (now `hash = computed`; `NotifiedAt` write `:293`). Drift, not the P9-07 fabrication mode: the cited constructs exist and say what v3 says they say, at different lines.

| finding | verdict | rests on (opened at 867b0e9) | why |
|---|---|---|---|
| **F1** bootstrap kill authorization | **FAIL** | `agent_proc.go:36-76` (`:44-52` gate reads only `spawnedPIDs`; `:47-50` sole decision; `:54-75` kill+untrack), `:104-106` (sole insertion, from `proc_start`), `agent.go:227-230` (`spawnedPIDs` decl), `:1123-1135` (mirror gate on `task_stop`), `:450-455` (canary arm), `:570-579` (`TypeAgentUpdate` at `:578`), `canary_launch.go:20-31` (`CanaryArgs`), `connect.go:48-58` (flags `:54-55`), `server_api.go:468-473` (`hasCapabilityStrict`), `:795-812` (`Bypass` "intentionally never set") | The measured defect is real and every citation resolves: one gate input, one insertion site, so hop 1 (`spawnedPIDs` empty) cannot kill. But the replacement is keyed to a **PID number only** — `contract:89-94` (`JobID/Source/ExpiresAt`, no start time, inode or image), TTL 600 s (`:105`), 300 s (`:110-113`). Nothing re-verifies that `old_pid` still names the predecessor. A PID recycled inside the TTL under a stale grant kills an unrelated process — the failure the contract itself names at `:177` ("A recycled PID is the failure this prevents"). Second defect, `contract:117`: condition 3 ("`old_pid` is not an ancestor further up than one hop") names no mechanism and no source (`/proc/<pid>/stat` field 4, or Toolhelp32 PPID) and is undecidable as written; on the legacy hop the predecessor *is* the direct parent, so the intent is ambiguous too. |
| **F2** check (c) identity | **FAIL** | `server_ws.go:82-116` (`agentID := info.Name` at `:93`; plain `json.Unmarshal` `:82-89`; conn map `:113-116`), `messages.go:219-229` (no `PID` field), `:587-596` (`ProcessInfo` = pid,name,cpu,mem), `:798-803` (`AgentUpdateResult.OldPID`), `agent_proc.go:129-157` (tasklist CSV), `:159-180` (`ps -eo pid,comm,%cpu,%mem`, `strings.Fields` at `:169`), `agent.go:430-441` (info envelope) | The fabrication is correctly deleted (`server_ws.go:93` really is `agentID := info.Name`) and L2 `commMatches` is arithmetically sound (POSIX `comm` is 15 B; 18- and 23-B names truncate; a 12-B `comm` is correctly refused). **L3, which the contract makes the decision, is unimplementable as written**: `contract:171` says "start ticks from `/proc/<pid>/stat` field 22 × `/proc/stat btime`". Field 22 is starttime in clock ticks; btime is seconds since epoch — a product is not a time, and no `CLK_TCK` is named (correct form: `btime + starttime/CLK_TCK`). The mandatory `ProcessInfo` extension (`:167-172`) never names its wire fields although §2.1 already uses `identity_exe`/`identity_source` (`:52`) — the agent side must invent the schema. Third: `contract:151` calls `s.connPID[agentID]` "the superseded connection's PID", but `agentID` is shared by predecessor and successor and the entry is overwritten at successor connect (`server_ws.go:113-116`); it records the *successor's* PID. |
| **F3** the 60 s fallback | **PASS** | `canary.go:186-208` (`:203-206` unconditional `stopProcess(CanaryOldPID)`), `server_ws.go:118-152` (`delete(s.pendingUpdates, agentID)` at `:123`, `Sleep(3s)` at `:137`, kill at `:139`) | v2 carries the fallback in four places (`P9-01_UPDATE_CONTRACT.md:230,:232,:599,:610`); v3 carries no fallback row, no `kill_grace`, no `kill_authority:"fallback"` — every "fallback" occurrence in v3 is a statement of deletion or an unrelated channel fallback (`:360`) — and I-1 (`:189-193`) admits only a server order carrying `job_id`. Both server-path defects it claims to fix are real at head: the pending record is deleted at `:123` *before* the 3 s sleep and the kill. Code leg still open by this unit's own design: `canary.go:203-206` still kills without an order. |
| **F4** watchdog / swap race | **FAIL** | `canary.go:149-175` (two renames `:163`, `:167`), `:126-139` (unprotected `restoreBackup`), `:97-120` (archive), `journal.go:9-10,67-85,89-107`; `grep -rn 'Flock\|LockFileEx' internal/ cmd/` = 0 hits | The race is real and the primitive is genuinely new. The hole is in the winner rule, `contract:227-230`: step 3 treats "epoch advanced since t0" as "someone else completed the swap" and **skips the restore**. A swap that dies after writing `step=rename1` releases the flock but leaves `canonical` absent; a watchdog whose t0 predates that write acquires the lease, sees a higher epoch, logs `WATCHDOG_SKIPPED reason=epoch_changed`, escalates — and canonical stays missing with no restore. That is exactly the branch-(ii) restore the lease exists to serialize, and the design's own retry loop (`:228`, 3 × 5 s) widens the window. The epoch must be *interpreted* (`step=rename1` ≠ `step=done`), not merely compared. Unpinned: test (a) (`:241`) covers only a live paused swap (lease busy → skip, correct) and test (c) two evaluations; no test covers crash-after-rename1 with an advanced epoch. |
| **F5** journal has no writer | **PASS** | `journal.go:1-7` (imports stdlib only), `:9-52` (events), `:67-85` (`FormatLine`), `:89-107` (`isJournalEventName`), `:110-147` (`ParseJournal`, `ParseInt` at `:126`), `:158`/`:38` (outcome vs event), `:170-205` (`Analyze`); `agent_update.go:30-151` (`:47-51` `.next` normalisation, `:131-136` watchdog arm) | Verified independently: `FormatLine` is called only from `journal_test.go`; no non-vendor importer of `internal/upgrade` exists (`git grep` on the module path → 0 hits); no `.update.journal` literal exists; `ParseInt(...,10,64)` at `:126` vs `Unix()` emission at `:67-71`, so C7's RFC3339 example would silently parse to 0. Writer, file, per-state writer table, append+fsync-before-report and the concurrency/rotation rules are named. No cycle is possible: `internal/upgrade` imports `strconv/strings/time` only (head `journal.go:1-7`). |
| **F6** the 409 dialect | **FAIL** | `server_api.go:296-316` (`IDEMPOTENCY_IN_FLIGHT` `:303`, `IDEMPOTENCY_STATE_UNKNOWN` `:309`), `:480-492` (flat `{code,message,detail}` writer), `api_v1.go:184-215` (`reformatV1` lifts typed bodies), `idempotency.go:8-20` (three states); `git grep 'UPDATE_PREVIOUSLY_FAILED\|LOCK_BUSY\|UPDATE_IN_FLIGHT'` = 0 hits | Codes, writer and envelope check out; deleting the two invented names costs nothing; the flat body is the tree's own. But the spec contradicts itself on duplicates: `contract:283` makes "a duplicate request for the same agent" a **409 `IDEMPOTENCY_IN_FLIGHT` with `detail.kind:"duplicate_request"`**, `contract:343` tells the client to treat that 409 as a replay, and `contract:347` requires "a duplicate of a non-terminal job returns **202** with the same `update_id` and `replayed:true`". One condition, two status codes — no implementer and no test can satisfy both. Secondary: `detail.kind:"unreadable_journal"` (`:284`) names a state the server cannot observe, because the journal lives on the agent host and the server writes no journal line by design (`:267`); the server-side source of that kind is unspecified. |

Distribution: **2 PASS (F3, F5), 4 FAIL (F1, F2, F4, F6)**. The self-check line (`:3`, "F1-F6 CLOSED-IN-V3") is false at this tree.

---

## A2 — the two commits as code (diffs read, then exercised)

### f07f241 — FIX-FIRST

- **"ws_remote_path carries its own sha256" — true, end to end.** `server_update.go:134-166`: with `binary_path` empty the supplied digest is required (400 otherwise) and used; with `binary_path` present it is optional and a mismatch is 400. Forwarded as `updateParams.SHA256` (`:211-220`); the agent re-hashes the staged file and refuses on mismatch (`agent_update.go:160-171`). Defect A (`hashFileServer("")`) is repaired.
- **"canary registry row purged at update start and completion" — start true, completion conditional.** Start: `server_update.go:227`, unconditional, after all validation and before dispatch. Completion: `server_ws.go:140-148` — the purge sits inside the `else` of `if err != nil`. When `proc_kill` errors — the log line at `:141` itself anticipates "old process may have already exited" — the stale `<agent>-canary` row survives, which is the exact defect B the commit exists to fix. Fix: hoist the purge out of the `else` (or `defer` it).
- **"4 tests" — true and passing.** But `TestP908CanaryRegistryCleanup` (`p908_update_hardening_test.go:247`) calls the helper directly and `TestP908UpdateStartDropsStaleCanary` (`:284`) drives the start path; **no test exercises the completion call site in `server_ws.go`**, which is where the defect is.
- Minor (M2): the supplied digest is never format-checked (64 hex), so a typo becomes an agent-side hash mismatch instead of a 400. The `shortHash` guard at `:230-234` correctly removes the `hash[:16]` panic a short digest would have caused.

### 867b0e9 — FIX-FIRST

What holds:
- **No residual literal in source.** `git grep -n '/tmp/probe-files' 867b0e9` over the whole tree: 0 hits in non-test source. Text hits are `p911_hygiene_test.go:15` (prose), `:117` (rejection fixture), `.workflow/architect/P9-01_UPDATE_CONTRACT.md:40` (historical design doc) and committed binaries. Wired at `cmd/probe-server/main.go:40,114-118,158`; `SetFilesDir` creates 0755 (`artifacts.go:83-107`).
- Census, re-counted: pre-image at f07f241 = **6 Go code sites** (`api_v1_transfer.go:213,237`; `builder_api.go:324`; `server_download.go:58,78`; `server_update.go:123`) **+ 3 Go comment lines** (`api_v1_transfer.go:212`, `server.go:431`, `server_update.go:60`) **+ 2 test + 3 shell + 2 TSX lines**. The message's "seven" reconciles with no census I can construct (6 Go sites; 9 Go lines; 13 non-test lines). The functional claim — no residual — holds; the numeral does not.
- Upload is genuinely streamed (`artifacts.go:414-543`: `MultipartReader`, `io.Copy` into `.upload-*.part`, 512 MiB cap `:506-511`, 413, then rename); name validation refuses `..`, `/`, `\` (`:477-481`); `ResolveManagedFilePath` refuses absolute paths outside the dir and delegates relative names to `ManagedPath`, which refuses separators (`:156-186`); traversal test passes (`p911:106-145`).
- Retention is real: newest-N per `agent|name`, `inFlight` never deleted, pin TTL 30 min (`artifacts.go:294-331,355-393`), pinned test (`p911:272-330`).

Findings:
1. **The served bundle is stale.** `web/dist/assets/index-qhL5A-As.js` (mtime 2026-11-22 22:38, **untracked** — `git ls-files web/dist` = 0) still contains `/tmp/probe-files/` twice and still calls the old 4-arg `createTransfer`. `embed.go:9` is `//go:embed all:web/dist`, so a server built at 867b0e9 ships the pre-P9-11 UI. "Replaces all seven literals" is true of source and false of what is served.
2. **A clean checkout of 867b0e9 does not build.** `git worktree add … 867b0e9` → `go build ./...` → `embed.go:9:12: pattern all:web/dist: no matching files found`, exit 1. My 386P/0F run succeeded only because the untracked `web/dist` was in the working tree. The commit's suite claim is not reproducible from what the commit contains. Pre-existing (`embed.go` predates it), but this unit changed the UI and did not rebuild the embedded asset.
3. **Four sites still hand-join the directory** rather than using the new constructor: `api_v1_transfer.go:230,254` and `server_download.go:59,79` build `s.FilesDir() + "/" + name`. Same bug class the unit exists to remove, one level down. Filenames there are validated, so this is a shape defect, not an open traversal.

SHAPE reuse `internal/server/registry.go:215` — `purgeCanaryRegistryRow` is `Registry.Remove` plus the existing `agent.canaryNameSuffix`; no second registry.
SHAPE parallel `internal/server/api_v1_transfer.go:230` (also `:254`, `server_download.go:59,79`) — hand-built `FilesDir() + "/" + name` pairs alongside the new constructor `artifacts.go:156`.
SHAPE none — contract v3 adds no code: `git show --stat caa0ea9` = 1 file, `.workflow/architect/P9-08_CONTRACT_V3.md`.

---

## A3 — suites, my own counts (no borrowed numbers)

`PATH=/tmp/go/bin GOFLAGS=-mod=vendor`, `-count=1 -v`, tree 867b0e9:

| tree | packages | PASS | FAIL | SKIP | build | vet |
|---|---|---|---|---|---|---|
| 867b0e9 | agent, upgrade, server, protocol | **458** | **0** | **1** | exit 0 | exit 0 |
| 867b0e9 | agent + server only (parsed per-package from the same log) | **386** | 0 | 1 | — | — |
| 867b0e9, clean worktree | ./... | — | — | — | **exit 1** (`embed.go:9:12`) | not reached |

Per package: agent 94, upgrade 55, server 292 (+1 skip), protocol 17. The skip is `TestV1_CreateTransfer` (`filetransfer_test.go:280`, self-skip on a transfer/cleanup race) and exists verbatim at `0352f3b:280` — pre-existing.
**No discrepancy with the children**: 386 is exact for `internal/server+internal/agent`, and 449 is v3's own four-package figure at 0352f3b (458 − 449 = 9 = the 4 + 5 new tests). I did **not** re-run 0352f3b and do not grade that 449 as verified; it is the contract's claim, not mine.

---

## A4 — ratification: NOT-READY (I do not ratify; that is Goran's call)

Named blockers:
1. **F2 L3 conversion** — `P9-08_CONTRACT_V3.md:171`: "field 22 × `/proc/stat btime`" is not a time; name `CLK_TCK` and the `btime + starttime/CLK_TCK` form, and name the additive `ProcessInfo` wire fields against `messages.go:587-596`.
2. **F1 grant identity** — `:89-94`, `:105`, `:110-113`: bind `killAuthorization` to a start time or exe inode, or re-verify identity at kill time. A PID-keyed grant with a 600 s TTL authorizes a recycled PID.
3. **F1 R-b condition 3** — `:117`: name the ancestry mechanism and source, or delete the condition; default-deny must not rest on an undecidable predicate.
4. **F4 epoch semantics** — `:227-230`, `:237-238`: "epoch advanced" must not imply "swap completed"; branch on `step` (`rename1` ⇒ restore candidate under the lease) and add the crash-after-rename1 test the table lacks.
5. **F6 duplicate semantics** — `:283` (409 `duplicate_request`) vs `:347` (202 `replayed:true`): one rule, plus a named server-side source for `kind:"unreadable_journal"` (`:284` vs `:267`).
6. **Citation re-anchor before ratification** — `server_update.go:20-25`→`:27-32`; `:133-158`→`:265-296` (write `:289`); `:154`→`:293`. Drift from f07f241, not fabrication, but the contract is unreadable against the head it would be ratified on.
7. **Code leg, not ratification** — hoist `purgeCanaryRegistryRow` out of `server_ws.go:142-148`; rebuild `web/dist`; decide how `embed.go:9` builds from a clean checkout; route the four hand-joined paths through `ManagedPath`.
8. **Nothing in F1-F6 is implemented** — `agent_proc.go:44-52` still has one gate input, `canary.go:203-206` still kills without an order, there is no `writer.go`, no lease, no `AgentInfo.PID`. Ratification is of the design; the implementation unit is still owed, and the P9-03..P9-06 block stands.

---

## A5 — what I could not execute

- **No Windows host.** Every Windows rule (`tasklist`/CSV, the 25-char image-name cap in O6, `LockFileEx`, `QueryFullProcessImageNameW` + `GetProcessTimes`) is graded as contract text against cited source, never as behaviour.
- **No live update hop.** No server leg, no target. A 1.18.6 binary exists and runs — `/opt/data/tmp/v119/bin/probe-client-1186 --version` → `PROBE Client v1.18.6`, 11,059,052 B — so O7's existence claim is true, but I executed no hop and `update_adopt` remains specified-only.
- **No deployed-container check**: I did not verify that `/data/files` exists or is writable on the real host; only that `SetFilesDir` creates it and `main.go` wires it.
- **No fault injection** on the lease/epoch: F4 is graded by reading the ordering rule; the hole I report is a rule-level consequence, not an observed crash.
- Build-from-committed-artifact was tested exactly once, in a clean worktree at 867b0e9, and it failed as stated.

---

## Nemesis record (8 attacks, 4 holes, 0 unaddressed)

1. PID recycled inside the `killAuth` TTL → **hole** (A4 #2): grant has no identity binding.
2. `connPID[agentID]` after a same-name reconnect → **hole** (F2): records the successor, not the superseded connection.
3. Swap dies after `step=rename1`, watchdog t0 predates it → **hole** (A4 #4): epoch comparison skips the restore; canonical stays missing.
4. Duplicate update request → **contradiction** (A4 #5): 409 `duplicate_request` vs 202 `replayed:true`.
5. "Fallback deleted only in prose" → tested: no fallback row, field or autonomous kill anywhere in v3; the code kill at `canary.go:203-206` is declared open implementation work, not a v3 hole (F3 PASS).
6. Fabricated citations (the P9-07 failure mode) → 81/81 files and spans resolve; 0 fabricated; 4 stale by drift (A4 #6).
7. Retention pin leak on a crashed update → answered: `inflightArtifactTTL` 30 min (`artifacts.go:49-52,380-393`) bounds it; no unbounded pin.
8. "Green suite proves the UI shipped" → falsified: the embedded bundle predates the source change and a clean checkout does not build (`embed.go:9`).

---

## Close record

| field | value |
|---|---|
| artefact | `/opt/data/tmp/probe119/P9-12_REVIEW.md` (repo copy `reviews/P9-12_REVIEW.md`) |
| reviewed_by | reviewer=independent (P9-12) |
| verdict | **FIX-FIRST** — F1 FAIL, F2 FAIL, F3 PASS, F4 FAIL, F5 PASS, F6 FAIL; f07f241 FIX-FIRST, 867b0e9 FIX-FIRST; ratification NOT-READY |
| verdict_sha_of_head | `867b0e9fd188076818386d454d0afbf83b5f2a31` (tree under grade; repo head at session start was `d32547ae`, differing only by the pre-existing `reviews/P9-12_REVIEW.md`) |
| suite | 458 PASS / 0 FAIL / 1 SKIP (agent+upgrade+server+protocol), 386 PASS agent+server; build+vet exit 0 — but a clean checkout of 867b0e9 fails to build at `embed.go:9:12` |
| open item | Goran's ratification, and it is NOT-READY: F1 identity binding, F2 L3 conversion + wire field names, F4 epoch semantics, F6 duplicate rule, 3 drifted citations; the whole F1-F6 implementation unit is still owed |
