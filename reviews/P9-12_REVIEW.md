# VERDICT FIX-FIRST P9-08_CONTRACT_V3.md 867b0e9 458P/0F/1S

P9-12 independent review. reviewer=independent; no authored artefact in this unit.
Grade head: `867b0e9fd188076818386d454d0afbf83b5f2a31` (branch r119-build).
Contract under grade: `/opt/data/tmp/probe119/P9-08_CONTRACT_V3.md`, 50,017 B, sha256 `bb9c7ebfa470f054268842b1a53bf407217ee59d1d3f0eb38d6c1b1ee557770c`.
Commits under grade as code: `f07f241`, `867b0e9`.

RECIPIENT EXPERIENCE: I ran the suites at head and re-ran them in a clean worktree at `0352f3b`; I executed `/opt/data/tmp/v119/bin/probe-client-1186 --version` → `PROBE Client v1.18.6`; I did not run a live update hop (A5).

---

## A1 — F1-F6 grade

Method: every `file:line` in the contract was extracted by script (71 distinct refs), the file resolved in the tree at 867b0e9, the span bounds checked, and each load-bearing span read. Three spans were also read at `0352f3b` to separate fabrication from line drift.

| finding | verdict | rests on (opened at 867b0e9) | why |
|---|---|---|---|
| **F1** bootstrap kill authorization | **FAIL** | `agent_proc.go:44-52` (gate reads only `spawnedPIDs`), `:104-106` (sole write site), `agent.go:227-230`, `:1125-1133`, `:453-455`, `:531-598` (`TypeAgentUpdate` at `:578`), `canary_launch.go:24-31`, `connect.go:54-55`, `server_api.go:471`, `:808-812` | The measured defect is real and every citation resolves. The mechanism fails on identity: the grant is keyed to a **PID number** (`contract:92-94`, `:105`, `:113`) with a 600 s TTL, and nothing in the grant records the target's start time, inode or image. A PID recycled inside the TTL after a failed/expired kill authorizes a kill of an unrelated process — the precise class the contract's own L3 calls out ("A recycled PID is the failure this prevents"). Second defect, `contract:117`: registration condition 3 ("`old_pid` is not an ancestor further up than one hop (reject `old_pid <= 1`)") names no mechanism and no source (`/proc/<pid>/stat` field 4 or Toolhelp32 PPID) and is self-contradictory as written — an implementer cannot decide it. |
| **F2** check (c) identity | **FAIL** | `server_ws.go:93` (`agentID := info.Name` — v2's cited PID source; confirmed fabricated), `:82-89` (plain `json.Unmarshal`, additive-safe), `:107`, `:113-116`; `messages.go:219-227` (no `PID`), `:587-592` (no exe/start), `:798-803`; `agent_proc.go:161` (`ps -eo pid,comm,...`), `:129-157` (tasklist CSV) | The fabrication is correctly deleted and L0/L1/L2 are sound: POSIX `comm` really is 15 bytes and `probe-client-linux` (18 B) / `hermes-remote-server-v20` (23 B) really cannot match `filepath.Base`. **L3 — the layer the contract says makes the decision — is malformed on Linux**: `contract:171` states "start ticks from `/proc/<pid>/stat` field 22 × `/proc/stat btime`". Those are not multiplicands; a time needs `btime + starttime/CLK_TCK` and no clock-tick constant is named. Also, the "mandatory additive extension" to `ProcessInfo` is never given wire field names (contrast `identity_exe`/`identity_source` used in §2.1), so the agent side must invent them. |
| **F3** the 60 s fallback | **PASS** | `canary.go:203-206` (the unconditional `stopProcess`), `server_ws.go:120-139` (`delete(pendingUpdates)` at `:123` before the 3 s `Sleep` at `:137`) | Verified against v2: `P9-01_UPDATE_CONTRACT.md:230,:232,:599,:610` carry the fallback row; v3 has no fallback row and no `kill_grace` / `kill_authority` field — the only hits for "fallback" in v3 are statements that it is deleted. Both server-path defects are real at head. Code still carries `canary.go:205` — expected, v3 is design-only, but it is implementation work, not a closed item. |
| **F4** watchdog / swap race | **PASS** | `canary.go:149-175` (two renames, `:163` and `:167`, no lock), `:126-139` (unlocked restore), `:97-120`, `journal.go:89-107` | The race is real: `grep -rn 'Flock\|LockFileEx' internal/ cmd/` = **0 hits**, so the lease/epoch primitive is genuinely new, not a description of existing code. The ordering rule (epoch at t0 → non-blocking lease → re-read epoch → re-verify branch condition inside the lease) closes the restore/rename-2 interleave; a crash after rename 1 leaves the branch condition true and the restore correct. |
| **F5** journal has no writer | **PASS** | `journal.go:9-10, :38, :67-85, :126, :149-159, :170-205`, `agent_update.go:47-51, :30-151, :131-136` | Verified independently: `FormatLine` is called only from `journal_test.go`; `grep -rn 'internal/upgrade' --include=*.go` (non-vendor) returns **0 importers**; no `.update.journal` literal exists; the parser is `strconv.ParseInt(...,10,64)` at `:126` while `FormatLine` emits `now.Unix()` at `:70` — C7 is correct and the RFC3339 example would have parsed to 0. The writer, file, durability rule and per-state writer table are named. |
| **F6** the 409 dialect | **PASS** | `server_api.go:303`, `:309`, `:480-490` (flat `{code,message,detail}` writer), `api_v1.go:186-204` (`reformatV1` lifts typed bodies), `idempotency.go:11-19` (three states) | Both reused codes exist at the cited lines; `LOCK_BUSY`/`UPDATE_IN_FLIGHT` have **0 hits** in the tree, so deleting them is free; the envelope is the tree's own. One internal contradiction: `contract:284` sets `detail.kind:"unreadable_journal"`, but `contract:267` states the server writes no journal and "cannot see the filesystem it would be swearing about" — the server cannot observe that condition. |

### Citation audit (all 71 refs)

All 71 referenced files exist; **no out-of-range span**; 68 spans say what the contract says they say. Three do not, and the cause is line drift, not fabrication:

| citation | head says | correct at head | correct at 0352f3b |
|---|---|---|---|
| `server_update.go:20-25` (C14, F3 row) | `canaryRegistrySuffix` comment + const | `:27-32` (`type pendingUpdate struct`) | yes — `:20-25` was the struct |
| `server_update.go:133-158` (F2 L1) | ws_remote_path sha block | `:271-296` (`handleAgentUpdateResult`); write at `:289` | yes — `:133-158` was the handler |
| `server_update.go:154` (C13) | `hash = computed` | `:293` (`NotifiedAt: time.Now()`) | yes — `:154` was the write |

Cause: `f07f241` inserted 7 lines at the top of `server_update.go`. The P9-07 failure mode was invented citations; this is not that. It is still a contract that cannot be read against the head it is being ratified on.

---

## A2 — the two commits as code

**`f07f241` — FIX-FIRST.** Claims checked against the diff and the head source:
- `ws_remote_path` carries its own sha256: **true**. `params.SHA256` added; when `binary_path` is empty the supplied digest is used and a missing one is 400 (`server_update.go:140-162`); the digest is forwarded as `updateParams.SHA256` (`:216`) and re-verified on the agent against the staged file (`agent_update.go:163-166`). End-to-end, not just plumbing.
- canary row purged "at update start and completion": **start true** (`server_update.go:227`, before dispatch), **completion only on kill success** — `server_ws.go:140-148` calls `purgeCanaryRegistryRow` inside the `else` branch. When `proc_kill` errors (the log line itself anticipates "old process may have already exited"), the stale `<agent>.canary` row — the exact defect the commit fixes — survives until the next update start. Defect: hoist the purge out of the `else` (or `defer` it).
- Test coverage: `TestP908CanaryRegistryCleanup` calls the helper directly; `TestP908UpdateStartDropsStaleCanary` exercises the start path through `postUpdate`. **No test exercises the completion path** (`server_ws.go`), which is where the defect is.

**`867b0e9` — FIX-FIRST.** Source census: `grep -rn '/tmp/probe-files'` over the tree gives **0 non-test source hits**; the two remaining hits are inside `internal/server/p911_hygiene_test.go` (`:15` prose, `:117` rejection fixture) and are intentional. `FilesDir()`/`--files-dir`/`PROBE_FILES_DIR` wired at `cmd/probe-server/main.go:40,114-117,158`. Upload is genuinely streamed (`io.Copy` into a temp file under a 413 cap, `artifacts.go:483-511`); retention keeps newest N and never deletes an in-flight path (`artifacts.go:294-331`); `binary_path` outside the dir is 400 (`artifacts.go:172-185`).

Two findings:
1. **The served bundle is stale.** `web/src/components/agent/FilesTab.tsx:109-131` was rewritten, but the built asset `web/dist/assets/index-qhL5A-As.js` (mtime Nov 22 22:38, untracked) still contains `/tmp/probe-files/` twice and still calls `createTransfer` with the old 4-arg shape. `embed.go:9` embeds `all:web/dist`, so a binary built at 867b0e9 ships the pre-P9-11 UI. The commit's "replaces all seven literals" is true of source and false of what the server serves.
2. **The tree cannot be built from a clean checkout.** `web/dist` is untracked (`git ls-files web/dist` empty) but required by `embed.go:9`; a worktree at `0352f3b` fails with `pattern all:web/dist: no matching files found` until a dist directory is copied in. This is pre-existing, and it is why (1) recurs.

SHAPE reuse `internal/server/registry.go:215` — `purgeCanaryRegistryRow` is `Registry.Remove` + the existing suffix constant; no second registry was introduced.
SHAPE parallel `internal/server/artifacts.go:156` — `ManagedPath` is the managed-path constructor, but four sites still assemble `s.FilesDir() + "/" + name` by hand (`api_v1_transfer.go:230,254`, `server_download.go:59,79`). Same class of bug the unit exists to remove, one level down.
SHAPE none — the contract adds no code.

---

## A3 — suites, my own counts

`PATH=/tmp/go/bin GOFLAGS=-mod=vendor`, `-count=1 -v`:

| tree | packages | PASS | FAIL | SKIP | build | vet |
|---|---|---|---|---|---|---|
| `867b0e9` (head) | agent, upgrade, server, protocol | **458** | **0** | **1** | exit 0 | exit 0 |
| `867b0e9` | agent + server only | **386** | 0 | 1 | — | — |
| `0352f3b` (worktree) | agent, upgrade, server, protocol | **449** | 0 | 1 | n/a | n/a |

No discrepancy: 867b0e9's "386 pass 0 fail in internal/server+internal/agent" is exact, and v3's "449" is the four-package count at its own head. 458 − 449 = 9 = 4 new tests in `p908_update_hardening_test.go` + 5 in `p911_hygiene_test.go`. The single skip is `TestV1_CreateTransfer` (`filetransfer_test.go:280`, self-skip: "background transfer goroutine races with test cleanup — covered by integration tests"); it is present at `0352f3b` too, i.e. pre-existing, not introduced by either commit.

---

## A4 — ratification: NOT-READY

Not ratified. What remains, each with its anchor:

1. **F2 L3, `P9-08_CONTRACT_V3.md:171`** — replace "field 22 × `/proc/stat btime`" with the real conversion (`started_at = btime + starttime/CLK_TCK`, with CLK_TCK named) and name the additive `ProcessInfo` wire fields (`messages.go:587-592`). Until then check (c)'s decision layer is unimplementable.
2. **F1 kill authorization, `:92-94`, `:105`, `:117`** — bind the grant to process identity (start time or exe inode), or re-verify identity at kill time; a PID-keyed grant with a 600 s TTL authorizes a recycled PID.
3. **F1 R-b condition 3, `:117`** — name the PPID/ancestry mechanism and state the rule unambiguously, or delete the condition (default-deny must not depend on an undecidable predicate).
4. **F6, `:284` vs `:267`** — `detail.kind:"unreadable_journal"` names something the server cannot read; rename it (job-store) or specify the server-side state source.
5. **Citation re-anchor before ratification** — `server_update.go:20-25 → :27-32`, `:133-158 → :271-296`, `:154 → :293` (drift introduced by `f07f241`).
6. Code leg, not ratification: hoist `purgeCanaryRegistryRow` out of `server_ws.go:140-148`'s `else`; rebuild `web/dist`; decide how `embed.go:9` builds from a clean checkout.
7. Nothing in F1-F6 is implemented: `agent_proc.go:46` still has one gate input, `canary.go:203-206` still kills without an order, there is no `writer.go` and no lease. Ratification is of the design; the implementation unit is still owed.

---

## A5 — limits

- No Windows host. Every Windows rule (`tasklist` 25-char cap, `LockFileEx`, `QueryFullProcessImageNameW` + creation time) is graded as contract text against cited source, never as behaviour. O6 stands.
- No live update hop executed on either platform: no server leg, no Windows target. A 1.18.6 binary **does** exist and runs (`/opt/data/tmp/v119/bin/probe-client-1186 --version` → `PROBE Client v1.18.6`, 11,059,052 B), so O7's existence claim is true — the hop was still not run, and `update_adopt` remains specified-only.
- The 449 figure was reproduced in a worktree at `0352f3b` only after copying an untracked `web/dist` in; I did not grade the contents of that bundle against `0352f3b`'s source.
- Crash/restart behaviour of the epoch and lease is graded by reading the ordering rule, not by fault injection.

---

## Nemesis record (3+ attacks, all answered)

1. Tiny PID/`comm` names and truncation boundaries — `commMatches` rule and its five examples hold; a 12-byte non-truncated name is correctly rejected.
2. Watchdog restore during a crashed swap — after rename 1 the branch condition (`canonical` missing ∧ `sha(.old)==sha(self)`) is true and the restore is correct; the lease blocks the interleave while alive. No hole found.
3. Authorization replay / stale grant — **hole found** (A4 #2): PID reuse inside the TTL.
4. Journal concurrency — `O_APPEND` one-write-per-line plus `job`/`seq` makes an interleave detectable by `Analyze`; acceptable for local files.
5. Contract citations — 71/71 resolve, 3 stale-by-drift, 0 fabricated: the P9-07 failure mode is not repeated.

---

## Close record

| field | value |
|---|---|
| artefact | `/opt/data/tmp/probe119/P9-12_REVIEW.md` |
| reviewed_by | reviewer=independent (P9-12) |
| verdict | **FIX-FIRST** — F1 FAIL, F2 FAIL, F3/F4/F5/F6 PASS; both commits FIX-FIRST; ratification NOT-READY |
| verdict_sha_of_head | `867b0e9fd188076818386d454d0afbf83b5f2a31` |
| suite | 458 PASS / 0 FAIL / 1 SKIP (agent+upgrade+server+protocol); build+vet exit 0 |
| open item | P9-08 contract v3 ratification is Goran's; F1/F2 defects and 3 drifted citations must be fixed first |
