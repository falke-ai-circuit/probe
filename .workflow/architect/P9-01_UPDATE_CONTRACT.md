# P9-01 — UPDATE CONTRACT (design gate; no code)

| field | value |
|---|---|
| unit | P9-01 · milestone P9 · workflow wf_probe1 |
| owner | architect (design only — no code, no dispatch) |
| session | api_server · profile `architect` · gateway: none |
| timestamp | 2026-11-23T13:05Z (kernel `date -u`, not recall) — v2 correction pass |
| reviewed_by | architect self-check v2 (independent reviewer: **pending**) |
| verdict | **CONTRACT COMPLETE — four measured defects in v1 corrected (§0.4 X1-X4); coder may start §2/§6/§7, UI from §8, Windows from §9** |
| ratified input | `/opt/data/tmp/probe119/P8-02_DESIGN.md` · 871 lines · 69,707 B · sha256 head `88e6fbf2983c17a3` — read this unit; OD-1 and A-1 govern §9 and §7 |
| tree | `/opt/data/projects/PROBE_UPGRADE/v119wt` · branch `r119-build` · HEAD `b3c0b3c` (= `3ad56e5` + the v1 copy of this document) · working tree clean at read |
| tree_read sha | `3ad56e5` (source code; this unit modifies no source file — the closure commit adds this document only) |
| research leg | **NONE** — the existing mechanism was read and is cited below; every claim is `file:line` from this tree or a live probe run this unit |
| status | design gate artefact. Coder units may start from §2/§6/§7; §8/§9 are frontend and Windows work orders |
| prohibited compliance | no parallel swap mechanism (reuses `internal/upgrade` + `internal/agent/canary.go`); no agent-wire field the 1.18.6 fleet must learn; no confirmation claim outside §2's four checks; no frontend rewrite (§8 verdict: additive); no code changed |

---

## 0. Evidence base — measured this unit

### 0.1 Wire and code facts (read, this tree)

| # | fact | evidence |
|---|---|---|
| M1 | The update route exists on the v1 surface and is **live**: `POST /api/v1/agents/{id}/update` → `v1WrapAgentHandler("update", handleAgentUpdate)` | `internal/server/api_v1.go:386-387`; live probe on `127.0.0.1:17801` (`token t119`) → **HTTP 400** with empty body = handler reached, params rejected. Not 404 |
| M2 | The same handler is also mounted on the legacy surface — the bootstrap lane | `internal/server/server_api.go:112-113`; RBAC mapping `"update"` at `:908` |
| M3 | Params already on the wire: `download_url, filename, ws_path, sha256, version` | `internal/protocol/messages.go:787-793` |
| M4 | Result already on the wire: `success, old_pid, new_pid, message` | `internal/protocol/messages.go:798-803` |
| M5 | The agent flow is 7 steps: download → sha256 → stage `<exe>.next` → `StartCanary(new, canonical, config, oldPID)` → canary proves → promotes → stops old → reports old/new PID | `internal/agent/agent_update.go:19-29`, `:44-150` |
| M6 | **sha256 verification is skipped when the field is empty** (`if params.SHA256 != ""`) | `internal/agent/agent_update.go:84-95`; same skip on the WS path `:163-172` |
| M7 | `StartCanary` truncates `<canonical>.canary.log` every update and passes `connect --config P --canary --canary-old-pid N` | `internal/agent/canary_launch.go:24-31`, `:35-51` |
| M8 | Canary proves health for a fixed **10 s**, aborts if the connection dropped, swaps, then stops the old PID — the swap is performed FROM the new process | `internal/agent/canary.go:19`, `:33-75`, `:186-209` |
| M9 | Promotion is two renames with sha-proof: archive previous `.old` → `canonical → .old` → `.next → canonical`; failed commit restores and asserts sha; unverifiable rollback is a loud error | `internal/agent/canary.go:97-175`; tests `internal/agent/canary_swap_test.go:42`, `:93`, `:140` |
| M10 | Old-process stop = `TerminateProcess` on Windows (no graceful signal), SIGTERM then SIGKILL after 5 s on POSIX | `internal/agent/canary.go:214-235` |
| M11 | After a successful commit the canary strips `-canary` from its name and **closes its own connection**, relying on the reconnect loop to re-register under the canonical name | `internal/agent/canary.go:63-74`; `cmd/probe/connect.go:106-109` |
| M12 | Server keeps `pendingUpdates[agentID]{OldPID, NewVersion}` and on the next connection sleeps a fixed 3 s, then sends `proc_kill{PID: oldPID, Signal: 9}` to the **new** agent | `internal/server/server_ws.go:118-139`; record written at `internal/server/server_update.go:133-158` |
| M13 | `pendingUpdates` has **no TTL and no PID-identity check** — a stale record can fire a kill on a recycled PID after an arbitrary delay | `internal/server/server_ws.go:20-25` (struct has `NotifiedAt`, never read), `:121-139` |
| M14 | Today a forward timeout returns **HTTP 200** `{"status":"update_in_progress"}` — the A-1 "200 that means not-run" pattern, still present on this route | `internal/server/server_update.go:111-124` |
| M15 | Server requires `binary_path` or `ws_remote_path`, hashes a **server-local** file, copies it to the hardcoded `/tmp/probe-files/`, and builds `http://<host>/download/<file>?token=<server token>` (token in the URL) | `internal/server/server_update.go:36-50`, `:53-70`, `:85` |
| M16 | Builds are downloadable by id: `GET /api/v1/builds/{id}/download` — the artifact never needs a hand-copied path | `internal/server/api_v1.go:352-356` (P8-02 F17) |
| M17 | Rollback kit exists and is reusable: `ParkOld` / `RestoreOld` / `RestoreAndRelaunch`, rename-only (atomic, no delete primitive needed), `ProcStarter` = agent `proc-start` | `internal/upgrade/rollback.go:19-40`, `:52`, `:74`, `:98-112`, `ParkTimeoutS=15` `:114` |
| M18 | Journal vocabulary and `Analyze` already carry the terminal readings this contract needs, including `OutcomeOldPIDAlive` | `internal/upgrade/journal.go:149-205` |
| M19 | 256 KiB chunks are the measured-safe WS chunk: 10,700,000 B in 41 chunks, 1836 ms; the old 10.7 MB collapse was a **write/write race** (unsynchronised pong writer), fixed, not a size cliff | `/opt/data/tmp/probe119/P8-06_TRANSFER.md` §2, §7 |
| M20 | Windows service host exists: service `PROBEA`, `StartAutomatic`, LocalSystem, recovery 5/10/30 s; flags `--install-service`, `--service-status`, `--uninstall-service`; runtime leg UNVERIFIED-ON-HOST | `/opt/data/tmp/probe119/P8-10_HYGIENE.md` §4.1-§4.4 |
| M21 | Windows build size measured: **13,556,224 B** (base 13,423,104 B + 133,120 B service host) | `P8-10_HYGIENE.md` §4.3 |
| M22 | Unknown JSON fields are ignored on both directions (no `DisallowUnknownFields` anywhere) — additive fields are safe for old agents | `P8-02_DESIGN.md` F7 |

### 0.2 The live 404 — resolved, not designed around

The brief states `GET /api/v1/agents/{id}/fs/download` returns 404 while `api_v1.go:342` registers it. Measured resolution:

| probe | result | reading |
|---|---|---|
| `curl -o /dev/null -w '%{http_code}' -H 'Authorization: Bearer t119' 'http://127.0.0.1:17801/api/v1/agents/nope/fs/download?path=/etc/hostname'` | **404** | route not in that binary |
| `strings /opt/data/tmp/v119/bin/probe-server-main \| grep -c 'fs/download'` | **0** | the route string is absent from the deployed binary |
| `ls -l --time-style=full-iso /opt/data/tmp/v119/bin/probe-server-main` | `2026-11-22 22:35:46` | binary predates the route |
| `git log -S'fs/download' -- internal/server/api_v1.go` | `fa29134 P8-06: raw-body upload/download…` | route landed in P8-06, **2026-11-23 07:41** |
| `curl … /api/v1/agents/nope/update` (POST, empty body) | **400** | handler present; params rejected — the brief's reading is confirmed |

