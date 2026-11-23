# Agent update — operator procedure

How to push a new agent binary to a connected host through the probe HTTP API, what
you can observe while it runs, how to read what happened afterwards, and how to get
back to the previous binary.

This document describes the code that exists at HEAD. It does not describe the
update contract's target design except in the clearly separated section
"Contract-only, not implemented".

## Status of this document

| fact | value |
|---|---|
| tree read | `/opt/data/projects/PROBE_UPGRADE/v119wt`, branch `r119-build` |
| commit read | `d32547aeacd10f07f442544515c96998dfc5b61b` |
| unit head named in the brief | `867b0e9`; verified as the parent of the commit above. `git diff --stat 867b0e9 d32547a -- '*.go'` is empty (the only delta is `reviews/P9-12_REVIEW.md`), so every `file:line` below resolves identically at both commits. |
| research leg | none — every statement comes from the repository at the commit above |
| contract state | `P9-01_UPDATE_CONTRACT.md` v2 is committed; v3 (`P9-08_CONTRACT_V3.md`) is **NOT RATIFIED**. No contract text is treated as normative here. |

Three-way boundary, stated up front so no reader mistakes one lane for another:

| lane | what is in it |
|---|---|
| **Implemented at HEAD** | `POST /api/v1/agents/{id}/update` (and the legacy `/api/agent/{id}/update`); three artifact source modes; the managed-artifacts-directory gate; sha256 rules; the 30 s forward; the `update_in_progress` response; the agent-side `.next` stage, canary prove, swap, and self-stop; the server-side backstop kill and canary-row purge; artifact retention pinning. Everything in sections 1–6 below. |
| **Contract-only, awaiting ratification** | a job object and `update_id`; `GET .../update/{update_id}` status; the S0–S6 state machine and the four confirmation checks; the watchdog; the update lease / idempotency key; the typed 409 dialect; `POST .../update/{update_id}/rollback`; a journal writer and `<canonical>.update.journal`; `kill_authorization` / `update_adopt`; the swap lease and epoch; a TTL on the pending-update record. See "Contract-only, not implemented". |
| **Never executed on a live Windows host** | the entire flow. The agent-side code paths are covered by Go tests only. The 1.18.6 legacy hop has never been run end to end. |

## 1. Start an update

### 1.1 Endpoint and authentication

```
POST /api/v1/agents/{id}/update
Content-Type: application/json
```

Registered at `internal/server/api_v1.go:386-387`. The legacy path
`POST /api/agent/{id}/update` maps to the same handler (`internal/server/server_update.go:60`,
`internal/server/server_api.go:113`).

The v1 route authenticates through `v1CheckAuth` with the RBAC action `update`
(`internal/server/api_v1.go:154-157`); the agent command name `agent_update` maps to
that action (`internal/server/enrollment.go:463-464`). A failed auth or role check
returns 401/403 before the handler runs.

### 1.2 Request body

Fields, exactly as decoded (`internal/server/server_update.go:78-86`):

| field | type | meaning |
|---|---|---|
| `build_id` | string | A completed build. The server resolves its artifact and copies it into the managed artifacts directory. |
| `binary_path` | string | A file **already inside** the managed artifacts directory: either a bare file name or an absolute path under it. |
| `version` | string | Version label sent to the agent and reported back by the new process. Not validated against the binary. |
| `download_host` | string | Overrides the host in the download URL (e.g. `187.124.31.229:80`). |
| `ws_path` | string | Declared in the struct but **not read** by the handler; the WS-staged location is `ws_remote_path`. |
| `ws_remote_path` | string | Path on the agent where a binary was already staged over the WS tunnel. The server does not fetch or hash it. |
| `sha256` | string | Required with `ws_remote_path`. Optional with `binary_path`; if present it must equal the server-computed digest. |

Rules the handler enforces, in order:

1. At least one of `build_id`, `binary_path`, `ws_remote_path` is required, else
   `400` with `one of build_id, binary_path or ws_remote_path is required`
   (`internal/server/server_update.go:91-93`).
2. `build_id` resolves to a build record: unknown id → `404` (`:101-104`); build not
   in `complete` state → `409` (`:106-109`); build with no binary path → `404`
   (`:110-113`). On success the build artifact is copied into the managed directory
   and `binary_path` is set to that staged path (`:114-119`). Supplying `build_id`
   together with `binary_path` therefore makes `build_id` win — do not combine them.
3. A `binary_path` outside the managed directory is refused with `400` and a message
   naming the repair (`internal/server/server_update.go:126-130`, message built at
   `internal/server/artifacts.go:181`).
