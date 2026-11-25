# P9-18 — THE KILL PROCEDURE (design; no code)

| field | value |
|---|---|
| unit | P9-18 · family P9-08 (kill/update path) · owner `architect=design` |
| tree | `/opt/data/projects/PROBE_UPGRADE/v119wt`, branch `r119-build`, head **`05ddd30b`** (P14A-03 close) |
| what this is | the operational procedure for terminating a process, specified end to end: components, interfaces, the step sequence, and the failure path of each step |
| what this is not | an implementation. No `.go` file is touched. It is the procedure that P9-08 §F1/F2/F3 + P9-15 review add up to, restated as something an operator and an implementer can both execute |
| grounding | every `file:line` below was re-read at `05ddd30b` in this session. P9-08_CONTRACT_V5.md is anchored at `70de7fa`; the only `.go` change between that head and this one is not assumed — the sites cited here were read live, not shifted |
| status | NOT RATIFIED. Ratification is Goran's |

## 0. Referent

"The killing" = **termination of one process on an agent host, as part of the update/promotion flow and as an operator action**. Three things share the word and they are one procedure: the canary killing its predecessor, the server backstop killing a superseded PID, and an operator killing a PID through `POST /api/v1/agents/{id}/proc-kill`. Machine processes only.

## 1. What exists at `05ddd30b` — three entrances, one executor, five defects

**Entrances.**
1. Canary, direct: `canary.go:216` → `stopProcess(a.cfg.CanaryOldPID)`, reached from `swapAndKillOld` (`:197`) ← `runCanaryCommit` (`:57`). No order, no grant, no identity.
2. Server backstop: `server_ws.go:162` spawns `completeUpdatePromotion` (`:47`), which sleeps 3 s (`:48`), forwards `proc_kill{pid, signal:9}` with a 30 s timeout (`:49-50`), logs the outcome, then purges the canary row **on both paths** (`:60`).
3. Operator: `server_api.go:724-745` — body is unmarshalled into `TaskStopParams` and forwarded verbatim (`:738`).

**Executor.** `agent_proc.go:36-76`, all three land here.

**Defects, each measured, each still live.**
- **D-A `Killed:true` is not a kill.** `:63` shells out to `kill -<sig> <pid>`; exit 0 means *the signal was delivered*, then `:75` returns `Killed: true` with no confirmation. A SIGKILL'd process that nobody reaped is still a PID; a SIGTERM'd process that ignored the signal is still running. Both read as success.
- **D-B every failure collapses to `ErrInternal`.** `:66-68`. `ESRCH` (already gone), `EPERM` (no rights), "binary missing" and "target ignored the signal" are one string, so no caller can choose between success, retry and page.
- **D-C authorization is an allow-list, not an identity.** The gate (`:44-52`, mirrored for legacy `task_stop` at `agent.go:1314`) accepts a PID present in `spawnedPIDs`, whose only insertion site is `proc_start` (`:105`). A PID is a recyclable integer; `:71-73` deletes the entry only on the success path, so a failed kill leaves a dead PID permanently authorised and a successful kill of a recycled PID removes the wrong entry.
- **D-D no identity on the wire.** `TaskStopParams` is `{PID, Signal}` only (`messages.go:817-820`); `killAuthorization`, `pidfd`, `StartTicks`, `ExeIno` have **zero hits** in the tree (`git grep` → 0). Nothing anywhere can tell "pid 41288" from "the process that held 41288".
- **D-E the order can be lost before it is executed.** `server_ws.go:149` deletes `pendingUpdates[agentID]` *before* the 3 s sleep and the kill; a server restart in that window loses the order forever, and `:60` then purges the canary row anyway, leaving a live predecessor with no registry row to point at it.

Note the two-writer overlap: entrances 1 and 2 can both target the same old PID. The second one gets `ESRCH` → `ErrInternal` → a "failed" log for a kill that already succeeded.

## 2. Components

