# P10-01 v2 — OS-agnostic primitive contract (context · windows · input · capture)

**STATE:** profile=architect · gateway=up (s6 `gateway run`, api_server lane, `HERMES_S6_SUPERVISED_CHILD=1`) · status=design-gate, no code written

| field | value |
|---|---|
| unit | P10-01 v2 — neutral contract. **Supersedes the Windows-shaped v1** at `docs/contracts/P10-01_SESSION_CONTRACT.md` (571 lines) wherever the two differ |
| owner | architect — design gate. No handler edited, no coder dispatched, no new lane, no new knob |
| tree | `/opt/data/projects/PROBE_UPGRADE/v119wt` · branch `r119-build` · base `ec647ed` |
| input | `inputs/PROBE_SPEC_ORCH_v1.21.md` §P0.1 (l.29-53), §P0.2 (l.55-77), §P1 input (l.81-101), §P1 capture (l.105-123), non-goal (l.176-181) |
| owner ruling | **OS-AGNOSTIC.** A primitive is defined by its semantics, not by the OS that first needed it. A platform that cannot do it answers with a typed refusal — never a silent stub, never a Windows-only definition |
| non-goal | no OCR, no semantic control recognition, no HMI understanding (spec l.176-181). Eyes and hands only |
| status | CONTRACT v2 — ratify (valmet + Goran) before any coder dispatch |

---

## 1. What changed from v1, and why

| v1 (Windows-shaped) | v2 (neutral) | Reason |
|---|---|---|
| `Session *int` selector | `context` (opaque string) + `session` (int, legacy alias) | A Windows session is one *kind* of execution context. X11/Wayland have displays and seats; Android has user profiles and displays. Hard-coding `int` makes the wire format platform-truth |
| `sessions` endpoint (WTS rows) | `contexts` endpoint (kind-tagged rows) | Same enumeration on every OS; `quser` semantics are one row type |
| `ui-tree` rows with `hwnd int` | rows with `id string` + `id_kind` (+ `hwnd` alias on Windows) | hwnd is a Windows handle. X11 gives XIDs, Wayland gives toplevel handles, Android gives a11y window ids |
| input = absolute px, `hwnd`-relative | same geometry, neutral `window` selector | The measured bug (covered click answered success) is OS-independent: absolute injection with no identity |
| `TypeText` implicitly ASCII | `text` carries an explicit encoding + per-backend Unicode method | `platform_windows.go:500` iterates **bytes**; X11 `xdotool type` happens to do better but is an external binary; the contract must not depend on luck |
| capture = full screen only | `capture` with `window|rect|format|quality|hash_only` | spec §P1 l.105-123 (measured: 1024×768 JPEG to read a 1038×118 strip) |
| non-Windows = one paragraph ("PLATFORM_NOT_SUPPORTED") | full backend matrix per primitive, per platform, with the refusal code for each cell | Owner ruling: a platform that cannot do a primitive answers with a typed refusal, it is not described as "not supported" and left undefined |

**Invariant carried forward unchanged:** absent selector = the agent's own context and today's behaviour byte-for-byte; present selector = resolved exactly or refused. There is no third outcome and no fallback (v1 §3.5, kept as E1-E3 in §5.5 below).

---

## 2. Substrate (read at base `ec647ed`, verified by probe — not from recall)

| Fact | Evidence |
|---|---|
| `session`, `context`, `scroll`, `drag`, `ui-tree`, `ui-window` absent from protocol | `grep -rn '"scroll"\|"drag"\|ui-tree' internal/protocol internal/server/api_v1.go cmd/probe` → **0** each (non-test) |
| `InputParams` = `X,Y,Button,Text,Key,Keys` only, no count/move-only/scroll/drag | `internal/protocol/messages.go:557-564` |
| `ExecParams` already carries principal/detach/credential/idempotency | `messages.go:230-280` |
| `CaptureResult` = format,width,height,data,size_bytes — no hash, no window, no region | `messages.go:467-473` |
| capture route exists, params free-form (server forwards body) | route `internal/server/api_v1.go:358-359`; handler `internal/server/server_api.go:666` |
| input routes = 4 only (pointer-click, key-press, key-combo, text-input) | `api_v1.go:489-492`; handlers `api_v1_transfer.go:375,450` (all `v1CheckAuth(...,"input")`) |
| Windows click = `SetCursorPos` + `mouse_event`, no window identity | `internal/platform/platform_windows.go:430-454` |
| Windows `TypeText` iterates **bytes** and maps ASCII VKs | `platform_windows.go:500-524` |
| Linux click/text/key = external `xdotool`; capture = `import`/`scrot`; screen info returns zeros | `internal/platform/platform_linux.go:237-243`, `:191-210`, `:212-214` |
| Linux/Darwin hook into the same `Platform` interface and 1-line fallbacks | `internal/platform/platform.go:65-95`, `:115-145`; `platform_darwin.go:203,273` |
| capability gate exists and is coarse (`exec`/`filesystem`/`capture`/`input`) | `internal/server/enrollment.go:414-435`, `:533-540` |
| advertised caps are a flat `[]string` persisted in the registry | `messages.go AgentInfo.Capabilities`; `internal/server/registry.go:165` |
| android is a **build target** but runs as a Termux process (`pure-Go, CGO_ENABLED=0`) | `internal/server/builder.go:90-94` — no accessibility service, no APK |
| audit entry is flat and additive-friendly | `internal/server/audit.go:14-29` |
| response envelope `{ok,data,error}` | `internal/server/api_v1.go:27-31` |
| typed-refusal precedent (declaration + code + detail + remediation) | `messages.go:181,193,195`; `ErrorInfo.Detail` |

**No new dependency.** Every backend named below is either a syscall/wire protocol reachable from the existing `Platform` interface, an OS-supplied compositor interface, or an OS tool already present on the target (declared as a **backend prerequisite**, not a vendored library).

---

## 3. The neutral model (four definitions, then everything else follows)

### 3.1 Context

A **context** is the addressable view a process has onto a UI: one input queue, one screen, one set of windows. Its identity is platform-defined; the protocol carries it as an opaque string.

| kind | platform | `id` form | what it means |
|---|---|---|---|
| `wts` | Windows | `wts:7` | a WTS session (0 = services) |
| `x11` | Linux/BSD (X11), headless Xvfb | `x11::0` → `x11:<host>:<display>` | one X display (`$DISPLAY`), including its virtual screen |
| `wl-seat` | Linux (Wayland) | `wl:seat0` | one seat; **windows are not globally enumerable** (§11) |
| `a11y-display` | Android (probe inside an APK) | `a11y:u0:d0` | a user profile + display, via AccessibilityService |
| `uid` | Android (Termux, today) | `uid:10123` | the process uid — the only context a Termux process has |
| `tty` | any POSIX | `tty:/dev/pts/3` | fallback for exec-only contexts: no screen, no windows |

Rules that make this neutral instead of decorative:

- **A context is advertised, never inferred.** A caller obtains an `id` from `GET …/contexts` (§7) and passes it back verbatim. The agent never accepts an id it did not advertise.
- **Kinds are capability-scoped, not platform-scoped.** An X11 display has windows; a `uid` context does not. `exec` works in every kind; `window.tree` does not.
- **`session` is a compatibility alias.** `session:7` on a `wts`-kind agent resolves to `wts:7`. On any other kind it is **refused** (`CONTEXT_UNAVAILABLE`, `reason:"KIND_NOT_APPLICABLE"`), never ignored — ignoring is the bug being fixed. `session` and `context` in the same request with different targets → `400 INVALID_PARAMS`, `reason:"SELECTOR_CONFLICT"`.

### 3.2 Window identity

```json
{ "id": "0x3c00007", "id_kind": "xid", "native": {"xid": 62914567},
  "title": "…/Control Panel", "class": "Qt51516QWindowIcon",
  "rect": {"x":0,"y":0,"w":1038,"h":118},
  "iconic": false, "foreground": true, "owner_pid": 2640,
  "visible": true, "kind": "normal" }
```

`id` is opaque and context-scoped; `native` exists for the operator's debugging and is never parsed by the server. `hwnd` reappears as a top-level integer **only** on `wts` contexts (1.21 wire compat). `class` is the platform's class string, normalised to `""` when the backend has none. `kind` is `normal|dialog|popup|override-redirect|tool` (popups and override-redirect windows are excluded from the default tree — they are not addressable targets).

### 3.3 Units and coordinate space

