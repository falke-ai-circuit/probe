# P10-01 — session context contract (`session` selector · `sessions` · `ui-tree` · `ui-window`)

| field | value |
|---|---|
| unit | P10-01 · campaign probe v1.21 · design gate (no implementation) |
| owner | architect (profile `architect`, gateway api_server) |
| worktree | `/opt/data/projects/PROBE_UPGRADE/v119wt` · branch `r119-build` |
| base | `85853f3` (P9-20 follow-up: `.probe-token` ignored) |
| contract | this file, v1 — **RATIFY BEFORE ANY CODER DISPATCH** (valmet/Goran) |
| source of requirements | `/opt/data/projects/PROBE_UPGRADE/inputs/PROBE_SPEC_ORCH_v1.21.md` §P0.1 (l.29-53), §P0.2 (l.55-77) |
| research leg | NONE — the operator's measurements are the research (§1) |
| unit cap | 50 min |
| status | CONTRACT — no handler edited, no coder dispatched, no new lane/knob |

## 0. Verdict

Ship the design below as v1.21 P0. `session` is a **verified selector, never an
escalation primitive, never a silent fallback**. The mechanism is the P8-03 shape
(declaration + typed refusal + capability-gated pre-dispatch check) applied to
sessions, with one addition P8-03 did not need: a **session worker** process,
because on Windows you cannot address another session's windows, input or screen
from inside your own — the work must run *in* the target session. That worker is
the reason this contract is longer than P0.1's three JSON lines suggest.

## 1. Substrate (read at base `85853f3`; not re-derived from memory)

| Fact | Evidence |
|---|---|
| `session` absent from the protocol | `internal/protocol/messages.go` — 0 matches |
| `ui-tree` / `ui-window` absent | repo-wide 0 matches (non-test) |
| `InputParams` is X,Y,Button,Text,Key,Keys only | `messages.go:557-564` |
| capture is full-screen only, no params struct | `CaptureResult` `messages.go:467`; `windowsPlatform.CaptureDisplay` `platform_windows.go:310` (GetSystemMetrics(SM_CXSCREEN)+GetDC(0)) |
| input routes are 4 | `api_v1.go:489-492` — pointer-click, key-press, key-combo, text-input |
| click is absolute physical pixels, no window context | `platform_windows.go:430-454` (`SetCursorPos(x,y)` then `mouse_event`) — this is why a covered point still answers `success:true` |
| typed-refusal precedent | `ELEVATION_UNAVAILABLE` `messages.go:193`, `UNSUPPORTED_AGENT` `:195`, `PLATFORM_NOT_SUPPORTED` `:181`, `ErrorInfo.Detail` `:215` |
| capability gate already exists | `capabilityForCommand` `enrollment.go:414-435`, `CheckAgentCapability` `:533`; server pre-dispatch check `server_api.go:789-797` |
| response envelope is `{ok,data}` | `APIResponse` `api_v1.go:44` |
| audit entry is flat and extensible | `audit.go:14-29` (flow-specific fields were added the same way) |
| RBAC scopes in use | `operator.go:22-33` (viewer: list/health/fs-*), `v1CheckAuth` `api_v1.go:87` |
| Windows APIs needed are **already vendored** | `golang.org/x/sys v0.20.0` (`go.mod:7`): `WTSEnumerateSessions` + `WTSQueryUserToken` `vendor/.../security_windows.go:887-888`, `WTS_SESSION_INFO{SessionID,WindowStationName,State}` `:881`, `CreateProcessAsUser` `syscall_windows.go:207`, `ProcessIdToSessionId` `:303`, `WTSActive..WTSInit` `security_windows.go:864-873` |
| **not** vendored → declare locally, no new module | `WTSQuerySessionInformationW` (0 matches in vendor) and the `WTS_INFO_CLASS` constants: `WTSUserName=5, WTSDomainName=7, WTSConnectState=8, WTSClientName=10, WTSClientAddress=14, WTSClientProtocolType=16, WTSIsRemoteSession=29` — 15 lines of `syscall.NewLazyDLL("wtsapi32.dll")` |

**No new dependency, no new artefact to deploy.** The worker is the same
`probe-client.exe` with a hidden `--session-worker` flag (distribution discipline
of P2.2/rollback: one binary, one digest).

## 2. Measured blockers → design consequence

| Operator measurement (spec l.36-38) | What it means | What this contract does |
|---|---|---|
| agent in session 0 → EnumWindows 0 visible windows | a SYSTEM service's window station is session 0; the HMI is not there | every UI/input op runs **in the resolved session**, via a worker |
| agent in session 7 → 16 windows incl. HMI | the same binary asked from the right session sees everything | `session:7` selects it — provided the agent is allowed to enter |
| Control Panel `0,0 1038x118` under page window `-7,0 742x536`, click answered `success:true` | absolute clicks have no window identity; success ≠ effect | `ui-tree` gives hwnd+rect; input takes `hwnd` + window-relative coords resolved **at click time**, with an occlusion hit-test that refuses instead of clicking |