| id | component | owns | may never |
|---|---|---|---|
| **K1** Issuer (server) | mints orders. Single writer: one place in the server can authorise a kill | the job, the target identity snapshot, the order id, the grant | signal anything |
| **K2** Grant (record) | `{order_id, source, job_id, pid, procIdentity, issued_at, expires_at, used}`; one live entry per PID at most | the proof that this kill was decided | outlive its TTL; be reused |
| **K3** Verifier (agent) | `killAuthValid(params)` — signature/source trust, unexpired, job scope (server_adopt only), identity equality, unused | the decision to proceed | proceed on any missing condition; ever read the grant as "allow any pid" |
| **K4** Executor (agent) | the actual signal: `pidfd_open` + `pidfd_send_signal` on Linux, `OpenProcess` handle + `TerminateProcess` on Windows | the binding between "the process I verified" and "the process I signalled" | use `exec kill`, a bare PID, or a name lookup |
| **K5** Confirmer (agent) | terminal state: `exited` / `absent` / `zombie` / `alive` / `unknown`, bounded grace | the truth of "it is dead" | report success on delivery |
| **K6** Journal (agent) | `<canonical>.update.journal`, append + fsync, one line per attempt | the audit trail | be the only evidence a state was reached (server keeps the job row) |
| **K7** Reaper (operator-initiated) | the only path allowed to kill without a live order, and only on identity proof | recovery from pre-grant-era leftovers | kill by PID alone |

Registry (server, not a component): gains a third state beside present/absent — **`suspect`** — for a row whose kill failed. Deletion is never the answer to an unconfirmed kill.

## 3. Interfaces

### 3.1 Order envelope (additive, backward compatible)

```go
type TaskStopParams struct {
    PID    int    `json:"pid"`
    Signal int    `json:"signal,omitempty"`
    JobID  string `json:"job_id,omitempty"`        // NEW: required only for source=server_adopt
    KillAuthorization *KillAuthorization `json:"kill_authorization,omitempty"` // NEW
}

type KillAuthorization struct {
    OrderID   string       `json:"order_id"`
    Source    string       `json:"source"`     // "canary_argv" | "server_adopt"
    JobID     string       `json:"job_id"`     // "" for canary_argv — never compared
    PID       int          `json:"pid"`
    Identity  ProcIdentity `json:"identity"`
    IssuedAt  int64        `json:"issued_at"`
    ExpiresAt int64        `json:"expires_at"`
}
```

A 1.18.6 agent ignores both fields (unknown JSON keys) and behaves exactly as today — that is the compatibility contract, and the legacy hop is the reason `server_adopt` exists at all.

### 3.2 Result

```json
{"killed": true, "pid": 41288, "terminal_state": "exited",
 "observed_identity": {"start_ticks": 918273, "exe_ino": 55123, "exe_dev": 2049},
 "signal": 9, "elapsed_ms": 412, "grant_state": "consumed"}
```

`killed` is true **only** for `terminal_state ∈ {exited, absent}`. `absent` is success: the goal is no process at that identity, not a signal sent.

### 3.3 Error taxonomy — each one a different operational action

| code | meaning | HTTP | caller does |
|---|---|---|---|
| `not_authorised` | no grant for this PID | 403 | fix the issuer; never retry blindly |
| `grant_expired` | TTL passed | 403 | re-mint once |
| `grant_consumed` | replay | 409 | read `terminal_state`; if exited/absent, treat as success |
| `job_mismatch` | grant is `server_adopt` and job ids differ | 409 | stop; wrong order for wrong job |
| `identity_changed` | the PID is not the process the grant names | 409 | **do not signal**; re-derive the target from proc-list |
| `target_gone` | no such PID | 200 `absent` | none |
| `permission_denied` | EPERM, or tier `read-only` | 403 | escalate privilege decision, not a retry |
| `still_alive_after_grace` | survived SIGKILL + grace | 504 | page; registry → `suspect` |
| `journal_unavailable` | the outcome cannot be recorded | 500 | do not report the state as reached |

### 3.4 Adopt (legacy hop) — `update_adopt`

Server → agent, only for agents advertising `update.adopt`:

```json
{"job_id":"upd_…","old_pid":41288,"expected_sha256":"9f2c…",
 "canonical_path":"/opt/probe/probe-client-linux","expires_in_s":300}
```