**Verdict on the discrepancy: STALE BINARY, not a missing route.** The v1.19 server under probe on `:17801` was built 2026-11-22 22:35, nine hours before P8-06 landed the route. Rebuild and the 404 becomes a 401/404-for-agent, not 404-for-route. Consequence for this contract: the delivery leg may rely on `fs/download` **only on a build at ≥ `fa29134`**; the readiness probe in §6.4 checks the route's presence (`/api/v1/version` build sha or an OPTIONS/probe call) before the HTTP channel is selected. That is a 6-line pre-flight, not a redesign.

### 0.3 What the brief gets right, and the two places it is incomplete

| brief says | source says | effect |
|---|---|---|
| "swap to new then kill old FROM new so it confirms" | exactly what `canary.go:186-209` does — swap, then `stopProcess(CanaryOldPID)` | design keeps it; the missing piece is durability + identity, not sequence |
| "server confirms via `handleAgentUpdateResult`" | it records `pendingUpdates` (`server_update.go:133-158`); the actual confirmation is a **3 s sleep + blind `proc_kill`** (`server_ws.go:130-139`) | confirmation as implemented is a timing guess, not a proof — §2 replaces it with four checks |
| "the old process restores `.old` if the canary does not promote" | nothing is renamed before promotion, so there is nothing to restore in the common case; `.old` restore matters only after a **partial** swap | §3 watchdog has three branches, because two of them are not "restore" |
| kill is "from the server after confirmation" | the measured kill is the canary's own `stopProcess`; the server kill is a 3 s-delayed backstop | both exist; §4 assigns authority explicitly so they cannot race |

### 0.4 v2 correction pass — three v1 claims that did not survive re-reading the wire

v1 of this document asserted three things the source does not support. They are corrected here rather than left for the coder to discover at build time. Each is a `file:line` fact, not a preference.

| # | v1 claim | source says | correction |
|---|---|---|---|
| **X1** | Kill identity guard reads the target row's `exe` and start time from `proc-list` (§4.3 steps 2-3) | `protocol.ProcessInfo` carries **`pid`, `name`, `cpu_percent`, `memory_mb` only** — no exe path, no start time (`internal/protocol/messages.go:587-592`); `windowsProcessList` parses `tasklist /FO CSV` name+PID only (`internal/agent/agent_proc.go:129-157`) | the guard cannot be built on that wire. §4.3 is rewritten to use evidence that exists: the canary's own in-process `CanaryOldPID` (`internal/agent/agent.go:177-178`) and the superseded connection's PID as recorded server-side at connect (`internal/server/server_ws.go:93`). `exe`/`started_at` become an **additive, availability-gated** `ProcessInfo` extension — declared, never required |
| **X2** | The server backstop `proc_kill` is a working second killer (§4.2 row 2) | in `sandboxed`\\|`standard` permissions the agent refuses to kill a PID it did not spawn (`internal/agent/agent_proc.go:44-52`); the new agent never spawned the old PID, so the backstop is **dead code** on those agents as written | §4.2 gains a mandatory registration step: the canary inserts `CanaryOldPID` into `spawnedPIDs` at startup (`internal/agent/agent.go:227-229`, `:105` pattern), which is the existing allowlist, not a new bypass. Without it the backstop legitimately refuses and the job ends F4 |
| **X3** | Check (d) runs `exec "echo probe-update-ok"` (§2.1 row d) | there is no `echo` executable on Windows; `exec` spawns the string as a command (`internal/server/server_api.go:184-201`) | the probe is `cmd /c echo probe-update-ok` on Windows and `/bin/echo probe-update-ok` elsewhere, selected from the agent's advertised `os` (`internal/server/registry.go:37`) |
| **X4** | Kill authority was assigned canary-first (v1 §4.2, D-04) | the owner's instruction is explicit: *"proc_kill from the server after confirmation, not self-kill before it"*, while the intent line says the swap-then-kill is driven *from* the new process | resolved by splitting **authority** from **execution**: the **server** issues `proc_kill`; the **new process** executes it (it is the only process that receives the command, and it holds the inherited PID). The canary never calls `TerminateProcess`/`stopProcess` on its own initiative — `canary.go:203-205` is the line that violates the instruction and must be gated off. §4.1/§4.2 rewritten; §1.1 S5 and D-04 updated |

No other v1 claim was contradicted by the tree. The 404 resolution (§0.2), the byte arithmetic (§6.2) and the 256 KiB measurement (§6.3) were re-verified this pass.

---

## 1. State machine — one update

Single job object, `update_id`, per agent. Every state is persisted before it is reported; a state the journal cannot prove is not a state.

### 1.1 States

| # | state | driver (who writes it) | entry evidence (what proves it) | terminal |
|---|---|---|---|---|
| S0 | `idle` | server | no open job for the agent; no lease | no |
| S1 | `requested` | **server** — `POST /api/v1/agents/{id}/update` | job row created + lease claimed; server audit line `[update] job=upd_… agent=… sha=… channel=http lease_until=…`; `AgentUpdate` envelope written to the agent | no |
| S2 | `artifact_verified` | **old process** (agent), reported to server | agent hashes the artifact and compares to `params.SHA256`; journal `event=ARTIFACT_VERIFIED sha256=<hex> size=<n> source=<http\|ws>`; server confirms the same sha against the build record | no |
| S3 | `canary_connected` | **canary** (new process) | a connection registering `<name>-canary` (`connect.go:106-109`); server registry row for `<name>-canary` with the expected version; journal `event=CANARY_CONNECTED pid=<n>` | no |
| S4 | `promoted` | **canary** | `swapBinaryPaths` returned success: `<canonical>.old` holds the previous sha, `<canonical>` holds the expected sha; journal `event=PROMOTED canonical_sha=<hex> backup=<path>` | no |
| S5 | `old_killed` | **server** (authority) → **new process** (executes `proc_kill`) | server issued `proc_kill{PID: old_pid}` after S4 was durable and checks (a)(b) were true; agent returned `TaskStopResult{killed:true}` (`messages.go:603-606`) or the PID is absent from `proc-list`; journal `event=OLD_KILLED pid=<n> by=server executor=new` | no |
| S6 | `confirmed` | **server** | all four checks of §2 true, in order, each with its own evidence row | **yes** |
| F1 | `failed` | server (from any pre-promotion state) | `failure{code,message,at}` + journal `event=ABANDONED reason=…`; canonical binary untouched (sha == pre-update) | **yes** |
| F2 | `rolled_back` | old process or canary | journal `event=ROLLED_BACK_*`; canonical sha == pre-update sha, asserted (`canary.go:126-139`) | **yes** |
| F3 | `rollback_failed` | whoever attempted the restore | journal `event=ROLLBACK_FAILED`; loud, never silent (`canary_swap_test.go:140`) | **yes — page-worthy** |
| F4 | `promoted_not_confirmed` | server | S4 true, S5 or a §2 check false | **no** — escalates, see §3.3 |

### 1.2 Legal transitions

| from | to | guard (must be true) | illegal if |
|---|---|---|---|
| S0 | S1 | lease acquired; `sha256` present and equals the build record; artifact ≤ §6.3 max | any check false → 4xx, state stays S0 |
| S1 | S2 | agent returned `artifact_verified` **or** the agent's journal line exists (the agent's reply may be lost when its socket drops, M14) | hash mismatch → F1 `INTEGRITY_MISMATCH` |
| S2 | S3 | canary registration observed within `canary_connect_deadline` (§3.1) | deadline → F1 `CANARY_NEVER_CONNECTED` |
| S3 | S4 | 10 s prove held (M8) **and** both renames succeeded **and** restored sha asserted | commit rename failed → F2 (restore verified) or F3 (restore unverified) |
| S4 | S5 | kill issued only after the `PROMOTED` journal record is on disk; `old_pid` identity matches the probe exe path (§4.2) | identity mismatch → do not kill; F4 |
| S5 | S6 | §2 checks (a)(b)(c)(d) all true | any false → F4 or F1 per §2.4 |
| S1/S2/S3 | F1 | any deadline or refusal, before promotion | after promotion F1 is forbidden — promotion is not undone quietly |
| S4 | F2 | only on an explicit operator rollback, never automatically | — |