## 3. `session` selector semantics

### 3.1 Type and absence

```go
// one field, added to ExecParams and InputParams and their params-only siblings
Session *int `json:"session,omitempty"`  // pointer: 0 is a legal session id
```

- **absent** → the agent's own session. This is today's behaviour, unchanged
  (for the LocalSystem service that is session 0 — 0 windows; documented, not
  a bug). The result still echoes `session_context.source:"agent_own"` so the
  caller always knows where the action happened.
- **present** → must be verified (§3.2) and either resolved exactly to that id
  or refused (§3.4). There is no third outcome.
- **range** → `0..65535`; anything else is `400 INVALID_PARAMS` with
  `detail.field:"session"`, before any syscall.
- **detach + session** → `400 INVALID_PARAMS`, `detail.reason:"DETACH_WITH_SESSION_UNSUPPORTED"`
  (the guardian lives outside the session; refusing is honest — OD-3).

### 3.2 Verification ladder (all six steps, in order, before anything is attempted)

| # | Step | Call | Failure code |
|---|---|---|---|
| V1 | session exists | `WTSEnumerateSessions(0,0,1)` → match `SessionID` | `SESSION_UNAVAILABLE` `NOT_FOUND` |
| V2 | session is interactive | `State == WTSActive` (0). `WTSConnected` (1), `WTSDisconnected` (4), `WTSListen` (6)… are refused | `NOT_INTERACTIVE` |
| V3 | a user is logged on | `WTSQuerySessionInformationW(WTSUserName)` non-empty | `NO_USER` |
| V4 | this agent may address that user | §9 predicate: SYSTEM → any; non-SYSTEM → only its own session id and its own user SID | `OTHER_USER` / `PRIVILEGE_NOT_HELD` |
| V5 | the token is obtainable | `WTSQueryUserToken(sessionID)` (`ERROR_PRIVILEGE_NOT_HELD` = 1314) | `PRIVILEGE_NOT_HELD` |
| V6 | a process can be created in it | `DuplicateTokenEx(TokenPrimary)` + `CreateProcessAsUserW(lpDesktop="winsta0\\default")` | `ENTER_FAILED` (with `win32_error`) |

`WTSGetActiveConsoleSessionId` is **not** the predicate — it answers only for the
physical console and lies on multi-RDP stations. Enumeration is the truth.

### 3.3 The worker (the only honest route)

Windows gives no supported way to call `EnumWindows`, `SendInput` or `GetDC(0)`
against another session from inside your own: the window station and desktop are
per-session. Therefore:

| op | mechanism |
|---|---|
| `exec{session}` | `CreateProcessAsUserW` with the V5 token, `CREATE_UNICODE_ENVIRONMENT|CREATE_NO_WINDOW`, stdout/stderr inherited; existing timeout/credential/stop logic unchanged |
| `ui-tree`, `ui-window`, `capture`, all `input` | spawn `probe-client.exe --session-worker` in the target session (same token path), one JSON request on stdin, one JSON response on stdout, exit |

Worker contract (over-specified on purpose):

```
probe-client.exe --session-worker
stdin : exactly one JSON object {"op":"ui-tree|ui-window|capture|input", "params":{...}, "v":1}
stdout: exactly one JSON object {"ok":true,"data":{...}} or {"ok":false,"code":"...","reason":"...","detail":{...}}
exit  : 0 after writing the response; non-zero only if it could not even read stdin
```

- **one worker per call.** No pool, no daemon, no shared state: spawn cost
  ~40-80 ms, far below the HMI's own response time, and it makes
  cross-request session mixups structurally impossible.
- **hard timeout** 5000 ms (ui-tree 3000 ms); `TerminateProcess` on expiry →
  `504 TIMEOUT`, `detail.ran:false`.
- **no privilege in the worker.** It runs *as the target user*, with that user's
  token — it holds no SeTcbPrivilege and can do nothing the logged-on operator
  could not do by hand. It is not an escalation surface even if launched manually.
- **prohibited forever** (a reviewer test greps for them): `CreateRemoteThread`,
  `NtCreateThreadEx`, `SetThreadDesktop` on a foreign desktop, `OpenWindowStation`
  of another session, handle duplication of another session's winsta,
  `CreateProcessWithLogonW`, password-based logon.
- **per-session serialization:** `sessionLocks map[uint32]*sync.Mutex` held for the
  duration of one worker call, shared by ui/input/capture *of that session*. Two
  clicks cannot interleave with a foreground change. Cross-session runs in parallel.
- **DPI:** the worker sets `DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2` before
  enumerating, so `GetWindowRect` returns physical pixels — the same units
  `SetCursorPos` (`platform_windows.go:436`) consumes. Reported as
  `"dpi_awareness":"per_monitor_v2"` in every ui-tree response. Without this, a
  125% display puts every click 25% off and nothing errors.