The agent registers a grant only if, on **its own** filesystem: `canonical_path == TrimSuffix(os.Executable(), ".next")`; `sha256(canonical_path) == expected_sha256`; `old_pid >= 2 && old_pid != os.Getpid()`; `identityOf(old_pid)` succeeds **and names that image**; no live entry already covers `old_pid` with a *different* identity. Otherwise `update_adopt_result{accepted:false, reason}` and nothing is registered. 10 s deadline server-side; timeout = no kill.

### 3.5 Journal lines (agent, `<canonical>.update.journal`, `ts=<unix int> EVENT k=v`)

`KILL_ORDER`, `KILL_GRANT_REFUSED reason=…`, `KILL_BOUND pidfd|handle`, `KILL_SIGNAL sig=… scope=group|pid`,
`KILL_CONFIRM state=exited|absent|zombie|alive elapsed_ms=…`, `KILL_ESCALATE`, `OLD_KILLED`, `KILL_ABANDONED reason=…`.

## 4. The procedure

Preconditions for every step: the target is named by **identity + PID**, never by name alone; the order carries a `job_id`; the issuer is K1.

| # | step | who | does | fails how → action |
|---|---|---|---|---|
| 1 | **Intent** | caller | states target: pid, job id, expected identity source (`update_result` / `proc-list-exe` / `canary_argv`) | no identity available → refuse at issue, do not guess |
| 2 | **Authorise** | K1 | mints order id + single-use grant bound to pid + identity + job; TTL 120 s; at most one live entry per PID | duplicate live grant with a different identity → drop the old, refuse, log `identity_changed` |
| 3 | **Deliver** | transport | sends the envelope | delivery failure is a **non-event**: an undelivered grant expires unused. Never retry a kill on transport error alone |
| 4 | **Filter** | K3 | five conditions, cheapest first: entry exists → unexpired → job scope (`server_adopt` only) → unused → identity re-read | each returns its own code; the target is **not touched** |
| 5 | **Bind** | K4 | `pidfd_open(pid)` / `OpenProcess`; re-read identity **through that handle** | open fails `ESRCH` → `target_gone`; identity differs → close handle, `identity_changed`. This is the authoritative read; step 4's is a filter |
| 6 | **Signal** | K4 | `pidfd_send_signal`/`TerminateProcess` through the bound handle, to the **process group** where the platform allows | `EPERM` → `permission_denied`; any other → typed, not `ErrInternal` |
| 7 | **Confirm** | K5 | waits on the pidfd/handle up to grace G, then classifies `exited`/`absent`/`zombie`/`alive` | `zombie` is **not** `exited` for routing purposes: the PID is still held; the parent must reap. Report it as its own state |
| 8 | **Escalate** | K4+K5 | `alive` after G → SIGKILL through the same handle → confirm again | still alive → `still_alive_after_grace`, page, row `suspect` |
| 9 | **Record** | K6 | one journal line, `fsync` before the result is sent; grant consumed whether or not the signal landed | journal fault → `journal_unavailable`, state not reported as reached |
| 10 | **Reconcile** | server | registry row from the **observed** outcome: `exited`/`absent` → remove; failed/unknown → `suspect`; `pendingUpdates` deleted only **after** an ack or a verified absence | never delete the pending record before the order is executed (`server_ws.go:149` today) |
| 11 | **Verify absence** | server | `proc-list`, match on identity fields, not the name | row present with our identity → `OLD_PID_ALIVE`; one retry, then escalate. Row present with a different identity → done, `identity_changed` recorded |
| 12 | **Close** | K1 | job terminal state, release the update lease, emit metrics | unclosed job → the duplicate rule returns 202 `replayed:true`, never a second kill |

Timeouts, fixed: grant TTL 120 s; adopt deadline 10 s; confirm grace G = 5 s (configurable `PROBE_KILL_GRACE_S`); escalation confirm 5 s; order forward 30 s (existing). One knob per number, no derived duplicates.

## 5. Failure modes (edge cases first)