| concept | definition |
|---|---|
| `px` | **native device pixels of the context's coordinate space** — Windows per-monitor DPI-aware virtual screen; X11 root-window pixels; Android physical display pixels ÷ reported `density` (echoed as `dpi_scale`) |
| coordinate origin | top-left of the context's screen space; **negative values are legal** (measured `-7,0`, multi-monitor) |
| `window.rect` | the window's outer rect in that space — **one origin for eyes (`ui-tree`), hands (input) and capture (`hwnd`)** (v1 D5, kept) |
| echoed every response | `"units":"px"`, `"dpi_scale":1.0`, `"dpi_awareness":"per_monitor_v2"|"x11_native"|"none"` |

No unit conversion happens inside probe, ever. If a backend reports scaled units, the backend normalises to px and says so; the caller cannot be silently 25% off (the v1 D11 failure mode, generalised).

### 3.4 Primitive catalogue

Six primitives, named once and used by every platform:

| # | primitive | verbs | endpoints |
|---|---|---|---|
| **P1** | context selector | field on any UI-affecting call | `exec`, `capture`, `ui-tree`, `ui-window`, `pointer-*`, `key-*`, `text-input` |
| **P2** | context enumeration (read-only) | `GET` | `/api/v1/agents/{id}/contexts` |
| **P3** | window tree | `POST` | `/api/v1/agents/{id}/ui-tree` |
| **P4** | window control | `POST` | `/api/v1/agents/{id}/ui-window` |
| **P5** | pointer + keyboard + text | `POST` | `…/pointer-move`, `…/pointer-click`, `…/pointer-scroll`, `…/pointer-drag`, `…/key-press`, `…/key-combo`, `…/text-input` |
| **P6** | capture | `POST` | `/api/v1/agents/{id}/capture` |

`hover` is **not** a primitive: it is `pointer-move` with no button. Adding a verb for "move but call it something else" buys a second code path for one behaviour.

---

## 4. Primitive × parameter table (acceptance quote #1)

| primitive / endpoint | parameter | type | req | default | unit / domain |
|---|---|---|---|---|---|
| **all** `context` | `context` | string | no | agent's own | an `id` from `/contexts`; absent ≠ own — absent means *no selector was sent* |
| | `session` | int | no | — | 0-65535; `wts`/`a11y` kinds only; alias for `context` |
| **P1** `exec` | `context`/`session` | — | no | own | same resolution ladder as UI; `detach:true` + selector → `400 INVALID_PARAMS reason:"DETACH_WITH_CONTEXT_UNSUPPORTED"` (v1 D12, kept) |
| **P2** `contexts` | — | — | — | — | `GET` only; no query params, no writes |
| **P3** `ui-tree` | `visible_only` | bool | no | **true** | hidden windows are noise; the 0-visible measurement is the detector |
| | `window` | string | no | — | exact `id` filter; unknown id → `200 []` (a window may legitimately vanish) — not 404 |
| | `max` | int | no | 500 | hard cap; `truncated:true` when hit |
| **P4** `ui-window` | `window` | string | **yes** | — | must be in the same context's tree; else `404 WINDOW_NOT_FOUND` |
| | `action` | enum | **yes** | — | `foreground\|minimize\|restore\|move\|resize\|close` |
| | `x`,`y` | int | for `move` | — | px, may be negative |
| | `w`,`h` | int | for `resize` | — | `1..32767`; else `400` |
| **P5** `pointer-move` | `x`,`y` | int | **yes** | — | px; no button, no click (hover = this) |
| | `window` | string | no | — | when present, `x,y` are **relative to `window.rect` origin**, resolved at dispatch |
| **P5** `pointer-click` | `x`,`y` | int | **yes** | — | px, or window-relative when `window` present |
| | `button` | enum | no | `left` | `left\|right\|middle` |
| | `count` | int | no | 1 | `1..3`; **`2` = one double-click sequence**, not two calls (§9.2) |
| | `window` | string | no | — | as above; enables the occlusion hit-test |
| **P5** `pointer-scroll` | `x`,`y` | int | no | cursor | anchor point (some toolkits scroll the window under the given point) |
| | `delta` | int | **yes** | — | **notches**; **negative = up / away from user / toward the top** |
| | `unit` | enum | no | `notch` | `notch\|px` (`px` only where the backend supports pixel-precise wheels, else `PRIMITIVE_UNSUPPORTED reason:"UNIT_NOT_SUPPORTED"`) |
| | `window` | string | no | — | as above |
| **P5** `pointer-drag` | `from`,`to` | object | **yes** | — | `{"x":..,"y":..}`, same space as click |
| | `button` | enum | no | `left` | |
| | `duration_ms` | int | no | 300 | `50..5000`; the down→up span |
| | `steps` | int | no | 12 | intermediate moves; `0` = jump (some UIs require intermediate motion) |
| **P5** `text-input` | `text` | string | **yes** | — | UTF-8 on the wire; **full Unicode** (spec l.92) |
| | `encoding` | enum | no | `utf8` | `utf8\|utf16`; `utf16` = caller already knows the code units (never guess which the target wants) |
| | `window` | string | no | foreground | text goes to the focused window; `window` is a *claim* verified before typing |
| **P5** `key-press` | `key` | string | **yes** | — | canonical name (`Enter`, `Escape`, `F5`, `Tab`) |
| | `window` | string | no | foreground | |
| **P5** `key-combo` | `keys` | []string | **yes** | — | order-insensitive modifiers (`["Ctrl","Shift","S"]`) |
| **P6** `capture` | `window` | string | no | full screen | takes precedence over `rect` |
| | `rect` | object | no | full screen | `{x,y,w,h}` in context px; `w,h ≥ 1` |
| | `format` | enum | no | `png` | `png\|jpeg`; png for text/UI, jpeg for photos |
| | `quality` | int | no | 80 | `jpeg` only, `1..100` |
| | `hash_only` | bool | no | false | when true, **`data` is omitted** — the point of the primitive (spec l.119) |
| | `since` | string | no | agent's stored baseline | `sha256:<hex>` to compare against; overrides the stored baseline for this call |

Response shapes are in §6-§10; refusal enums in §5.4 and §11.

---

## 5. P1 — execution-context selector

### 5.1 Type and absence (neutral)

```json
{ "context": "wts:7" }        // canonical
{ "session": 7 }              // compatibility alias, wts/a11y only
{ }                           // no selector: the agent's own context, unchanged behaviour
```

- **absent** → the agent's own context. The response still echoes `context_ref.source:"agent_own"`, so where the action happened is never a guess.
- **present** → the ladder in §5.2 runs to completion, then the op runs in exactly that context, or the op is refused. No partial entry, no retry in another context.
- **`session` on a non-`wts`/`a11y` kind** → `409 CONTEXT_UNAVAILABLE reason:"KIND_NOT_APPLICABLE"` (do not silently treat 7 as a display number).
- **`context` not advertised by this agent** → `409 CONTEXT_UNAVAILABLE reason:"NOT_FOUND"`, `detail.advertised` lists the ids.
- **both present, disagreeing** → `400 INVALID_PARAMS reason:"SELECTOR_CONFLICT"`.
- **range** (`session` outside `0..65535`) → `400 INVALID_PARAMS`, `detail.field:"session"`, before any syscall.

### 5.2 Resolution ladder — the same six questions on every OS

| # | question | Windows (`wts`) | X11 | Wayland | Android (Termux) |
|---|---|---|---|---|---|
| V1 | does the context exist? | `WTSEnumerateSessions` match | `$DISPLAY` socket reachable, `XOpenDisplay` | `WAYLAND_DISPLAY` + seat present | `getuid()` / profile lookup needs root |
| V2 | is it interactive? | `State == WTSActive` | display not `-nolisten`, XTEST extension present | seat has a keyboard capability | process has a foreground activity — **unknown to Termux** |
| V3 | is a principal attached? | `WTSQuerySessionInformationW(WTSUserName)` non-empty | `XGetSelectionOwner`-independent: peer uid of the socket / `~user/.Xauthority` readable | `wl_display` peer credentials (SO_PEERCRED) | the uid itself |
| V4 | may **this** agent address it? | §12 predicate | §12 predicate | §12 predicate | §12 predicate |
| V5 | can authority be obtained **without gaining any**? | `WTSQueryUserToken` (SYSTEM only) | `XAUTHORITY` cookie already readable by this uid | same-uid socket connect only | same-uid only |
| V6 | can work be entered? | `CreateProcessAsUserW(lpDesktop="winsta0\\default")` | fork+`setuid` (root only) or direct syscalls in-process | direct in-process (same seat only) | fork+`setuid` (root only) |