### 3.4 Refusal — exact shape (`SESSION_UNAVAILABLE`)

HTTP **409**, `ok:false`, never 200 (P8-03 Amendment A-1: a thing that did not
happen is never a result envelope).

```json
{
  "ok": false,
  "error": {
    "code": "SESSION_UNAVAILABLE",
    "message": "session 9 cannot be addressed: WTSQueryUserToken refused ERROR_PRIVILEGE_NOT_HELD (1314)",
    "detail": {
      "requested_session": 9,
      "agent_session": 0,
      "reason": "PRIVILEGE_NOT_HELD",
      "win32_error": 1314,
      "ran": false,
      "remediation": "The agent runs as 'flk' (session 0) and holds no SeTcbPrivilege, so it cannot obtain a token for session 9. Install and run the agent as the LocalSystem service (probe --install-service) to address any interactive session, or send session:0 to act in the agent's own session. Nothing was executed and no input was delivered."
    }
  }
}
```

Remediation contract (testable substrings): names the target session, the user of
that session, the failing call + Win32 code, the prerequisite, and the explicit
assertion that nothing ran. `detail.ran` is always `false` on this code.

`reason` enum (closed set — no free text):
`NOT_FOUND` · `NOT_INTERACTIVE` · `NO_USER` · `OTHER_USER` · `PRIVILEGE_NOT_HELD` ·
`ENTER_FAILED` · `GONE` · `PLATFORM_NOT_SUPPORTED`

Server status map: `SESSION_UNAVAILABLE` → 409; `UNSUPPORTED_AGENT` → 409;
`WINDOW_NOT_FOUND` → 404; `PROTOCOL_VIOLATION` → 502; `INVALID_PARAMS` → 400;
`TIMEOUT` → 504.

### 3.5 "No silent fallback" — three mechanical enforcements