| # | failure | caught by | today at `05ddd30b` |
|---|---|---|---|
| 1 | **PID recycled between verify and signal** — the classic | step 5 (handle) + step 4 (identity) | open: all three entrances signal a bare PID; `kill` binary adds its own exec window |
| 2 | **Target already exited, PID reused** — canary's worst case: 5 s sleep then unconditional `proc.Kill()` (`canary.go:242-245`) | step 5, step 7 | open; the sleep widens the reuse window to seconds by design |
| 3 | **Children survive** — SIGKILL to a PID leaves the tree | step 6 (group scope) | open; `taskkill /F` without `/T` (`agent_proc.go:57`) |
| 4 | **Two writers, one target** — canary direct and server backstop both kill the old PID | step 9/10 (`grant_consumed` → idempotent) | open: the loser logs a failure for a kill that succeeded |
| 5 | **Order lost before execution** — server restart in the 3 s window | step 10 (delete after ack) | open: `server_ws.go:149` deletes before the sleep |
| 6 | **Kill fails, row purged anyway** → live orphan with no registry row | step 10 (`suspect`, not delete) | open: `server_ws.go:60` purges on both paths |
| 7 | **`Bypass` envelope** turns the agent into a general-purpose killer for the whole command class | step 2 (every caller mints an order; `Bypass` off for this class) | open: `messages.go:225`, honoured at `agent_proc.go:44` |
| 8 | **EPERM / mixed principal** — agent is not the parent | step 4/5, typed | open: folded into `ErrInternal` at `:66-68` |
| 9 | **Grant replay** re-kills a future occupant of the same PID | single use at step 9 + identity at step 5 | n/a today (no grant exists) |
| 10 | **Stale allow-list** — `spawnedPIDs` entry outlives its process | step 10 + identity TTL | open: pruned only on success (`:71-73`) |
| 11 | **Zombie reported as dead** — PID still held, parent never reaps | step 7 (own state) | open: no confirmation at all |
| 12 | **Signal goes to the wrong thing because `kill` shells out** — the exec is the TOCTOU window | step 6 (syscall/handle only) | open: `exec.Command("kill", …)` `:63` |

## 6. Knobs and observability

Knobs: `PROBE_KILL_GRACE_S` (5), `PROBE_KILL_GRANT_TTL_S` (120), `PROBE_KILL_SCOPE` (`group`|`pid`, default group), `PROBE_KILL_REQUIRE_IDENTITY` (must never be false in production; refused to start if false while `permissions=standard`), `allow_bypass` (absent, permanently).

Metrics: one counter per error code (nine), histogram `kill_confirm_seconds`, gauge `kill_orders_outstanding`, counter `kill_suspect_rows`. Alerts: `still_alive_after_grace` (page), `suspect_rows > 0 for 10 min` (ticket), `journal_unavailable` (ticket).

The readout that matters: **terminal-state coverage** — of the last 100 kills, how many carry a confirmed `terminal_state`. A system that cannot print that number does not have a kill procedure.

## 7. Backward compatibility

- Wire fields are `omitempty` additions; a 1.18.6 agent ignores them and its behaviour is unchanged (still no confirmation — recorded as `legacy_flow:true` on the job).
- A 1.19 agent receiving an order **without** a grant behaves as today (allow-list), so a server that has not been upgraded cannot brick kills. An agent with a grant present but invalid refuses — fail closed on the new path only.
- `update_adopt` is capability-gated (`update.adopt`); agents without it never see the message.
- Registry `suspect` is additive; a UI that does not know the state must treat it as "present, unhealthy", never as absent.

## 8. Phasing (each phase ships alone)

**P0 — truth before authority (no wire change).** Replace the `kill`/`taskkill` shell-outs with `syscall.Kill` / `OpenProcess+TerminateProcess`; resolve SIGTERM→grace→SIGKILL→confirm; return `terminal_state`; type the errors; move `delete(pendingUpdates)` after the ack; stop purging the canary row on a failed kill (mark `suspect`); delete the direct `stopProcess` call from the canary path (`canary.go:216`) so one issuer exists. Cost: one agent release, no protocol change.