4. With `ws_remote_path` and no `binary_path`, `sha256` is mandatory → `400`
   `sha256 is required with ws_remote_path` (`internal/server/server_update.go:141-147`). The
   supplied digest is passed through; the agent verifies it against the file at that
   path.
5. With `binary_path`, the server hashes the file itself (`:148-154`). If the caller
   also supplied `sha256` and it disagrees, `400` `sha256 mismatch: supplied …,
   computed …` (`:158-161`) — never a silent install of different bytes.
6. The artifact is copied to `<managed dir>/<basename>` if it is not already there
   (`:173-180`). **The file name is the basename**, so two artifacts with the same
   name occupy the same slot; the newest copy wins.
7. The delivered file is registered for retention and pinned as in-flight
   (`:185-193`). The managed directory defaults to `/data/files`
   (`internal/server/artifacts.go:34`) and keeps the newest 5 artifacts per
   `agent|name` (`internal/server/artifacts.go:37`).

### 1.3 Idempotency and concurrency — read this before scripting

At HEAD there is **no idempotency key and no one-in-flight guard on this route**.
The request struct has no `idempotency_key` field (`internal/server/server_update.go:78-86`), no
lease is taken, and the pending-update map is simply overwritten
(`internal/server/server_update.go:288-295`). Two concurrent POSTs for the same agent are both
forwarded to the agent.

The agent side does remove an existing `<canonical>.next` before staging
(`internal/agent/agent_update.go:103`), so a repeated command replaces a canary that
may be mid-prove.

Operationally: serialize updates per agent yourself (one operator, one agent, one
call at a time) until the contract's lease is implemented.

### 1.4 What the server does with the request

1. Resolves and hashes the artifact (step 4/5 above).
2. Builds a download URL `http://<host>/download/<name>?token=<token>`
   (`internal/server/server_update.go:208`); `host` is `download_host`, else the request `Host`,
   else the server's own address (`:198-207`). Plain HTTP, token in the query string.
3. In `ws_remote_path` mode the download URL is deliberately blanked, which tells the
   agent not to fetch over HTTP (`:219-222`).
4. Purges any stale `<agentID>-canary` registry row left by an earlier attempt
   (`:227`, helper at `:44-57`).
5. Sends `agent_update` to the connected agent and waits up to **30 s**
   (`:243`).

The agent then: downloads (or uses the pre-staged file), verifies sha256, stages the
binary as `<canonical>.next`, and launches it as a canary with
`--canary --canary-old-pid <old pid>` (`internal/agent/agent_update.go:83-95`,
`:103`, `:132`; argv at `internal/agent/canary_launch.go:25-32`). The canary connects
under `<name>-canary` (`cmd/probe/connect.go:109`,
`internal/agent/canary.go:26`), proves the connection for **10 s**
(`internal/agent/canary.go:19`, `internal/agent/canary.go:33-53`), then archives any previous backup, renames
`<canonical>` → `<canonical>.old`, renames `<canonical>.next` → `<canonical>`, and
stops the old PID itself (`internal/agent/canary.go:141-183`, `internal/agent/canary.go:186-209`, `internal/agent/canary.go:205`). If the prove
window fails or the connection drops, the canary aborts and the old process keeps
running (`internal/agent/canary.go:33-60`, `internal/agent/agent.go:356-361`).

### 1.5 What the call returns

On an agent reply, the handler returns `200` with the agent's result body
(`internal/server/server_update.go:258-259`).

On **any** error from the forward — agent not connected, capability refusal, send
failure, or the 30 s timeout — the handler returns `200` with:

```json
{"status":"update_in_progress",
 "message":"agent_update command sent. The agent will download and restart. Check /api/agents for the new version.",
 "version":"<the version you sent>"}
```

(`internal/server/server_update.go:244-256`; the not-connected error originates at
`internal/server/server_api.go:784-786`).

**A 200 does not mean the command was delivered.** The only way to tell is to watch
the agent row (section 2). This is the single most important trap in the current
implementation: a dead agent and a healthy agent return the same body.

## 2. Observing an update — there is no status endpoint

No `GET /api/v1/agents/{id}/update` or `.../update/{update_id}` route is registered
at HEAD. The only update route in the mux is the POST at `internal/server/api_v1.go:386-387`. A
request to a status URL returns the router's 404.

What you can actually poll:

| observation | how | what it means |
|---|---|---|
| agent version | `GET /api/v1/agents/{id}` → registry record including `version` (`internal/server/api_v1.go:549-559`; field at `internal/server/registry.go:36`) | version still the old one = the new process has not registered yet |
| a second row | `GET /api/v1/agents` while an update runs | `<name>-canary` present means the new binary is alive and proving (`internal/server/server_ws.go:107` registers whatever name connects) |
| canary gone | same list | the canary promoted (it strips its suffix, `internal/agent/canary.go:71`) or died |
| stale canary row | same list | should be purged at the next update start (`internal/server/server_update.go:227`) and after a server-ordered kill (`internal/server/server_ws.go:147`) |

A successful update looks like: the agent row's `version` becomes the version you
sent, exactly one row exists for that agent (no `-canary`), and the host's
`<canonical>` sha256 equals the artifact you sent.

### 2.1 Server-side backstop kill

When the agent reports success it includes the old PID. The server records it
(`internal/server/server_update.go:265-298`). When that agent ID connects again, the server sleeps
3 s and sends `proc_kill{PID: old_pid, Signal: 9}` to the new process
(`internal/server/server_ws.go:120-149`), then purges the canary row (`:147`).
This is a backstop: the canary has normally stopped the old process itself
(`internal/agent/canary.go:205`).

## 3. Reading the record afterwards

### 3.1 The journal is not produced at HEAD

`internal/upgrade` contains the journal *format* and *reader*: `FormatLine`
(`internal/upgrade/journal.go:67`), `ParseJournal` (`:110-147`), `Analyze`
(`:170-205`), and the event vocabulary (`:33-52`). It contains **no writer**, no
`.update.journal` path constant, and **no importer anywhere in the tree** (a grep for
`internal/upgrade` outside that package returns nothing).

Consequence: no journal file is written by an update today. Do not go looking for
one; there is nothing to read.

What is written, and where:

| record | location | content |
|---|---|---|
| server log | `[update]` lines: send (`internal/server/server_update.go:233-234`), agent result (`internal/server/server_update.go:271-272`), old PID recorded (`internal/server/server_update.go:297`) | one line per transition the server knows about |
| canary log | `<canonical>.canary.log` beside the binary (`internal/agent/canary_launch.go:35-45`) | the new process's stdout/stderr; **truncated at every update start** (`internal/agent/canary_launch.go:45`), so it describes only the latest attempt |
| server audit | audit entry per forwarded command (`internal/server/server_api.go:778-860`, `auditLog` calls at `:846` and `:860`) | operator id, params, success/error, duration |
| artifact ledger | `<managed dir>/.artifacts.json` (`internal/server/artifacts.go:40`) | retention history of delivered files |

If you need a durable transition trail, capture the server log and the canary log
before the next update truncates anything.

### 3.2 Contract-only journal

The v3 contract specifies a writer, `<canonical>.update.journal`,
`ts=<unix> EVENT k=v` lines and fsync-before-report. None of that exists at HEAD.
The format is stable enough that an operator can parse an existing file with
`ParseJournal`'s rules (`ts` is Unix seconds, `internal/upgrade/journal.go:126`), but no file is
produced by the current code.

## 4. Rollback

There is **no rollback endpoint** at HEAD. The only route is `POST .../update`.

### 4.1 What already exists on the host

Every successful swap leaves the previous binary in a fixed slot and archives older
generations beside it (`internal/agent/canary.go:84-118`):

```
<canonical>              current binary
<canonical>.old          the immediately previous binary
<canonical>.old-<sha8>   earlier generations, one per previous update
```

`archiveExistingBackup` moves the existing `.old` to `.old-<sha8 of its contents>`
before reusing the slot, and refuses to overwrite an archive holding different bytes
(`internal/agent/canary.go:97-118`).

### 4.2 Two ways back

**A. Re-push the previous artifact through the API.** Upload the old binary into the
managed directory (`POST /api/v1/files`), then call the update endpoint with
`binary_path` (or `build_id`) and, recommended, `sha256` so a mismatch fails loudly
(`internal/server/server_update.go:158-161`). Note the basename collision: the old file must not
overwrite the artifact you are rolling back from until you have decided to keep the
old copy elsewhere.

**B. On the host, restore the backup slot.** Stop the agent, copy
`<canonical>.old` (or a chosen `.old-<sha8>`) over `<canonical>`, start the agent,
then verify sha256 of the restored file against the digest you expect.

### 4.3 What "rolled back" is expected to look like