`WTSGetActiveConsoleSessionId` is **not** the predicate (it lies on multi-RDP stations); enumeration is. Same discipline everywhere: **ask the platform who is there, never assume.**

### 5.3 Entering a context — one mechanism per OS, no tricks

| OS | exec in context | UI/input/capture in context |
|---|---|---|
| Windows | `CreateProcessAsUserW`, target user's token, `CREATE_UNICODE_ENVIRONMENT\|CREATE_NO_WINDOW` | the same binary relaunched as `--context-worker` **in** that context, one JSON request on stdin, one JSON response on stdout, exit (v1 §3.3, kept verbatim) |
| X11 | fork + `setgroups/setgid/setuid` to the target uid (root only), env `DISPLAY`+`XAUTHORITY` of that session | direct syscalls against that `Display*` — X11 has no per-session window station; a second display is simply a second connection |
| Wayland | same uid only (fork not needed) | direct, same seat |
| Android (Termux) | same uid only (`fork` + setuid if root) | **refused** — no accessibility surface (§11) |
| headless (Xvfb) | normal | X11 backend, no compositor |

**Prohibited mechanisms, forever (a reviewer greps for them):** `pkexec`, `sudo`, `su `, `runas`, `CreateProcessWithLogonW`, `CreateRemoteThread`, `NtCreateThreadEx`, `SetThreadDesktop` on a foreign desktop, `OpenWindowStation` of another session, handle duplication of another session's winsta, `setsid`-based seat theft, `adb shell` into another app, `LD_PRELOAD` into a foreign compositor. The rule they all violate: **a context is entered with authority the process already holds, never with authority it acquires.**

### 5.4 Refusal shape — `CONTEXT_UNAVAILABLE`

HTTP **409**, `ok:false`, never 200. (P8-03 A-1: a thing that did not happen is never a result envelope.)

```json
{ "ok": false,
  "error": {
    "code": "CONTEXT_UNAVAILABLE",
    "message": "context 'wts:9' cannot be entered: no token obtainable (ERROR_PRIVILEGE_NOT_HELD 1314)",
    "detail": {
      "requested": {"id":"wts:9","kind":"wts","session":9},
      "agent_context": {"id":"wts:0","kind":"wts"},
      "reason": "PRIVILEGE_NOT_HELD",
      "platform_error": {"backend":"wtsapi32","call":"WTSQueryUserToken","code":1314},
      "ran": false,
      "remediation": "The agent runs as 'flk' in wts:0 and does not hold SeTcbPrivilege, so it cannot obtain a token for wts:9. Run the agent as the OS service principal (probe --install-service) to address any interactive context on this host, or send no selector to act in the agent's own context. Nothing ran and no input was delivered."
    } } }
```

`reason` is a **closed enum** (no free text, tests enumerate all of them):
`NOT_FOUND` · `NOT_INTERACTIVE` · `NO_USER` · `OTHER_USER` · `PRIVILEGE_NOT_HELD` · `ENTER_FAILED` · `GONE` · `KIND_NOT_APPLICABLE` · `NO_BACKEND`.

`detail.ran` is always `false` on this code. A refusal that cannot say what it did not do is not a refusal.

### 5.5 No silent fallback — three mechanical enforcements (v1 E1-E3, generalised)

| # | Enforcement | Where |
|---|---|---|
| E1 | the **server refuses before dispatch** when the agent's advertised features lack `context.selector`: `409 UNSUPPORTED_AGENT`, `detail{feature:"context.selector", agent_version, ran:false}` | new pre-dispatch gate, mirroring `server_api.go:789-797` + `enrollment.go:533` |
| E2 | every context-scoped result **must** carry `context_ref`; a 1.21+ agent that omits it is `502 PROTOCOL_VIOLATION`, never passed through as success | server post-dispatch assertion |
| E3 | the agent's dispatcher refuses **any** command type not on the allow-list that arrives with a `context`/`session` key — one check at the dispatch table, not 74 struct edits | `internal/agent` cmdTable |

E2 is what makes the echo load-bearing: a caller can prove from the response which context the click landed in, and a missing echo is an error — not an absence of information.

**Endpoints that refuse the selector explicitly** (rather than ignore it): `stream-start`, `stream-frame`, `stream-stop`, `display-info` → `400 INVALID_PARAMS reason:"CONTEXT_NOT_ACCEPTED"`. These are the endpoints an operator would reach for next; silence there recreates the bug.

---

## 6. P2 — context enumeration (read-only)

`GET /api/v1/agents/{id}/contexts`

```json
{ "ok": true, "data": {
  "agent_context": {"id":"x11::0","kind":"x11"},
  "generated_at_utc": "2026-11-24T17:22:10Z",
  "contexts": [
    { "id":"x11::0", "kind":"x11", "name":":0", "user":"flk", "uid":1000,
      "active": true, "agent_own": true, "addressable": true,
      "addressable_reason":"agent_own",
      "surface": {"pixels":"1920x1080", "displays":1, "dpi_scale":1.0},
      "features": {"window.tree":true,"window.control":["foreground","minimize","restore","move","resize","close"],
                   "input.pointer":["move","click","scroll","drag"],"input.text":["unicode"],
                   "capture.window":true,"capture.region":true}},
    { "id":"uid:10123", "kind":"uid", "name":"termux", "user":"u0_a123", "uid":10123,
      "active": true, "agent_own": false, "addressable": true,
      "addressable_reason":"same_uid_other_process",
      "surface": {"pixels":"1080x2400", "displays":1, "dpi_scale":2.75},
      "features": {"window.tree":false,"window.control":[],"input.pointer":[],
                   "input.text":[],"capture.window":false,"capture.region":false} }
  ] } }
```

- `addressable` is computed by the **same predicate the selector enforces** (`classifyContext`), so discovery and enforcement cannot drift. Reasons: `agent_own` · `service_principal` · `same_uid_other_process` · `other_user` · `no_user` · `not_interactive` · `privilege_not_held` · `kind_has_no_surface`.
- `features` per context is the whole point: the same agent may have a full X11 display and a windowless `uid` context. A caller reads one row and knows what it can do **there** — it never guesses from the agent's OS string.
- `idle_s`/`logon_time` are `-1`/`""` when the platform cannot answer, **never a fabricated zero**.
- `GET` only. No `disconnect`, `logoff`, `message`, `switch`. A write verb is a separate decision (OD-4).
- RBAC: requires the **`input`** scope (as `api_v1_transfer.go:377` does) — a logged-on-user list is operator-activity data; a `viewer` token must not see it.

---

## 7. P3 — window tree

`POST /api/v1/agents/{id}/ui-tree` `{"context":"x11::0","visible_only":true}`

```json
{ "ok": true, "data": {
  "context_ref": {"requested":"x11::0","resolved":"x11::0","kind":"x11","user":"flk","source":"selector"},
  "units": "px", "dpi_scale": 1.0, "dpi_awareness": "x11_native",
  "truncated": false, "count": 3,
  "windows": [
    {"id":"0x3c00007","id_kind":"xid","native":{"xid":62914567},
     "title":"…/Control Panel","class":"Qt51516QWindowIcon",
     "rect":{"x":0,"y":0,"w":1038,"h":118},
     "iconic":false,"foreground":true,"owner_pid":2640,"visible":true,"kind":"normal",
     "hwnd":66102}
  ] } }
```

| field | definition | absent on |
|---|---|---|
| `id` | opaque, context-scoped, stable for the window's lifetime | — |
| `id_kind` | `hwnd` \| `xid` \| `toplevel` \| `a11y` | — |
| `title` | full title, no truncation (a page title **is** the signal) | `""` when the backend has none |
| `class` | platform class string, normalised | `a11y` |
| `rect` | outer rect, context px, virtual-screen origin (negative legal) | — |
| `iconic` | minimised / unmapped-but-alive | `a11y` (use `visible`) |
| `foreground` | the context's input focus owner; **exactly one true**, or none if the context has no focus | — |
| `owner_pid` | owning process id | — (0 if unknown, with `"owner_pid_known":false`) |
| `hwnd` | Windows-only compat integer, equals `native.hwnd` | every non-`wts` context |

Enumeration rules (neutral): top-level windows only (children/`WS_CHILD`/`override-redirect`/popups excluded unless `kind` filter is added later), z-order/stacking order, hard cap 500 with `truncated:true`. On X11, `IconicState` comes from `WM_STATE` (EWMH); on Windows, `IsIconic`; on Wayland, `zwlr_foreign_toplevel.minimized` when the compositor exposes it.