**P1 — authority.** `kill_authorization` + `killAuthValid` + two registration sites (canary argv, `update_adopt`); `job_id` on the wire; Linux first, Windows in the same release if a host is available, otherwise gated by capability.

**P2 — binding and audit.** `pidfd` on Linux, handle identity on Windows, process-group scope, journal writer, orphan reaper, operator UI states. After P2 the grant's step-4 read is a filter and step 5 is the only thing that decides.

## 9. Projections

**1 month.** P0 only. Kills become attributable: every order ends in a typed terminal state, the double-writer failure stops producing false failures, a failed kill stops erasing its own registry row. No new authority — a compromised operator API can still kill what the allow-list permits.

**6 months.** P1+P2 on Linux. One issuer, identity-bound grants, pidfd binding, group scope, journal with 100% terminal-state coverage; the reaper is the only order-less path and it is operator-initiated and logged. The fleet can answer "did any update leave an orphan?" per host.

**1 year.** Kills become reconcilable events: replay the journal against the registry and prove no orphan survived any update window — the property the current three-entrance design cannot state at all. Kill SLO (order→dead p99) becomes a monitored number instead of a hope.

## 10. Decisions owed (Goran, not me)

1. **Canary grants are job-exempt, in writing.** The argv carries no job id (`canary_launch.go:24-31` constructs `--canary --canary-old-pid n` only), so condition 3 must be `server_adopt`-only. Stated here as the binding reading; if you want it stricter, the argv has to carry a job id, which is a launcher change.
2. **Does `Bypass` survive at all?** With a grant mechanism it is either deleted or made to mint an order like any other caller. My recommendation: delete it — one fewer way to become a general-purpose killer.
3. **Grace G and escalation policy.** 5 s / SIGKILL is the proposal; a host with slow reapers may want 10 s and a `zombie`-aware wait.

## 11. Verifier appendix (re-runnable)

```
cd /opt/data/projects/PROBE_UPGRADE/v119wt
git rev-parse --short=8 HEAD                      # expect 05ddd30b
grep -rn "killAuthorization\|pidfd\|StartTicks\|ExeIno" --include=*.go .   # expect 0 today
grep -n "stopProcess(" internal/agent/canary.go   # expect :216 today; expect 0 after P0
grep -n "delete(s.pendingUpdates" internal/server/server_ws.go  # expect :149 before the ack today
grep -n "purgeCanaryRegistryRow(agentID)" internal/server/server_ws.go  # expect :60 on both paths today
sed -n '817,820p' internal/protocol/messages.go   # expect TaskStopParams{PID,Signal} only
go test ./internal/agent/ ./internal/server/ ./internal/protocol/ -count=1
```

A reviewer who cannot reproduce these six lines has not read the design's premises.

## 12. Decision log

| # | decision | because | cost accepted |
|---|---|---|---|
| D-01 | one issuer, grants not allow-lists | a PID is not an identity; `spawnedPIDs` proves parentage, not target | one new message type and a capability gate |
| D-02 | pidfd/handle binding, not a faster equality check | any check-then-signal pair has a TOCTOU window; binding removes it instead of narrowing it | Linux-only primitive; Windows needs the handle path |
| D-03 | `Killed:true` only after confirmation | delivery is not death; today's `:75` makes every caller's retry logic wrong | kill latency becomes G-bounded, not instantaneous |
| D-04 | registry `suspect`, never silent delete | an unconfirmed kill that deletes its row hides a live process | a state the UI must learn |
| D-05 | direct canary `stopProcess` deleted | two writers is not redundancy, it is two failure reports for one event | the canary depends on the server order landing |
| D-06 | typed error taxonomy | nine conditions, nine operator actions; `ErrInternal` collapses them into one useless string | more codes to keep in sync with the client |
| D-07 | grace and escalation as knobs | reaper behaviour varies by host; a constant would be a guess | two more numbers to validate |
| D-08 | `absent` is success | the goal is no process at that identity, not a delivered signal | callers must treat a 200 without a signal as a pass |

*No source file was modified. P9-08_CONTRACT_V5.md is untouched — it is under review, and mutating a graded artefact would void the grade.*