- `<canonical>` sha256 equals the pre-update digest, verified — not assumed.
- The failed candidate sits in `<canonical>.old` with its own digest.
- The agent registers the previous version on `GET /api/v1/agents/{id}`.
- No `<name>-canary` row remains.

The code already has a verified-restore primitive: `restoreBackup` renames the backup
back and refuses to report success unless the restored sha matches the expected
pre-update sha (`internal/agent/canary.go:126-139`). At HEAD that primitive is invoked
automatically only when the second rename of a swap fails
(`internal/agent/canary.go:167-178`) — it is not an operator-callable rollback.

## 5. Error dialect — what each status means to an operator

Two body shapes exist on this route: `http.Error` writes **plain text**; the
managed-path refusal writes the typed `{code,message}` body. The v1 wrapper passes
both through (`internal/server/api_v1.go:186-210`).

| status | source | message / code | operator meaning |
|---|---|---|---|
| 400 | `internal/server/server_update.go:88` | `invalid JSON: …` | body malformed; nothing was sent |
| 400 | `internal/server/server_update.go:92` | `one of build_id, binary_path or ws_remote_path is required` | pick a source |
| 400 | `internal/server/server_update.go:128` | `BAD_REQUEST` + path message | `binary_path` is outside the managed directory; upload to `POST /api/v1/files` or use `build_id` (`internal/server/artifacts.go:181`) |
| 400 | `internal/server/server_update.go:144` | `sha256 is required with ws_remote_path` | supply the digest you staged |
| 400 | `internal/server/server_update.go:159` | `sha256 mismatch: supplied …, computed …` | your bytes are not the bytes you claimed; re-upload |
| 404 | `internal/server/server_update.go:103` | `build <id> not found` | wrong build id |
| 404 | `internal/server/server_update.go:111` | `build <id> has no binary path` | build record has no artifact |
| 409 | `internal/server/server_update.go:107` | `build <id> is not complete (status …)` | wait for the build, or use another source |
| 500 | `internal/server/server_update.go:116` | `stage build artifact: …` | server-side copy of the build output failed |
| 500 | `internal/server/server_update.go:151` | `hash binary failed: …` | artifact unreadable on the server |
| 500 | `internal/server/server_update.go:177` | `copy to download dir failed: …` | managed directory unwritable/full |
| 200 | `internal/server/server_update.go:249-255` | `status: update_in_progress` | **delivery unknown.** Includes agent not connected and timeout |
| 401/403 | `internal/server/api_v1.go:154-159` | auth/RBAC refusal | credential or role problem; handler never ran |

A `409 CAPABILITY_MISSING` rewrite exists in the v1 wrapper
(`internal/server/api_v1.go:174-182`), but it fires only when the handler returns ≥400 with the text
`lacks required capability`. The update handler converts every forward error —
capability refusal included — into `200 update_in_progress` (`internal/server/server_update.go:244-256`),
so on this route that branch is unreachable at HEAD. An agent that does not advertise
`update` looks identical to one that accepted the command.

## 6. Operator checklist (current behaviour)

1. Confirm the target row exists and is connected: `GET /api/v1/agents/{id}`.
2. Put the artifact where the server can see it: `POST /api/v1/files`, or use a
   completed `build_id`.
3. Know the sha256 of the bytes you are sending; pass it if the mode requires it.
4. Serialize: one update per agent at a time — no server-side guard exists.
5. POST the update. Treat `200 update_in_progress` as "request accepted by the
   server", never as "delivered".
6. Poll `GET /api/v1/agents/{id}` (and the agent list for a `-canary` row) until the
   version changes or the update is clearly abandoned.
7. Capture `[update]` server log lines and `<canonical>.canary.log` before retrying —
   the canary log is truncated by the next attempt.
8. To go back: section 4.2. Verify the restored sha yourself.

## 7. Contract-only, not implemented

Listed so nobody scripts against it. These are design text in
`.workflow/architect/P9-01_UPDATE_CONTRACT.md` (v2) and
`/opt/data/tmp/probe119/P9-08_CONTRACT_V3.md` (v3, **not ratified**); none of it is
behaviour at HEAD.

