# P10-04 v3.1 — OS-agnostic primitive contract (corrected after falsification; the five P10-06 documentation defects closed)

**CLOSE HEADER — artefact**
`reviewed_by: reviewer` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: __SHA__` ·
`artefact: /opt/data/tmp/probe119/P10-07_CONTRACT_V3_1.md` · `committed as docs/contracts/P10-07_CONTRACT_V3_1.md on r119-build`
`suite: n/a — doc-only unit; five documentation defects closed by same-line replacement, no code, no handler edit, no .go file touched (git diff --name-only 7d1f325..HEAD | grep -c '\.go$' → 0)`
`open item: Windows has no node — every Windows cell is [S] and excluded from ratification (see ## Ratification scope)`

**STATE:** profile=architect · status=design-gate, contract text only — no code written, no handler edited, no coder dispatched, no lane, no knob.
**Gateway:** up (s6 `gateway run`, api_server lane, `HERMES_S6_SUPERVISED_CHILD=1`).

| field | value |
|---|---|
| unit | P10-07 — documentation-defect closure on P10-04 v3, ordered by `docs/contracts/P10-06_VERIFICATION.md` (`7d1f325`) FIX-FIRST §8 |
| owner | architect — design gate (contract text only) |
| supersedes | P10-04 v3 (`57b1415`, 931 lines, sha256 `e15fb9909a6e…`) as a **document**; as a **contract** it supersedes P10-01 v2 (`d86e879`) exactly as v3 did. v2's section numbers, primitive IDs P1-P6 and decision IDs N1-N16 are unchanged in v3.1, and so is every line range v3 cited outside `## 22` |
| tree | `/opt/data/projects/PROBE_UPGRADE/v119wt` · branch `r119-build` · **evidence base `b342636`** (branch head when every `file:line` here was read; the falsification's commit) · code base `ec647ed` · `git diff --name-only ec647ed..b342636 \| grep -c '\.go$'` → **0**, so no code moved under the contract. This document is committed on top of `7d1f325` (the P10-06 verification document), which changes no Go file either |
| evidence class | every claim below is either `[M]` measured on a HEAD build (1.19.1) by the falsification, or `[S]` a `file:line` read of the tree at `b342636`. No claim is inherited from recall, and no Windows claim is marked verified |
| non-goal | no OCR, no semantic control recognition, no HMI understanding (spec l.176-181). Eyes and hands only |
| status | CONTRACT v3.1 — v3 with the five documentation defects of `P10-06_VERIFICATION.md` §8 closed; no primitive, endpoint family, decision ID, claim or ratification scope changed otherwise. Ratification is **scoped** (§ Ratification scope); it is not a ratification of Windows, Wayland or Android |

This document is the third revision of one contract, not a new design. It changes no primitive, no endpoint family, no decision ID, and adds no capability. What it changes is enumerated in `## Corrections` with the line range of each change — 14 rows, one per falsification item the brief assigned (6 plus the two route names) plus three post-falsification corrections the reviewer should see rather than rediscover (the scroll inversion, the two one-answer-per-request contradictions, and the observed-failure enum gap).

## Corrections

One row per item. "Item" is the brief's numbering (§7 of the falsification in brackets). Every row cites the evidence for the correction; the line ranges are in **this** document and were generated from the document, not typed by hand.

| # | item | what changed | lines in v3 |
|---|---|---|---|
| 1 | **C-4** [§7.4] | The X11 `ui-tree` example carried `"hwnd":66102` while the field table types `hwnd` as absent on every non-`wts` context. The field is removed from the X11 row; a separate `wts` row now shows it, and the field table says "`wts` only — never emitted on `x11`/`wl`/`a11y` rows". Evidence: falsification C-4 (c:292 vs c:306); `[S]` no `hwnd` producer exists on Linux (`internal/platform/platform_linux.go` has no window code; `grep -rn '_NET_CLIENT_LIST' --include=*.go internal/` → 0). | lines 396-458 |
| 2 | **C-5** [§7.4] | The `capture` example sent `"window":66102` (number) while §4 types `window` as a string everywhere. Corrected body: `{"context":"x11::0","window":"0x3c00007","format":"png","hash_only":true}`. The contract now names the refusal for a number: `400 INVALID_PARAMS reason:"TYPE_MISMATCH"`, `detail{field:"window",want:"string",got:"number"}` — produced by the strict decoder in §5.5, not by a Go decode error leaking as `BAD_REQUEST`. Evidence: falsification C-5 (c:396 vs c:154); `[S]` `ScreenParams` at `internal/protocol/messages.go:454-457` has no `window` field at all, so today the key is dropped silently (SS-7). | lines 201, 328-340, 570-615 |
| 3 | **C-6** [§7.4] | Worked-sequence step 4 returned a bare array while §7 defines `{ok,data:{windows:[…]}}`. Step 4 is rewritten to the envelope, and §14 states the invariant: **no response body is ever a bare array** — every body is the `APIResponse` envelope at `internal/server/api_v1.go:27-35`. Evidence: falsification C-6 (c:565-566 vs c:283-293); `[S]` envelope struct. | lines 738-773 |
| 4 | **C-8** [§7.4] | `data` was specified "omitted when `hash_only`" but the response example carried `"data":"<base64 omitted when hash_only>"`. Two response bodies are now shown explicitly: `hash_only:true` → the `data` **and** `size_bytes` keys are absent; `hash_only:false` → both present. Rule added: *omitted means the key is absent — never `""`, never `null`, never `0`*, the same discipline §6 already applies to `idle_s`. Evidence: falsification C-8 (c:407 vs c:417); `[S]` `CaptureResult` at `messages.go:467-473` has no optional-key convention today (`Data string` is always serialised). | lines 570-615 |
| 5 | **C-9** [§7.4] | "encoded **server-side** … `detail.encoded_by:"agent"`" named two encoders. One remains: the **agent** encodes, always. The server never holds raw pixels (`CaptureResult` carries base64 only — `messages.go:467-473`), so a server-side transcode would require a decode of the agent's output; that is a new failure path for no benefit. `detail.encoded_by` is therefore always `"agent"`, and the sentence is rewritten to say so. Disagreement recorded in `## Disagreements` D-2. Evidence: falsification C-9; `[S]` `CaptureResult`. | lines 570-615 |
| 6 | **C-10** [§7.4] | The phantom enforcement site v2 named is **deleted**. It was never an HTTP route: `grep -rniE 'display[-]info' --include=*.go` at `b342636` → **0** hits. What exists is a wire command, `protocol.TypeDisplayInfo = "display_info"` (`messages.go:28`), handled at `agent.go:1062` with **no request body**. §5.6 now states the two rules that survive: (a) there is no HTTP surface for it, so it cannot be an enforcement site; (b) if the command ever grows a selector, §5.5's agent-side decoder covers it by type, not by a listed name. | lines 346-362 |
| 7 | **SS-13** [§7.5] — **the load-bearing fix** | Phase 1 no longer relies on one dispatch-table check. §5.5 is rewritten as **E3a** (server-side strict decode + per-route key allow-list, per handler), **E3b** (agent-side strict decode + allow-list, same helper), **E3c** (duplicate-key rejection). The old E3 (dispatch-table check) is **removed**, not extended: a table keyed on `env.Type` cannot see an unknown field on a *known* command, which is exactly SS-1…SS-11. A table in §5.5 names all **13** selector-bearing route sites plus one query-rejecting `GET` — 14 rows — with `file:line` at `b342636` (8 existing body-decoding, 1 GET with no body and no query decode, 5 new). Evidence: falsification §3 SS-1…SS-13 and its structural finding; `[S]` non-strict decode sites `server_api.go:179,675`, `api_v1_transfer.go:281,328,381,406,431,456`, `messages.go:986-991`. | lines 212-344 |
| 8 | **P3 headless** [§7.6] | `ui-tree` on an X server with no window manager now returns a **refusal with a reason**, never an empty list: `501 PRIMITIVE_UNSUPPORTED`, `reason:"NO_WINDOW_MANAGER"`, detected by `_NET_SUPPORTING_WM_CHECK` being absent on the root window. `reason` is added to the 501 enum; `NO_WINDOW_MANAGER` (501, environmental — start a WM) is kept distinct from `NO_GLOBAL_WINDOW_LIST` (403, the compositor refuses by design). An empty `windows` array is legal **only** after a successful enumeration. `/contexts` advertises the refusal up front (`features["window.tree"]=false` + `refusals[]`). Evidence: falsification §2 P3 headless and §7.6; `[S]` no EWMH code exists (`grep -rn '_NET_CLIENT_LIST'` → 0), and the measured headless node is Xvfb with no WM (falsification m:11). | lines 396-458 |
| 9 | **P4/P6 Android a11y** [§7.7] | Two claims removed as not implementable as labelled: (a) the a11y node-action call claimed as **window control** — P4's a11y cell is now `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND`; (b) "a11y node bounds + projection" claimed as `source:"window_direct"` — P6's a11y window cell is now `R:PRIMITIVE_UNSUPPORTED/NOT_EXPOSED`, and region capture is labelled `source:"screen_region"` (projection pixels cropped by bounds, occlusion-susceptible, which is what `screen_region` means in §10). Two adjacent claims of the same class are corrected in the same pass and marked as such: P2's `uid` row no longer asserts `active:true` (it is `null` + `active_known:false`, c:70/c:187), and Android `pointer-move`/hover is `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` (no injectable cursor; the falsification's §2 row 5e). Evidence: falsification C-12, §2 P2/P4/5e, §7.7. | lines 617-649 |
| 10 | **W-1…W-8** [§7.9] | Ratification is **scoped** in its own section, and the Windows column carries `[S]` in every one of its 11 cells — no `✓`, no "verified", no "implemented" anywhere in that column. §5.2, §5.3, §8, §9.2, §9.4, §10 and §14 keep the Windows backend text but every such cell is labelled "planned, unverified (W-n)", and §14 no longer offers a Windows variant of the worked sequence. Evidence: falsification §6 W-1…W-8; `[S]` no WTS/window/worker code exists in the tree (falsification §2 P3 Windows: 0 window symbols at HEAD, m:26). | lines 45-70 |
| 11 | **route names** [§5 M-1] | `/keypress` → **`/key-press`**, `/keycombo` → **`/key-combo`**. The register is `internal/server/api_v1.go:490-491`; the wrong spellings have no handler and 404 (falsification M-1, live). §2 and §4 cite the registered paths, and §19 greps for the wrong spellings with an expected count of 0. | lines 112-138 |
| 12 | *extra* — scroll sign [§7.1, C-2/C-3] | Not in the brief's six, fixed because it is a self-contradiction with a single correct answer and shipping it again would re-fail the doc: Windows `delta=-1` (up) now maps to `dwData = +120` (`dwData = -delta*120`), and Android `delta<0` maps to `ACTION_SCROLL_BACKWARD` (toward the beginning). The test expectation is derived from the convention, not from the table. Evidence: falsification C-2/C-3, c:367 vs c:371/c:374. | lines 515-568 |
| 13 | *extra* — one answer per request [§7.3, C-1/C-11] | `session` is `wts`-only: §4's row said `wts`/`a11y`, contradicting §3.1 (c:77) — and an `a11y` id has no session number to resolve, so the alias cannot exist there. Android other-uid is `OTHER_USER` in §11 as well as §12; §12's own table already splits same-user-other-context (`PRIVILEGE_NOT_HELD`) from different-user (`OTHER_USER`), so §11 was the outlier. Evidence: falsification C-1, C-11; `[S]` §12's table, this document. | lines 140-161, 170, 623, 662 |
| 14 | *extra* — observed-failure enum [§7.2, C-7] | `OCCLUDED`, `FOREGROUND_DENIED` and `NOT_SUPPORTED` were used as `ok:false` reasons but appeared in neither closed enum, so a caller could not catch them. They are now a **third, closed enum** for the 200-with-`ok:false` shape (§8), separate from the 403/501/409 refusal codes. The distinction is mechanical: 403/501/409 = nothing ran; 200+`ok:false` = the request ran, the platform's observed state did not permit the outcome. No new HTTP status and no code invented for a refusal. Evidence: falsification C-7, c:325/c:351/c:444; `[S]` §5.4/§11 enums. | lines 460-513 |

## Ratification scope

What the owner is being asked to sign, and what he is not.

**Can be ratified now — Linux-proven `[M]` + source-checked `[S]`, at `b342636`:**

1. **The neutral model** — §3 (context kinds, window identity, units, the P1-P6 catalogue) and §5.1/§5.2 (selector ladder, refusal shape). These are platform-independent by construction: no sentence in them depends on a measured platform.
2. **The refusal vocabulary** — §5.4 (9 `CONTEXT_UNAVAILABLE` reasons), §8.1 (3 observed-failure reasons), §11 (3 codes + 13 reasons). Ratified as a *specification*: the codes a caller will catch and the conditions that select each one.
3. **The phase-1 gate** — §5.5 E3a/E3b/E3c, whose every site is a named `file:line` at HEAD, plus the 400 reason enum. This is the fix for the 13 silent-success paths the falsification reproduced on a HEAD build (falsification §3) — the one part of the contract with a measured failure behind it and a mechanically verifiable cure.
4. **The Linux/X11 backend column** for the primitives where the falsification measured the platform: selector resolution (same-uid path), capture full-screen (measured: returns `width:0,height:0` — SS-11, i.e. the *defect*, not the cure), input plumbing to the OS tool, and every refusal on that column.
5. **The substrate table** — §2. Every row is a re-runnable command against `b342636`; a reviewer can refute any row in under a minute.
6. **The phase plan** — §18, phase 1..7, each phase shippable alone.

**Cannot be ratified — and nothing in this document asks for it:**

| excluded | why | what would move it |
|---|---|---|
| **Windows behaviour** (all 14 cells, W-1…W-8) | no Windows node; zero WTS / window-enumeration / worker code in the tree (falsification §2 P3 Windows: 0 window symbols, m:26). Every Windows sentence is `[S]` API-surface reasoning | a Windows host with an interactive session, `probe --install-service`, and a recorded run per cell |
| **Wayland** (all cells) | no seat, no Wayland code in the tree; every cell is a plan | a Wayland seat with a portal, plus `zwlr_foreign_toplevel_management_v1` |
| **Android** (all cells: Termux and a11y APK) | no node, no APK; Android is a build target only (`internal/server/builder.go:90-94`) | a Termux node for the exec/fs cells; an APK with an AccessibilityService for the UI cells |
| **Capture addressability** (`window`, `rect`, `format`, `hash_only`) | measured: every one of these keys is dropped today and the response is `width:0,height:0` (SS-5…SS-8, SS-11). The *cure* is phase 6 and has never run | phase 6 shipped, then a re-measure |
| every cell marked `[S]` or `[U]` in §11 | source reading or nothing | as above, per cell |

**One-line reading for the owner:** ratify the neutral model, the refusal vocabulary and the phase-1 gate as a *specification* for the Linux/X11 scope that has been measured or source-checked; treat every Windows, Wayland and Android cell as an unratified plan. A ratification that covers the Windows column is a ratification of source reading, not of behaviour — the falsification says so in §6 and this document does not contest it.

**What a ratification does not authorise:** a coder dispatch before OD-1..OD-10 are answered (§20), and any phase beyond 1 on a platform whose cells are `[S]`/`[U]`.

## Disagreements

Three, all recorded rather than silently kept. None of them changes a correction above.

**D-1 — the falsification's "WRONG" verdict on Android P2 is wider than its evidence (falsification §2, P2 Android row).**
It reads: "`root-visible profiles` … Cannot enumerate what it cannot identify". I accept the part that is proved by the contract's own text: a Termux process cannot know the foreground *activity* (c:70, c:187), so `active` must not be asserted — §6 now returns `"active":null, "active_known":false`. I do not accept the wider claim that profile/uid enumeration is impossible: a root probe can read `/proc` and the user list, which is enumeration by uid and does not require identifying an activity. Neither reading is measured on a node (there is no Android node in this campaign either way), so the honest label is `[U]`, not WRONG. I have labelled the cell `[U]` and removed the assertion the contract could not support; the reviewer may rule WRONG if a node shows enumeration also fails.

**D-2 — C-8 was a documentation defect, not a semantic ambiguity (falsification §4, C-8).**
The field table (c:417) already said `data` is **omitted** when `hash_only`. The contradiction lived only in the example's placeholder string `"<base64 omitted when hash_only>"`, which is a thing no server can emit. I corrected the example and added the "absent, never `""`/`null`/`0`" rule; I did not change the wire semantics, because there were never two semantics to choose between. If the reviewer reads C-8 as requiring a wire change, that change is `data:null` — which I reject: `null` is a value and would make "hash only" indistinguishable from "capture failed to produce data".

**D-3 — P3 headless at HEAD returns 404, not an empty list (falsification §2, P3 headless row).**
The falsification's cell says "Empty list is returned, indistinguishable from '0 windows'". At `b342636` there is no `ui-tree` handler at all (`grep -rn 'ui-tree\|ui_window' --include=*.go internal/` → 0; the falsification's own live run recorded `live:uitree` → 404). So the defect is entirely in the contract text — a specification that would have produced an empty list once implemented — not a live fabrication. This matters only for severity ordering, not for the fix, which is applied in full (§7.1). I record it so the next falsification does not inherit a claim about observed behaviour that was never observed.