**No state skips.** A `confirmed` without S3 is only legal for the **1.18.6 bootstrap lane**, where the canary does not exist: there S3 is satisfied by "a connection with the canonical name and the expected version arrived after the job opened" and the job carries `legacy_flow:true` (see §10).

### 1.3 Where each driver sits

```
server ──S1──▶ old process ──S2──▶ canary ──S3──▶ canary ──S4──▶ canary ──S5──▶ server ──S6──▶ confirmed
   (job,lease)      (hash)          (prove 10s)     (2 renames)     (kill old)      (4 checks)
```

| driver | may write | may never write |
|---|---|---|
| server | S1, S5, S6, F1, F4 | S4 — the server cannot observe a rename it did not do |
| old process | S2, F1 (pre-promotion), F2 branch (ii) of §3.2 | S4, S5 — and never its own termination |
| canary | S3, S4; executes S5's `proc_kill` on the server's order | S6 — the new process cannot certify itself; S5 authority |

---

## 2. The "100%" claim, made provable

`confirmed` **is** these four checks. It is not a log line, not a version string, not "the agent said success". All four are derivable from surfaces the 1.18.6 agent already speaks (`fs-hash`, `proc-list`, `exec`, registration) — no new agent command is introduced.

### 2.1 The four checks

| # | check (normative) | who computes | probe (re-runnable) | evidence field in the job |
|---|---|---|---|---|
| **a** | **version read FROM THE ARTIFACT equals expected** — and the artifact on the agent is byte-identical to the artifact that version was read from | server | `POST /agents/{id}/fs-hash {path:<canonical>}` → `sha256` must equal `artifact.sha256`; then `exec "<canonical> --version"` (10 s) → stdout must contain `version.expected`; the server independently runs `--version` on its own build artifact and compares to `build.version`. Where `exec` is unavailable, the sha-bridge stands alone: equal sha over a build whose version the server read = the agent runs that version | `checks.a = {ok, sha_agent, sha_expected, version_observed, version_expected, source:"artifact", at}` |
| **b** | **the new process registered under the SAME agent name** | server (registry) | `GET /api/v1/agents/{id}` → `name == id`, `version == expected`, and no registry row whose name ends `-canary` for this host (`canary.go:26`, `:71` strips it before reconnecting); `last_seen` within the job window | `checks.b = {ok, agent_id, name_registered, version_registered, canary_row_remaining:false, at}` |
| **c** | **the old PID is gone** | server (agent-side truth) | `POST /agents/{id}/proc-list` → **no row with `pid == old_pid`**. Identity is established *before* the check, not from the listing: the PID came from the old process itself (`AgentUpdateResult.OldPID`, `messages.go:798-803`) and from the superseded connection (`server_ws.go:93`). Where a 1.19+ agent supplies the additive `exe` field, the row's `exe` must resolve to the canonical path; on the current wire only `name` exists (`messages.go:587-592`), so the fallback is `name == filepath.Base(canonical)` and the job records `identity_source:"name_only"` | `checks.c = {ok, old_pid, present:false, identity_source:"ping"\|"proc-list-name"\|"proc-list-exe", at}` |
| **d** | **one functional probe succeeds post-swap** | server | `exec` the platform marker — `cmd /c echo probe-update-ok` on Windows, `/bin/echo probe-update-ok` elsewhere (selected from the agent's `os`, `registry.go:37`) — deadline 10 s → `ok:true`, `exit_code:0`, stdout contains the marker, `replayed:false`, and the response's `agent_clock_skew_ms` is present | `checks.d = {ok, exit_code, command, stdout_marker:true, request_id, at}` |

### 2.2 The identity bridge, stated because it is the load-bearing assumption

(a) does not trust the agent's self-reported version in the connect message (`AgentInfo.Version`), which is process memory. It trusts **bytes**: `fs-hash(canonical)` on the agent vs `sha256` of the build artifact the server holds. Version is then read from a byte-identical file. If sha matches and `--version` on that artifact equals the expected label, the version on the agent's disk **is** the expected version. That is the whole argument; break the sha equality and (a) is false regardless of what any process claims.

### 2.3 The slogan this replaces

| unacceptable evidence | why it fails | where it lives today |
|---|---|---|
| `AgentUpdateResult.success == true` | the old process sets it after **starting** a canary, before any promotion (`agent_update.go:141-150`) — success means "spawned", not "swapped" | `messages.go:798-803` |
| the new agent's `version` in its first envelope | self-report; a stale binary can claim anything, and an old process reconnecting can supply it | `server_ws.go:82-107` |
| "the server sent `proc_kill` without error" | a successful send is not a dead process; the target may have exited on its own | `server_ws.go:133-138` |
| version changed in `GET /api/v1/agents` | a second agent with the same name, or a reconnecting old process, moves this field | `server_ws.go:107` |

### 2.4 Check failure semantics

| failing check | state | action |
|---|---|---|
| (a) false | F1 `ARTIFACT_MISMATCH` (pre-promotion) / F4 (post) | pre-promotion: abandon staged `.next`, old keeps serving. post: escalate, offer rollback |
| (b) false | F4 `NAME_NOT_RECLAIMED` | the canary is alive under `-canary` → wait one reconnect cycle (10 s), then offer rollback |
| (c) false | F4 `OLD_PID_ALIVE` (matches `journal.go:158` `OutcomeOldPIDAlive`) | retry the kill once with identity proof; then escalate with `kill_requires_elevation` (§4.4) |
| (d) false | F4 `PROBE_FAILED` | one retry with a 30 s deadline; then escalate |

**A job never reports `confirmed` with any of the four false.** The UI must render `promoted_not_confirmed` as a warning, never as a success with a caveat.

---

## 3. Negative side — the watchdog

### 3.1 Numbers

| parameter | value | derivation |
|---|---|---|
| `canary_prove` | **10 s** | existing constant, `canary.go:19` — unchanged |
| `canary_connect_deadline` | **40 s** | process start + WS handshake + auth on a loaded box; 4× the measured handshake, bounded |
| `promote_deadline` | **50 s** from canary connect | 10 s prove + 2 renames + sha over a 13.5 MB file on a contended disk |
| **`watchdog_n` (the number)** | **90 s** from the old process dispatching the update command | 40 + 50 = 90 exactly; no slack is added, because slack is indistinguishable from a hang |
| server `pending_update_ttl` | **120 s** | 90 s watchdog + one reconnect cycle (10 s) + 20 s for the four checks to fail loudly |
| lease TTL | **300 s** (§5.2) | 120 + a second full attempt's budget |

### 3.2 Owner and the three branches

**Owner: the OLD process.** It is the only actor that (i) holds the pre-update binary's sha, (ii) is still serving commands, and (iii) is the party that must not die if the update fails. It starts a goroutine at `agent_update.go:132` (where `StartCanary` is called) and evaluates three branches at t=90 s:

| branch | condition at t=90 s | action | journal line |
|---|---|---|---|
| **i — nothing happened** | `sha(canonical) == sha(self)` and `<canonical>.next` exists | quarantine the staged file as `<canonical>.next.abandoned-<unix>`, keep serving, report F1 | `event=WATCHDOG_TIMEOUT branch=abandon_staged wait_s=90 canonical_sha=<hex> next=quarantined old_pid=<n>` |
| **ii — crash mid-swap** (the literal "restores .old") | `canonical` missing and `sha(<canonical>.old) == sha(self)` | `restoreBackup(.old → canonical, expected=self sha)` with the sha asserted, then keep serving, report F2 | `event=WATCHDOG_TIMEOUT branch=restore_old wait_s=90 restored_sha=<hex> match=true old_pid=<n>` |
| **iii — promoted, not confirmed** | `sha(canonical) == expected_new` while this process still runs | **do not roll back** — the new binary is in place and promoting it back would destroy a working update; escalate to the server, which owns §2 | `event=WATCHDOG_TIMEOUT branch=escalate wait_s=90 canonical_sha=<new> state=PROMOTED_NOT_CONFIRMED old_pid=<n>` |

Branch (iii) is the honesty clause: a watchdog that "restores" a successful promotion because a *confirmation check* failed would be a rollback triggered by a monitoring gap. Promotion is undone by operators (§8), not by timers.

### 3.3 The line that proves it ran

Exact greppable form, one line per evaluation, appended to `<canonical>.update.journal` (the existing `key=value` journal vocabulary from `journal.go`, event names added additively — no new mechanism):

```
ts=2026-11-23T13:02:11Z event=WATCHDOG_TIMEOUT branch=abandon_staged wait_s=90 canonical_sha=9f2c… next=quarantined old_pid=41288 job=upd_01H…
```

Verification a reviewer can run: `grep -c 'event=WATCHDOG_TIMEOUT' <canonical>.update.journal` must be ≥ 1 for a failed update and **0** for a confirmed one. A confirmed job with a watchdog line is a contradiction and fails the gate.

### 3.4 Why the watchdog is not the server's

The server cannot restore a file it cannot see, and its kill path is exactly the one that must not fire on a guess (M13). The old process owns the file and its own sha; the server owns the record. Each holds the half it can prove.

---

## 4. Kill-old semantics

### 4.1 The rule, in one line

**No kill of any kind before `PROMOTED` is durable and checks (a)+(b) pass; the SERVER issues the `proc_kill`, the NEW process executes it, and the old process never terminates itself.** Authority and execution are separated because the owner's instruction separates them: *"proc_kill from the server after confirmation, not self-kill before it"* — while the kill still happens *from* the new process, because that is the only process holding a live connection and the inherited PID.

### 4.2 Who kills, in what order

| order | actor | trigger | mechanism | evidence |
|---|---|---|---|---|
| **0 — forbidden** | old process | — | self-kill at any time | `canary.go:203-205` (`stopProcess(CanaryOldPID)`) is the line that must be **removed/gated**; self-termination before the server's order is a protocol violation, not a shortcut |
| **1 — authority** | **server** | S4 durable **and** checks (a) and (b) true | `proc_kill{PID: old_pid, Signal: 9}` to the **new** connection (`server_ws.go:132-133` path, now job-scoped instead of a 3 s blind sleep) | journal `event=OLD_KILLED pid=<n> by=server order_id=<upd_…>` |
| **2 — execution** | **new process (canary, now canonical)** | receives `proc_kill` | `taskkill /PID <n> /F` on Windows; `kill -9` on POSIX (`agent_proc.go:53-64`) | `TaskStopResult{killed:true,pid:<n>}` (`messages.go:603-606`); journal `event=KILL_ACK pid=<n> executor=new` |
| **3 — fallback** | **new process** | `kill_grace` = **60 s** after S4 with no server order (server unreachable / restarting) | same `taskkill`/`kill`, journaled as a fallback | journal `event=OLD_KILLED pid=<n> by=canary_fallback reason=server_silent` — visible in the job as `kill_authority:"fallback"`, never silently equal to a server order |

**Why the fallback exists and why it is not the primary.** If the server dies between promotion and the order, the old and new processes both serve and the registry flaps between versions. A 60 s bounded fallback converges the host; it is journaled as a fallback so an auditor can distinguish "confirmed normally" from "confirmed while the server was down". The primary path is still the server's order.

**Mandatory precondition for rows 1-3 (corrects X2).** `proc_kill` is refused by any agent in `sandboxed`/`standard` permissions for a PID it did not spawn (`internal/agent/agent_proc.go:44-52`). The new process therefore must register the inherited PID in the existing allowlist at canary startup — `a.spawnedPIDs[CanaryOldPID] = true`, the same map `proc_start` populates (`internal/agent/agent.go:227-229`, `:105`) — so the kill is authorized for exactly one PID and nothing else. Without this line every kill path above is dead code on a standard-permission agent.

### 4.3 Identity guard (mandatory, closes M13) — rebuilt on evidence that exists

The v1 version of this table asked `proc-list` for the target's **exe path and start time**. Neither is on the wire: `ProcessInfo` is `{pid, name, cpu_percent, memory_mb}` (`internal/protocol/messages.go:587-592`) and the Windows collector parses `tasklist /FO CSV` for name+PID only (`internal/agent/agent_proc.go:129-157`). A guard that reads a field the protocol does not carry is not a guard. Rebuilt:

| layer | source of identity | availability | rule |
|---|---|---|---|
| **L1 — primary (always)** | the PID is inherited, not looked up: `AgentUpdateResult.OldPID` from the old process itself (`messages.go:798-803`) and the superseded connection's PID recorded server-side at connect time (`server_ws.go:93`) | every agent, every version | the canary kills `CanaryOldPID` (`agent.go:177-178`); the server kills only `pending.OldPID`. **Neither ever kills a PID discovered by scanning `proc-list`** — a scan is how a recycled PID gets chosen |
| **L2 — name check (always, on the wire today)** | `ProcessInfo.Name` | every agent | before the kill, `proc-list` must show a row with `pid == old_pid` and `name == filepath.Base(canonical)`. If the PID is absent → already exited, success. If present with a **different name** → F4 `PID_IDENTITY_MISMATCH`, no kill |
| **L3 — strong check (1.19+, additive)** | `ProcessInfo.Exe` + `ProcessInfo.StartedAt`, appended beside the existing JSON fields (no `DisallowUnknownFields`, P8-02 F7) | agents ≥ 1.19; `identity_source:"proc-list-exe"` | the row's `exe` resolves (after `TrimSuffix(".next")`) to the canonical path **and** `started_at < job.requested_at`. A 1.19 agent that omits the fields is not penalised — it degrades to L2 and says so |

Recycled-PID protection is not theoretical: the server's `pendingUpdates` entry survives indefinitely today, so a `proc_kill` can arrive minutes or hours later (M13). §5.3 gives it a TTL; L1 + L2 make the kill wrong-PID-proof even inside the TTL and even without the additive fields.

### 4.4 When the kill cannot complete

| failure | observable | design response |
|---|---|---|
| **Access denied** — old process runs under a principal the canary cannot terminate (old started interactively as user, service as SYSTEM, or vice versa) | `proc_kill` returns an error envelope / `exit_code != 0`; `proc-list` still shows the PID | F4 `OLD_PID_ALIVE`, `action:"kill_requires_elevation"`. Not confirmed. UI shows both PIDs live. Remedy is the OD-1 migration: `probe.exe --install-service` then a repeat update, where both principals are LocalSystem |
| **locked exe** on Windows | lock does **not** block `TerminateProcess`; it blocks delete/overwrite of the file | kill proceeds; the `.old` file simply stays (it is never deleted by design, `rollback.go:9-16`) |
| **process already gone** | `proc-list` has no row | treat as success — (c) is "gone", and absence is the goal; journal `event=OLD_KILLED pid=<n> by=canary already_exited=true` |
| **kill of the wrong PID** (recycled) | identity guard fails | never issued (§4.3) |
| **zombie takeover** — the old process reconnects and displaces the new one | registry row flip-flops between versions | **server refuses a connection whose reported PID equals `pending.OldPID`**: close it, log `[update] refused zombie reconnect from superseded pid=<n>`; the old process's retry loop backs off and the canary keeps the name |

The zombie-takeover guard is a real gap in the measured code: nothing today prevents the old process from re-registering under the canonical name between promotion and kill (M11-M12), and a duplicate-name re-registration would make check (b) flap. It is a server-side filter on the accept path, ~10 lines, no wire change.

---

## 5. Idempotency + concurrency

### 5.1 Identity of a request

The idempotency key is **content**, not intent:

```
idempotency_key = "sha256:" + artifact.sha256
```

Rationale: the operation's meaning is "make this agent run these bytes". Two operators asking for the same bytes are one operation; two asking for different bytes are two operations that must not interleave.

### 5.2 Claim / lease

| field | value |
|---|---|
| resource | `agent:{agent_id}:update` |
| guard | lease record `{update_id, operator_id, sha256, state, expires_at}`, written to the registry store (survives server restart) |
| TTL | **300 s**, renewed on every state transition |
| acquire | atomic compare-and-set on `(agent_id, no live lease)`; loser gets 409 `LOCK_BUSY` with `holder`, `expires_at` |
| release | on terminal state; on expiry, only by the reconciler, and only after `proc-list` proves no `-canary` process and no in-flight `.next` newer than 300 s |
| forced takeover | `{"force":true,"replace_lease":true}` — allowed, audited with both operator ids, never silent |

### 5.3 Behaviour matrix

| situation | response | effect on the agent |
|---|---|---|
| same sha, job non-terminal | `202` + same `update_id`, `replayed:true` | **nothing sent** — no second `agent_update` |
| same sha, job `confirmed` | `200` + `state:"confirmed"`, `replayed:true`, `no_op:true` | nothing |
| same sha, job `failed`/`rolled_back` | `409 UPDATE_PREVIOUSLY_FAILED` + `retry_allowed:true` with an explicit `new_attempt:true` flag | retry permitted, new `update_id`, same lease discipline |
| different sha, job non-terminal | `409 UPDATE_IN_FLIGHT` + `current{update_id,sha,state}` | nothing |
| different sha, terminal | new job | normal |
| two operators, second within TTL | `409 LOCK_BUSY` + holder | nothing |
| server restarted mid-job | job resumes from persisted state; `replayed:false`, `resumed:true`; if the state is S2-S4 the server re-derives from journal + registry rather than re-sending | at most one `agent_update` per `update_id` |

### 5.4 Agent-side idempotency (the missing half)

`handleAgentUpdate` today removes any existing `<canonical>.next` at `agent_update.go:104-105` before staging — a repeated command **clobbers a canary that is mid-prove**. Contract:

| agent condition on receipt | action |
|---|---|
| `<canonical>.next` exists, sha == requested sha, mtime < 300 s, no `PROMOTED` journal record | reply `AgentUpdateResult{success:true, old_pid:self, new_pid:known_canary_pid, message:"duplicate ignored, canary in flight"}` — **no second canary** |
| `.next` exists with a different sha and mtime < 300 s | refuse `ERR_INVALID_PARAMS` "update already staging different artifact" |
| `.next` older than 300 s (abandoned) | quarantine it, proceed |

That is a guard, not a mechanism: same code path, one pre-check.

---

## 6. Delivery channel

### 6.1 Decision

| option | verdict | reason |
|---|---|---|
| **`download_url` (HTTP GET from agent → server `/download/<file>`, Range-capable)** | **PRIMARY** | 1.00× bytes on the wire — the 13,556,224 B build stays 12.93 MiB instead of 17.24 MiB of base64 (§6.2 arithmetic); the bulk transfer does not share the WebSocket control channel, so a slow chunk cannot delay `proc_kill` or heartbeats; resumable with `Range` (proven, P8-06 A3); independent of the 30 s forward deadline (M15, `server_update.go:111`) |
| `ws_path` pre-staging (base64 `file_save` chunks) | **FALLBACK, mandatory** | the only path for agents whose outbound HTTP is blocked but whose WS is up — the exact firewall case P8-02 F13 names. Also the only path when the server's HTTP port is not reachable from the agent's network |
| both at once | **forbidden** | two artifact legs double the failure surface and can deliver two different shas |

Selection is **server-side and automatic**: attempt HTTP pre-flight (§6.4); on failure within 10 s, switch to `ws_path` and set `channel:"ws"` in the job. The agent never chooses; it only honours `ws_path` when non-empty (`agent_update.go:40-42`), which it already does.

### 6.2 The arithmetic that decides it

| leg | bytes on the wire for the measured Windows build (13,556,224 B) |
|---|---|
| HTTP raw | 13,556,224 B = **12.93 MiB** |
| WS base64 | 18,074,965 B = **17.24 MiB** (+33.3%) |
| valmet's case (8 MB exe) | 10.7 MB base64 — the inflation that "made the server fall over" (with the race, now fixed, P8-06 §7) |

### 6.3 Chunk size and maximum artifact

| parameter | value | derivation |
|---|---|---|
| **chunk size** | **256 KiB raw (262,144 B)** | measured-safe: 10,700,000 B in 41 chunks / 1836 ms (M19); one chunk = one bounded `fs-read`/`file_save` message, memory bounded regardless of artifact size; the 13.5 MB build = **52 chunks** |
| per-chunk deadline | 120 s | P8-02 §1.4 |
| **maximum artifact size** | **64 MiB (67,108,864 B)** | 4.7× the measured Windows build (headroom for the frontend bundle + service host growth); 256 chunks at 256 KiB; ~11 s at the measured 5.8 MB/s; fits one bounded staging allocation |
| behaviour above the max | **413 `PAYLOAD_TOO_LARGE`, refused before any byte moves**, `detail{max_bytes:67108864, artifact_bytes:<n>}` | a partial transfer that cannot complete is worse than a refusal, and the server must not begin a job it cannot finish |
| `PROBE_UPDATE_MAX_BYTES` | env knob, default 67108864, `0` = unlimited (operator override, logged) | knobs without code changes (G-TRACEABLE-CONTROLLABLE) |

### 6.4 Pre-flight (the 404 lesson, productised)

Before `channel:"http"` is chosen the server asserts:

| check | probe | fail action |
|---|---|---|
| route present | `GET /api/v1/builds/{id}/download` with `Range: bytes=0-0` → 206 or 200 | 409 `SERVER_BUILD_TOO_OLD` — a binary predating the route cannot serve the artifact leg (§0.2) |
| artifact exists + size | build record `size` matches the file on disk | 404 `ARTIFACT_NOT_FOUND` |
| agent reachability | the agent's `exec` `curl`-equivalent is **not** required; instead the server uses the agent's first `--version`-free probe: a 1-byte `fs-read` **does not** prove outbound HTTP. Concretely: attempt the download from the agent and treat a 10 s no-progress as "fall back to ws" | channel switch, `channel:"ws"`, logged `event=CHANNEL_FALLBACK reason=http_unreachable` |

The reachability leg is honestly the weakest assertion in this contract: it is discovered by attempting, not by probing. That is stated rather than hidden — a pre-probe would need a new agent capability, which the brief forbids for the bootstrap lane.

---

## 7. API surface for the frontend

All new routes are **server-side and frontend-only**. The agent never calls them; the 1.18.6 fleet needs to know none of them (§10).

### 7.1 Start an update

```
POST /api/v1/agents/{id}/update
Content-Type: application/json

{
  "build_id": "bld_01HX…",          // preferred; server resolves path, sha256, version, size
  "sha256": "9f2c…",                // required when build_id is absent; must equal the build record
  "version": "1.19.0",              // required when build_id is absent
  "filename": "probe.exe",          // default: basename of the resolved artifact
  "channel": "auto",                // auto | http | ws   (auto = §6.4 pre-flight)
  "confirm_timeout_s": 90,          // 30..600, default 90 — must equal watchdog_n or be larger
  "idempotency_key": "sha256:9f2c…",// optional; server derives it if absent
  "new_attempt": false,             // true = explicit retry after failed/rolled_back
  "force": false                    // true requires replace_lease:true
}
```

`202 Accepted` (never 200 with a "maybe" body — that is A-1's exact defect, M14):

```json
{
  "ok": true,
  "data": {
    "update_id": "upd_01HX…",
    "agent_id": "V6049A-EAS1",
    "state": "requested",
    "state_seq": 1,
    "channel": "http",
    "artifact": {"sha256": "9f2c…", "size": 13556224, "version": "1.19.0", "chunks": 52, "chunk_bytes": 262144},
    "lease": {"holder": "op_goran", "expires_at": "2026-11-23T13:05:00Z"},
    "watchdog": {"n_s": 90, "deadline_at": "2026-11-23T13:04:10Z"},
    "status_url": "/api/v1/agents/V6049A-EAS1/update/upd_01HX…",
    "poll_after_ms": 1000,
    "replayed": false,
    "resumed": false,
    "legacy_flow": false
  }
}
```

### 7.2 Status

```
GET /api/v1/agents/{id}/update/{update_id}          # one job
GET /api/v1/agents/{id}/update                      # latest job (list of 1, newest first)
```

```json
{
  "ok": true,
  "data": {
    "update_id": "upd_01HX…",
    "agent_id": "V6049A-EAS1",
    "state": "confirmed",
    "state_seq": 6,
    "updated_at": "2026-11-23T13:01:44Z",
    "started_at": "2026-11-23T12:59:58Z",
    "elapsed_ms": 106000,
    "channel": "http",
    "legacy_flow": false,
    "artifact": {"sha256": "9f2c…", "size": 13556224, "version": "1.19.0"},
    "versions": {"before": "1.18.6", "expected": "1.19.0", "observed": "1.19.0"},
    "pids": {"old": 41288, "canary": 41502, "new": 41502, "old_alive": false},
    "checks": {
      "a": {"ok": true, "sha_agent": "9f2c…", "sha_expected": "9f2c…", "version_observed": "1.19.0", "version_expected": "1.19.0", "source": "artifact", "at": "…"},
      "b": {"ok": true, "agent_id": "V6049A-EAS1", "name_registered": "V6049A-EAS1", "version_registered": "1.19.0", "canary_row_remaining": false, "at": "…"},
      "c": {"ok": true, "old_pid": 41288, "identity_exe": "C:\\ProgramData\\PROBEA\\probe.exe", "present": false, "at": "…"},
      "d": {"ok": true, "exit_code": 0, "stdout_marker": true, "request_id": "exec-…", "at": "…"}
    },
    "watchdog": {"n_s": 90, "deadline_at": "…", "fired": false, "branch": null},
    "failure": null,
    "rollback": {"available": false, "backup_path": "C:\\ProgramData\\PROBEA\\probe.exe.old", "backup_sha256": "1a77…", "action": "POST /api/v1/agents/{id}/update/{update_id}/rollback"},
    "journal_lines": 14
  }
}
```

**Poll cadence:** 1000 ms while non-terminal and `elapsed < 120 s`; **5000 ms** after that; stop on `confirmed | failed | rolled_back | rollback_failed`. Send `If-None-Match: "<state_seq>"` and accept `304` — 6 checks plus a poll is otherwise 6x the traffic for one click. The existing 5 s page poll (`AgentDetail.tsx:47`) may reuse the status route for a badge; the update tab uses its own faster poll.

### 7.3 Rollback (operator action, never automatic)

```
POST /api/v1/agents/{id}/update/{update_id}/rollback
{"reason": "smoke test failed", "relaunch": true}
```

`202` with the same job shape, `state:"rolled_back"` or `rollback_failed`; driven with the **existing** `internal/upgrade` kit (`ParkOld`/`RestoreOld`/`RestoreAndRelaunch`, `proc-start`), no new agent command.

### 7.4 Error codes

| HTTP | code | when | `detail` |
|---|---|---|---|
| 400 | `INVALID_PARAMS` | missing build_id+sha, bad timeout range, unknown channel | offending field |
| 401/403 | `PERMISSION_DENIED` | RBAC (`server_api.go:908` maps `update`) | required role |
| 404 | `NOT_FOUND` | agent or build or artifact absent | which |
| 409 | `LOCK_BUSY` | lease held | `holder`, `expires_at` |
| 409 | `UPDATE_IN_FLIGHT` | different artifact mid-flight | `current{update_id,sha,state}` |
| 409 | `UPDATE_PREVIOUSLY_FAILED` | retry without `new_attempt:true` | `retry_allowed:true` |
| 409 | `CAPABILITY_MISSING` | agent lacks `update`/`process` | `agent_id`, `ran:false`, `exit_code:null` (P8-02 §1.3) |
| 409 | `SERVER_BUILD_TOO_OLD` | artifact route absent (the §0.2 class) | `min_build` |
| 409 | `INTEGRITY_MISMATCH` | sha of the artifact ≠ record | expected, actual |
| 413 | `PAYLOAD_TOO_LARGE` | artifact > 64 MiB | `max_bytes`, `artifact_bytes` |
| 503 | `AGENT_UNAVAILABLE` | no live connection | — |
| 504 | `DEADLINE_EXCEEDED` | forwarding the command timed out **and** no job was created | `deadline_ms` |

The 200-means-not-run body of M14 is retired: a job exists ⇔ 202; no job ⇔ typed error.

---

## 8. UI contract

### 8.1 What AgentDetail shows

| region | before the update | during | after |
|---|---|---|---|
| version (conn bar) | `1.18.6` (existing `AgentDetail.tsx:106`) | `1.18.6 → 1.19.0` with the arrow dimmed until (a) passes | `1.19.0`, plus a `previous: 1.18.6` chip |
| state | `idle` | live stepper: requested → artifact-verified → canary-connected → promoted → old-killed → confirmed, each with its `at` and its driver label (server / old / canary) | terminal badge `confirmed` or `promoted_not_confirmed` in amber |
| PIDs | both PIDs as today under Processes | `old 41288` (struck through once (c) passes), `new 41502` | `new` only |
| four checks | — | four rows, green/red/unknown, each expandable to its evidence JSON (§7.2 `checks`) | four green rows are the success claim; anything less renders as "not confirmed" even at 6/6 states |
| failure reason | — | `failure.code` + message + `at` + the journal tail (last 5 lines) | — |
| rollback | — | disabled until `promoted`; enabled at `promoted_not_confirmed` and terminal states with `rollback.available` | button posts §7.3, shows the resulting state |
| watchdog | — | countdown to `watchdog.deadline_at` when `state == canary_connected` | `fired` branch if it fired |

### 8.2 Files that change

| file | change | size |
|---|---|---|
| `web/src/components/agent/UpdateTab.tsx` | **new** — stepper, four-check grid, PIDs, failure reason, rollback button, journal tail | ~260 lines |
| `web/src/pages/AgentDetail.tsx` | add `{ name: 'Update', icon: Rocket }` to the `tabs` array (`:17-28`), one render line beside `:81-89`, `agent_update` in the fetch (`:42`) | 3 edits |
| `web/src/api/client.ts` | `startAgentUpdate`, `getAgentUpdate`, `getLatestAgentUpdate`, `rollbackAgentUpdate` (beside `redeployAgent` at `:299`) | ~30 lines |
| `web/src/api/types.ts` | `AgentUpdateJob`, `UpdateChecks`, `UpdateArtifact`, `UpdateFailure` | ~35 lines |
| `web/src/pages/Agents.tsx` | one row action "Update…" opening a dialog (build chooser + channel), beside the existing capabilities editor (`:42`); state badge from the latest job | ~40 lines |
| `web/src/components/agent/ProcessesTab.tsx` | mark the superseded PID row (optional, cosmetic) | ~10 lines |

### 8.3 Verdict on the overhaul question

**ADDITIVE ONLY. The frontend does not need an overhaul.** Reasons, each checkable:

| reason | evidence |
|---|---|
| AgentDetail is already tab-driven; a new view is a new tab, not a new layout | `AgentDetail.tsx:17-28` (tab list), `:80-102` (render switch) |
| polling infrastructure exists and matches the contract's cadence | `AgentDetail.tsx:47` (5 s), `Agents.tsx:23` (5 s) |
| the API client is a flat method bag — four methods slot in | `client.ts:299` (`redeployAgent`) |
| the list page already hosts a per-agent action dialog | `Agents.tsx:40-42` (capabilities + redeploy) |
| no state management, no router change, no design-system change is implied | the job object is one more entity beside agents/builds |

What **would** force an overhaul (and is therefore explicitly out of scope): live push (SSE/WS) instead of polling, a global update queue across agents, or multi-tenant audit views. None are required by the owner's ask; if they arrive, they arrive as their own unit.

The honest cost: ~375 lines across 6 files, one new component. The dishonest alternative — rewriting 12 pages to accommodate one panel — buys nothing and violates the brief's prohibition.

---

## 9. Windows console (OD-1 governs)

### 9.1 The constraint that decides it

OD-1 ratifies that the elevated channel ships **via the service install**: the agent runs as `PROBEA` under SCM as LocalSystem (M20). A service runs in **session 0**, which is not the interactive window station — a console allotted from inside the service is invisible to the logged-in user, and `AttachConsole(ATTACH_PARENT_PROCESS)` from a service has no parent console. **Attach-console-to-service is rejected on the mechanism, not on taste.**

### 9.2 The design: two processes, one status surface

| piece | who | what it does |
|---|---|---|
| **status record** | the service (LocalSystem) | writes `C:\ProgramData\PROBEA\status.json` atomically (write `status.json.tmp`, `MoveFileEx` replace) every 5 s and on every command/update transition. Also mirrors `last_error` to the Windows Event Log source `PROBEA` |
| **status window** | a separate, non-elevated process — `probe.exe --status-window` (or `probe.exe --console` when running foreground) | a plain Win32 console (Go stdlib, no new deps, no GUI toolkit) that reads `status.json` every 1 s and renders. It never owns the service, never needs admin, and closing it cannot affect the agent |
| **service control** | `probe.exe --service-status --json` | machine-readable one-shot (extends the existing flag, M20) for scripts and for the status window's first paint |

Why not one process: a service that also owns a window dies with the window's message loop, and a console process cannot hold SCM state. Two processes, one file, is the junior-dev-in-10-minutes version.

### 9.3 What the window displays

| line | source field | why the operator needs it |
|---|---|---|
| `PID` | `pid` | the number they paste into `taskkill` and into the update job (§7.2 `pids.old`) |
| `VERSION (artifact)` | `version` + `artifact_sha256` | version **read from the artifact**, not from memory — the §2(a) distinction, visible on the host |
| `EXE` | `exe_path` | proves which file is canonical after a swap (`probe.exe` vs `probe.exe.old`) |
| `SERVER` / `STATE` | `server_url`, `connected` | is it actually connected or looping |
| `RECONNECTS` | `reconnect_count` | the flapping detector — a rising count during an update is the canary/old fight (§4.4) |
| `LAST CMD` | `last_command{type, at}` | shows the update command arriving and `proc_kill` following it |
| `LAST ERROR` | `last_error{code, message, at}` | the failure text without opening the Event Log |
| `UPDATE` | `update{update_id, state, state_seq}` | the six-state stepper in text form; blank when idle |
| `PRINCIPAL` | `principal{is_system, elevated}` | the OD-1 fact that decides whether a kill can succeed (§4.4) |

### 9.4 Files

| file | change |
|---|---|
| `cmd/probe/service_windows.go` | write the status record on a 5 s ticker + on transitions; `--service-status --json`; Event Log source |
| `cmd/probe/status_window.go` (new) | the console renderer, 1 s poll, `--status-window` flag |
| `cmd/probe/service_other.go` | stub refuses with a readable message (pattern already there, M20) |
| `internal/agent/status_record.go` (new) | the record type + atomic writer, shared by service, foreground agent, and window |
| `internal/agent/capabilities_elevation.go` | expose `principal{is_system, elevated}` into the record (the fields exist, P8-10 §2) |

### 9.5 Failure paths

| failure | behaviour |
|---|---|
| `status.json` unreadable/corrupt | window prints `status unavailable (last good 12s ago)` and keeps polling; it never blocks the service |
| disk full | the writer logs once per minute, keeps the previous record; the service does not fail on a status write |
| window closed | nothing happens to the agent — by construction |
| service stopped | window shows `SERVICE STOPPED` from `--service-status`, not a stale JSON |
| two windows open | both read the same file; no lock, no writer |

---

## 10. Backward compatibility — the bootstrap rides the OLD API

| surface | 1.18.6 agent | 1.19 agent | note |
|---|---|---|---|
| `AgentUpdate` command | handled (`agent_update.go` exists in 1.18.6 per P8-02 F13) | handled | no new command |
| params `download_url/filename/ws_path/sha256/version` | parsed, unknown fields ignored (M22) | same | **no new required field** |
| canary flow | **absent** — 1.18.6 renames `.old`, swaps, starts new, reports PID | full canary | job carries `legacy_flow:true`; S3 satisfied by "canonical-name connection with expected version"; S4 by `sha(canonical) == expected` |
| old kill | server `proc_kill` after reconnect (M12) | canary first, server backstop | same command, same params |
| `fs-hash` / `proc-list` / `exec` | present | present | the four checks use only these |
| `/api/v1/.../update` route | server-side only | server-side only | agent never calls it |
| legacy `/api/agent/{id}/update` | still routed (M2) | still routed | `push-agent-update.sh` keeps working |

**Consequence, stated plainly:** a 1.18.6 agent updated by this contract gets checks (a)-(d) but not the canary's 10 s prove, because the canary lives in the *new* binary — the first hop is exactly the old flow. That is acceptable and is why the first hop is the only one that can be un-proven; every subsequent hop has the canary. Job field `legacy_flow:true` makes this visible instead of implied.

---

## 11. Failure-path matrix (before the happy path, per G-GRACEFUL-FAILURE)

| # | failure | detected by | state | canonical binary | recovery |
|---|---|---|---|---|---|
| E1 | download 404 / HTTP 500 | agent (`agent_update.go:61-63`) | F1 `DOWNLOAD_FAILED` | untouched | retry; channel fallback if ws |
| E2 | sha mismatch | agent (`:90-93`) | F1 `INTEGRITY_MISMATCH` | untouched, `.tmp` removed | operator re-selects build |
| E3 | disk full during stage | agent `os.Create` / `io.Copy` (`:65-75`) | F1 `STAGE_IO_FAILED` | untouched | free space; **never** leaves a partial `.next` (tmp removed) |
| E4 | canary will not start (locked path, AV) | `StartCanary` error (`:133-136`) | F1 `CANARY_START_FAILED` + `.canary.log` tail | untouched, `.next` removed | read the log; the log is truncated per update by design (M7) |
| E5 | canary starts, never connects | watchdog branch (i) at 90 s | F1 `CANARY_NEVER_CONNECTED` | untouched (sha == self) | quarantine `.next`; retry |
| E6 | canary connects, dies during prove | `canary.go:50-53` | F1 | untouched | old keeps serving; retry |
| E7 | commit rename fails | `swapBinaryPaths` (`:167-172`) | F2 or F3 | restored, sha asserted | E7 with F3 = page someone: canonical may be missing, `.old` holds the only copy |
| E8 | crash between rename 2 and 3 | next start `RecoverCanonicalBinary`, or watchdog branch (ii) | F2 | restored from `.old` | automatic |
| E9 | promoted, kill denied | identity/`proc_kill` error | F4 `OLD_PID_ALIVE` | new | OD-1 migration, then repeat |
| E10 | promoted, new process cannot reclaim the name | check (b) false | F4 `NAME_NOT_RECLAIMED` | new | one reconnect cycle, then operator rollback |
| E11 | two operators | lease | 409 `LOCK_BUSY` | — | wait or `replace_lease` |
| E12 | server restarts mid-job | persisted job | resumes | unchanged | `resumed:true`; reconcile from journal |
| E13 | agent reconnects as zombie (old PID) | server accept filter (§4.4) | stays S4/S5 | new | connection refused, logged |
| E14 | `proc_kill` refused by the sandbox allowlist (registration missing) | `permission_denied` envelope (`agent_proc.go:44-52`) | F4 `OLD_PID_ALIVE` | new | register `CanaryOldPID` in `spawnedPIDs`; retry once |
| E15 | server dies after S4, before the kill order | 60 s `kill_grace` expiry in the new process | S5 via `kill_authority:"fallback"` | new | journal records the fallback; job still reaches `confirmed` with the distinction visible |

---

## 12. Decision log

| # | decision | alternative rejected | why | cost accepted |
|---|---|---|---|---|
| D-01 | `download_url` primary, `ws_path` fallback | ws-only (the P8-06 narrative) or both | 33% fewer bytes, no control-channel blocking, Range resume, independent deadline | agents behind HTTP-block must fall back — measured by attempting, §6.4 |
| D-02 | 256 KiB chunks, 64 MiB max | 1 MiB chunks; 512 MiB max | 256 KiB is the measured-safe unit; 64 MiB is 4.7× the real build and bounds staging memory | a >64 MiB build needs a knob change, not a code change |
| D-03 | watchdog N = **90 s**, owned by the old process | 30 s / 300 s; server-owned | 40 s connect + 50 s promote, no slack that hides a hang; the file owner must be the restorer | a slow disk >50 s to rename + hash a 13.5 MB file fails the update; knob `PROBE_UPDATE_WATCHDOG_S` |
| D-04 | **server issues** `proc_kill`; **new process executes**; 60 s fallback if the server is silent | canary self-kill (v1) or server-only kill without an executor | the owner's instruction is explicit ("proc_kill from the server … not self-kill before it"); execution must stay on the new process because it holds the connection and the inherited PID | one more round trip per update; a 60 s convergence window when the server is down |
| D-05 | three-branch watchdog (abandon / restore / escalate) | single "restore .old" branch | two of three cases have nothing to restore; restoring a successful promotion is a self-inflicted outage | one more journal event name |
| D-06 | idempotency by artifact sha | per-request nonce | "make these bytes run" is the operation; a nonce makes retries duplicate | two different builds = two jobs, correctly |
| D-07 | lease 300 s, persisted | in-memory lock | a server restart must not allow a double swap | stale claims need the reconciler |
| D-08 | 202 + job for start; typed errors otherwise | today's 200 `update_in_progress` (M14) | A-1: a 200 never means not-run | old callers of the legacy route keep the old shape (untouched) |
| D-09 | four checks from existing surfaces (`fs-hash`, `proc-list`, `exec`, registry) | new agent-side `update_status` command | the bootstrap may rely only on the old vocabulary | check (a) leans on the sha→version bridge (§2.2) |
| D-10 | `confirmed` ≠ `success:true` | trust `AgentUpdateResult` | success is set before promotion (M6) | one more round of server work per update |
| D-11 | frontend additive | overhaul | tab array + flat client + existing polls are the exact extension points | no live push; 1-5 s polling latency |
| D-12 | status window separate from the service; attach-console rejected | console allotted from the service | session 0 has no visible window station | one extra process to launch |

---

## 13. Future projections (forward-design, per G-SCENARIO-COVERAGE)

| horizon | what the system becomes | what this contract already carries | what will strain |
|---|---|---|---|
| **1 month** | 11 agents, mixed 1.18.6/1.19.0; operators update from the UI; valmet's three-hop hand-copy is gone | `legacy_flow` handles the mixed fleet; job persistence survives restarts; rollback is a button | the HTTP reachability guess (§6.4) will mispredict on the first genuinely firewalled node — expect one channel-fallback incident |
| **6 months** | scheduled fleet-wide rollouts; 40+ agents; artifact store grows; someone asks for "update all at 03:00" | the lease is per-agent, so N agents = N leases; the job object is already a queue element | no fleet-level coordinator is designed here. It is a **new** unit (queue, ordering, blast-radius caps) — do not grow it out of this contract |
| **1 year** | delta updates (binary diff), signature verification beyond sha256, staged rings (canary cohort → fleet) | `artifact.sha256` is an identity seam a signature can join; `channel` selects a leg; the four checks are ring-agnostic | 64 MiB max and whole-file replace will be the binding constraints; delta delivery needs a manifest, which is where `internal/upgrade/manifest.go` already points |

---

## 14. Open items (not designed here, not hidden)

| # | item | owner | blocks |
|---|---|---|---|
| O1 | OD-6 measurement — the field threshold on the real EAS1 host (10.7 MB must be re-measured post P8-06) | measurement unit | nothing in this contract; the numbers here are local measurements |
| O2 | Windows runtime leg (service install → restart → RDP logoff) remains UNVERIFIED-ON-HOST (M20) | whoever has a Windows host | §9 acceptance and E9's real behaviour |
| O3 | kill under mixed principals — untested; §4.4 is design, not evidence | coder + a Windows host with an interactive-run agent | E9 |
| O4 | HTTP reachability pre-flight (§6.4) is attempt-based; no cheap agent-side reachability probe exists without a new capability | coder | D-01's cost line |
| O5 | fleet-level rollout coordinator (6-month projection) | not this unit | — |

---

## 15. Acceptance quotes

| required | how |
|---|---|
| **1. state machine** | §1.1 nine-state table with driver + entry evidence per state; §1.2 legal-transition guards; §1.3 driver authority table |
| **2. four checks** | §2.1 (a) version from artifact / (b) same name / (c) old PID gone / (d) functional probe; §2.3 names the slogans they replace; "`confirmed` **is** these four" stated at the head of §2 |
| **3. watchdog number** | §3.1 `watchdog_n = 90 s`; §3.2 owner = old process, three branches; §3.3 exact journal line `event=WATCHDOG_TIMEOUT branch=… wait_s=90 …` |
| **4. kill-old semantics** | §4.1 rule (server authority / new-process execution, no self-kill); §4.2 four-row order table incl. the forbidden self-kill line and the 60 s fallback, plus the `spawnedPIDs` authorization (§0.4 X2); §4.3 three-layer identity guard built on wire-available evidence (§0.4 X1); §4.4 five failure cases incl. locked exe and access denied |
| **5. idempotency + concurrency** | §5.1 key = `sha256:<artifact>`; §5.2 lease TTL 300 s persisted; §5.3 seven-row behaviour matrix; §5.4 agent-side duplicate guard |
| **6. delivery channel** | §6.1 `download_url` primary with fallback; §6.3 chunk 256 KiB, max 64 MiB, 413 above it; §6.2 the byte arithmetic |
| **7. API surface** | §7.1 request + 202 body; §7.2 status fields + cadence (1 s → 5 s); §7.3 rollback; §7.4 thirteen error codes |
| **8. UI contract + overhaul verdict** | §8.1 seven regions before/during/after; §8.2 six files; §8.3 **ADDITIVE ONLY** with five reasons and the cost |
| **9. Windows console** | §9.1 attach-console rejected on session-0 mechanism; §9.2 service writes `status.json`, separate window reads it; §9.3 nine displayed fields |

---

## 16. Close record

| field | value |
|---|---|
| artefact | `/opt/data/tmp/probe119/P9-01_UPDATE_CONTRACT.md` (canonical copy committed at `.workflow/architect/P9-01_UPDATE_CONTRACT.md`) |
| version | v2 — §0.4 carries the four corrections (X1 wire-incomplete identity guard, X2 sandbox-blocked kill, X3 Windows probe command, X4 kill authority) |
| code changed | **none.** The closure commit adds this document only; `git status` clean at close |
| research leg | NONE — every claim is `file:line` in this tree or a live probe run this unit (§0.2 404 resolution: `strings` count 0 on the deployed binaries, route present at `api_v1.go:342`, commit `fa29134` 2026-11-23T07:41Z vs binary mtime 2026-11-22T22:35Z) |
| acceptance | all nine owner items answered; §15 maps each to its section |
| open items | O1-O5 (§14) — none blocks the coder's first unit; O2 (Windows runtime leg) blocks only §9 acceptance |
| reviewed_by | architect self-check v2; **independent reviewer pending** |
| verdict | **CONTRACT COMPLETE for coder dispatch on §2/§6/§7, frontend on §8 (additive), Windows console on §9.** The §4 kill path requires the `canary.go:203-205` self-kill removal and the `spawnedPIDs` registration before any field test |

*End of P9-01. No source file was modified. The closure commit adds this document only.*