| item | state |
|---|---|
| `update_id`, job object, `state`/`state_seq` | not implemented — no job store, no id |
| `GET /api/v1/agents/{id}/update[/{update_id}]` | not implemented — route absent |
| S0–S6 state machine, four checks (a)–(d) | not implemented |
| watchdog (`PROBE_UPDATE_WATCHDOG_S`) | not implemented |
| update lease / `idempotency_key` / `new_attempt` | not implemented |
| typed 409 dialect (`IDEMPOTENCY_IN_FLIGHT`, `IDEMPOTENCY_STATE_UNKNOWN`, `UPDATE_PREVIOUSLY_FAILED`) | not implemented |
| `POST /api/v1/agents/{id}/update/{update_id}/rollback` | not implemented |
| journal writer, `<canonical>.update.journal`, fsync-before-report | not implemented (format/parser only) |
| `kill_authorization`, `update_adopt`, TTL on `pendingUpdate` | not implemented |
| swap lease + epoch (`<canonical>.swap.lock`, `.swap.epoch`) | not implemented |
| `202 Accepted` with a job body | not implemented — the route returns 200 |

Contract v3 additionally closed six review findings F1–F6 on paper. That
remediation is not ratified, and this document takes no position on it.

## 8. What has never been executed

- No step in this document has been run against a live Windows host.
- The 1.18.6 → 1.19 legacy hop, which the contract describes as the only hop that
  cannot use the canary, has never been run end to end.
- The canary prove/swap/self-stop sequence is exercised by Go tests only
  (`internal/agent/canary_swap_test.go`, `internal/server/p908_update_hardening_test.go`).

## 9. Citation spot-check

Every row re-opened at `d32547a`; the quote is the cited line.

| # | `file:line` | quoted line | supports |
|---|---|---|---|
| 1 | `internal/server/api_v1.go:386` | `s.mux.HandleFunc("POST /api/v1/agents/{id}/update",` | the only update route |
| 2 | `internal/server/server_api.go:113` | `s.handleAgentUpdate(w, r, agentID)` | legacy path shares the handler |
| 3 | `internal/server/server_update.go:91` | `if params.BinaryPath == "" && params.WSRemotePath == "" && params.BuildID == "" {` | source is mandatory |
| 4 | `internal/server/server_update.go:107` | `http.Error(w, fmt.Sprintf("build %s is not complete (status %s)", params.BuildID, build.Status), http.StatusConflict)` | 409 for incomplete build |
| 5 | `internal/server/server_update.go:128` | `writeError(w, http.StatusBadRequest, "BAD_REQUEST", rerr.Error())` | managed-dir refusal is 400 |
| 6 | `internal/server/server_update.go:144` | `http.Error(w, "sha256 is required with ws_remote_path", http.StatusBadRequest)` | ws mode needs a digest |
| 7 | `internal/server/server_update.go:159` | `http.Error(w, fmt.Sprintf("sha256 mismatch: supplied %s, computed %s", supplied, computed), http.StatusBadRequest)` | mismatch is 400 |
| 8 | `internal/server/server_update.go:243` | `resp, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeAgentUpdate, updateParams, 30*time.Second, operatorIDFromRequest(r))` | 30 s forward |
| 9 | `internal/server/server_update.go:251` | `"status":  "update_in_progress",` | 200 on send failure |
| 10 | `internal/server/server_ws.go:137` | `time.Sleep(3 * time.Second) // give new agent time to stabilize` | backstop kill timing |
| 11 | `internal/server/server_ws.go:138` | `killParams := protocol.TaskStopParams{PID: oldPID, Signal: 9}` | backstop kill params |
| 12 | `internal/server/artifacts.go:34` | `DefaultFilesDir = "/data/files"` | managed directory default |
| 13 | `internal/server/artifacts.go:181` | `return "", fmt.Errorf("binary_path %q is outside the managed artifacts directory %s — upload it with POST /api/v1/files or use build_id", p, dir)` | remediation text |
| 14 | `internal/agent/agent_update.go:132` | `newPID, canaryLog, err := StartCanary(newExePath, currentExe, configPath, oldPID)` | agent launches a canary |
| 15 | `internal/agent/canary.go:19` | `const canaryProveInterval = 10 * time.Second` | prove window |
| 16 | `internal/agent/canary.go:205` | `stopProcess(a.cfg.CanaryOldPID)` | canary stops the old process itself |
| 17 | `internal/agent/canary.go:126` | `func restoreBackup(backup, canonical, expectedSHA string) error {` | verified restore primitive exists |
| 18 | `internal/upgrade/journal.go:67` | `func FormatLine(now time.Time, name string, kv ...string) string {` | journal format exists |
| 19 | `internal/upgrade/journal.go:126` | `if ts, err := strconv.ParseInt(strings.TrimPrefix(tokens[0], "ts="), 10, 64); err == nil {` | `ts` is Unix seconds |
| 20 | `internal/server/registry.go:36` | ``Version       string        `json:"version"` `` | the pollable version field |