---

## 8. P4 — window control

`POST /api/v1/agents/{id}/ui-window` `{"context":"x11::0","window":"0x3c00007","action":"foreground"}`

```json
{ "ok": true, "data": {
  "context_ref": {"requested":"x11::0","resolved":"x11::0","kind":"x11","source":"selector"},
  "window": "0x3c00007",
  "action": "foreground",
  "state": {"iconic":false,"foreground":true,"closed":false},
  "observed_ms": 84 } }
```

**`ok` means observed, not attempted.** The request reached the context (HTTP 200); `ok:true` asserts a re-read of the platform state after acting. A window that refuses the foreground returns `200 {"ok":false,"reason":"FOREGROUND_DENIED","state":{…observed…}}`. This is the direct fix for the measured lie (`pointer-click` answering `success:true` while the click landed elsewhere).

| action | params | Windows (`wts`) | X11/EWMH | Wayland (foreign-toplevel) | Android |
|---|---|---|---|---|---|
| `foreground` | — | `ShowWindow(SW_RESTORE)` if iconic → `SetForegroundWindow` → documented `AttachThreadInput`/`BringWindowToTop` fallback, poll 300 ms; verified `GetForegroundWindow()==hwnd` | `_NET_ACTIVE_WINDOW` client message (source=2), `XRaiseWindow` fallback, poll `_NET_ACTIVE_WINDOW` root property | `activate` request; verified via `activated` signal | refused (`OS_FORBIDDEN`) |
| `minimize` | — | `ShowWindow(SW_MINIMIZE)`; verified `IsIconic` | `WM_CHANGE_STATE IconicState` (ICCCM), verified `WM_STATE` | `set_minimized(true)` | refused |
| `restore` | — | `ShowWindow(SW_RESTORE)`; verified `!IsIconic` | `_NET_ACTIVE_WINDOW` + map (no ICCCM "deiconify" verb); verified `NormalState` | `set_minimized(false)` | refused |
| `move` | `x,y` | `SetWindowPos(SWP_NOSIZE\|SWP_NOZORDER\|SWP_NOACTIVATE)`; verified rect origin | `XMoveWindow`; verified `XGetGeometry` translate | **refused** `NOT_EXPOSED` — the protocol has no move | refused |
| `resize` | `w,h` | `SetWindowPos(SWP_NOMOVE\|…)`; verified rect size | `XResizeWindow`; verified `XGetGeometry` | **refused** `NOT_EXPOSED` | refused |
| `close` | — | `PostMessage(WM_CLOSE)` — **never** `TerminateProcess`; verified absent within 2000 ms | `_NET_CLOSE_WINDOW` client message; verified `DestroyNotify`/absence | `close` request | refused |

Per-action availability is advertised (`features["window.control"]` is a **list**), so a caller that sees `move` missing on Wayland learns it from `/contexts`, not from a failure mid-flow. Rules: `window` validated inside the resolved context before any action (`404 WINDOW_NOT_FOUND`, `detail{resolved_context, window}`); unknown action → `400 INVALID_PARAMS` with `detail.allowed`; `state.closed` is additive because a successful `close` is otherwise unobservable.

---

## 9. P5 — pointer, keyboard, text

All seven endpoints share: `context` (§5), `window` (relative coords + hit-test), and the same result envelope.

### 9.1 Geometry and the occlusion rule (the measured bug, generalised)

- `window` absent → `x,y` are absolute context px, exactly as today (`platform_windows.go:436`, `platform_linux.go:237`).
- `window` present → `x,y` are **relative to `window.rect` origin**, resolved **at dispatch time** (a window that moved between `ui-tree` and the click cannot misplace it).
- **Occlusion hit-test, always on, no knob:** compute the absolute point → ask the platform what is on top there (`WindowFromPoint` / `XQueryPointer`+`XTranslateCoordinates` / Wayland `OS_FORBIDDEN` / a11y `OS_FORBIDDEN`). If the top window is not the requested one, **the click is not performed**:

```json
{ "ok": false, "reason": "OCCLUDED",
  "hit": {"id":"0x3c0011a","title":"…:11.11 SYSTEM LAYOUT","owner_pid":3180},
  "detail": {"intended":"0x3c00007","point":{"x":42,"y":96}} }
```

HTTP 200 (the request ran; the input intentionally did not), `ok:false`. The caller re-reads `ui-tree` and acts on the truth.

- `window` present but not in the resolved context → `404 WINDOW_NOT_FOUND`, before any injection.
- `window` present but not focused for `text-input`/`key-*` → the primitive foregrounds it **only if `focus:true`** is sent (additive field, default false); otherwise `403 PRIMITIVE_REFUSED reason:"NO_FOCUS_TARGET"`. Rationale: stealing focus during typing is a silent context switch, and silent context switches are the class of bug this campaign exists to kill.

### 9.2 `count=2` is a real double-click

One primitive call emits `down,up,down,up` with the gap **≤ 50% of the platform's double-click interval** (`GetDoubleClickTime()` on Windows, `XkbSetDetectableAutoRepeat`-independent multi-click time on X11 via `XGetPointerControl`/default 400 ms, seat's configured interval on Wayland). The gap is echoed as `detail.gap_ms`, and the response is `{ok:true,count:2,gap_ms:120}`. Two separate `pointer-click` calls are **not** equivalent and the contract says so, because the app decides on timing.

### 9.3 Scroll sign convention (one convention, mapped per backend)

`delta` is in **notches**; **negative = up / away from the user / toward the beginning**. The backend maps, it does not reinterpret:

| backend | mapping for `delta=-1` (one notch up) |
|---|---|
| Windows | `mouse_event(MOUSEEVENTF_WHEEL, dwData = -120)` — Windows' own positive wheel is *away*, so `dwData = delta*120` after the sign flip is stated here explicitly |
| X11 | `XTestFakeButtonEvent(button 4, press, release)` once per notch |
| Wayland (portal/libei) | `ei_scroll` with `v120 = delta*120`; if the portal exposes no scroll capability → `PRIMITIVE_UNSUPPORTED reason:"BACKEND_MISSING"` |
| Android (a11y) | `ACTION_SCROLL_FORWARD` for `delta<0` on the node under the point; node not scrollable → `PRIMITIVE_REFUSED reason:"NOT_SCROLLABLE"` |

`unit:"px"` is refused with `PRIMITIVE_UNSUPPORTED reason:"UNIT_NOT_SUPPORTED"` on every backend that lacks pixel-precise wheels — a partial scroll is worse than a refusal because it moves the list by an unknown amount.

### 9.4 Unicode text (today: bytes)

| backend | method | fidelity |
|---|---|---|
| Windows (`wts`) | `SendInput` with `KEYEVENTF_UNICODE`, one event per **UTF-16 code unit** (surrogate pairs are two events, which is what the OS expects) | all of Unicode incl. emoji; layout-independent |
| X11 | remap a spare keycode to `0x01000000+codepoint` (`XChangeKeyboardMapping`, spare keycode discovered by scanning the map), `XTestFakeKeyEvent`, restore map after the string | all of UCS-2; astral planes where the keycode range allows, else `PRIMITIVE_UNSUPPORTED reason:"CODEPOINT_UNSUPPORTED"` with the codepoint named |
| Wayland | `zwp_text_input_v3.commit_string` if the focused surface has a text-input v3 object; otherwise `PRIMITIVE_REFUSED reason:"NO_TEXT_INPUT_PROTOCOL"` | full Unicode **only when the app exposes the protocol**; no keyboard-emulation fallback (there is none on Wayland) |
| Android (Termux) | no IME, no a11y → refused `PRIMITIVE_REFUSED reason:"NO_INPUT_METHOD"` | — |

`encoding:"utf16"` means the caller supplies code units and probe must **not** re-encode: on Windows it emits them verbatim; elsewhere it decodes to UTF-8 first and reports `detail.transcoded:true`. Never silently lossy.

---

## 10. P6 — capture

`POST /api/v1/agents/{id}/capture`

```json
{ "context":"wts:7", "window":66102, "format":"png", "hash_only":true }
```

```json
{ "ok": true, "data": {
  "context_ref": {"requested":"wts:7","resolved":"wts:7","kind":"wts","source":"selector"},
  "target": {"kind":"window","window":"66102","id_kind":"hwnd"},
  "format": "png", "width": 1038, "height": 118,
  "hash": "sha256:4f9c…", "changed": true, "baseline": "sha256:1b2e…",
  "source": "window_direct",
  "units": "px", "dpi_scale": 1.0,
  "data": "<base64 omitted when hash_only>", "size_bytes": 0 } }
```