An old agent simply ignores an unknown `"session"` JSON field and acts in its own
session. That is the exact failure the operator named ("that silent fallback is
how a click lands in the wrong window"), so it is closed three ways:

| # | Enforcement | Where |
|---|---|---|
| E1 | the **server refuses before dispatch** when the agent does not advertise cap `session`/`ui`: `409 UNSUPPORTED_AGENT`, `detail{feature:"session", ran:false}` | new handlers, mirroring `server_api.go:789-797` + P8-03's pre-dispatch gate |
| E2 | every session-scoped result **must** carry `session_context`; a 1.21+ agent that omits it is a protocol violation → `502 PROTOCOL_VIOLATION`, never passed through as success | server post-dispatch assertion |
| E3 | the agent's envelope dispatcher refuses **any** command type not on the allow-list that arrives with a `session` key — one check in `cmdTable` dispatch, not 74 struct edits | `internal/agent/agent.go:540+` |

E2 is what makes the echo load-bearing: the caller can prove from the response
which session the click landed in, and a missing echo is an error, not an
absence of information.

## 4. State / selector table (which endpoints take `session`)

| endpoint | `session` | other new fields | agent caps required | audit |
|---|---|---|---|---|
| `POST …/exec` | **yes** | — | `exec` + `session` when selector present | on refusal, and when selector ≠ own |
| `POST …/capture` | **yes** | — (`hwnd/rect/format/hash_only` are **P10-02**, not here) | `capture` + `session` | selector ≠ own |
| `POST …/pointer-click` | **yes** | `hwnd`, `count` later (P1) | `input` + `session`/`ui` | always (a click is an action) |
| `POST …/key-press` | **yes** | — | `input` + `session` | always |
| `POST …/key-combo` | **yes** | — | `input` + `session` | always |
| `POST …/text-input` | **yes** | — | `input` + `session` | always |
| `POST …/ui-tree` | **yes** (optional; own session if absent) | `visible_only` (default **true**), `hwnd` (exact filter) | `ui` + `session` | selector ≠ own |
| `POST …/ui-window` | **yes** (optional) | `hwnd`, `action`, `x,y,w,h` | `ui` + `session` | **always** |
| `GET …/sessions` | n/a (enumerates all) | — | `session` | always (`result:"success"`, count only) |
| `POST …/stream-start`, `…/stream-frame`, `…/stream-stop`, `…/display-info` | **refused** with `400 INVALID_PARAMS`, `detail.reason:"SESSION_NOT_ACCEPTED"` | — | — | on refusal |

The last row is deliberate: those four are the endpoints an operator would reach
for next, and silently ignoring the selector there would recreate the bug this
unit exists to kill. Refusing costs one line each (E3 covers it in one place).

RBAC scope: `ui-tree`, `ui-window`, `sessions`, and session-scoped input all
require the existing **`input`** scope (`v1CheckAuth(w,r,"input")` as
`api_v1_transfer.go:381` does). `sessions` additionally requires it because a
logged-on user list is operator-activity data — a `viewer` token must not see it.

## 5. `GET /api/v1/agents/{id}/sessions` — quser equivalent

```json
{
  "ok": true,
  "data": {
    "agent_session": 0,
    "agent_user": "SYSTEM",
    "generated_at_utc": "2026-11-24T16:52:03Z",
    "sessions": [
      { "id": 7, "user": "flk", "domain": "EAS1", "state": "active",
        "winstation": "console", "client_name": "", "client_address": "",
        "logon_time": "2026-11-24T06:11:41Z", "idle_s": 12,
        "is_agent_session": false, "addressable": true,
        "addressable_reason": "system_agent" },
      { "id": 0, "user": "", "domain": "", "state": "active",
        "winstation": "services", "client_name": "", "client_address": "",
        "logon_time": "", "idle_s": 0,
        "is_agent_session": true, "addressable": true,
        "addressable_reason": "agent_own" }
    ]
  }
}
```

- `addressable` is computed by the **same predicate** the selector enforces
  (`classifySession`), so discovery and enforcement cannot drift. Reasons:
  `agent_own` · `system_agent` · `other_user` · `no_user` · `not_interactive` ·
  `privilege_not_held`.
- `state` is the WTS state lowercased (`active`, `connected`, `disconnected`,
  `listen`, …), `winstation` from `WTS_SESSION_INFO.WindowStationName`.
- `idle_s` from `WTSIdleTime`-equivalent is optional: if the call fails, emit
  `-1`, never a fabricated zero.
- Read-only: no `disconnect`, no `logoff`, no `send-message`. The endpoint has
  exactly one verb, `GET`. (A logoff primitive is a separate decision, OD-4.)

## 6. `POST /api/v1/agents/{id}/ui-tree`

```json
{ "session": 7, "visible_only": true, "hwnd": 66102 }
```

all three optional; `visible_only` defaults to **true** (hidden windows are noise
and the 0-visible measurement is the detector). Response:

```json
{
  "ok": true,
  "data": {
    "session_context": { "requested": 7, "resolved": 7, "user": "flk",
                         "domain": "EAS1", "state": "active", "source": "selector" },
    "dpi_awareness": "per_monitor_v2",
    "truncated": false,
    "windows": [
      { "hwnd": 66102, "title": "…/Control Panel",
        "class": "Qt51516QWindowIcon",
        "rect": { "x": 0, "y": 0, "w": 1038, "h": 118 },
        "iconic": false, "foreground": true, "pid": 2640 }
    ]
  }
}
```

| field | definition (exact) |
|---|---|
| `hwnd` | top-level window handle, decimal |
| `title` | `GetWindowTextW`, full, no truncation (a page title *is* the signal) |
| `class` | `GetClassNameW` |
| `rect` | `GetWindowRect` = **window rect**, physical px, virtual-screen coords (may be negative, e.g. the measured `-7,0`) — same origin as input `hwnd` coordinates and as P10-02's `capture{hwnd}` |
| `iconic` | `IsIconic` |
| `foreground` | `GetForegroundWindow() == hwnd` (exactly one true, or none if another session owns the foreground) |
| `pid` | `GetWindowThreadProcessId` |

Enumeration: `EnumWindows` in the worker (so: in the resolved session), top-level
only (`GetParent(hwnd)==0`), skip `WS_CHILD`. `visible_only:true` → `IsWindowVisible`.
Hard cap 500 entries in z-order, `truncated:true` if hit. Unknown `hwnd` →
empty `windows` array (200), not a 404 — a window can legitimately vanish.

## 7. `POST /api/v1/agents/{id}/ui-window`

```json
{ "session": 7, "hwnd": 66102, "action": "foreground" }
```

```json
{ "ok": true,
  "data": { "hwnd": 66102, "action": "foreground",
            "state": { "iconic": false, "foreground": true, "closed": false },
            "session_context": { "requested": 7, "resolved": 7, "user": "flk",
                                 "domain": "EAS1", "state": "active", "source": "selector" } } }
```

| action | params | mechanism | `ok:true` iff (re-read after acting) |
|---|---|---|---|
| `foreground` | — | `ShowWindow(SW_RESTORE)` if iconic; `SetForegroundWindow`; on refusal the documented `AttachThreadInput(fg,self,true) → BringWindowToTop → SetForegroundWindow → AttachThreadInput(false)`; poll 300 ms | `GetForegroundWindow()==hwnd` |
| `minimize` | — | `ShowWindow(SW_MINIMIZE)` | `IsIconic` |
| `restore` | — | `ShowWindow(SW_RESTORE)` | `!IsIconic` |
| `move` | `x,y` | `SetWindowPos(SWP_NOSIZE|SWP_NOZORDER|SWP_NOACTIVATE)` | rect.x/rect.y match |
| `resize` | `w,h` | `SetWindowPos(SWP_NOMOVE|SWP_NOZORDER|SWP_NOACTIVATE)` | rect.w/rect.h match |
| `close` | — | `PostMessage(WM_CLOSE)` — **never** `TerminateProcess` | window absent from the session's enumeration within 2000 ms |

Rules:

- **`ok` means observed, not attempted.** A Qt window that refuses the foreground
  returns `200 {"ok":false, "reason":"FOREGROUND_DENIED", "state":{...}}` with the
  observed state. This is the direct fix for the measured lie
  (`pointer-click` answering `success:true` while the click landed elsewhere).
  200 means "the request reached the session"; `ok` means "the transition was seen".
- **hwnd is validated inside the resolved session** before any action: not in that
  session's top-level enumeration → `404 WINDOW_NOT_FOUND`,
  `detail{resolved_session, hwnd}`. A handle from another session never reaches a
  Win32 call.
- `move`/`resize` with a missing or out-of-range param → `400 INVALID_PARAMS`,
  `detail.field` named. `w,h` must be `>0` and `<= 32767`.
- Unknown `action` → `400 INVALID_PARAMS`, `detail.allowed` lists the six.
- `state.closed` is additive to the brief's `{iconic,foreground}`: without it a
  successful `close` is unobservable (the handle is gone).

## 8. Input with `hwnd` (closes the acceptance sequence without `exec`)

`InputParams` gains `Session *int` and — because §P0.2's acceptance criterion is
"click in (x,y) relative to X" — `Hwnd *int64` and a hit-test rule. Coordinates:

- `hwnd` absent → `x,y` are absolute physical pixels, exactly as today
  (`platform_windows.go:436`). Unchanged.
- `hwnd` present → `x,y` are **relative to the ui-tree `rect` origin** (window
  rect, physical px). The worker resolves `GetWindowRect` **at dispatch time**:
  a window that moved between read and click cannot misplace the click. One
  origin for eyes (`ui-tree`, P10-02 `capture{hwnd}`) and hands (this).
- **occlusion hit-test, always on, no knob:** before `SetCursorPos`, the worker
  computes the absolute point and calls `WindowFromPoint`; if
  `GetAncestor(hit, GA_ROOT) != hwnd` the click is **not performed**:

  ```json
  { "ok": false, "reason": "OCCLUDED",
    "hit": { "hwnd": 10034, "title": "…:11.11 SYSTEM LAYOUT", "pid": 3180 },
    "detail": { "intended_hwnd": 66102, "point": { "x": 42, "y": 96 } } }
  ```

  HTTP 200 (the request ran; the click intentionally did not), `ok:false`. The
  caller can re-read `ui-tree` and act on the truth. This is the measured bug
  turned into a refusal.
- `hwnd` present but not in the resolved session → `404 WINDOW_NOT_FOUND`.
- Applies to all four existing input endpoints (`pointer-click`, `key-press`,
  `key-combo`, `text-input`); focus is the target session's foreground window,
  which `foreground` made deterministic.

## 9. Security boundary

| agent principal | own session | other active session, **same** user | other active session, **different** user | session 0 (no user) | non-active session |
|---|---|---|---|---|---|
| **SYSTEM** (service) | allowed, direct | allowed (V5 token), audited with target user | **allowed** (V5 token), audited with target user + `system_agent` | `SESSION_UNAVAILABLE NO_USER` (its own session 0 is allowed by `agent_own`, but enumerates 0 windows — the measured fact) | `NOT_INTERACTIVE` |
| **user** (`flk`) | allowed, direct | **refused** `PRIVILEGE_NOT_HELD` (needs SeTcbPrivilege even for the same user) | refused `OTHER_USER` | refused `NO_USER` | `NOT_INTERACTIVE` |

Why SYSTEM may address any interactive session and a user may not: the SYSTEM
service **already holds** SeTcbPrivilege — entering an interactive session is not
an escalation of anything it lacks; a user agent would **gain** privilege, so the
answer is no. There is no `as:`-style declaration for sessions and no config knob:
the predicate is the process principal, nothing else.

"Session exists but belongs to another user": SYSTEM → resolved, acted, and the
audit row carries both the requested session and that user's name, so a
wrong-target action is visible after the fact. Non-SYSTEM → refused with
`OTHER_USER` **before** a token is requested, so no partial entry is possible.

Corollary the implementer must not "improve": a non-SYSTEM agent cannot borrow
the service's session by talking to it through the server; the server forwards to
one agent, and the agent's own principal decides. Cross-agent session borrowing
would be a new lane — refused by construction.

## 10. Audit

`AuditEntry` (`audit.go:14-29`) gains, additively:

```go
RequestedSession *int   `json:"requested_session,omitempty"` // null when the caller sent no selector
ResolvedSession  *int   `json:"resolved_session,omitempty"`  // null when refused
SessionUser      string `json:"session_user,omitempty"`      // user of the resolved session
SessionReason    string `json:"session_reason,omitempty"`    // refusal reason enum, §3.4
```

Rows (JSONL, one per line):

```json
{"timestamp":"2026-11-24T16:52:03.412Z","agent_id":"V6049A-EAS1","operator_id":"op-valmet","action":"ui-window","params":{"hwnd":66102,"action":"foreground"},"requested_session":7,"resolved_session":7,"session_user":"flk","result":"success","duration_ms":84}
{"timestamp":"2026-11-24T16:52:04.907Z","agent_id":"V6049A-EAS1","operator_id":"op-valmet","action":"exec","requested_session":9,"resolved_session":null,"session_user":"","session_reason":"OTHER_USER","result":"denied","error_msg":"SESSION_UNAVAILABLE: session 9 belongs to user 'mrossi'; the agent principal is 'flk'","duration_ms":0}
```

Recording rule (closed list — the reviewer tests it):

1. every refusal (`result:"denied"`), whatever the endpoint;
2. every `ui-window` call, success or not;
3. every `sessions` read (count in `params`, never the user list);
4. every call whose `requested_session != agent_session`;
5. nothing else — a session-scoped ui-tree on the agent's own session is not
   written (audit volume, and it reveals nothing the operator's own screen does).

`resolved_session` is written **from the agent's echo**, never from the server's
request — the audit must record what happened, not what was asked.

## 11. Non-Windows

Mirrors P8-03 exactly, in two layers:

| layer | behaviour |
|---|---|
| agent (non-Windows build) | does **not** advertise `session` or `ui` (`capabilities_elevation.go:20` set). If a request still reaches it (mixed fleet, stale caps): `exec`/`input` with a selector → `SESSION_UNAVAILABLE`, `reason:"PLATFORM_NOT_SUPPORTED"`; `ui-tree`/`ui-window` → `PLATFORM_NOT_SUPPORTED` (`messages.go:181`); `sessions` → `PLATFORM_NOT_SUPPORTED` |
| server (pre-dispatch) | agent lacks cap `session`/`ui` → `409 UNSUPPORTED_AGENT`, `detail{feature, agent_version, ran:false}` — before anything is sent (E1) |

Non-test agent code stays buildable on Linux: `newSessionPlatform()` returns the
stub (`internal/platform/session_other.go`), and `--session-worker` on Linux
exits 2 with `PLATFORM_NOT_SUPPORTED` on stdout. Cross-compile is part of the gate.

## 12. Worked sequence — "foreground X, click relative to X, read X's title", no `exec`

```
1  GET  /api/v1/agents/V6049A-EAS1/sessions
   → sessions[0] = {id:7, user:"flk", state:"active", addressable:true}

2  POST /api/v1/agents/V6049A-EAS1/ui-tree
   {"session":7,"visible_only":true}
   → windows[3] = {hwnd:66102, title:"…/Control Panel",
                   class:"Qt51516QWindowIcon", rect:{x:0,y:0,w:1038,h:118},
                   iconic:false, foreground:false, pid:2640}

3  POST /api/v1/agents/V6049A-EAS1/ui-window
   {"session":7,"hwnd":66102,"action":"foreground"}
   → {"ok":true,"data":{"state":{"iconic":false,"foreground":true,"closed":false}, …}}

4  POST /api/v1/agents/V6049A-EAS1/ui-tree
   {"session":7,"hwnd":66102}
   → [{hwnd:66102, rect:{x:0,y:0,w:1038,h:118}, foreground:true, …}]
      (re-read: a restore can move the window — never reuse a stale rect)

5  POST /api/v1/agents/V6049A-EAS1/pointer-click
   {"session":7,"hwnd":66102,"x":42,"y":96}
   → absolute = rect(0,0)+ (42,96) resolved at dispatch; hit-test passes;
     200 {"ok":true}

6  POST /api/v1/agents/V6049A-EAS1/ui-tree
   {"session":7,"hwnd":10034}
   → [{hwnd:10034, title:"11.11 SYSTEM LAYOUT", foreground:true, …}]
```

Zero `exec` calls, zero P/Invoke script, and every step names the session it
acted in. If step 5's point were covered, it returns `ok:false OCCLUDED` with the
covering window named — instead of today's `{"success":true}`.

## 13. Edge cases and failure paths (each has a named outcome)

| # | Case | Outcome |
|---|---|---|
| X1 | session vanishes between V1 and V6 (operator logs off) | `SESSION_UNAVAILABLE`, `reason:"GONE"`; no retry, no fallback to own session |
| X2 | worker hangs (GPU driver, modal Qt loop) | 5 s → `TerminateProcess` → `504 TIMEOUT`, `detail.ran:false` (for `ui-window` the state is unknown — reported as `ran:true, applied:false, state:null`, see D7) |
| X3 | hwnd recycled between ui-tree and click (same handle, new window) | hit-test + `pid` mismatch against the caller's expectation is not available; the worker re-reads class+pid and returns them in the response, so the caller can see the swap. Documented limitation: hwnd is not stable identity, `class+title+pid` is |
| X4 | two operators drive the same session at once | per-session mutex serializes each call; the sequence is still two independent visions — the contract does not claim atomicity across calls (that is a flow/mixer concern) |
| X5 | station has two monitors, negative coords | `rect` and clicks are virtual-screen physical px (negative allowed); no per-monitor remapping — one coordinate space |
| X6 | RDP session that reconnects (id preserved, state flaps) | each call re-runs the ladder; a flap yields `NOT_INTERACTIVE`, never a stale success |
| X7 | foreground lock by an elevated window in the same session | foreground action returns `ok:false FOREGROUND_DENIED` after the AttachThreadInput attempt; the click is not attempted if the target is not foreground and `hwnd` hit-test fails |
| X8 | old agent in the fleet (1.19/1.20) | E1 pre-dispatch `409 UNSUPPORTED_AGENT`, `ran:false` |

Graceful failure, component by component: server handler → typed status +
`ran:false`; agent verification → typed code + remediation + audit row; worker →
one JSON object, non-zero exit only if it cannot speak; worker crash → agent maps
EOF to `INTERNAL_ERROR` with `detail.worker_exit`; timeout → kill + 504.

## 14. Future projections

**1 month** — EAS1 only, one operator in session 7. ~200 ui-tree calls/day,
~60 clicks/day. One worker per call at 40-80 ms is invisible against HMI latency.
`sessionLocks` contention is effectively zero. Audit adds ≤200 rows/day. Risk:
the operator's first reflex is still `exec` + P/Invoke for things this contract
does not cover (`scroll`, `drag` — P1, next unit). Mitigation: the contract's
worked sequence is the documented alternative for the four ops it does cover.

**6 months** — 3-4 stations, 2 sessions each (console + RDP for remote support),
~5k UI calls/day, `sessions` polled by a fleet dashboard every 30 s. Two things
will strain: (a) worker spawn becomes the p95 if input rate rises with scroll/drag
— the measurement point is *sustained >5 calls/s per station*; below that, pooling
is complexity with no payoff; (b) audit volume ~8k rows/day makes the JSONL file
rotation a real gate (existing rotation, not this unit). The `addressable` field
becomes the dashboard's core column ("who is at which HMI") — it must stay derived
from `classifySession`, never recomputed server-side.

**1 year** — session becomes a fleet concept, not a client detail: flows pinned to
"the session where operator X is on watch", station handover (`7 → 11`) mid-run,
and hwnd as the addressing key shared by ui-tree, input and capture (P10-02), so a
flow discovers a window once and acts on it without absolute coordinates. If
multi-monitor window-station work (`winsta1`) or a second desktop ever appears,
the selector generalises to `{session, winsta, desktop}` — the V1-V6 ladder is the
seam (V6 already passes `lpDesktop`), so that is a parameter extension, not a
redesign. What would force a redesign: needing UI operations *without* a logged-on
user (a headless spooler) — then the worker model has no token to run under and the
answer is a different product, not a patch.

## 15. Decision log

| # | Decision | Why | Cost I accept |
|---|---|---|---|
| D1 | `Session *int`, absent = own session | 0 is a legal session id; absence must differ from 0 | one pointer type on the wire |
| D2 | one worker process per call | no shared state, no session mixups, no daemon to supervise | 40-80 ms/call; a pool is a later decision with a measured trigger |
| D3 | `SESSION_UNAVAILABLE` with a closed `reason` enum | one code to catch, one remediation shape to test; the enum carries the detail | a wide detail object (cheap) |
| D4 | SYSTEM may address any active session; a user may address only its own | SYSTEM already holds SeTcbPrivilege; a user would gain it | no same-user convenience path (refused even for the same user — simpler and stricter) |
| D5 | `ui-tree.rect` = window rect = input origin = capture origin | one origin for eyes and hands; a second rect is a second truth | client-area clicks need the caller to know the frame (HMI windows are borderless) |
| D6 | occlusion hit-test on every `hwnd` click, default on, no knob | the measured bug was a covered click answering success | a legitimate click on a deliberately overlapped point is refused (caller uses absolute mode) |
| D7 | `ok` = observed, not attempted; 200 = request reached the session | P8-03 A-1 carried to UI; the operator's complaint was a lie, not a failure | callers must read `ok`, not just the status |
| D8 | refuse `session` on stream-*/display-info explicitly | silent ignore on the next endpoint is the same bug | four one-line refusals |
| D9 | per-session mutex, shared by ui/input/capture | input and foreground cannot race | a slow capture blocks a click in that session (correct semantics) |
| D10 | `sessions` requires the `input` scope, `GET` only | a user list is operator-activity data; no logoff primitive | viewers cannot see it |
| D11 | worker sets per-monitor-v2 DPI awareness | physical px everywhere, or every click is off by the scale factor | none — it is the only correct default |
| D12 | `detach + session` refused in v1 | the guardian lives outside the session | named as OD-3 rather than half-built |

## 16. Build phases (phase 1 shippable alone) and tests

| phase | content | starts without reading later phases |
|---|---|---|
| **1** | protocol: `Session *int` on `ExecParams`/`InputParams`, new params/result types, codes; `capabilityForCommand` entries; agent E3 dispatcher check; server E1 gate + E2 echo assertion; handlers that refuse when the cap is absent | yes — a fleet-wide, behaviour-neutral ship: no selector = today's behaviour, byte for byte |
| 2 | `sessions` + `classifySession` + WTS enumeration (no worker: `WTSEnumerateSessions`/`WTSQuerySessionInformationW` report every session on the machine from any session; only *token acquisition* for a foreign session needs SYSTEM) | needs phase 1 types only |
| 3 | `ui-tree` + worker (`--session-worker`, pipes, timeout, DPI) | needs V1-V6 ladder from phase 3's shared file |
| 4 | `ui-window` (six actions, observed-`ok` rule, hwnd validation) | yes |
| 5 | `hwnd` on input + occlusion hit-test + per-session mutex | yes |
| 6 | docs: `docs/SESSIONS.md`, capability table, remediation strings | — |

Tests that must fail on the old behaviour: refusal before dispatch on a cap-less
agent (409, `ran:false`); `SESSION_UNAVAILABLE` for each `reason` (7 cases,
including `NO_USER` on session 0 and `OTHER_USER` with a user principal);
missing `session_context` echo → 502; `ui-tree{hwnd}` filter; all six `ui-window`
actions against a test window, with `ok:false FOREGROUND_DENIED` simulated;
`close` observed via enumeration; occlusion refusal with the covering hwnd named;
`detach+session` 400; `session` on `stream-start` 400; no-selector regression on
all ten endpoints (identical envelopes to base).

## 17. Verifier appendix (re-runnable at review time)

```bash
cd /opt/data/projects/PROBE_UPGRADE/v119wt
grep -c '"session"' internal/protocol/messages.go          # was 0 at 85853f3; must be >0
grep -rn "CreateRemoteThread\|NtCreateThreadEx\|CreateProcessWithLogonW\|SetThreadDesktop" \
     --include=*.go internal/ cmd/ | grep -v _test          # must be empty
grep -n "WTSQuerySessionInformation" internal/platform/*windows*.go   # local declaration present
grep -n "session" internal/server/enrollment.go             # capabilityForCommand entries
go build ./... && GOOS=windows GOARCH=amd64 go build ./...
go vet ./internal/agent ./internal/server ./internal/platform ./internal/protocol
go test ./internal/agent ./internal/server ./internal/protocol -count=1 -run 'Session|UITree|UIWindow' -v
go test ./... -count=1 -timeout 1500s                      # full suite; baseline 458 pass / 1 skip
```
Windows-only tests must be guarded (`//go:build windows`) so the Linux suite keeps
its 458/1-skip baseline; the cross-compile above is what proves they compile.

## 18. Open decisions for valmet / Goran (ratify or overturn before build)

| # | Question | Architect's default |
|---|---|---|
| OD-1 | Does the deployment actually run the agent **as the LocalSystem service** on EAS1 (P2's recommendation)? Without it, `session:7` refuses with `PRIVILEGE_NOT_HELD` and this unit delivers nothing in the field. | REQUIRED prerequisite; contract ships either way, but the acceptance sequence only passes in service mode |
| OD-2 | Is SYSTEM-may-address-any-active-session accepted as policy, or must it be limited to one operator's session per station? | any active session, audited with the target user (D4) |
| OD-3 | `detach + session` (a guardian job inside an operator's session) — needed for long HMI runs, or out of scope? | refused in v1 |
| OD-4 | A `sessions` write verb (logoff/disconnect) — ever? | no; read-only forever, separate decision if needed |
| OD-5 | `ui-window close` on the HMI: allowed for any window, or denied for the HMI process itself? | allowed for any window (the operator owns the station); a deny-list would be a second policy place |

## 19. Review

| field | value |
|---|---|
| reviewed_by | PENDING — valmet (operator/requirements) and Goran (fleet) |
| verdict | **CONTRACT v1 — RATIFY BEFORE CODER DISPATCH.** No implementation unit may start until OD-1..OD-5 are answered. |
| evidence class | substrate read at `85853f3` (§1), operator measurements quoted from spec v1.21 §P0.1/P0.2 |
| prohibited and honoured | no OCR / semantic HMI (spec l.176-181), no new lane or knob, no coder dispatch, no handler edits |