## Carried and deferred

Named so that nothing falsified is silently dropped, and nothing outside this unit is silently taken.

| item | state | owner |
|---|---|---|
| Falsification §7.8 — P10-02 matrix re-measure (HEAD build, `/keypress` spellings, M(stale-binary) relabel, artefact path) | **not touched.** The brief forbids it and a parallel unit (P10-05) owns it | analyst |
| Falsification §3 SS-12 — `409 CAPABILITY_MISSING` (the wrapper's vocabulary, `api_v1.go:174-181`) vs the contract's `409 UNSUPPORTED_AGENT` with `detail.feature` | **not fixed here** — it is in the silent-stub list but in neither the §7 ordered list nor this brief's six items. Recorded as **OD-10** so it is visible to the ratifier | architect, next leg |
| Falsification §2 rows 5a/5b/5f and §11's Wayland column | partially corrected: the scroll sign (row 12 above) and Android hover (row 9) are fixed; the rest are `[S]`/`[U]` and belong to a platform node, not to text | — |
| `[S]`→`[M]` promotion of any Windows/Wayland/Android cell | impossible from this host; no edit to this document can promote a cell | Goran (node access) |

## 1. What changed from v1, and why (unchanged in v3)

| v1 (Windows-shaped) | v2/v3 (neutral) | Reason |
|---|---|---|
| `Session *int` selector | `context` (opaque string) + `session` (int, legacy alias) | A Windows session is one *kind* of execution context. X11/Wayland have displays and seats; Android has user profiles and displays. Hard-coding `int` makes the wire format platform-truth |
| `sessions` endpoint (WTS rows) | `contexts` endpoint (kind-tagged rows) | Same enumeration on every OS; `quser` semantics are one row type |
| `ui-tree` rows with `hwnd int` | rows with `id string` + `id_kind` (+ `hwnd` alias on `wts` only) | hwnd is a Windows handle. X11 gives XIDs, Wayland gives toplevel handles, Android gives a11y window ids |
| input = absolute px, `hwnd`-relative | same geometry, neutral `window` selector | The measured bug (covered click answered success) is OS-independent: absolute injection with no identity |
| `TypeText` implicitly ASCII | `text` carries an explicit encoding + per-backend Unicode method | `platform_windows.go:500-524` iterates **bytes**; X11 `xdotool type` happens to do better but is an external binary; the contract must not depend on luck |
| capture = full screen only | `capture` with `window|rect|format|quality|hash_only` | spec §P1 l.105-123 (measured: 1024×768 JPEG to read a 1038×118 strip) |
| non-Windows = one paragraph ("PLATFORM_NOT_SUPPORTED") | full backend matrix per primitive, per platform, with the refusal code for each cell | Owner ruling: a platform that cannot do a primitive answers with a typed refusal, it is not described as "not supported" and left undefined |

**Invariant carried forward unchanged:** absent selector = the agent's own context and today's behaviour byte-for-byte; present selector = resolved exactly or refused. There is no third outcome and no fallback (E1-E3 in §5.5).

**v3 delta over v2, in one paragraph:** the examples and types now agree with the field tables (C-4/C-5/C-6/C-8/C-9), the phantom enforcement site is gone (C-10), phase 1 enforces strict decoding per handler instead of pretending a dispatch-table check can see unknown fields (SS-13), `ui-tree` refuses on a no-WM X server instead of returning an empty list (P3), the two Android a11y claims that were not implementable as labelled are removed (P4/P6), and the Windows column is `[S]` everywhere with ratification scoped accordingly (W-1..W-8).

## 2. Substrate (re-read at `b342636`, not from recall)

`git diff --name-only ec647ed..b342636` → three additions, **zero** `.go` files: the code is byte-identical to the base v2 was written against, so v2's substrate claims transfer and are re-stated here with the head that carries them.

| Fact | Evidence at `b342636` |
|---|---|
| input routes are registered as `/pointer-click`, **`/key-press`**, **`/key-combo`**, `/text-input`; the spellings `/keypress` and `/keycombo` have no handler (404) | `internal/server/api_v1.go:489-492`; falsification §5 M-1 (live) |
| stream routes: `POST …/stream-start` `:484`, `POST …/stream-stop` `:485`, **`GET …/stream-frame` `:486`** (no request body at all — §5.6) | `api_v1.go:484-486` |
| exec route `:320`; capture route `:358-359` (wrapped by `v1WrapAgentHandler("capture", …)`) | `api_v1.go:320,358-359` |
| `InputParams` = `X,Y,Button,Text,Key,Keys` only — no `context`, `window`, `count`, `focus`, `scroll`, `drag` | `internal/protocol/messages.go:557-563` |
| `ScreenParams` = `Display,Quality` only — no `window`, `rect`, `format`, `hash_only`, `since` | `messages.go:454-457` |
| `ScreenStreamParams` = `Display,FPS,Quality` only — no `context` | `messages.go:487-491` |
| `ScreenStreamStopParams` = `StreamID` only | `messages.go:542-544` |
| `CaptureResult` = `Format,Width,Height,Data,SizeBytes` — no `hash`, no `source`, no `target` | `messages.go:467-473` |
| every route decodes **non-strictly**: server uses `json.Unmarshal` at `server_api.go:179,675` and `json.NewDecoder` at `api_v1_transfer.go:281,381,406,431,456`; `stream-stop` **discards the decode error** (`_ = json.Unmarshal(body, &params)`, `:328`) after reading 4096 bytes | `[S]` |
| the agent decodes through one non-strict generic: `protocol.ParseCommand` uses `json.Unmarshal` with no `DisallowUnknownFields` | `messages.go:986-991`; call sites `agent.go:669,1051,1067,1078,1089,1100` |
| **no HTTP route exists for the `display_info` command**; what exists is the wire command `TypeDisplayInfo = "display_info"` (`messages.go:28`) with a body-less handler (`agent.go:1062`) | `grep -rniE 'display[-]info' --include=*.go .` → **0** hits; falsification C-10 |
| no window/EWMH code whatsoever: no `_NET_CLIENT_LIST`, no `EnumWindows`, no `XQueryTree` in the tree — P3/P4 are greenfield | `grep -rn '_NET_CLIENT_LIST\|EnumWindows\|XQueryTree' --include=*.go internal/` → 0 |
| Android is a **build target**, not a platform implementation: `CGO_ENABLED=0`, Termux process, no accessibility service, no APK | `internal/server/builder.go:90-94` |
| Linux capture shells out to `import`/`scrot` and returns `Width:0, Height:0`; `ScreenInfo()` returns zeros | `internal/platform/platform_linux.go:191-210`, `:212-214` |
| Windows capture is primary-monitor only (`SM_CXSCREEN` + `BitBlt(…,0,0,w,h)`) — not the virtual screen v2's §3.3 defines | `internal/platform/platform_windows.go:316,366` |
| Windows click = `SetCursorPos` + `mouse_event` with no window identity; `TypeText` iterates bytes | `platform_windows.go:430-454`, `:500-524` |
| capability gate is coarse (`exec`/`filesystem`/`capture`/`input`) and treats an agent with **no** advertised capabilities as having all of them | `internal/server/enrollment.go:414-435,533-540`; `server_api.go:789-797` |
| response envelope is a single struct: `{ok, data?, error?}` | `api_v1.go:27-35` |
| release version at HEAD is `1.19.1` (not `1.9.4`) | `internal/version/version.go:21` |

**No new dependency.** Every backend named below is either a syscall/wire protocol reachable from the existing `Platform` interface, an OS-supplied compositor interface, or an OS tool already present on the target (declared as a **backend prerequisite**, not a vendored library).

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

- **A context is advertised, never inferred.** A caller obtains an `id` from `GET …/contexts` (§6) and passes it back verbatim. The agent never accepts an id it did not advertise.
- **Kinds are capability-scoped, not platform-scoped.** An X11 display has windows; a `uid` context does not. `exec` works in every kind; `window.tree` does not.
- **`session` is a compatibility alias, `wts` only.** `session:7` on a `wts`-kind agent resolves to `wts:7`. On any other kind it is **refused** (`CONTEXT_UNAVAILABLE`, `reason:"KIND_NOT_APPLICABLE"`), never ignored — ignoring is the bug being fixed. `session` and `context` in the same request with different targets → `400 INVALID_PARAMS`, `reason:"SELECTOR_CONFLICT"`.
  *Correction (C-1):* v2's §4 parameter row said `session` is valid on `wts`/`a11y` kinds. Only `wts` is meant, and only `wts` is possible: an `a11y` context id (`a11y:u0:d0`) has no session number for the alias to resolve to. This paragraph (c:77) is the governing text; §4's row now matches it.
- **`display` is a compatibility alias on exactly two routes** — `capture` and `stream-start` — because those two param structs already carry it on the wire (`messages.go:454-457`, `:487-491`) and the shipping console sends it. It resolves through the same ladder: on an `x11` agent, `display:9` → the context id `x11:<host>:9`, which must be advertised or the request is `409 CONTEXT_UNAVAILABLE/NOT_FOUND`; on a `wts`/`a11y` agent → `409 CONTEXT_UNAVAILABLE/KIND_NOT_APPLICABLE`. It is never dropped (that was SS-8) and never silently coerced to `0`.

## 4. Primitive × parameter table (acceptance quote #1)

`[S]` every type below is a wire fact, not a preference. The keys in the **required** column are also the per-route allow-list that §5.5 enforces: any other key is `400 INVALID_PARAMS reason:"UNKNOWN_FIELD"`.

| primitive / endpoint | parameter | type | req | default | unit / domain |
|---|---|---|---|---|---|
| **all** `context` | `context` | string | no | agent's own | an `id` from `/contexts`; absent means *no selector was sent*, which is not the same as "own" |
| | `session` | int | no | — | 0-65535; **`wts` kind only** (C-1); alias for `context` |
| **P1** `exec` | `context`/`session` | — | no | own | same resolution ladder as UI; `detach:true` + selector → `400 INVALID_PARAMS reason:"DETACH_WITH_CONTEXT_UNSUPPORTED"` |
| **P2** `contexts` | — | — | — | — | `GET` only; no query params, no writes |
| **P3** `ui-tree` | `visible_only` | bool | no | **true** | hidden windows are noise; the 0-visible measurement is the detector |
| | `window` | string | no | — | exact `id` filter; unknown id → `200 {ok:true,count:0,windows:[]}` (a window may legitimately vanish) — not 404, and never a bare array (C-6) |
| | `max` | int | no | 500 | hard cap; `truncated:true` when hit |
| **P4** `ui-window` | `window` | string | **yes** | — | must be in the same context's tree; else `404 WINDOW_NOT_FOUND` |
| | `action` | enum | **yes** | — | `foreground|minimize|restore|move|resize|close` |
| | `x`,`y` | int | for `move` | — | px, may be negative |
| | `w`,`h` | int | for `resize` | — | `1..32767`; else `400 INVALID_PARAMS reason:"OUT_OF_RANGE"` |
| **P5** `pointer-move` | `x`,`y` | int | **yes** | — | px; no button, no click (hover = this) |
| | `window` | string | no | — | when present, `x,y` are **relative to `window.rect` origin**, resolved at dispatch |
| **P5** `pointer-click` | `x`,`y` | int | **yes** | — | px, or window-relative when `window` present |
| | `button` | enum | no | `left` | `left|right|middle` |
| | `count` | int | no | 1 | `1..3`; **`2` = one double-click sequence**, not two calls (§9.2) |
| | `window` | string | no | — | as above; enables the occlusion hit-test |
| **P5** `pointer-scroll` | `x`,`y` | int | no | cursor | anchor point (some toolkits scroll the window under the given point) |
| | `delta` | int | **yes** | — | **notches**; **negative = up / away from user / toward the beginning** (§9.3) |
| | `unit` | enum | no | `notch` | `notch|px` (`px` only where the backend supports pixel-precise wheels, else `501 PRIMITIVE_UNSUPPORTED reason:"UNIT_NOT_SUPPORTED"`) |
| | `window` | string | no | — | as above |
| **P5** `pointer-drag` | `from`,`to` | object | **yes** | — | `{"x":..,"y":..}`, same space as click |
| | `button` | enum | no | `left` | |
| | `duration_ms` | int | no | 300 | `50..5000`; the down→up span |
| | `steps` | int | no | 12 | intermediate moves; `0` = jump |
| **P5** `text-input` | `text` | string | **yes** | — | UTF-8 on the wire; **full Unicode** (spec l.92) |
| | `encoding` | enum | no | `utf8` | `utf8|utf16`; `utf16` = caller already knows the code units |
| | `window` | string | no | foreground | text goes to the focused window; `window` is a *claim* verified before typing |
| | `focus` | bool | no | false | §9.1: without it, an unfocused `window` is `403 PRIMITIVE_REFUSED/NO_FOCUS_TARGET` |
| **P5** `key-press` | `key` | string | **yes** | — | canonical name (`Enter`, `Escape`, `F5`, `Tab`) |
| | `window` | string | no | foreground | |
| **P5** `key-combo` | `keys` | []string | **yes** | — | order-insensitive modifiers (`["Ctrl","Shift","S"]`) |
| **P6** `capture` | `window` | string | no | full screen | takes precedence over `rect`; **type is `string` in every primitive** — a JSON number is `400 INVALID_PARAMS reason:"TYPE_MISMATCH"` (C-5) |
| | `rect` | object | no | full screen | `{x,y,w,h}` in context px; `w,h ≥ 1` |
| | `format` | enum | no | `png` | `png|jpeg`; png for text/UI, jpeg for photos |
| | `quality` | int | no | 80 | `jpeg` only, `1..100` |
| | `hash_only` | bool | no | false | when true, **`data` and `size_bytes` are absent** — the point of the primitive (spec l.119), C-8 |
| | `since` | string | no | agent's stored baseline | `sha256:<hex>`; overrides the stored baseline for this call |
| **P6** `stream-start` | `display` | int | no | 0 | legacy alias, resolved to a context (see §3.1); never dropped |
| | `fps`,`quality` | int | no | 10, 80 | as today |

Response shapes are in §6-§10; refusal enums in §5.4, §8.1 and §11.

## 5. P1 — execution-context selector

### 5.1 Type and absence (neutral)

```json
{ "context": "wts:7" }        // canonical
{ "session": 7 }              // compatibility alias, wts kind only
{ }                           // no selector: the agent's own context, unchanged behaviour
```

- **absent** → the agent's own context. The response still echoes `context_ref.source:"agent_own"`, so where the action happened is never a guess.
- **present** → the ladder in §5.2 runs to completion, then the op runs in exactly that context, or the op is refused. No partial entry, no retry in another context.
- **`session` on a non-`wts` kind** → `409 CONTEXT_UNAVAILABLE reason:"KIND_NOT_APPLICABLE"` (do not silently treat 7 as a display number).
- **`context` not advertised by this agent** → `409 CONTEXT_UNAVAILABLE reason:"NOT_FOUND"`, `detail.advertised` lists the ids.
- **both present, disagreeing** → `400 INVALID_PARAMS reason:"SELECTOR_CONFLICT"`.
- **range** (`session` outside `0..65535`) → `400 INVALID_PARAMS reason:"OUT_OF_RANGE"`, `detail.field:"session"`, before any syscall.

### 5.2 Resolution ladder — the same six questions on every OS

The Windows column is **`[S]` planned and unverified (W-1..W-3)**. It is one column, not the definition.

| # | question | Windows `wts` `[S]` | X11 | Wayland | Android (Termux) |
|---|---|---|---|---|---|
| V1 | does the context exist? | `WTSEnumerateSessions` match (W-1) | `$DISPLAY` socket reachable, `XOpenDisplay` | `WAYLAND_DISPLAY` + seat present | `getuid()`; profile lookup needs root |
| V2 | is it interactive? | `State == WTSActive` (W-1) | display not `-nolisten`, XTEST extension present | seat has a keyboard capability | process has a foreground activity — **not knowable in Termux** (c:70/c:187); reported `null` |
| V3 | is a principal attached? | `WTSQuerySessionInformationW(WTSUserName)` non-empty (W-1) | peer uid of the socket / `~user/.Xauthority` readable | `wl_display` peer credentials (SO_PEERCRED) | the uid itself |
| V4 | may **this** agent address it? | §12 predicate | §12 predicate | §12 predicate | §12 predicate |
| V5 | can authority be obtained **without gaining any**? | `WTSQueryUserToken` (SYSTEM only) (W-1) | `XAUTHORITY` cookie already readable by this uid | same-uid socket connect only | same-uid only |
| V6 | can work be entered? | `CreateProcessAsUserW(lpDesktop="winsta0\\default")` (W-1, W-2) | fork+`setuid` (root only) or direct syscalls in-process | direct in-process (same seat only) | fork+`setuid` (root only) |

`WTSGetActiveConsoleSessionId` is **not** the predicate (it lies on multi-RDP stations); enumeration is. Same discipline everywhere: **ask the platform who is there, never assume.**

### 5.3 Entering a context — one mechanism per OS, no tricks

| OS | exec in context | UI/input/capture in context |
|---|---|---|
| Windows `[S]`, unverified (W-2) | `CreateProcessAsUserW`, target user's token, `CREATE_UNICODE_ENVIRONMENT|CREATE_NO_WINDOW` | the same binary relaunched as `--context-worker` **in** that context, one JSON request on stdin, one JSON response on stdout, exit. The worker has never been run; the IPC shape is unproven (W-2) |
| X11 | fork + `setgroups/setgid/setuid` to the target uid (root only), env `DISPLAY`+`XAUTHORITY` of that session | direct syscalls against that `Display*` — X11 has no per-session window station; a second display is simply a second connection |
| Wayland `[S]` | same uid only (fork not needed) | direct, same seat |
| Android (Termux) | same uid only (`fork` + setuid if root) | **refused** — no accessibility surface (§11) |
| headless (Xvfb) | normal | X11 backend, no compositor; **no window manager is the common case, not an edge** — §7.1 |

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

> The Windows wording above is an **example of shape**, `[S]` (W-3): `ERROR_PRIVILEGE_NOT_HELD 1314` has never been observed on a Windows node. It is retained because the shape must be shown once with real nouns; it is not a claim that a run produced it.

`reason` is a **closed enum** (no free text, tests enumerate all of them):
`NOT_FOUND` · `NOT_INTERACTIVE` · `NO_USER` · `OTHER_USER` · `PRIVILEGE_NOT_HELD` · `ENTER_FAILED` · `GONE` · `KIND_NOT_APPLICABLE` · `NO_BACKEND`.

`detail.ran` is always `false` on this code. A refusal that cannot say what it did not do is not a refusal.

### 5.5 No silent fallback — the enforcements (E1, E2, E3a, E3b, E3c)

v2's **E3 is deleted**. It said: *"the agent's dispatcher refuses any command type not on the allow-list that arrives with a `context`/`session` key — one check at the dispatch table, not 74 struct edits"*. That check cannot work, and the falsification proved it: every command it needs to catch is **on** the allow-list, and the field it needs to see is **unknown to the struct**, so `json.Unmarshal` discards it before any table lookup (falsification §3 SS-1…SS-11, and the structural finding that follows them). A check keyed on `env.Type` cannot see unknown fields on a known type.

| # | Enforcement | Where |
|---|---|---|
| E1 | the **server refuses before dispatch** when the agent's advertised features lack `context.selector`: `409 UNSUPPORTED_AGENT`, `detail{feature:"context.selector", agent_version, ran:false}` | new pre-dispatch gate, mirroring `server_api.go:789-797` + `enrollment.go:533-540` |
| E2 | every context-scoped result **must** carry `context_ref`; a 1.21+ agent that omits it is `502 PROTOCOL_VIOLATION`, never passed through as success | server post-dispatch assertion |
| **E3a** | **server side, per handler**: the request body is decoded through one helper `decodeStrict[T](w, r, allowed []string)` which (i) reads the body once, (ii) checks every top-level key against the route's allow-list, (iii) rejects duplicate top-level keys, (iv) decodes with `json.Decoder.DisallowUnknownFields()`. Any failure → `400 INVALID_PARAMS` with the reason below, `detail.ran:false`, **before** `forwardToAgent` | called by each of the 14 handlers in the table below |
| **E3b** | **agent side, per handler**: the same three checks inside `protocol.ParseCommand`'s replacement (`ParseCommandStrict[T](env, allowed)`), so a server that is old, misconfigured, or bypassed cannot smuggle a field to the platform | `messages.go:986-991` replaced; call sites `agent.go:669,1051,1067,1078,1089,1100` + the five new handlers |
| **E3c** | duplicate top-level keys are refused (`reason:"DUPLICATE_FIELD"`, `detail.field`). Go's decoder keeps the **last** duplicate silently, so `{"context":"x11::0","context":"wts:7"}` is a selector-smuggling primitive today: the audit row would record one context and the platform would act in the other | inside the same helper, a `json.Decoder` token walk before the struct decode |

E3a/E3b are one helper each, invoked per handler — not 74 struct edits and not one dispatch-table check. The allow-list is the route's own §4 row, which is why §4 now states "the keys in the required column are also the allow-list".

**The 13 selector-bearing sites at `b342636`, plus one query-rejecting `GET`** — 14 rows: 8 existing body-decoding, 1 GET that carries no selector (row 5), 5 new. This table is the phase-1 work list; every `file:line` is re-runnable. Row 5 is listed because phase 1 must *reject* query keys on it, not because it can carry a context — §5.6's table answers "can it carry a selector?" with **no** for `stream-frame` (c:359).

| # | route / command | handler at `b342636` | decodes today | allow-list after phase 1 |
|---|---|---|---|---|
| 1 | `POST /exec` (`api_v1.go:320`) | `server_api.go:170`, `json.Unmarshal` `:179`; agent `handleExec` `agent.go:668-669` | `ExecParams` (`messages.go:230-280`) | `command,timeout,workdir,env,as,detach,credential,credential_mode,job_id,on_death,job_timeout_s,idempotency_key,context,session` |
| 2 | `POST /capture` (`api_v1.go:358-359`) | `server_api.go:666`, `json.Unmarshal` `:675`; agent `handleCapture` `agent.go:1050-1051` | `ScreenParams` (`messages.go:454-457`) | `context,session,display,window,rect,format,quality,hash_only,since` |
| 3 | `POST /stream-start` (`api_v1.go:484`) | `api_v1_transfer.go:275`, `NewDecoder` `:281`; agent `handleStreamBegin` | `ScreenStreamParams` (`:487-491`) | `context,session,display,fps,quality` |
| 4 | `POST /stream-stop` (`api_v1.go:485`) | `api_v1_transfer.go:314`, decode **error discarded** `:328`; agent `handleStreamEnd` | `ScreenStreamStopParams` (`:542-544`) | `stream_id,context,session` — `stream_id` becomes **required** (`reason:"STREAM_ID_REQUIRED"`): today an empty id stops **every** stream (SS-10) |
| 5 | `GET /stream-frame` (`api_v1.go:486`) | `api_v1_transfer.go:353` — **no body, no query decode** | nothing | no keys; any query parameter → `400 INVALID_PARAMS reason:"UNKNOWN_FIELD"`. A frame belongs to the stream that was bound at `stream-start` (§5.6) |
| 6 | `POST /pointer-move` *(new)* | — | — | `context,session,x,y,window` |
| 7 | `POST /pointer-click` (`api_v1.go:489`) | `api_v1_transfer.go:375`, `NewDecoder` `:381`; agent `handleClick` `agent.go:1066-1067` | `InputParams` (`:557-563`) | `context,session,x,y,button,count,window,focus` |
| 8 | `POST /pointer-scroll` *(new)* | — | — | `context,session,x,y,delta,unit,window` |
| 9 | `POST /pointer-drag` *(new)* | — | — | `context,session,from,to,button,duration_ms,steps,window` |
| 10 | `POST /key-press` (`api_v1.go:490`) | `api_v1_transfer.go:400`, `NewDecoder` `:406`; agent `handleKey` `agent.go:1088-1089` | `InputParams` | `context,session,key,window,focus` |
| 11 | `POST /key-combo` (`api_v1.go:491`) | `api_v1_transfer.go:425`, `NewDecoder` `:431`; agent `handleKeyCombo` `agent.go:1099-1100` | `InputParams` | `context,session,keys,window,focus` |
| 12 | `POST /text-input` (`api_v1.go:492`) | `api_v1_transfer.go:450`, `NewDecoder` `:456`; agent `handleType` `agent.go:1077-1078` | `InputParams` | `context,session,text,encoding,window,focus` |
| 13 | `POST /ui-tree` *(new)* | — | — | `context,session,visible_only,window,max` |
| 14 | `POST /ui-window` *(new)* | — | — | `context,session,window,action,x,y,w,h` |

**Why a per-route allow-list and not just `DisallowUnknownFields`:** four routes share one Go type (`InputParams`), so strict decoding alone accepts `{"text":"abc"}` on `pointer-click` and `{"keys":["Ctrl","S"]}` on `text-input` — the field is *known to the type* and wrong for the *route*. Only the route's own key set catches that. This is the cross-parameter case the falsification's SS-2/SS-4 shapes exercise.

Refusal bodies (one shape, five reasons — `reason` is a closed enum, tests enumerate all of them):

```json
// unknown key for this route
{ "ok": false, "error": { "code": "INVALID_PARAMS",
  "message": "field "display" is not accepted by POST /pointer-click",
  "detail": { "field": "display",
              "allowed": ["context","session","x","y","button","count","window","focus"],
              "ran": false } } }
```
```json
// wrong JSON type for a known key  (C-5: capture {"window":66102})
{ "ok": false, "error": { "code": "INVALID_PARAMS",
  "message": "field "window" must be a string",
  "detail": { "field": "window", "want": "string", "got": "number", "ran": false } } }
```
```json
// duplicate top-level key (E3c: the last one wins in Go, silently)
{ "ok": false, "error": { "code": "INVALID_PARAMS",
  "message": "field "context" appears twice",
  "detail": { "field": "context", "reason": "DUPLICATE_FIELD", "ran": false } } }
```

`INVALID_PARAMS` reason enum (closed): `UNKNOWN_FIELD` · `TYPE_MISMATCH` · `DUPLICATE_FIELD` · `MISSING_FIELD` · `OUT_OF_RANGE` · `SELECTOR_CONFLICT` · `CONTEXT_NOT_ACCEPTED` · `DETACH_WITH_CONTEXT_UNSUPPORTED` · `STREAM_ID_REQUIRED` · `REGION_TOO_LARGE` · `ALIAS_KIND_NOT_APPLICABLE`.

**Scope, stated so the document does not overclaim:** phase 1 applies the gate to the **14 routes above**. The other ~60 routes keep today's non-strict decoding until phase 7; that is a hygiene backlog, not a promised refusal. Every route in this table is one a UI primitive or its selector touches — which is why the falsification's 13 silent-success paths all live inside it.

**The behaviour change, named (OD-9):** a caller that has been sending junk fields to these routes will start getting `400` instead of a silent success. That is the intended break — every one of the 13 measured shapes is a caller that asked for something and was told `ok:true`. It is escalated as an open decision rather than applied by taste, because "never break what works" is a standing rule and here what "works" is a lie.

### 5.6 Endpoints that cannot carry a selector (replaces v2's phantom list)

v2 had: *"Endpoints that refuse the selector explicitly: `stream-start`, `stream-frame`, `stream-stop`, and a fourth name → `400 INVALID_PARAMS reason:"CONTEXT_NOT_ACCEPTED"`"*. Two errors in one line, both now corrected (the fourth name is quoted nowhere in v3: it does not exist, and writing it down again would only teach the next reader a route to call):

- **The fourth name in that list is not an endpoint.** It was never registered (`grep -rniE 'display[-]info' --include=*.go` → 0 hits at `b342636`). The only thing close to it is the body-less wire command `TypeDisplayInfo` (`messages.go:28`, handler `agent.go:1062`), which has no HTTP surface and therefore cannot be an enforcement site. The name is removed from this section and from the contract's enforcement language — an enforcement site that does not exist is worse than an omission.
- **`stream-frame` is a `GET` with no body** (`api_v1.go:486`). It cannot carry a selector, so "refusing the selector" on it is not a thing to specify — what it must refuse is a **query parameter**, because that is where an operator would put one.

The rules that survive, per route:

| route | can it carry a selector? | rule |
|---|---|---|
| `stream-start` | yes (`context`/`session`/`display`) | the stream is **bound** to the resolved context at start; the echo carries `context_ref`. Bad selector → the §5.4 refusal, before any frame is produced |
| `stream-stop` | no | `context`/`session` are accepted by the allow-list only so they can be **refused**: `400 INVALID_PARAMS reason:"CONTEXT_NOT_ACCEPTED"`, `detail{allowed:"stream_id"}`. A stream is stopped by its id, never by a context. `stream_id` is required (§5.5 row 4) |
| `stream-frame` | no | no keys at all. Any query parameter → `400 INVALID_PARAMS reason:"UNKNOWN_FIELD"`. The frame belongs to the stream started earlier; there is nothing to select per frame |
| `ui-tree`, `ui-window`, `pointer-*`, `key-*`, `text-input`, `capture`, `exec` | yes | §5.5 |

**Invariant:** a selector is either *resolved and used*, *refused with a code*, or *not applicable because the route has no body to carry it* — and in the third case the route refuses the key that tried, rather than dropping it.

## 6. P2 — context enumeration (read-only)

`GET /api/v1/agents/{id}/contexts`

```json
{ "ok": true, "data": {
  "agent_context": {"id":"x11::0","kind":"x11"},
  "generated_at_utc": "2026-11-24T17:22:10Z",
  "contexts": [
    { "id":"x11::0", "kind":"x11", "name":":0", "user":"flk", "uid":1000,
      "active": true, "active_known": true, "agent_own": true, "addressable": true,
      "addressable_reason":"agent_own",
      "surface": {"pixels":"1920x1080", "displays":1, "dpi_scale":1.0},
      "features": {"window.tree":true,"window.control":["foreground","minimize","restore","move","resize","close"],
                   "input.pointer":["move","click","scroll","drag"],"input.text":["unicode"],
                   "capture.window":true,"capture.region":true}},
    { "id":"uid:10123", "kind":"uid", "name":"termux", "user":"u0_a123", "uid":10123,
      "active": null, "active_known": false, "agent_own": false, "addressable": true,
      "addressable_reason":"same_uid_other_process",
      "surface": {"pixels":"1080x2400", "displays":1, "dpi_scale":2.75},
      "features": {"window.tree":false,"window.control":[],"input.pointer":[],
                   "input.text":[],"capture.window":false,"capture.region":false} }
  ] } }
```

- `active` is `true`/`false` only when the platform answered, and **`null` with `active_known:false`** when it cannot — the `uid` row above is the case: a Termux process cannot know the foreground activity (c:70, c:187; falsification §2 P2). v2 showed `"active": true` there, which was a fabricated zero in boolean form.
- `addressable` is computed by the **same predicate the selector enforces** (`classifyContext`), so discovery and enforcement cannot drift. Reasons: `agent_own` · `service_principal` · `same_uid_other_process` · `other_user` · `no_user` · `not_interactive` · `privilege_not_held` · `kind_has_no_surface`.
- `features` per context is the whole point: the same agent may have a full X11 display and a windowless `uid` context. A caller reads one row and knows what it can do **there** — it never guesses from the agent's OS string. On an X11 display with **no window manager**, phase 2 reports `"window.tree": false` and the `refusals` entry `{feature:"window.tree",code:"PRIMITIVE_UNSUPPORTED",reason:"NO_WINDOW_MANAGER"}` (§7.1), so the caller learns it here rather than from a 501 mid-flow.
- `idle_s`/`logon_time` are `-1`/`""` when the platform cannot answer, **never a fabricated zero**.
- `GET` only. No `disconnect`, `logoff`, `message`, `switch`. A write verb is a separate decision (OD-4).
- RBAC: requires the **`input`** scope (as `api_v1_transfer.go:377` does) — a logged-on-user list is operator-activity data; a `viewer` token must not see it.

## 7. P3 — window tree

`POST /api/v1/agents/{id}/ui-tree` `{"context":"x11::0","visible_only":true}`

```json
{ "ok": true, "data": {
  "context_ref": {"requested":"x11::0","resolved":"x11::0","kind":"x11","user":"flk","source":"selector"},
  "units": "px", "dpi_scale": 1.0, "dpi_awareness": "x11_native",
  "truncated": false, "count": 1, "tree_source": "ewmh_client_list",
  "windows": [
    {"id":"0x3c00007","id_kind":"xid","native":{"xid":62914567},
     "title":"…/Control Panel","class":"Qt51516QWindowIcon",
     "rect":{"x":0,"y":0,"w":1038,"h":118},
     "iconic":false,"foreground":true,"owner_pid":2640,"visible":true,"kind":"normal"}
  ] } }
```

> **C-4 corrected:** v2's example carried `"hwnd":66102` on this X11 row while its own field table said `hwnd` is absent on every non-`wts` context. It is gone. A `wts` row is the only place it appears: `{"id":"66102","id_kind":"hwnd","native":{"hwnd":66102}, …,"hwnd":66102}` — and that row is `[S]`, unverified (W-1).

| field | definition | absent on |
|---|---|---|
| `id` | opaque, context-scoped, stable for the window's lifetime | — |
| `id_kind` | `hwnd` \| `xid` \| `toplevel` \| `a11y` | — |
| `title` | full title, no truncation (a page title **is** the signal) | `""` when the backend has none |
| `class` | platform class string, normalised | `a11y` |
| `rect` | outer rect, context px, virtual-screen origin (negative legal) | — |
| `iconic` | minimised / unmapped-but-alive | `a11y` (use `visible`) |
| `foreground` | the context's input focus owner; **exactly one true**, or none if the context has no focus | — |
| `owner_pid` | owning process id | `[S]` `hwnd` row only |
| `hwnd` | Windows-only compat integer, equals `native.hwnd` | **every non-`wts` context** — never emitted on `x11`/`wl`/`a11y` rows (C-4) |
| `tree_source` | which enumeration produced the list: `ewmh_client_list` \| `foreign_toplevel` \| `enum_windows` \| `a11y_windows` | — |

Enumeration rules (neutral): top-level windows only (children/`WS_CHILD`/`override-redirect`/popups excluded), z-order/stacking order, hard cap 500 with `truncated:true`. On X11, `IconicState` comes from `WM_STATE` (EWMH); on Windows, `IsIconic` `[S]` (W-4); on Wayland, `zwlr_foreign_toplevel.minimized` when the compositor exposes it.

### 7.1 X server with no window manager — a refusal, not an empty list

This is not an edge case. The measured headless substrate is **Xvfb with no window manager** (falsification m:11), and Xvfb never sets `_NET_CLIENT_LIST`. v2 defined X11 enumeration as EWMH-only (c:308) and said nothing about the property being absent, which would have produced `windows: []` — indistinguishable from "this display has no windows", the same class of fabrication §6 forbids for `idle_s`.

**Detection (before any enumeration):** read `_NET_SUPPORTING_WM_CHECK` on the root window. Absent or pointing at a dead window → there is no EWMH-compliant WM → refuse.

```json
{ "ok": false,
  "error": {
    "code": "PRIMITIVE_UNSUPPORTED",
    "message": "display :0 has no EWMH window manager: _NET_SUPPORTING_WM_CHECK is not set on the root window",
    "detail": {
      "reason": "NO_WINDOW_MANAGER",
      "context": {"id":"x11::0","kind":"x11"},
      "display": ":0",
      "tree_source_attempted": "ewmh_client_list",
      "ran": false,
      "remediation": "This X server has no window manager, so it publishes no client list and no stacking order. Start an EWMH-compliant WM on :0 (e.g. openbox, fluxbox, i3, mutter) and retry; a bare Xvfb session is an exec/input substrate, not an enumerable desktop. probe will not fall back to XQueryTree(root) here, because that list has different semantics (no WM_STATE, no _NET_ACTIVE_WINDOW, override-redirect windows included) and would answer a different question than the one asked."
    } } }
```

Rules:

- HTTP **501**, `ok:false`, `ran:false`. `reason:"NO_WINDOW_MANAGER"` is added to the 501 enum (§11).
- **An empty `windows` array is only legal after a successful enumeration.** Zero windows found by a real enumeration is `200 {count:0}`; no enumeration possible is `501`. The two are never the same body.
- **No silent `XQueryTree` fallback.** A fallback would return a list the contract does not define (no `foreground`, no `iconic`, override-redirect included) — a second truth, which is the failure this campaign exists to kill. If the operator wants that list, it is a later primitive with its own row and its own `tree_source`, not an implicit fallback under `ui-tree`.
- Advertised up front: `/contexts` reports `features["window.tree"]=false` + a `refusals` entry (§6, §13), so a flow routes around it before it starts.
- Same reasoning applies to `ui-window` on an X11 display with no WM: `foreground`/`restore` depend on `_NET_ACTIVE_WINDOW`, so they refuse `501 PRIMITIVE_UNSUPPORTED reason:"NO_WINDOW_MANAGER"` rather than attempting `XRaiseWindow` into a desktop with no focus policy. `move`/`resize`/`close` remain available (they are pure X calls) — the §8 table says which.
- `NO_WINDOW_MANAGER` (501, environmental: start a WM) is deliberately **not** `NO_GLOBAL_WINDOW_LIST` (403, the compositor's own security model on Wayland). One reason per cause; the operator's next action differs.

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

**`ok` means observed, not attempted.** The request reached the context (HTTP 200); `ok:true` asserts a re-read of the platform state after acting.

### 8.1 The 200-with-`ok:false` shape (C-7)

A window that refuses the action returns **HTTP 200, `ok:false`**, with one of three **closed** reasons. These are not refusal codes: nothing was denied before running — the request ran and the platform's observed state did not permit the outcome.

```json
{ "ok": false,
  "data": {
    "context_ref": {"requested":"x11::0","resolved":"x11::0","kind":"x11","source":"selector"},
    "window": "0x3c00007",
    "action": "foreground",
    "reason": "FOREGROUND_DENIED",
    "state": {"iconic":false,"foreground":false,"closed":false},
    "observed_ms": 301 } }
```

| `reason` (closed) | means | caller's next move |
|---|---|---|
| `FOREGROUND_DENIED` | the platform refused to make this window the focus owner (focus-stealing prevention, elevated window, no WM) | read `state`, decide whether to retry or report; do not assume the window is now foreground |
| `OCCLUDED` | for input: the point is covered by another window; the input was **not** injected | re-read `ui-tree` and act on the truth (`hit` names the covering window, §9.1) |
| `NOT_SUPPORTED` | this backend cannot perform this action on this window (e.g. `move` under a WM that refuses `XMoveWindow`) | read `features["window.control"]` at `/contexts` and route |

v2 used all three of these strings in examples while they appeared in neither closed enum, so a caller could not catch them — the exact "failed vs unsupported" ambiguity the brief asks about. They are now enumerated, in one place, with the 200-`ok:false` rule attached. `state` is **always** the observed state, never the requested one; `state.closed` is additive because a successful `close` is otherwise unobservable.

### 8.2 Per-action backend table

Windows column `[S]` — planned, **unverified** (W-4, W-5). No `✓` in that column means anything measured.

| action | params | Windows `wts` `[S]`, unverified (W-4) | X11/EWMH | Wayland (foreign-toplevel) | Android a11y APK `[U]` |
|---|---|---|---|---|---|
| `foreground` | — | plan: `ShowWindow(SW_RESTORE)` if iconic → `SetForegroundWindow` → `AttachThreadInput`/`BringWindowToTop` fallback, poll 300 ms, verify `GetForegroundWindow()==hwnd` — none of it run | `_NET_ACTIVE_WINDOW` client message (source=2), poll root property; **no WM → `501 NO_WINDOW_MANAGER`** (§7.1) | `activate` request; verified via `activated` signal | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` |
| `minimize` | — | plan: `ShowWindow(SW_MINIMIZE)`, verify `IsIconic` | `WM_CHANGE_STATE IconicState` (ICCCM), verify `WM_STATE`; no WM → `501 NO_WINDOW_MANAGER` | `set_minimized(true)` | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` |
| `restore` | — | plan: `ShowWindow(SW_RESTORE)`, verify `!IsIconic` | `_NET_ACTIVE_WINDOW` + map, verify `NormalState`; no WM → `501 NO_WINDOW_MANAGER` | `set_minimized(false)` | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` |
| `move` | `x,y` | plan: `SetWindowPos(SWP_NOSIZE|SWP_NOZORDER|SWP_NOACTIVATE)`, verify rect origin | `XMoveWindow`, verify `XGetGeometry`; WM may refuse → `200 ok:false NOT_SUPPORTED` | `R:PRIMITIVE_UNSUPPORTED/NOT_EXPOSED` — the protocol has no move | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` |
| `resize` | `w,h` | plan: `SetWindowPos(SWP_NOMOVE|…)`, verify rect size | `XResizeWindow`, verify `XGetGeometry` | `R:PRIMITIVE_UNSUPPORTED/NOT_EXPOSED` | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` |
| `close` | — | plan: `PostMessage(WM_CLOSE)` — **never** `TerminateProcess`; verify absent within 2000 ms | `_NET_CLOSE_WINDOW` client message; verify `DestroyNotify`/absence | `close` request | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` |

**Correction (P4/Android, falsification C-12/§7.7):** v2's Android a11y column claimed the node-action call covered all six window actions. It does not: an accessibility node action operates on a *node inside an application*, and no node action moves, resizes, minimises, raises or closes a top-level window; §12 additionally has no principal model for it. Every Android cell in this table is now a typed refusal. The a11y lane keeps what it can actually do — node actions for *input* (§9), not window management.

Rules: per-action availability is advertised (`features["window.control"]` is a **list**), so a caller that sees `move` missing on Wayland learns it from `/contexts`, not from a failure mid-flow; `window` is validated inside the resolved context before any action (`404 WINDOW_NOT_FOUND`, `detail{resolved_context, window}`); unknown action → `400 INVALID_PARAMS reason:"OUT_OF_RANGE"` with `detail.allowed`.

## 9. P5 — pointer, keyboard, text

All seven endpoints share: `context` (§5), `window` (relative coords + hit-test), and the same result envelope.

### 9.1 Geometry and the occlusion rule (the measured bug, generalised)

- `window` absent → `x,y` are absolute context px, exactly as today (`platform_windows.go:436`, `platform_linux.go:237`).
- `window` present → `x,y` are **relative to `window.rect` origin**, resolved **at dispatch time** (a window that moved between `ui-tree` and the click cannot misplace it).
- **Occlusion hit-test, always on, no knob:** compute the absolute point → ask the platform what is on top there (`WindowFromPoint` `[S]` W-5 / `XQueryPointer`+`XTranslateCoordinates` / Wayland `403 OS_FORBIDDEN` / a11y `501 NO_BACKEND`). If the top window is not the requested one, **the click is not performed**:

```json
{ "ok": false,
  "data": {
    "reason": "OCCLUDED",
    "hit": {"id":"0x3c0011a","title":"…:11.11 SYSTEM LAYOUT","owner_pid":3180},
    "detail": {"intended":"0x3c00007","point":{"x":42,"y":96}},
    "ran": false } }
```

HTTP 200 (the request ran; the input intentionally did not), `ok:false`, `reason` from §8.1's closed enum. The caller re-reads `ui-tree` and acts on the truth.

- `window` present but not in the resolved context → `404 WINDOW_NOT_FOUND`, before any injection.
- `window` present but not focused for `text-input`/`key-*` → the primitive foregrounds it **only if `focus:true`** is sent (additive field, default false); otherwise `403 PRIMITIVE_REFUSED reason:"NO_FOCUS_TARGET"`. Rationale: stealing focus during typing is a silent context switch.

### 9.2 `count=2` is a real double-click

One primitive call emits `down,up,down,up` with the gap **≤ 50% of the platform's double-click interval** (`GetDoubleClickTime()` on Windows `[S]` (W-6), multi-click time on X11 via `XGetPointerControl`/default 400 ms, seat's configured interval on Wayland). The gap is echoed as `detail.gap_ms`, and the response is `{ok:true,count:2,gap_ms:120}`. Two separate `pointer-click` calls are **not** equivalent and the contract says so, because the app decides on timing. Android: no multi-tap backend is specified; `count>1` on a Termux context is `501 PRIMITIVE_UNSUPPORTED reason:"NO_BACKEND"` rather than two taps pretending to be a double-tap.

### 9.3 Scroll sign convention (one convention, mapped per backend) — C-2/C-3 corrected

`delta` is in **notches**; **negative = up / away from the user / toward the beginning**. The backend maps, it does not reinterpret.

| backend | mapping for `delta=-1` (one notch up) |
|---|---|
| Windows `[S]` (W-7) | `mouse_event(MOUSEEVENTF_WHEEL, dwData = +120)` — i.e. **`dwData = -delta*120`**. Windows' own positive wheel is *away from the user*, so a negative `delta` (up) is a **positive** `dwData`. v2 printed `-120`, which scrolls **down**; the row contradicted its own convention |
| X11 | `XTestFakeButtonEvent(button 4, press, release)` once per notch — button 4 **is** up |
| Wayland (portal/libei) | `ei_scroll` with `v120 = -delta*120`; if the portal exposes no scroll capability → `501 PRIMITIVE_UNSUPPORTED reason:"BACKEND_MISSING"` |
| Android (a11y) | **`ACTION_SCROLL_BACKWARD`** for `delta<0` (toward the beginning) and `ACTION_SCROLL_FORWARD` for `delta>0`; v2 had these inverted. Node not scrollable → `403 PRIMITIVE_REFUSED reason:"NOT_SCROLLABLE"` |

`unit:"px"` is refused with `501 PRIMITIVE_UNSUPPORTED reason:"UNIT_NOT_SUPPORTED"` on every backend that lacks pixel-precise wheels — a partial scroll is worse than a refusal because it moves the list by an unknown amount.

**The test derives the expectation from the convention, not from this table** (the falsification's §7.1 point): `delta=-1` must produce *content movement toward the top*. On X11 that is observable as button 4; on Windows as `dwData>0`; on Android as `ACTION_SCROLL_BACKWARD`. A test that copies the table's number proves nothing.

### 9.4 Unicode text (today: bytes)

| backend | method | fidelity |
|---|---|---|
| Windows `wts` `[S]` (W-8) | plan: `SendInput` + `KEYEVENTF_UNICODE`, one event per **UTF-16 code unit** (surrogate pairs are two events, which is what the OS expects) | claim only; never run |
| X11 | remap a spare keycode to `0x01000000+codepoint` (`XChangeKeyboardMapping`, spare keycode discovered by scanning the map), `XTestFakeKeyEvent`, restore the map after the string | all of UCS-2; astral planes where the keycode range allows, else `501 PRIMITIVE_UNSUPPORTED reason:"CODEPOINT_UNSUPPORTED"` naming the codepoint |
| Wayland | `zwp_text_input_v3.commit_string` if the focused surface has a text-input v3 object; otherwise `403 PRIMITIVE_REFUSED reason:"NO_TEXT_INPUT_PROTOCOL"` | full Unicode **only when the app exposes the protocol**; no keyboard-emulation fallback (there is none on Wayland) |
| Android (Termux) | no IME, no a11y → refused `403 PRIMITIVE_REFUSED reason:"NO_INPUT_METHOD"` | — |
| Android (a11y APK) `[U]` | `ACTION_SET_TEXT` on the focused editable node — **sets** the node's text, does not emit keystrokes. A node that rejects it is refused (`403 PRIMITIVE_REFUSED reason:"NOT_EDITABLE"`), not typed into by other means | full Unicode via the node API; semantics differ from typing and the field says so |

`encoding:"utf16"` means the caller supplies code units and probe must **not** re-encode: on Windows it emits them verbatim; elsewhere it decodes to UTF-8 first and reports `detail.transcoded:true`. Never silently lossy.

## 10. P6 — capture

`POST /api/v1/agents/{id}/capture`

**Request (C-5 corrected — `window` is a string, exactly as §4 types it):**

```json
{ "context":"x11::0", "window":"0x3c00007", "format":"png", "hash_only":true }
```

**Response, `hash_only:true` (C-8 corrected — `data` and `size_bytes` are ABSENT, not present-and-empty):**

```json
{ "ok": true, "data": {
  "context_ref": {"requested":"x11::0","resolved":"x11::0","kind":"x11","source":"selector"},
  "target": {"kind":"window","window":"0x3c00007","id_kind":"xid"},
  "format": "png", "width": 1038, "height": 118,
  "hash": "sha256:4f9c…", "changed": true, "baseline": "sha256:1b2e…",
  "source": "window_direct",
  "units": "px", "dpi_scale": 1.0 } }
```

**Response, `hash_only:false`** — identical, plus `"data":"<base64>"` and `"size_bytes": 51734`.

| field | semantics (exact) |
|---|---|
| `hash` | `sha256:` + hex of the **decoded RGBA8 pixels**, stride-normalised to `width*4` — **not** of the encoded file. Re-encoding the same pixels to jpeg must not change the hash; a 1-pixel change must |
| `changed` | `true`/`false` vs the baseline; **`null` when there is no baseline** (first sight of this target, or cache evicted). Never fabricated |
| `baseline` | which hash was compared; `""` when none |
| `width`,`height` | pixels actually captured (a window may have changed size since `ui-tree`). **`0` is not an acceptable value**: it means the backend did not parse its own output (measured, SS-11) and must be a `500 INTERNAL_ERROR` with `detail{backend,stage:"decode"}` instead of a success |
| `source` | `window_direct` (content read from the window, occlusion-independent) \| `screen_region` (pixels lifted from the screen; occlusion **does** affect it) \| `compositor` |
| `data` | base64 image. **The key is absent when `hash_only:true`** — never `""`, never `null`, never a placeholder string (C-8) |
| `size_bytes` | encoded size. **Absent when `hash_only:true`** for the same reason: nothing was encoded |
| `format` | `png` or `jpeg`. If the backend can only emit one, the requested other one is re-encoded **by the agent**, from raw pixels, and `detail.encoded_by` is `"agent"` — the only encoder there is (C-9). The server never holds raw pixels (`CaptureResult` carries base64 only, `messages.go:467-473`), so a server-side transcode would mean decoding the agent's output: a new failure path for no benefit |

Baseline cache: per `(context, target, dpi_scale)` key, LRU 64 entries, TTL 600 s, in-agent, never persisted. A `since` hash on the request overrides the stored baseline for that call (stateless hash-only polling).

Backends — Windows is `[S]`, planned and unverified (W-8); the Android a11y column is corrected per C-12/§7.7:

| target / OS | Windows `wts` `[S]` | X11 (+headless Xvfb) | Wayland | Android Termux (today) | Android a11y APK `[U]` |
|---|---|---|---|---|---|
| full screen | plan: `GetDC(0)`/`BitBlt` — the only implementation is `SM_CXSCREEN` + `BitBlt(…,0,0)` (primary monitor, `platform_windows.go:316,366`), so the **virtual screen with negative origins that §3.3 defines is not implemented**; the plan is per-monitor enumeration + `CreateDC(device)` per monitor | `XGetImage(root)`; today `import`/`scrot` shell-out returns `width:0` (`platform_linux.go:191-210`) | portal `ScreenCast` (consent) → `403 PRIMITIVE_REFUSED reason:"CONSENT_REQUIRED"` without it | `403 PRIMITIVE_REFUSED reason:"CONSENT_REQUIRED"` (no MediaProjection in a Termux process) | MediaProjection + consent → `source:"screen_region"` |
| window | plan: `PrintWindow(PW_RENDERFULLCONTENT)` → `source:"window_direct"`; unusable surface → fall back to screen-region of `rect` **and say** `source:"screen_region"` | `XGetImage(window)`; redirected windows need `XCompositeNameWindowPixmap` → `source:"compositor"` | `501 PRIMITIVE_UNSUPPORTED reason:"NOT_EXPOSED"` (no per-window cast) | `501 PRIMITIVE_UNSUPPORTED reason:"NO_BACKEND"` | **`501 PRIMITIVE_UNSUPPORTED reason:"NOT_EXPOSED"`** — corrected: accessibility yields node **bounds**, MediaProjection yields **screen pixels**; cropping projection pixels by bounds is `screen_region` (occlusion-affected), never `window_direct`. v2 labelled it `window_direct`, which was unobtainable |
| region | plan: `BitBlt(rect)` | `XGetImage(rect)` | portal full frame + crop → `source:"screen_region"` | `501 PRIMITIVE_UNSUPPORTED reason:"NO_BACKEND"` | ✓ projection + crop → `source:"screen_region"` (the honest label for the same pixels) |

Cost guard: `w*h > 8_294_400` (4K) → `400 INVALID_PARAMS reason:"REGION_TOO_LARGE"` with `detail.max_px`; no silent downscale (a downscaled capture that claims to be the window is a second truth).

## 11. Per-primitive × platform backend and refusal matrix (acceptance quote #2)

**Markers, and what they license.** `M` = **the route's current behaviour** was measured on a HEAD build (1.19.1) in the falsification's live run — which for most rows means "measured to be a 404, or measured to drop the field", not "measured to work". A `M` never means the backend exists. `S` = source-planned only: **no code in the tree, nothing run**. `U` = no node and no source path. `R:CODE/reason` = a typed refusal, i.e. a **specification** (ratifiable as spec, not a measured behaviour). A `✓` appears only where `M` does — there is no `✓` in the Windows column, and its absence there is the point of `## Ratification scope`.

| primitive | Windows `wts` | Linux X11 (+headless Xvfb) | Wayland | Android Termux (today) | Android a11y APK (future) |
|---|---|---|---|---|---|
| **P1 selector** on `exec` | `S` (W-1): plan `CreateProcessAsUserW` + WTS token | `M` route measured (`exec` runs); **selector unimplemented** (`grep -c '"context"'` in the protocol → 0): phase 1 adds the field, then same-uid direct / root fork+setuid | `S` same-uid only; other seat → `R:CONTEXT_UNAVAILABLE/OTHER_USER` | `R:CONTEXT_UNAVAILABLE/OTHER_USER` for another uid, `R:CONTEXT_UNAVAILABLE/PRIVILEGE_NOT_HELD` for same-user-other-context (§12; C-11) | `U` |
| **P1 selector** on UI ops | `S` (W-2): plan `--context-worker`, never run | `M` route measured; selector unimplemented — phase 1/3 (`Display*` direct in-process) | `S` same seat only; cross-seat → `R:CONTEXT_UNAVAILABLE/KIND_NOT_APPLICABLE` | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` (no surface exists) | `U` |
| **P2 contexts** | `S` (W-1): plan `WTSEnumerateSessions` + `WTSQuerySessionInformationW` | `M` route measured (404 today — absence, not capability); backend is phase 2: `$DISPLAY` sockets + peer uid, `idle_s`/`logon_time` `-1`/`""` where unanswerable | `S` seats + peer credentials | `M` own uid only; **`active` is `null`** — profile enumeration needs root and is unverified `[U]`. v2's "root-visible profiles" was not implementable in a Termux process (c:70/c:187) | `U` |
| **P3 window tree** | `S` (W-4): plan `EnumWindows`; 0 window symbols in tree | `M` route measured (404 today); backend is phase 3: EWMH `_NET_CLIENT_LIST` + `WM_STATE`; **no WM → `R:PRIMITIVE_UNSUPPORTED/NO_WINDOW_MANAGER`** (§7.1), never `[]` | `S` only if `zwlr_foreign_toplevel_management_v1`; else `R:PRIMITIVE_REFUSED/NO_GLOBAL_WINDOW_LIST` | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` | `U` plan: `getWindows()` |
| **P4 control** | `S` (W-4): plan six actions, none run | `M` route measured (404 today); phase 4: six actions, `move`/`foreground` may return `200 ok:false NOT_SUPPORTED`; no WM → `501 NO_WINDOW_MANAGER` for the three focus-dependent ones | `S` foreground/minimize/restore/close; move/resize → `R:PRIMITIVE_UNSUPPORTED/NOT_EXPOSED`; without the protocol → `R:PRIMITIVE_REFUSED/NO_GLOBAL_WINDOW_LIST` | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` | **`R:PRIMITIVE_UNSUPPORTED/NO_BACKEND`** — corrected: the a11y node-action API is not window control (C-12) |
| **P5 pointer** | `S`: plan `SetCursorPos`+`mouse_event` (today's code is `platform_windows.go:430-454`, primary-screen, no window identity — the defect, not the cure) | `M` XTEST; today's `Click` hard-codes `click 1` and drops `button` (`platform_linux.go:237-243`, SS-2) | `S` libei/portal, consent → `R:PRIMITIVE_REFUSED/CONSENT_REQUIRED`; no portal → `R:PRIMITIVE_UNSUPPORTED/BACKEND_MISSING` | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` | `U` plan: `dispatchGesture`; **`move`/hover → `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND`** (no injectable cursor; falsification §2 row 5e) |
| **P5 scroll** | `S` (W-7): plan `MOUSEEVENTF_WHEEL`, `dwData = -delta*120` (§9.3, corrected) | `M` XTEST buttons 4/5/6/7 | `S` `ei_scroll` `v120 = -delta*120`; else `R:PRIMITIVE_UNSUPPORTED/BACKEND_MISSING` | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` | `U` plan `ACTION_SCROLL_BACKWARD` for `delta<0`; non-scrollable → `R:PRIMITIVE_REFUSED/NOT_SCROLLABLE` |
| **P5 drag** | `S`: plan down→moves→up | `M` XTEST sequence | `S` `ei_button` + motion, consent | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` | `U` plan `dispatchGesture` stroke |
| **P5 unicode text** | `S` (W-8): plan `KEYEVENTF_UNICODE`; today iterates bytes (`platform_windows.go:500-524`) | `M` keycode remap + XTEST; `type città è à 名前` reached `xdotool` intact in the falsification's live run (SS-4) | `S` `text_input_v3` only; else `R:PRIMITIVE_REFUSED/NO_TEXT_INPUT_PROTOCOL` | `R:PRIMITIVE_REFUSED/NO_INPUT_METHOD` | `U` plan `ACTION_SET_TEXT` (sets, does not type; §9.4) |
| **P6 capture** full | `S` (W-8): plan per-monitor `CreateDC`; today primary-only (`:316,:366`) | `M` today returns `width:0,height:0` (SS-11) — the defect the contract forbids; `XGetImage(root)` is the phase-6 cure | `S` portal, consent | `R:PRIMITIVE_REFUSED/CONSENT_REQUIRED` | `U` MediaProjection + consent |
| **P6 capture** window/region | `S` (W-8): plan `PrintWindow` / `BitBlt` | `M` today ignores `window`/`rect`/`format`/`hash_only` (SS-5…SS-8); `XGetImage` / `XComposite` is the phase-6 cure | `S` window `R:PRIMITIVE_UNSUPPORTED/NOT_EXPOSED`; region via portal+crop | `R:PRIMITIVE_UNSUPPORTED/NO_BACKEND` | window `R:PRIMITIVE_UNSUPPORTED/NOT_EXPOSED`; region: plan projection+crop → `source:"screen_region"` (corrected, C-12) |

Count: the Windows column is **11 cells, every one `S`, zero verified**; the Android a11y column is `U` except the two cells corrected to a typed refusal; the Linux column is `M` wherever the row was exercised, and every `M` says what it measured. Nothing in this table is a behaviour claim for a platform without a node.

**Refusal codes are three, not ten** (a caller catches the code; `reason` carries the detail):

| code | HTTP | when | reasons |
|---|---|---|---|
| `CONTEXT_UNAVAILABLE` | 409 | the requested context cannot be resolved/entered | §5.4 enum (9) |
| `PRIMITIVE_UNSUPPORTED` | 501 | this build/backend/deployment has no implementation of the primitive | `BACKEND_MISSING` · `NOT_IMPLEMENTED` · `NO_BACKEND` · `NOT_EXPOSED` · `HEADLESS_NO_DISPLAY` · **`NO_WINDOW_MANAGER`** · `UNIT_NOT_SUPPORTED` · `CODEPOINT_UNSUPPORTED` |
| `PRIMITIVE_REFUSED` | 403 | the platform's own security model forbids it for this principal | `OS_FORBIDDEN` · `CONSENT_REQUIRED` · `PERMISSION_NOT_GRANTED` · `NO_GLOBAL_WINDOW_LIST` · `NO_TEXT_INPUT_PROTOCOL` · `NO_INPUT_METHOD` · `NO_FOCUS_TARGET` · `NOT_SCROLLABLE` · `NOT_EDITABLE` |

Observed failures that are **not** refusals (§8.1, HTTP 200 + `ok:false`): `OCCLUDED` · `FOREGROUND_DENIED` · `NOT_SUPPORTED`.

Existing codes stay as they are: `INVALID_PARAMS` 400 (reasons in §5.5) · `WINDOW_NOT_FOUND` 404 · `UNSUPPORTED_AGENT` 409 · `TIMEOUT` 504 · `PROTOCOL_VIOLATION` 502 · `INTERNAL_ERROR` 500.

**"Can't" vs "may not" vs "didn't":** 501 = the code or the environment is not there (fixable by shipping or by starting a WM); 403 = the OS/compositor says no to this principal (policy, not a bug); 200+`ok:false` = it ran and the observed state said no. None is ever a silent stub.

## 12. Security boundary (contexts per privilege, per platform)

| principal | own context | other context, **same user** | other context, **different user** | context with no principal | non-interactive context |
|---|---|---|---|---|---|
| Windows **SYSTEM** (service) `[S]` | ✓ direct (W-1) | token, audited | token, audited with target user | `CONTEXT_UNAVAILABLE/NO_USER` | `NOT_INTERACTIVE` |
| Windows **user** `[S]` | ✓ direct | ✗ `PRIVILEGE_NOT_HELD` (SeTcbPrivilege, W-3) | ✗ `OTHER_USER` | `NO_USER` | `NOT_INTERACTIVE` |
| Linux **root** | ✓ | ✓ `setuid` child, audited | ✓ `setuid` child, audited with target uid | ✗ `NO_USER` | ✗ `NOT_INTERACTIVE` |
| Linux **user** | ✓ | ✗ `PRIVILEGE_NOT_HELD` | ✗ `OTHER_USER` | ✗ `NO_USER` | ✗ `NOT_INTERACTIVE` |
| Android **root** `[U]` | ✓ | ✓ `setuid` child | ✓ `setuid` child, audited | ✗ `NO_USER` | ✗ `NOT_INTERACTIVE` |
| Android **app uid** (Termux) | ✓ (own uid) | ✗ `PRIVILEGE_NOT_HELD` | ✗ `OTHER_USER` | ✗ `NO_USER` | ✗ `NOT_INTERACTIVE` |

**C-11 corrected:** v2's §11 said Android other-uid is `PRIVILEGE_NOT_HELD` while this table says `OTHER_USER` for a different user. This table is the ruling and §11 now matches it: `OTHER_USER` = a different principal; `PRIVILEGE_NOT_HELD` = the same principal in a context it may not enter (it would need authority it does not hold). The distinction is the operator's next action — get credentials vs re-run as root.

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
| `text_len` + `text_sha256` | int/string | `text-input`: **length and digest, never the plaintext** |
| `ok`,`ran`,`error_code`,`reason`,`duration_ms` | — | existing + `ran` (false when nothing was injected) |

Recording rule (closed list, the reviewer tests it):
1. every refusal (`result:"denied"`), whatever the endpoint;
2. every `ui-window` call, success or not;
3. every `contexts` read (count in `params`, never the user list);
4. every call whose `context_resolved != agent_context`;
5. every `text-input` (digest row only);
6. nothing else — a window read in the agent's own context is not written.

## 13. Capability advertisement (a caller never guesses)

Three layers, all additive to the existing flat `Capabilities []string` (`messages.go AgentInfo`, `registry.go:165`):

**Layer 1 — flat strings (unchanged, back-compat):** `exec`, `filesystem`, `capture`, `input`, plus `ui` and `context` when the new features exist. An old server keeps working; an old client keeps working.

**Layer 2 — structured feature matrix (new, on `AgentInfo` and echoed by `health`):**

```json
{ "features": {
    "v": 1, "platform": "linux", "backend_family": "x11",
    "context": {"selector": true, "enumerate": true, "kinds": ["x11","tty"]},
    "window": {"tree": true, "id_kind": "xid", "tree_source": "ewmh_client_list",
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

On an X11 display with **no window manager** the same agent advertises honestly instead of failing late:

```json
"window": {"tree": false, "control": ["move","resize","close"]},
"refusals": [{"feature":"window.tree","code":"PRIMITIVE_UNSUPPORTED","reason":"NO_WINDOW_MANAGER"},
             {"feature":"window.control.foreground","code":"PRIMITIVE_UNSUPPORTED","reason":"NO_WINDOW_MANAGER"}]
```

`refusals` is a list of `{feature, code, reason}` the agent **knows it cannot do here** (e.g. `{"feature":"capture.window","code":"PRIMITIVE_UNSUPPORTED","reason":"NOT_EXPOSED"}` on a Wayland session). Advertising a refusal up front is worth more than a fast failure: a flow can route around it before it starts.

**Layer 3 — per-context overrides:** `GET /contexts` carries `features` per row (§6). The agent-level matrix is the union; a specific context may support strictly less (the `uid` row above supports nothing UI). **Callers read the context row, not the agent matrix, before acting in that context** — that sentence is the whole advertisement contract.

**Server-side use:** `capabilityForCommand` (`enrollment.go:414-435`) gains granular return values for the new commands (`context.selector`, `window.tree`, `window.control`, `input.pointer`, `capture.window`…), and the existing pre-dispatch check (`server_api.go:789-799`) refuses `409 UNSUPPORTED_AGENT` with `detail{feature, agent_version, ran:false}` when the matrix lacks it.

**One existing rule must NOT be inherited — this is the trap in the current gate.** `server_api.go:791-796` reads: *"Agents with no capabilities advertised are treated as having all capabilities (backward compat)"*. For the four coarse buckets that is defensible. For the six primitives it is precisely the silent fallback this contract exists to kill: an old-device agent that advertises nothing would be **allowed** to receive `ui-window{action:"close"}` and would ignore the unknown fields. Therefore:

| command class | empty/unadvertised capability | why |
|---|---|---|
| coarse (existing `exec`/`filesystem`/`capture`/`input`) | allowed — unchanged | 1.19/1.20 fleet compat, no behaviour change permitted |
| granular (the six primitives, all new message types) | **refused** `409 UNSUPPORTED_AGENT`, `detail{feature, missing:"features", ran:false}` | a new command type never existed in an old agent; "empty means all" would be a lie about a message the agent's dispatcher cannot even parse |

`GET /api/v1/agents/{id}/capabilities` exposes the matrix to callers: `{features, as_of, source:"registration"|"health", stale:bool, agent_version}`. `stale:true` when the last health is older than 90 s.

## 14. Worked sequence — "bring window X forward, click relative to X, read X's title", zero `exec` (acceptance quote #5)

X11 lane only. **There is no Windows lane in this section** — every Windows sentence in v2's version of it was `[S]`, and a worked sequence that has never been worked is an illustration, not evidence (W-1…W-8).

```
1  GET  /api/v1/agents/AG1/contexts
   → contexts[0] = {id:"x11::0", user:"flk", addressable:true,
                    features:{"window.tree":true,"window.control":[6 actions],"capture.window":true}}

2  POST /api/v1/agents/AG1/ui-tree   {"context":"x11::0","visible_only":true}
   → data.windows[3] = {id:"0x3c00007", title:"…/Control Panel", class:"Qt51516QWindowIcon",
                        rect:{x:0,y:0,w:1038,h:118}, iconic:false, foreground:false, owner_pid:2640}

3  POST /api/v1/agents/AG1/ui-window {"context":"x11::0","window":"0x3c00007","action":"foreground"}
   → 200 {"ok":true,"data":{"state":{"iconic":false,"foreground":true,"closed":false},"observed_ms":81}}

4  POST /api/v1/agents/AG1/ui-tree   {"context":"x11::0","window":"0x3c00007"}
   → 200 {"ok":true,"data":{"count":1,"windows":[{id:"0x3c00007",
            rect:{x:0,y:0,w:1038,h:118}, foreground:true}]}}
     (re-read: a restore can move the window — never reuse a stale rect.
      C-6 corrected: the response is the envelope, never a bare array)

5  POST /api/v1/agents/AG1/pointer-click
   {"context":"x11::0","window":"0x3c00007","x":42,"y":96}
   → absolute = rect origin + (42,96) resolved at dispatch; hit-test passes; 200 {"ok":true,"count":1}

6  POST /api/v1/agents/AG1/ui-tree   {"context":"x11::0","visible_only":true}
   → data.windows[7] = {id:"0x3c0011a", title:"11.11 SYSTEM LAYOUT", foreground:true}
     (title read from the tree — no exec, no OCR, no screenshot)
```

Six calls, zero `exec`, zero P/Invoke/shell, and every response names the context it acted in. If step 5's point were covered by another window, it returns `200 ok:false` with `reason:"OCCLUDED"` naming the covering window — instead of today's `{"success":true}`.

Wayland variant, same flow: step 1 succeeds (seat), step 2 succeeds **only** if the compositor exposes foreign-toplevel (else `403 NO_GLOBAL_WINDOW_LIST`, advertised at step 1); step 3 succeeds only for `foreground|minimize|restore|close`; step 5 returns `403 PRIMITIVE_REFUSED/CONSENT_REQUIRED` if no portal — the caller learns that at step 1 from `features` and `refusals`, before it starts.

Headless variant (Xvfb, no WM): step 1 succeeds and advertises `window.tree:false` + `NO_WINDOW_MANAGER`; step 2 returns `501 PRIMITIVE_UNSUPPORTED/NO_WINDOW_MANAGER` with the remediation, and the flow stops there instead of acting on an empty list.

## 15. Edge cases and failure paths (each with a named outcome)

| # | case | outcome |
|---|---|---|
| X1 | context vanishes between ladder step V1 and V6 | `CONTEXT_UNAVAILABLE/GONE`; no retry, no fallback to own context |
| X2 | worker hangs (GPU driver, modal loop) | 5 s (ui-tree 3 s) → terminate → `504 TIMEOUT`, `detail.ran:false`; for `ui-window` `ran:true, applied:false, state:null` (an unobservable action is reported as unknown, never as success) |
| X3 | window destroyed between tree and click | `404 WINDOW_NOT_FOUND` (validated before injection) |
| X4 | window recreated under the same native handle | `id` is context-scoped and re-read; the response echoes `class+owner_pid`, so a swap is visible. Documented limitation: a handle is not stable identity; `class+title+pid` is |
| X5 | window moves between tree and click | `window`-relative coords resolve `rect` at dispatch — safe by construction; absolute mode is documented as caller's risk |
| X6 | DPI change mid-session (RDP reconnect, monitor swap) | every response echoes `dpi_scale`; a change between two `ui-tree` reads is visible as a rect discontinuity; capture hash changes because pixels changed — never silently rescaled |
| X7 | fractional scaling (Wayland 1.5×) | `units:"px"` is physical; `dpi_scale` echoes 1.5; window rect and input share the space, so clicks land. If the backend cannot report physical px → `PRIMITIVE_UNSUPPORTED/NOT_IMPLEMENTED` rather than a guess |
| X8 | foreground lock (elevated window, other app) | `200 ok:false reason:"FOREGROUND_DENIED"` with observed `state` (§8.1); the click is not attempted while the target is not foreground and the hit-test fails |
| X9 | screen locked / screensaver | Windows `[S]`: capture returns the lock surface and `foreground` fails → observed `ok:false` (W-4). X11: `XGetImage` yields the locker window; reported honestly, no attempt to bypass |
| X10 | two operators drive one context | per-context mutex serialises each call; the contract does not claim atomicity across calls — that is a flow concern |
| X11 | portal consent revoked mid-flow (Wayland) | next call → `403 PRIMITIVE_REFUSED/CONSENT_REQUIRED`; no retry loop |
| X12 | old agent in the fleet (1.19/1.20) | server pre-dispatch `409 UNSUPPORTED_AGENT`, `ran:false`, before anything is sent |
| X13 | headless host with `DISPLAY` unset | `501 PRIMITIVE_UNSUPPORTED/HEADLESS_NO_DISPLAY`; `exec`/`filesystem` unaffected |
| X14 | non-root agent asked for another uid (Linux) | `CONTEXT_UNAVAILABLE/PRIVILEGE_NOT_HELD` before any `setuid` attempt |
| **X15** | **X server present but no window manager** (Xvfb default, measured substrate) | `501 PRIMITIVE_UNSUPPORTED reason:"NO_WINDOW_MANAGER"` with remediation (§7.1); `ui-window` focus-dependent actions likewise; **never `windows:[]`**. Advertised at `/contexts` so the flow never starts |
| **X16** | unknown field, wrong type, or duplicate top-level key on a selector-bearing route | `400 INVALID_PARAMS` with `reason` from the §5.5 enum, `detail.ran:false`, before dispatch — instead of today's silent drop (SS-1…SS-11) |
| **X17** | `hash_only:true` on a backend whose capture failed to decode its own output | `500 INTERNAL_ERROR`, `detail{backend,stage:"decode"}` — never `width:0` with `ok:true` (SS-11) |

Graceful failure, component by component: server handler → typed status + `ran:false`; agent verification → typed code + remediation + audit row; worker → one JSON object, non-zero exit only if it cannot speak; worker crash → EOF mapped to `INTERNAL_ERROR` with `detail.worker_exit`; timeout → kill + 504; absent platform backend → advertised refusal at `/contexts` and `/capabilities`, and the same code if called anyway.

## 16. Future projections

**1 month — one station, one operator.** The first deployment is whichever lane has a node; on this fleet that is the Linux/X11 one, because the Windows service lane has no host and is `[S]` (W-1). Volume ~200 tree calls + ~60 clicks/day; one worker per call at 40-80 ms is invisible against HMI latency. The Windows lane is the *next* month's work, gated on OD-1 and a node. Risk: the operator's reflex is still `exec` + OS scripting for anything not in the matrix — mitigation is the worked sequence (§14) and the `refusals` advertisement.

**6 months — 4-6 stations, mixed OS (three Windows HMIs, two Linux panels, one Android tablet).** ~5k UI calls/day, `/contexts` polled every 30 s by a fleet dashboard. Two strains: (a) worker spawn becomes p95 if input rate rises — measurement point is *sustained >5 calls/s per station*; below that, pooling is complexity with no payoff; (b) the per-context `features` matrix becomes the dashboard's core column and must stay derived from `classifyContext`, never recomputed server-side. The Android lane is refused by design until an APK with an AccessibilityService exists — a tablet in the fleet is an exec+fs node, and that is stated, not hidden. **If a Windows node still does not exist at 6 months, the Windows column should be deleted from the contract rather than carried as `[S]` forever** — an unverified column that nobody can verify is a liability, not a plan.

**1 year — context becomes a fleet concept.** Flows pinned to "the context where operator X is on watch", station handover (7 → 11) mid-run, and `window.id` as the addressing key shared by tree, input and capture. If a second desktop/winstation or a nested compositor appears, the selector generalises to `{context, desktop}` — the ladder is the seam (V6 already passes `lpDesktop`). What would force a redesign: UI operations with **no logged-on principal** (a headless spooler): the worker model has no token to run under, and the honest answer is a different product, not a patch.

## 17. Decision log

| # | decision | why | cost accepted |
|---|---|---|---|
| N1 | `context` is an opaque advertised string; `session` is an alias | one wire format for four platforms; kind-specific ids stay inside the agent | callers handle a string, not an int |
| N2 | absent selector = own context; present = exact or refused | 0 is a legal session id and "absent" must differ from "0" | a pointer/absence distinction on the wire |
| N3 | three refusal codes, reasons in a closed enum | a caller catches a code, not eleven strings | a wider `detail` object |
| N4 | `PRIMITIVE_REFUSED` = 403, `PRIMITIVE_UNSUPPORTED` = 501 | 403 = policy said no; 501 = not there (code or environment) | two codes instead of one |
| N5 | window `id` opaque + `id_kind` + `native`, `hwnd` compat on `wts` **only** | one shape for hwnd/XID/toplevel/a11y; the compat field keeps 1.21 rows unchanged | one extra field per row |
| N6 | `window`-relative coords + always-on occlusion hit-test | the measured bug is OS-independent | a legitimate click on an overlapped point is refused; caller uses absolute mode |
| N7 | `ok` = observed, not attempted; 200 = the request reached the context | the operator's complaint was a lie, not a failure | callers must read `ok`, not only the status |
| N8 | `count:2` is one primitive call with a platform-timed gap | two calls are not a double-click; apps decide on timing | one field, one code path |
| N9 | scroll `delta` in notches, negative = up, per-backend mapping documented | one convention beats four | callers cannot ask for pixels everywhere |
| N10 | capture hash is over **pixels**, not bytes; `changed:null` without baseline | re-encoding must not look like a change; a fabricated `false` is worse than `null` | agent keeps a 64-entry baseline cache |
| N11 | `source:"window_direct"|"screen_region"|"compositor"` in every capture | occlusion susceptibility differs per backend | one more field |
| N12 | capability matrix is structured and **per context**, flat strings kept | a caller can route before acting; old server/client unaffected | a second advertisement shape to keep in sync |
| N13 | audit stores `text_sha256` + length, never plaintext | operators type credentials into HMIs | text content is not forensically available |
| N14 | no config knob anywhere; predicates are the process principal | a knob is a second policy place and a silent-fallback surface | no per-site exceptions |
| N15 | per-context mutex shared by tree/control/input/capture | input and foreground cannot race | a slow capture blocks a click in that context |
| N16 | Wayland/Android advertise refusals **up front** rather than erroring late | a flow can route around a platform limit before it starts | the matrix must be honest at registration, or it becomes the new lie |
| **N17** | **`display` is a compatibility alias on exactly two routes** (`capture`, `stream-start`), resolved through the ladder, never dropped | those structs already carry it (`messages.go:454-457`, `:487-491`) and the console sends it; refusing it would break a shipping caller, and honouring it costs one mapping. Dropping it *is* SS-8 | a third selector spelling, scoped to two routes and one kind |
| **N18** | **strict decode is per handler + per-route allow-list, not per dispatcher** | a dispatch-table check cannot see unknown fields on a known type (SS-13); four routes share `InputParams`, so type-level strictness alone still allows cross-parameter smuggling | 14 call sites instead of 1, and a 400 where junk used to be ignored (OD-9) |
| **N19** | **observed failures (200+`ok:false`) get their own closed enum, separate from refusal codes** | 403/409/501 mean *nothing ran*; `OCCLUDED`/`FOREGROUND_DENIED`/`NOT_SUPPORTED` mean *it ran and the state said no*. Mixing them is what made C-7 uncatchable | two enums to document instead of one that lies |
| **N20** | **ratification is scoped by evidence class (`M`/`S`/`U`), not by document** | a contract can be ratified as a specification while its Windows column remains unverified; conflating the two is how P10-01 v2 got a FIX-FIRST | the ratifier must read the scope, and can refuse a column instead of the whole |

## 18. Build phases (phase 1 shippable alone) and tests

| phase | content | starts without reading later phases |
|---|---|---|
| **1** | protocol: `Context *string` + `Session *int` on every selector-bearing type (`ExecParams`, `InputParams`, `ScreenParams`, `ScreenStreamParams` + the new `UITreeParams`/`UIWindowParams`/`PointerScrollParams`/`PointerDragParams`), `Display` kept as the alias (N17); the three codes + reason enums + the §8.1 observed-failure enum; `features` matrix struct; `capabilityForCommand` granular entries; **E3a/E3b/E3c strict decode on all 14 sites (§5.5)**; server E1 gate + E2 echo assertion; `stream_id` required on stream-stop; handlers that refuse when the feature is absent | **yes** — behaviour-neutral for a caller that sends only documented fields: no selector = today's behaviour byte-for-byte |
| 2 | `contexts` + `classifyContext` + per-platform enumeration (X11 sockets/peer-uid; Windows WTS is `[S]`, phase 8 at the earliest) — no worker needed; **WM presence detection feeds `features["window.tree"]` and `refusals`** (§7.1) | phase 1 types only |
| 3 | `ui-tree` + X11 direct backend (EWMH + the no-WM refusal); Windows `--context-worker` is a separate unit gated on a node (W-2) | needs the ladder from phase 2 |
| 4 | `ui-window` (six actions, observed-`ok` with §8.1 reasons, id validation, no-WM refusal for focus actions) | yes |
| 5 | input: `window`-relative + hit-test + `count` + `scroll` + `drag` + `pointer-move` + Unicode text | yes |
| 6 | `capture`: window/region/format/hash_only + baseline cache + the `width>0` assertion | yes |
| 7 | `capabilities` endpoint + matrix, docs (`docs/PRIMITIVES.md`, backend matrix, remediation strings), non-strict routes outside §5.5 (hygiene backlog) | — |

**Tests that must fail on old behaviour** — each names the defect it pins:

1. **Strict decode, per route** (§5.5): for each of the 14 sites, `{"<field-not-in-allow-list>":1}` → `400 INVALID_PARAMS/UNKNOWN_FIELD` with `detail.allowed`. Pins SS-1 (`exec`+`session`), SS-2 (`pointer-click`+`button`/`count`/`window`), SS-4 (`text-input`+`encoding`), SS-9 (`stream-start`+`context`), SS-7 (`capture`+`window`/`rect`), SS-5 (`hash_only`), SS-6 (`format`/`quality`).
2. **Cross-parameter smuggling:** `{"text":"abc"}` on `pointer-click` → 400 (not a click at 0,0); `{"keys":["Ctrl","S"]}` on `text-input` → 400.
3. **Duplicate key:** `{"context":"x11::0","context":"wts:7"}` → `400 DUPLICATE_FIELD` (today: last wins, silently).
4. **Type:** `capture {"window":66102}` → `400 TYPE_MISMATCH` (C-5).
5. `stream-stop` with no `stream_id` → `400 STREAM_ID_REQUIRED`; with an unknown id → 404-class refusal, never "stopped every stream" (SS-10).
6. `stream-frame?context=x11::0` → `400 UNKNOWN_FIELD` (§5.6).
7. Each `CONTEXT_UNAVAILABLE` reason (9 cases, incl. `KIND_NOT_APPLICABLE` for `session:7` on X11 and `NO_USER`).
8. Missing `context_ref` echo → 502.
9. `ui-tree` **no-WM** → `501 NO_WINDOW_MANAGER` with remediation, and `windows` absent from the body (not `[]`); with a WM and zero clients → `200 count:0` (the two must differ).
10. `ui-tree{window}` filter and `truncated`; response is the envelope, never a bare array (C-6).
11. `hwnd` present on a `wts` row, **absent** on `x11`/`wl`/`a11y` rows (C-4).
12. All six `ui-window` actions with a simulated `FOREGROUND_DENIED` → `200 ok:false` + observed state + enumerated reason (§8.1).
13. Occlusion refusal naming the covering window; `count:2` gap within the platform interval.
14. **Scroll sign per backend, expectation derived from the convention:** `delta=-1` moves content toward the top on all four backends (X11 button 4; Windows `dwData>0`; Wayland `v120>0`; Android `ACTION_SCROLL_BACKWARD`).
15. Unicode round-trip `àèìòù — € 名前 🙂` (4 backends, refusals where advertised).
16. `hash_only` returns **no `data` key and no `size_bytes` key** and `changed:null` on first call (C-8); identical pixels re-encoded png→jpeg keep the same hash; a failed decode is `500`, never `width:0 ok:true` (SS-11).
17. `detach+context` → 400; selector on `stream-start` → resolved or refused; no-selector regression on all 14 endpoints (identical envelopes to base `ec647ed`).

**The phase-1 gate is testable without a platform**: every case above is an HTTP/agent-envelope assertion. That is deliberate — it is the part of the contract that can be proven on this host the day it is built.

## 19. Verifier appendix (re-runnable at review time, no unreviewable claims)

```bash
cd /opt/data/projects/PROBE_UPGRADE/v119wt
git log --oneline -1                                   # must be the P10-04 commit
git diff --name-only ec647ed..b342636 | grep -c '\.go$'   # 0 — the code did not move under the contract

# 1 substrate (§2) still holds
grep -n 'key-press\|key-combo' internal/server/api_v1.go          # 490,491
grep -c 'keypress\|keycombo' internal/server/api_v1.go            # 0 — the acceptance check: scoped to the register, where a live route would have to appear
grep -c 'keypress\|keycombo' docs/contracts/P10-07_CONTRACT_V3_1.md   # informational only, never 0: the wrong spellings appear in corrective prose (correction row 11, the carried-item row, §2). The acceptance check is the router grep above
grep -rniE 'display[-]info' --include=*.go .                      # 0 — no such route, no such site (C-10)
grep -n 'type ScreenParams' -A 4 internal/protocol/messages.go     # Display, Quality only
grep -n 'type ScreenStreamParams' -A 4 internal/protocol/messages.go  # Display, FPS, Quality only
grep -n 'type InputParams' -A 8 internal/protocol/messages.go      # X,Y,Button,Text,Key,Keys only
grep -n 'func ParseCommand' -A 6 internal/protocol/messages.go     # json.Unmarshal, no DisallowUnknownFields
grep -rn '_NET_CLIENT_LIST\|EnumWindows\|XQueryTree' --include=*.go internal/  # 0 — P3/P4 are greenfield
grep -n 'Semver' internal/version/version.go | head -1             # 1.19.1
grep -ciE 'perform[A]ction' docs/contracts/P10-07_CONTRACT_V3_1.md   # 0 — the claim is gone, not relabelled

# 2 prohibited mechanisms (§5.3) — must be empty
grep -rn 'CreateRemoteThread\|NtCreateThreadEx\|CreateProcessWithLogonW\|SetThreadDesktop\|pkexec\|runas' \
     --include=*.go internal/ cmd/ | grep -v _test

# 3 the contract's own artefacts
grep -ciE 'display[-]info' docs/contracts/P10-07_CONTRACT_V3_1.md   # 0
grep -n 'NO_WINDOW_MANAGER' docs/contracts/P10-07_CONTRACT_V3_1.md   # §7.1 + enums + matrix
grep -n 'DUPLICATE_FIELD\|UNKNOWN_FIELD\|TYPE_MISMATCH' docs/contracts/P10-07_CONTRACT_V3_1.md
grep -n '## Corrections\|## Ratification scope\|## Disagreements' docs/contracts/P10-07_CONTRACT_V3_1.md

# 4 build + suite (the tree is unchanged by this unit; re-record, never inherit)
go build ./... && GOOS=windows GOARCH=amd64 go build ./... \
  && GOOS=linux GOARCH=arm64 go build ./... && GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build ./...
go vet ./internal/agent ./internal/server ./internal/platform ./internal/protocol
go test ./... -count=1 -timeout 1500s
```

Platform-specific tests are build-tagged (`//go:build windows`, `//go:build linux && !android`) so the Linux suite keeps whatever pass/skip counts it holds at review time (re-record them, do not inherit a number); the four cross-compiles are what prove a primitive has an implementation or a declared refusal on every target.

**Evidence-class note, unchanged and still true:** `go` is **not installed on this design host** (`go: command not found`, re-checked this session), so no build or test was run for this document. §2's claims are `grep`/`sed` reads at `b342636`; the commands above are review-time gates, not things I ran. The suite numbers quoted anywhere in the campaign belong to the falsifying unit's node (572 pass / 0 fail / 1 skip at `79349a6`), not to this host.

## 20. Open decisions (ratify or overturn before build)

| # | question | architect's default |
|---|---|---|
| OD-1 | Does the first station run the agent as the OS **service** principal? Without it, `wts:7` refuses `PRIVILEGE_NOT_HELD` and the Windows acceptance can never pass | required prerequisite for the Windows lane; the contract ships either way |
| OD-2 | Strong principal may address **any** interactive context on the host, audited with the target user — accepted on every OS, or limited to one operator's context per station? | any active context (N3/§12) |
| OD-3 | `detach` + selector (a guardian job inside an operator's context) — needed for long HMI runs? | refused in v1/v2/v3 |
| OD-4 | A `contexts` write verb (logoff / disconnect / switch) — ever? | no; read-only, a separate decision if ever needed |
| OD-5 | `ui-window close` on the HMI process itself: allowed, or denied for a configured process list? | allowed for any window; a deny-list is a second policy place |
| OD-6 | Wayland: ship the **portal/libei** backend (consent dialog per session) or declare Wayland UI primitives refused-by-platform? | refused-by-platform today (403/`CONSENT_REQUIRED` advertised), portal as a later unit with its own contract |
| OD-7 | Android: build the **a11y APK** lane at all, or keep Android as an exec+fs node? | exec+fs only in 1.21; the matrix says so explicitly |
| OD-8 | `PRIMITIVE_REFUSED` = 403 vs 409 (alignment with `UNSUPPORTED_AGENT`)? | 403 — policy refusal, not a fleet-state problem |
| **OD-9** | **Phase 1's strict decoding turns silent successes into `400`s for callers that send undocumented fields. Accept the break?** | **accept.** Every one of the 13 measured shapes is a caller that asked for something and was told `ok:true`; preserving that "compatibility" preserves the lie (N18). If refused: the gate ships behind the selector only, and SS-13 stays open — which is what the falsification rejected |
| **OD-10** | **Two refusal vocabularies on the same 409: the wrapper's `CAPABILITY_MISSING` (`api_v1.go:174-181`) vs the contract's `UNSUPPORTED_AGENT` with `detail.feature`. Which survives?** | `UNSUPPORTED_AGENT` survives; `CAPABILITY_MISSING` is mapped onto it in phase 1. Recorded here rather than fixed silently because SS-12 is outside this unit's six items |

## 21. Review

| field | value |
|---|---|
| reviewed_by | **reviewer** (independent falsification leg, per the brief) |
| verdict | **NEEDS-REVIEW — CONTRACT v3, CORRECTIONS APPLIED, RATIFICATION SCOPED.** No implementation unit may start until OD-1..OD-10 are answered |
| verdict_sha_of_head | the commit that adds this document (recorded in `P10-04_CLOSE.md`; a file cannot contain the hash of the commit that contains it) |
| evidence class | `[M]` measured by the falsification on a HEAD build (1.19.1); `[S]` `file:line` reads at `b342636`, re-runnable via §19. No claim is inherited from recall |
| prohibited and honoured | no code, no handler edits, no new lane, no new knob, no OCR/semantic recognition (N14, spec l.176-181), no Windows-only wording — every primitive is neutrally defined with Windows as one `[S]` column |
| supersedes | `docs/contracts/P10-01_NEUTRAL_CONTRACT.md` (v2, `d86e879`). Section numbers, primitive IDs P1-P6 and decision IDs N1-N16 are unchanged; this document adds N17-N20 and OD-9/OD-10, and the `## Corrections` table maps every change to a line range |
| what a reviewer should attack first | (1) the 14-site table in §5.5 against the router and handler lines it cites; (2) whether the `NO_WINDOW_MANAGER` refusal is the right call versus an `XQueryTree` fallback; (3) the `display` alias (N17) — it widens the selector surface; (4) whether any Windows cell still reads as verified |

## 22. Defect closure — the five defects of `P10-06_VERIFICATION.md` §8

`docs/contracts/P10-06_VERIFICATION.md` (`7d1f325`) returned FIX-FIRST on v3 with five numbered documentation defects. All five were reproduced against the v3 blob (sha256 `e15fb9909a6e…`) before any text was touched; none is disputed. Line numbering is held stable — every edit above this section is a same-line replacement plus one inserted appendix line, so every range v3 cited still resolves in v3.1, and this section is appended after §21. No primitive, endpoint family, decision ID, refusal code, Windows cell or ratification scope changed.

| # | defect (P10-06 §8) | what changed in v3.1 | lines in v3.1 | proof |
|---|---|---|---|---|
| 1 | **C-4** row 1 — the row claimed an executed test behind the absent-`hwnd` rule, and no `*_test.go` at HEAD mentions `hwnd` | sentence deleted. No test is claimed and none was written: the field-table rule stands as text, which is what it always was | 30 | D1 |
| 2 | **C-5** row 2 — range `570-615` did not contain the `TYPE_MISMATCH` / `detail{field,want,got}` change | range extended to the three sites where the change lives: §4's `capture.window` row, the wrong-type refusal body, §10's corrected example | 31 (row); cites 201, 328-340, 570-615 | D2 |
| 3 | **C-1/C-11** row 13 — range `140-161` did not contain the C-11 landing | range extended to §4's `session` row, §11's Android other-uid cell and the C-11 ruling paragraph | 42 (row); cites 140-161, 170, 623, 662 | D3 |
| 4 | **SS-13** site #5 — `GET /stream-frame` is not selector-bearing: no body, no query decode | the table is re-labelled **13 selector-bearing sites plus one query-rejecting `GET`** (14 rows); correction row 7 says the same; the site row states why it is listed | 36 (row 7), 296 (heading), 304 (site row) | D4 |
| 5 | **F-5** grep item 2c — an unscoped contract grep returns 3 and 2, so an expected 0 was unachievable | the **acceptance check is scoped to the router**, where a live route would have to appear; the document-level count sits beside it as informational with its reason; §19's contract-path greps are repointed to this file so they are not vacuous | 875-876 | D5 |

Proof commands, run at `cd /opt/data/projects/PROBE_UPGRADE/v119wt`; the comment is the observed stdout:

```bash
# D1 — the deleted claim, and the test that does not exist
grep -c 'A test assert[s]' docs/contracts/P10-07_CONTRACT_V3_1.md      # 0
grep -rln 'hwnd' --include=*_test.go . | wc -l                          # 0

# D2 — the three lines row 2 now cites, by content
sed -n '201p;331p;340p' docs/contracts/P10-07_CONTRACT_V3_1.md | cut -c1-104   # the type rule · the detail{field,want,got} body · the closed enum

# D3 — the three lines row 13 now cites, by content
sed -n '170p;623p;662p' docs/contracts/P10-07_CONTRACT_V3_1.md | cut -c1-104   # §4 session row · §11 Android other-uid · the C-11 ruling

# D4 — no place in the document counts all fourteen rows as selector-bearing
grep -c '1[4] selector-bearing' docs/contracts/P10-07_CONTRACT_V3_1.md  # 0
grep -c '1[3] selector-bearing' docs/contracts/P10-07_CONTRACT_V3_1.md  # 2

# D5 — the acceptance check is router-scoped; the old unscoped check is shown on the v3 blob with its result
grep -c 'keypress\|keycombo' internal/server/api_v1.go                  # 0
grep -c '/keypress' docs/contracts/P10-04_CONTRACT_V3.md                # 3   (old check, v3 blob)
grep -c '/keycombo' docs/contracts/P10-04_CONTRACT_V3.md                # 2   (old check, v3 blob)
```

**§19 re-run after the fix** — every count-producing grep in the appendix, with the observed count:

```bash
grep -c 'keypress\|keycombo' internal/server/api_v1.go                   # 0   — unchanged, and now the named acceptance check
grep -c 'keypress\|keycombo' docs/contracts/P10-07_CONTRACT_V3_1.md      # 10   — informational only: corrective prose, so never 0
grep -rniE 'display[-]info' --include=*.go . | wc -l                      # 0
grep -rn '_NET_CLIENT_LIST\|EnumWindows\|XQueryTree' --include=*.go internal/ | wc -l   # 0
grep -ciE 'perform[A]ction' docs/contracts/P10-07_CONTRACT_V3_1.md        # 0
grep -ciE 'display[-]info' docs/contracts/P10-07_CONTRACT_V3_1.md        # 0
grep -rn 'CreateRemoteThread\|NtCreateThreadEx\|CreateProcessWithLogonW\|SetThreadDesktop\|pkexec\|runas' --include=*.go internal/ cmd/ | grep -v _test | wc -l   # 0
```

Line-output greps, re-run unchanged: `key-press\|key-combo` → **490,491** · `type ScreenParams -A 4` → **454-457** (`Display`, `Quality`) · `type ScreenStreamParams -A 4` → **487-491** (`Display`, `FPS`, `Quality`) · `type InputParams -A 8` → **557-564** (`X`, `Y`, `Button`, `Text`, `Key`, `Keys`) · `func ParseCommand -A 6` → **986-992** (`json.Unmarshal`, no `DisallowUnknownFields`) · `var Semver` → **21** (`= "1.19.1"`; §19's `grep -n 'Semver' | head -1` prints c:5, a comment that contains the word, and the value line is c:21) · `NO_WINDOW_MANAGER` → **20 occurrences, first at 37** · `DUPLICATE_FIELD\|UNKNOWN_FIELD\|TYPE_MISMATCH` → **15 occurrences, first three at 31, 165, 201**.

Build and suite are **not** re-run: `go` is not installed on this design host and this unit changes no `.go` file (`git diff --name-only 7d1f325..HEAD -- '*.go'` → 0), so no build or test claim is made here — the suite numbers in §19 remain the falsifying node's.