| field | semantics (exact) |
|---|---|
| `hash` | `sha256:` + hex of the **decoded RGBA8 pixels**, stride-normalised to `width*4` — **not** of the encoded file. Re-encoding the same pixels to jpeg must not change the hash; a 1-pixel change must |
| `changed` | `true`/`false` vs the baseline; **`null` when there is no baseline** (first sight of this target, or cache evicted). Never fabricated |
| `baseline` | which hash was compared; `""` when none |
| `width`,`height` | pixels actually captured (a window may have changed size since `ui-tree` — the tree is not re-read for you) |
| `source` | `window_direct` (content read from the window, occlusion-independent) \| `screen_region` (pixels lifted from the screen; occlusion **does** affect it) \| `compositor` |
| `data` | base64 image, **omitted** when `hash_only:true` |
| `format` | `png` or `jpeg`; if the backend can only emit one format, the requested other one is encoded server-side from raw pixels and `detail.encoded_by:"agent"` |

Baseline cache: per `(context, target, dpi_scale)` key, LRU 64 entries, TTL 600 s, in-agent, never persisted. A `since` hash on the request overrides the stored baseline for that call (stateless use: hash-only polling from a flow that keeps its own state).

Backends:

| target / OS | Windows | X11 (+headless Xvfb) | Wayland | Android |
|---|---|---|---|---|
| full screen | `GetDC(0)`/`BitBlt` | `XGetImage(root)` | portal `ScreenCast` (consent) → `PRIMITIVE_REFUSED reason:"CONSENT_REQUIRED"` without it | `MediaProjection` → same consent rule |
| window | `PrintWindow(PW_RENDERFULLCONTENT)` → `source:"window_direct"`; if it returns an unusable surface, fall back to screen-region of `rect` and say `source:"screen_region"` | `XGetImage(window)`; redirected windows need `XCompositeNameWindowPixmap` → `source:"compositor"` | not possible per-window without portal window-cast → `PRIMITIVE_UNSUPPORTED reason:"NOT_EXPOSED"` | refused |
| region | `BitBlt(rect)` | `XGetImage(rect)` | portal screen-cast full frame + crop → `source:"screen_region"` | refused |

Cost guard: `w*h > 8_294_400` (4K) → `400 INVALID_PARAMS reason:"REGION_TOO_LARGE"` with `detail.max_px`; no silent downscale (a downscaled capture that claims to be the window is a second truth).

---

## 11. Per-primitive × platform backend and refusal matrix (acceptance quote #2)

Legend: **✓backend** = implemented; **R:CODE/reason** = typed refusal; the code is what the caller catches.

| primitive | Windows `wts` | Linux X11 (+headless Xvfb) | Wayland | Android (Termux, today) | Android (a11y APK, future) |
|---|---|---|---|---|---|
| **P1 selector** on `exec` | ✓ `CreateProcessAsUserW` + WTS token | ✓ fork+setuid (root) / direct (same uid) | ✓ same uid only; other seat → R:`CONTEXT_UNAVAILABLE/OTHER_USER` | ✓ same uid; other uid → R:`CONTEXT_UNAVAILABLE/PRIVILEGE_NOT_HELD` | ✓ `am`/a11y service scope |
| **P1 selector** on UI ops | ✓ `--context-worker` | ✓ direct `Display*` | ✓ same seat only; cross-seat → R:`CONTEXT_UNAVAILABLE/KIND_NOT_APPLICABLE` | R:`PRIMITIVE_UNSUPPORTED/NO_BACKEND` (no surface exists) | ✓ direct |
| **P2 contexts** | ✓ `WTSEnumerateSessions` + `WTSQuerySessionInformationW` | ✓ `DISPLAY` sockets + `~user/.Xauthority` + peer uid | ✓ seats + peer credentials | ✓ own uid + root-visible profiles | ✓ `UserManager` + `DisplayManager` |
| **P3 window tree** | ✓ `EnumWindows` in worker | ✓ EWMH `_NET_CLIENT_LIST` (+`WM_STATE`) | ✓ **only if** `zwlr_foreign_toplevel_management_v1`; else R:`PRIMITIVE_REFUSED/NO_GLOBAL_WINDOW_LIST` | R:`PRIMITIVE_UNSUPPORTED/NO_BACKEND` | ✓ `AccessibilityService.getWindows()` |
| **P4 control** | ✓ six actions | ✓ six actions (EWMH pending: no `move` on some WMs → `ok:false NOT_SUPPORTED` observed) | ✓ foreground/minimize/restore/close; move/resize → R:`PRIMITIVE_UNSUPPORTED/NOT_EXPOSED`; without the protocol → R:`PRIMITIVE_REFUSED/NO_GLOBAL_WINDOW_LIST` | R:`PRIMITIVE_UNSUPPORTED/NO_BACKEND` | ✓ `performAction` on nodes (cross-app) |
| **P5 pointer** | ✓ `SetCursorPos`+`mouse_event` | ✓ XTEST (`XTestFakeMotionEvent`/`ButtonEvent`) | ✓ libei/portal, consent required → R:`PRIMITIVE_REFUSED/CONSENT_REQUIRED`; no portal → R:`PRIMITIVE_UNSUPPORTED/BACKEND_MISSING` | R:`PRIMITIVE_UNSUPPORTED/NO_BACKEND` | ✓ `dispatchGesture` |
| **P5 scroll** | ✓ `MOUSEEVENTF_WHEEL` | ✓ XTEST buttons 4/5/6/7 | ✓ `ei_scroll` v120; else R:`PRIMITIVE_UNSUPPORTED/BACKEND_MISSING` | R:`PRIMITIVE_UNSUPPORTED/NO_BACKEND` | ✓ `ACTION_SCROLL_*`; non-scrollable → R:`PRIMITIVE_REFUSED/NOT_SCROLLABLE` |
| **P5 drag** | ✓ down→moves→up | ✓ XTEST sequence | ✓ `ei_button` + motion, consent | R:`PRIMITIVE_UNSUPPORTED/NO_BACKEND` | ✓ `dispatchGesture` |
| **P5 unicode text** | ✓ `KEYEVENTF_UNICODE` | ✓ keycode remap + XTEST | ✓ `text_input_v3` only; else R:`PRIMITIVE_REFUSED/NO_TEXT_INPUT_PROTOCOL` | R:`PRIMITIVE_REFUSED/NO_INPUT_METHOD` | ✓ `ACTION_SET_TEXT` |
| **P6 capture** full | ✓ | ✓ | ✓ portal, consent | R:`PRIMITIVE_REFUSED/CONSENT_REQUIRED` (no MediaProjection in a Termux process) | ✓ MediaProjection + consent |
| **P6 capture** window/region | ✓ `PrintWindow` / `BitBlt` | ✓ `XGetImage` / `XComposite` | window R:`PRIMITIVE_UNSUPPORTED/NOT_EXPOSED`; region ✓ via portal+crop | R:`PRIMITIVE_UNSUPPORTED/NO_BACKEND` | window ✓ (a11y node bounds + projection), region ✓ |

**Refusal codes are three, not ten** (a caller catches code; `reason` carries the detail):

| code | HTTP | when | reasons |
|---|---|---|---|
| `CONTEXT_UNAVAILABLE` | 409 | the requested context cannot be resolved/entered | §5.4 enum |
| `PRIMITIVE_UNSUPPORTED` | 501 | this build/backend has no implementation of the primitive | `BACKEND_MISSING` · `NOT_IMPLEMENTED` · `NO_BACKEND` · `NOT_EXPOSED` · `HEADLESS_NO_DISPLAY` · `UNIT_NOT_SUPPORTED` · `CODEPOINT_UNSUPPORTED` |
| `PRIMITIVE_REFUSED` | 403 | the platform's own security model forbids it for this principal | `OS_FORBIDDEN` · `CONSENT_REQUIRED` · `PERMISSION_NOT_GRANTED` · `NO_GLOBAL_WINDOW_LIST` · `NO_TEXT_INPUT_PROTOCOL` · `NO_INPUT_METHOD` · `NO_FOCUS_TARGET` · `NOT_SCROLLABLE` |

Existing codes stay as they are: `INVALID_PARAMS` 400 · `WINDOW_NOT_FOUND` 404 · `UNSUPPORTED_AGENT` 409 · `TIMEOUT` 504 · `PROTOCOL_VIOLATION` 502 · `INTERNAL_ERROR` 500.

**"Can't" vs "may not" is the distinction that keeps this honest:** 501 means the code is not there yet (a build/deployment fact, fixable by shipping); 403 means the OS says no to this principal (a policy fact, not a bug). Neither is ever a silent stub.

---

## 12. Security boundary (contexts per privilege, per platform)

| principal | own context | other context, **same user** | other context, **different user** | context with no principal | non-interactive context |
|---|---|---|---|---|---|
| Windows **SYSTEM** (service) | ✓ direct | ✓ token, audited | ✓ token, audited with target user | `CONTEXT_UNAVAILABLE/NO_USER` | `NOT_INTERACTIVE` |
| Windows **user** | ✓ direct | ✗ `PRIVILEGE_NOT_HELD` (SeTcbPrivilege) | ✗ `OTHER_USER` | `NO_USER` | `NOT_INTERACTIVE` |
| Linux **root** | ✓ | ✓ `setuid` child, audited | ✓ `setuid` child, audited with target uid | ✗ `NO_USER` | ✗ `NOT_INTERACTIVE` |
| Linux **user** | ✓ | ✗ `PRIVILEGE_NOT_HELD` | ✗ `OTHER_USER` | ✗ `NO_USER` | ✗ `NOT_INTERACTIVE` |
| Android **root** | ✓ | ✓ `setuid` child | ✓ `setuid` child, audited | ✗ `NO_USER` | ✗ `NOT_INTERACTIVE` |
| Android **app uid** (Termux) | ✓ (own uid) | ✗ `PRIVILEGE_NOT_HELD` | ✗ `OTHER_USER` | ✗ `NO_USER` | ✗ `NOT_INTERACTIVE` |

Why the strong principals may cross and the weak ones may not: a SYSTEM service already holds `SeTcbPrivilege`; root already holds `CAP_SETUID`. Entering an interactive session is **not an escalation of anything they lack**. A user agent would **gain** privilege, so the answer is no, permanently and without a config knob. There is no `as:`-style declaration for contexts: the predicate is the process principal, nothing else.

Corollary the implementer must not "improve": a non-privileged agent cannot borrow a service agent's context through the server. The server forwards to one agent; that agent's principal decides. Cross-agent context borrowing would be a new lane — refused by construction.

**Audit row** (`internal/server/audit.go:14-29`, additive fields only):

| field | type | written when |
|---|---|---|
| `context_requested` | object `{id,kind}` | whenever a selector was sent |
| `context_resolved` | object `{id,kind}` | whenever resolution succeeded (null on refusal) — copied **from the agent's echo**, never from the request |
| `context_user` | string | when the resolved context has a principal |
| `context_reason` | string | on refusal (closed enum) |
| `feature` | string | the primitive: `context.selector` `window.tree` `window.control` `input.pointer` `input.text` `capture` |
| `backend` | string | the backend actually used: `wts` `x11-xtest` `wl-foreign-toplevel` `ei` `a11y` `xgetimage` `printwindow` |
| `window_id` + `id_kind` | string | any window-scoped op |
| `target_hash` | string | `capture` (and only the hash — never the pixels) |
| `text_len` + `text_sha256` | int/string | `text-input`: **length and digest, never the plaintext** (an HMI operator types passwords; the audit must not become the leak) |
| `ok`,`ran`,`error_code`,`reason`,`duration_ms` | — | existing + `ran` (false when nothing was injected) |

Recording rule (closed list, the reviewer tests it):
1. every refusal (`result:"denied"`), whatever the endpoint;
2. every `ui-window` call, success or not;
3. every `contexts` read (count in `params`, never the user list);
4. every call whose `context_resolved != agent_context`;
5. every `text-input` (digest row only);
6. nothing else — a window read in the agent's own context is not written (audit volume, and it reveals nothing the operator's own screen does).

---

## 13. Capability advertisement (a caller never guesses)

Three layers, all additive to the existing flat `Capabilities []string` (`messages.go AgentInfo`, `registry.go:165`):

**Layer 1 — flat strings (unchanged, back-compat):** `exec`, `filesystem`, `capture`, `input`, plus `ui` and `context` when the new features exist. An old server keeps working; an old client keeps working.

**Layer 2 — structured feature matrix (new, on `AgentInfo` and echoed by `health`):**

```json
{ "features": {
    "v": 1, "platform": "linux", "backend_family": "x11",
    "context": {"selector": true, "enumerate": true, "kinds": ["x11","tty"]},
    "window": {"tree": true, "id_kind": "xid",
               "control": ["foreground","minimize","restore","move","resize","close"]},
    "input": {"pointer": ["move","click","scroll","drag"],
              "text": {"unicode": true, "method": "keycode_remap", "encoding": ["utf8","utf16"]},
              "key": ["press","combo"], "doubleclick": true},
    "capture": {"formats": ["png","jpeg"], "window": true, "region": true,
                "hash": "sha256-rgba8", "source": ["window_direct","compositor","screen_region"]},
    "limits": {"max_windows": 500, "max_capture_px": 8294400,
               "scroll_unit": "notch", "dpi_awareness": "x11_native"},
    "refusals": [] } }
```

`refusals` is a list of `{feature, code, reason}` the agent **knows it cannot do here** (e.g. `{"feature":"capture.window","code":"PRIMITIVE_UNSUPPORTED","reason":"NOT_EXPOSED"}` on a Wayland session). Advertising a refusal up front is worth more than a fast failure: a flow can route around it before it starts.

**Layer 3 — per-context overrides:** `GET /contexts` carries `features` per row (§6). The agent-level matrix is the union; a specific context may support strictly less (the `uid` row above supports nothing UI). **Callers read the context row, not the agent matrix, before acting in that context** — that sentence is the whole advertisement contract.

**Server-side use:** `capabilityForCommand` (`enrollment.go:414-435`) gains granular return values for the new commands (`context.selector`, `window.tree`, `window.control`, `input.pointer`, `capture.window`…), and the existing pre-dispatch check (`server_api.go:789-799`) refuses `409 UNSUPPORTED_AGENT` with `detail{feature, agent_version, ran:false}` when the matrix lacks it.

**One existing rule must NOT be inherited — this is the trap in the current gate.** `server_api.go:791-796` reads: *"Agents with no capabilities advertised are treated as having all capabilities (backward compat)"*. For the four coarse buckets that is defensible. For the six primitives it is precisely the silent fallback this contract exists to kill: an old-device agent that advertises nothing would be **allowed** to receive `ui-window{action:"close"}` and would ignore the unknown fields. Therefore:

| command class | empty/unadvertised capability | why |
|---|---|---|
| coarse (existing `exec`/`filesystem`/`capture`/`input`) | allowed — unchanged | 1.19/1.20 fleet compat, no behaviour change permitted |
| granular (the six primitives, all new message types) | **refused** `409 UNSUPPORTED_AGENT`, `detail{feature, missing:"features", ran:false}` | a new command type never existed in an old agent; "empty means all" would be a lie about a message the agent's dispatcher cannot even parse |

Mechanically: `capabilityForCommand` returns `""` (no requirement) for control messages and coarse buckets as today; for the new types it returns a granular key **and** sets `granular=true`, and the gate refuses when `granular && len(features)==0`. Absence of evidence is not evidence of support.

`GET /api/v1/agents/{id}/capabilities` exposes the matrix to callers: `{features, as_of, source:"registration"|"health", stale:bool, agent_version}`. `stale:true` when the last health is older than 90 s (spec §P2.3's dead-agent signal, consumed here rather than duplicated).

---

## 14. Worked sequence — "bring window X forward, click relative to X, read X's title", zero `exec` (acceptance quote #5)

X11 lane (a Windows lane is identical with `wts:7` and `hwnd` ids):

```
1  GET  /api/v1/agents/AG1/contexts
   → contexts[0] = {id:"x11::0", user:"flk", addressable:true,
                    features:{"window.tree":true,"window.control":[6 actions],"capture.window":true}}

2  POST /api/v1/agents/AG1/ui-tree   {"context":"x11::0","visible_only":true}
   → windows[3] = {id:"0x3c00007", title:"…/Control Panel", class:"Qt51516QWindowIcon",
                   rect:{x:0,y:0,w:1038,h:118}, iconic:false, foreground:false, owner_pid:2640}

3  POST /api/v1/agents/AG1/ui-window {"context":"x11::0","window":"0x3c00007","action":"foreground"}
   → 200 {"ok":true,"state":{"iconic":false,"foreground":true,"closed":false},"observed_ms":81}

4  POST /api/v1/agents/AG1/ui-tree   {"context":"x11::0","window":"0x3c00007"}
   → [{id:"0x3c00007", rect:{x:0,y:0,w:1038,h:118}, foreground:true}]
     (re-read: a restore can move the window — never reuse a stale rect)

5  POST /api/v1/agents/AG1/pointer-click
   {"context":"x11::0","window":"0x3c00007","x":42,"y":96}
   → absolute = rect origin + (42,96) resolved at dispatch; hit-test passes; 200 {"ok":true,"count":1}

6  POST /api/v1/agents/AG1/ui-tree   {"context":"x11::0","visible_only":true}
   → windows[7] = {id:"0x3c0011a", title:"11.11 SYSTEM LAYOUT", foreground:true}
     (title read from the tree — no exec, no OCR, no screenshot)
```

Six calls, zero `exec`, zero P/Invoke/shell, and every response names the context it acted in. If step 5's point were covered by another window, it returns `ok:false OCCLUDED` naming the covering window — instead of today's `{"success":true}`.

Wayland variant, same flow: step 1 succeeds (seat), step 2 succeeds **only** if the compositor exposes foreign-toplevel; step 3 succeeds only for `foreground|minimize|restore|close`; step 5 returns `403 PRIMITIVE_REFUSED/CONSENT_REQUIRED` if no portal — the caller learns that at step 1 from `features` and `refusals`, before it starts.

---

## 15. Edge cases and failure paths (each with a named outcome)

| # | case | outcome |
|---|---|---|
| X1 | context vanishes between ladder step V1 and V6 | `CONTEXT_UNAVAILABLE/GONE`; no retry, no fallback to own context |
| X2 | worker hangs (GPU driver, modal loop) | 5 s (ui-tree 3 s) → terminate → `504 TIMEOUT`, `detail.ran:false`; for `ui-window` `ran:true, applied:false, state:null` (an unobservable action is reported as unknown, never as success) |
| X3 | window destroyed between tree and click | `404 WINDOW_NOT_FOUND` (validated before injection) |
| X4 | window recreated under the same native handle | `id` is context-scoped and re-read; the response echoes `class+owner_pid`, and `ui-window`/`input` re-read them, so a swap is visible to the caller. Documented limitation: a handle is not stable identity; `class+title+pid` is |
| X5 | window moves between tree and click | `window`-relative coords resolve `rect` at dispatch — safe by construction; absolute mode is documented as caller's risk |
| X6 | DPI change mid-session (RDP reconnect, monitor swap) | every response echoes `dpi_scale`; a change between two `ui-tree` reads is visible as a rect discontinuity; capture hash changes because pixels changed — never silently rescaled |
| X7 | fractional scaling (Wayland 1.5×) | `units:"px"` is physical; `dpi_scale` echoes 1.5; window rect and input share the space, so clicks land. If the backend cannot report physical px → `PRIMITIVE_UNSUPPORTED/NOT_IMPLEMENTED` rather than a guess |
| X8 | foreground lock (elevated window, other app) | `ok:false FOREGROUND_DENIED` with observed state; the click is not attempted while the target is not foreground and the hit-test fails |
| X9 | screen locked / screensaver | Windows: capture returns the lock surface and `foreground` fails → observed `ok:false`; X11: `XGetImage` on a locked session yields the locker window; reported honestly, no attempt to bypass |
| X10 | two operators drive one context | per-context mutex serialises each call (shared by ui/input/capture of that context); the contract does not claim atomicity across calls — that is a flow concern |
| X11 | portal consent revoked mid-flow (Wayland) | next call → `403 PRIMITIVE_REFUSED/CONSENT_REQUIRED`; no retry loop |
| X12 | old agent in the fleet (1.19/1.20) | server pre-dispatch `409 UNSUPPORTED_AGENT`, `ran:false`, before anything is sent |
| X13 | headless host with `DISPLAY` unset | `PRIMITIVE_UNSUPPORTED/HEADLESS_NO_DISPLAY`; `exec`/`filesystem` unaffected |
| X14 | non-root agent asked for another uid (Linux) | `CONTEXT_UNAVAILABLE/PRIVILEGE_NOT_HELD` before any `setuid` attempt |

Graceful failure, component by component: server handler → typed status + `ran:false`; agent verification → typed code + remediation + audit row; worker → one JSON object, non-zero exit only if it cannot speak; worker crash → EOF mapped to `INTERNAL_ERROR` with `detail.worker_exit`; timeout → kill + 504; absent platform backend → advertised refusal at `/contexts` and `/capabilities`, and the same code if called anyway.

---

## 16. Future projections

**1 month — one station, one operator.** The Windows service lane on EAS1 is the first deployment; the Linux X11 backend ships in the same release because the contract is written once. Volume ~200 tree calls + ~60 clicks/day; one worker per call at 40-80 ms is invisible against HMI latency. Risk: the operator's reflex is still `exec` + OS scripting for anything not in the matrix — mitigation is the worked sequence (§14) and the `refusals` advertisement, not documentation.

**6 months — 4-6 stations, mixed OS (three Windows HMIs, two Linux panels, one Android tablet).** ~5k UI calls/day, `/contexts` polled every 30 s by a fleet dashboard. Two strains: (a) worker spawn becomes p95 if input rate rises — measurement point is *sustained >5 calls/s per station*; below that, pooling is complexity with no payoff (v1 D2, held); (b) the per-context `features` matrix becomes the dashboard's core column ("who is at which HMI, and what can be done there") and must stay derived from `classifyContext`, never recomputed server-side. The Android lane is refused by design until an APK with an AccessibilityService exists — a tablet in the fleet is an exec+fs node, and that is stated, not hidden.

**1 year — context becomes a fleet concept.** Flows pinned to "the context where operator X is on watch", station handover (7 → 11) mid-run, and `window.id` as the addressing key shared by tree, input and capture, so a flow discovers a window once and acts without coordinates. If a second desktop/winstation or a nested compositor appears, the selector generalises to `{context, desktop}` — the ladder is the seam (V6 already passes `lpDesktop`), so it is a parameter extension. What would force a redesign: UI operations with **no logged-on principal** (a headless spooler): the worker model has no token to run under, and the honest answer is a different product, not a patch.

---

## 17. Decision log

| # | decision | why | cost accepted |
|---|---|---|---|
| N1 | `context` is an opaque advertised string; `session` is an alias | one wire format for four platforms; kind-specific ids stay inside the agent | callers handle a string, not an int |
| N2 | absent selector = own context; present = exact or refused | 0 is a legal session id and "absent" must differ from "0" | a pointer/absence distinction on the wire |
| N3 | three refusal codes (`CONTEXT_UNAVAILABLE`, `PRIMITIVE_UNSUPPORTED`, `PRIMITIVE_REFUSED`), reasons in a closed enum | a caller catches a code, not eleven strings; "can't" vs "may not" stays visible | a wider `detail` object (cheap) |
| N4 | `PRIMITIVE_REFUSED` = 403, `PRIMITIVE_UNSUPPORTED` = 501 | 403 means policy said no; 501 means the code is not there — different operators, different fixes | two codes instead of one |
| N5 | window `id` opaque + `id_kind` + `native`, `hwnd` compat on `wts` only | one shape for hwnd/XID/toplevel/a11y; the compat field keeps 1.21 rows unchanged | one extra field per row |
| N6 | `window`-relative coords + always-on occlusion hit-test | the measured bug (covered click answered success) is OS-independent | a legitimate click on an overlapped point is refused; caller uses absolute mode |
| N7 | `ok` = observed, not attempted; 200 = the request reached the context | P8-03 A-1 carried to UI; the operator's complaint was a lie, not a failure | callers must read `ok`, not only the status |
| N8 | `count:2` is one primitive call with a platform-timed gap | two calls are not a double-click; apps decide on timing | one field, one code path |
| N9 | scroll `delta` in notches, negative = up, per-backend mapping documented | one convention beats four; `unit:"px"` is refused where fidelity is absent | callers cannot ask for pixels everywhere |
| N10 | capture hash is over **pixels**, not bytes; `changed:null` without baseline | re-encoding must not look like a change; a fabricated `false` is worse than `null` | agent keeps a 64-entry baseline cache |
| N11 | `source:"window_direct"|"screen_region"|"compositor"` in every capture | occlusion susceptibility differs per backend; the caller must know which truth it has | one more field |
| N12 | capability matrix is structured and **per context**, flat strings kept | a caller can route before acting; old server/client unaffected | a second advertisement shape to keep in sync (tested) |
| N13 | audit stores `text_sha256` + length, never plaintext | operators type credentials into HMIs | text content is not forensically available (accepted) |
| N14 | no config knob anywhere; predicates are the process principal | a knob is a second policy place and a silent-fallback surface | no per-site exceptions (by design) |
| N15 | per-context mutex shared by tree/control/input/capture | input and foreground cannot race | a slow capture blocks a click in that context (correct) |
| N16 | Wayland/Android advertise refusals **up front** rather than erroring late | a flow can route around a platform limit before it starts | the matrix must be honest at registration, or it becomes the new lie |

---

## 18. Build phases (phase 1 shippable alone) and tests

| phase | content | starts without reading later phases |
|---|---|---|
| **1** | protocol: `Context *string` + `Session *int` on `ExecParams`/`InputParams`, new params/results, three codes + reason enums, `features` matrix struct, `capabilityForCommand` granular entries, agent E3 dispatcher check, server E1 gate + E2 echo assertion, handlers that refuse when the feature is absent | **yes** — behaviour-neutral ship: no selector = today's behaviour byte-for-byte |
| 2 | `contexts` + `classifyContext` + per-platform enumeration (Windows WTS, X11 sockets/peer-uid) — no worker needed: enumeration is answerable from any context | phase 1 types only |
| 3 | `ui-tree` + Windows `--context-worker` + X11 direct backend | needs the ladder from phase 2 |
| 4 | `ui-window` (six actions, observed-`ok`, id validation) | yes |
| 5 | input: `window`-relative + hit-test + `count` + `scroll` + `drag` + `pointer-move` + Unicode text | yes |
| 6 | `capture`: window/region/format/hash_only + baseline cache | yes |
| 7 | `capabilities` endpoint + matrix, docs (`docs/PRIMITIVES.md`, backend matrix, remediation strings) | — |

Tests that must fail on old behaviour: refusal before dispatch on a feature-less agent (409, `ran:false`); each `CONTEXT_UNAVAILABLE` reason (9 cases, incl. `KIND_NOT_APPLICABLE` for `session:7` on X11 and `NO_USER`); missing `context_ref` echo → 502; `ui-tree{window}` filter and `truncated`; all six `ui-window` actions with a simulated `FOREGROUND_DENIED` → `ok:false` + observed state; occlusion refusal naming the covering window; `count:2` gap within the platform interval; scroll sign per backend (up on all five); Unicode round-trip `àèìòù — € 名前 🙂` (4 backends, refusals where advertised); `hash_only` returns no `data` and `changed:null` on first call; identical pixels re-encoded png→jpeg keep the same hash; `detach+context` 400; selector on `stream-start` 400; no-selector regression on all 11 endpoints (identical envelopes to base `ec647ed`).

---

## 19. Verifier appendix (re-runnable at review time, no unreviewable claims)

```bash
cd /opt/data/projects/PROBE_UPGRADE/v119wt
git log --oneline -1                                   # base-level anchor for this review

# 1 the substrate claims of §2 still hold (or the contract's premise moved)
grep -c '"scroll"' internal/protocol/messages.go       # 0 at ec647ed
grep -rn 'ui-tree\|ui_window\|uiwindow' --include=*.go internal/ | grep -v _test | wc -l   # 0
grep -n 'type InputParams' -A 8 internal/protocol/messages.go        # X,Y,Button,Text,Key,Keys only
grep -n 'for i := 0; i < len(text); i++' internal/platform/platform_windows.go  # byte loop
grep -n 'xdotool' internal/platform/platform_linux.go  # external binary, not a backend
grep -n '"android": true' internal/server/builder.go    # Termux build target, no a11y service

# 2 prohibited mechanisms (§5.3) — must be empty
grep -rn 'CreateRemoteThread\|NtCreateThreadEx\|CreateProcessWithLogonW\|SetThreadDesktop\|pkexec\|runas' \
     --include=*.go internal/ cmd/ | grep -v _test

# 3 the contract's own artefacts
grep -c 'CONTEXT_UNAVAILABLE\|PRIMITIVE_UNSUPPORTED\|PRIMITIVE_REFUSED' docs/contracts/P10-01_NEUTRAL_CONTRACT.md
grep -n 'PRIMITIVE_UNSUPPORTED\|PRIMITIVE_REFUSED' internal/protocol/messages.go   # after phase 1: present

# 4 build + suite, cross-OS (the neutrality is only real if it compiles everywhere)
go build ./... && GOOS=windows GOARCH=amd64 go build ./... \
  && GOOS=linux GOARCH=arm64 go build ./... && GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build ./...
go vet ./internal/agent ./internal/server ./internal/platform ./internal/protocol
go test ./internal/agent ./internal/server ./internal/protocol -count=1 -run 'Context|UITree|UIWindow|Pointer|Capture' -v
go test ./... -count=1 -timeout 1500s                  # full suite; re-record the pass/skip counts at review time
                                                       # (v1 recorded 458 pass / 1 skip at 85853f3 — treat as historical, not as this review's number)
```

Platform-specific tests are build-tagged (`//go:build windows`, `//go:build linux && !android`) so the Linux suite keeps whatever pass/skip counts it holds at review time (re-record them, do not inherit a number); the four cross-compiles above are what prove a primitive has an implementation or a declared refusal on every target.

**Design-host note (honesty about evidence class):** `go` is not installed on this design host (`go: command not found`), so no build or test was run for this contract. §2's claims are `grep`/`sed` reads of the tree at `ec647ed`; the commands above are review-time gates, not things I ran. Prohibited-mechanism grep **was** run at base: clean (no matches), so §5.3's ban list starts from zero.

---

## 20. Open decisions (ratify or overturn before build)

| # | question | architect's default |
|---|---|---|
| OD-1 | Does EAS1 run the agent as the OS **service** principal (P2's recommendation)? Without it, `wts:7` refuses `PRIVILEGE_NOT_HELD` and the field acceptance fails | required prerequisite; contract ships either way, acceptance only passes in service mode |
| OD-2 | Strong principal may address **any** interactive context on the host, audited with the target user — accepted as policy on every OS, or limited to one operator's context per station? | any active context (N3/§12) |
| OD-3 | `detach` + selector (a guardian job inside an operator's context) — needed for long HMI runs? | refused in v1/v2 |
| OD-4 | A `contexts` write verb (logoff / disconnect / switch) — ever? | no; read-only forever, a separate decision if it is ever needed |
| OD-5 | `ui-window close` on the HMI process itself: allowed, or denied for a configured process list? | allowed for any window (the operator owns the station); a deny-list is a second policy place |
| OD-6 | Wayland: ship the **portal/libei** backend (consent dialog per session) or declare Wayland UI primitives refused-by-platform and ship X11 only? | refused-by-platform today (403/`CONSENT_REQUIRED` advertised), portal backend as a later unit with its own contract |
| OD-7 | Android: build the **a11y APK** lane at all, or keep Android as an exec+fs node? | exec+fs only in v1.21; the matrix says so explicitly |
| OD-8 | `PRIMITIVE_REFUSED` = 403 vs 409 (alignment with `UNSUPPORTED_AGENT`)? | 403 — policy refusal, not a fleet-state problem |

## 21. Review

| field | value |
|---|---|
| reviewed_by | **PENDING** — valmet (operator/requirements) + Goran (fleet) |
| verdict | **CONTRACT v2 — OS-AGNOSTIC, RATIFY BEFORE CODER DISPATCH.** No implementation unit may start until OD-1..OD-8 are answered; OD-6/OD-7 decide whether Wayland and Android ship as refusals (the default) |
| evidence class | substrate read at `ec647ed` (§2, re-runnable via §19); operator measurements quoted from spec v1.21 §P0.1/P0.2/P1 |
| prohibited and honoured | no code, no handler edits, no new lane, no new knob, no OCR/semantic recognition (§17 N14, spec l.176-181), no Windows-only wording — every primitive is defined neutrally with Windows as one backend column |
| supersedes | `docs/contracts/P10-01_SESSION_CONTRACT.md` (v1, Windows-shaped): §3 selector, §5 sessions→contexts, §6 tree, §7 control, §8 input, §11 non-Windows are replaced by §5-§11 here. v1's decision log D1-D12 stands where not restated (D2 worker-per-call, D5 one rect, D7 observed-ok, D9 mutex, D12 detach refusal are carried as N6/N7/N15/OD-3) |
