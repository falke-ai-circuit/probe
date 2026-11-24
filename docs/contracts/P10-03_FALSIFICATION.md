# P10-03 — FALSIFICATION OF P10-01 (neutral contract) and P10-02 (platform matrix)

**CLOSE HEADER**
`reviewed_by: reviewer` · `verdict: FIX-FIRST` · `verdict_sha_of_head: 79349a6958c96b72e1a0c24cbfb7987691d4c87c`
`artefact: /opt/data/tmp/probe119/P10-03_FALSIFICATION.md` · `commit: docs/contracts/P10-03_FALSIFICATION.md on r119-build`
`suite: 572 pass / 0 fail / 1 skip (go test ./... at HEAD) · builds linux+windows+linux/arm64+android/arm64 · vet clean` · `open item: Windows host absent — 14 Windows cells remain S-labelled (§6)`

**Objects falsified** — P10-01 `docs/contracts/P10-01_NEUTRAL_CONTRACT.md` @ `d86e879` (714 lines, sha256 `d3f1ba4f9d569778f7864647621d96d387859ea352e82ba5103d14eb195516fb`); P10-02 `.workflow/analyst/P10-02_PLATFORM_MATRIX.md` @ `79349a6` (102 lines, sha256 `1776bad08b691f726230be410ff50aecfde4eae79a51cea99154c7b4c45075b2`).
Neither was edited. This is a third document.

**Verdict rule used per cell** (the brief's three classes):
`IMPLEMENTABLE` = the backend named in the contract can deliver the stated semantics without amending the contract; `WRONG` = the named backend cannot, or the contract contradicts itself; `REFUSAL-GAP` = the platform cannot comply and the caller cannot distinguish "unsupported" from "failed" because the refusal is missing, uncoded, or two codes are specified for one request.
`[M]` measured this session · `[S]` source-only · `[U]` no node, no source path.

**RECIPIENT EXPERIENCE (E1/E3):** a caller is the recipient. Built `./cmd/probe` from HEAD (`go1.24.4`, `BUILD_LINUX_OK`), ran `probe serve` + `probe connect` on `p1003-linux-headless`, and issued the contract's own request bodies (33 requests across P1-P6; raw in `cache/scratch/p1003/run/{live,live2,stream}.json`). Where a helper was missing (`xdotool`, `import`, `scrot`), a shim that logs argv and exits 0 was placed on the agent's PATH so the **success** path could be observed, not just the dependency error. Shim = harness, not evidence of the real tool; it proves parameter plumbing.

---

## 1. Provenance defects (must correct before either artefact is ratified)

| # | Claim | Observed |
|---|---|---|
| P-1 | Brief and P10-02 `## Coverage bound` place the matrix at `docs/contracts/P10-02_PLATFORM_MATRIX.md` | The file is not at that path in any commit. `git show --name-status 79349a6` → `A .workflow/analyst/P10-02_PLATFORM_MATRIX.md`. `git show HEAD:docs/contracts/P10-02_PLATFORM_MATRIX.md` → fatal: path does not exist. The P10-02 close report repeats the wrong path. |
| P-2 | Brief gives P10-01 working-copy sha256 `742b1dd03c76` | Actual `d3f1ba4f9d56…`; it equals both the working copy and the `d86e879` blob. No object with `742b1dd03c76` exists in this repo (`git rev-parse 742b1dd03c76` fails). |
| P-3 | P10-02 measures "the tree `/opt/data/projects/PROBE_UPGRADE/v119wt`", agent "v1.9.4" | `internal/version/version.go:21` at HEAD is `Semver = "1.19.1"` (`git log -S` shows 1.18.6 → 1.19.0 → 1.19.1; `1.9.4` never existed in this file). The measured binary is the **committed stale** `./probe` (mtime 2026-11-22 22:38, self-reports `PROBE v1.9.4`). Every M cell in P10-02 is therefore measured on an artifact ~13 minors behind the tree it claims to describe, and no M cell is attributable to a build of `79349a6`/`d86e879`. |

Consequence: P10-02's 28 M cells are re-classified **M(stale-binary)** until re-run against a HEAD build. My live node below is a HEAD build and reproduces the silent-drop findings, so those conclusions survive; the key-press conclusion does not (§5 M-1).

---

## 2. Verdict table — 14 primitive rows × 5 platforms = 70 cells

Rows are the matrix's rows (contract §4/§11 catalogue). Evidence prefixes: `c:` contract line · `m:` matrix line · `src:` file:line · `live:` case id in §5.

| primitive | Windows | Linux/X11 | Linux/headless | Linux/Wayland | Android |
|---|---|---|---|---|---|
| **P1 selector on exec** | IMPLEMENTABLE [S] — c:199 `CreateProcessAsUserW`; no WTS code exists yet, so S only (§6 W-1) | IMPLEMENTABLE [M] — c:200; live:ss-1/ss-2 show the field is dropped today, but fork+setuid/direct is available | IMPLEMENTABLE [M] — c:203; same binary, env-fixed context (m:63) | IMPLEMENTABLE [S] — c:201; no Wayland code in tree, same-uid path is a new backend | **REFUSAL-GAP** — c:440 says `PRIVILEGE_NOT_HELD` for other-uid, c:475 says `OTHER_USER`; one request, two codes |
| **P1 selector on UI ops** | IMPLEMENTABLE [S] — c:199 `--context-worker` | IMPLEMENTABLE [M] — c:200; routes accept it and drop it today (live:ss-2) | IMPLEMENTABLE [M] — c:203 on Xvfb; no-display is c:600 `HEADLESS_NO_DISPLAY` | IMPLEMENTABLE [S] — c:441 same seat | **REFUSAL-GAP** — c:441 `PRIMITIVE_UNSUPPORTED/NO_BACKEND` vs c:77/c:177 `CONTEXT_UNAVAILABLE/KIND_NOT_APPLICABLE` for `session` on a non-wts kind |
| **P2 contexts** | IMPLEMENTABLE [S] — c:442 `WTSEnumerateSessions`; no code | IMPLEMENTABLE [M] — c:442; live:sessions/contexts 404 (absence, not capability) | IMPLEMENTABLE [M] — m:63 `DISPLAY=[]`, own context still enumerable | IMPLEMENTABLE [S] — c:442 seats + peer creds | **WRONG** — c:442 promises "root-visible profiles"; c:70 defines `uid` as "the only context a Termux process has" and c:187 says foreground activity is "unknown to Termux". Cannot enumerate what it cannot identify |
| **P3 window tree** | IMPLEMENTABLE [S] — c:443 `EnumWindows`; 0 window symbols at HEAD (m:26) | IMPLEMENTABLE [M] — c:443 EWMH; live:uitree 404 | **REFUSAL-GAP** — c:308 is EWMH-only (`_NET_CLIENT_LIST`); m:11 records Xvfb **with no window manager**, which never sets that property. Empty list is returned, indistinguishable from "0 windows" — the exact fabrication c:272 forbids for `idle_s` | IMPLEMENTABLE [S] — c:443 conditional + `NO_GLOBAL_WINDOW_LIST` | IMPLEMENTABLE [M] — c:443 refusal `NO_BACKEND`; live:uitree 404 |
| **P4 window control** | **REFUSAL-GAP** — c:325 returns `200 {"ok":false,"reason":"FOREGROUND_DENIED"}`; `FOREGROUND_DENIED` is absent from both closed enums (c:457-458) and no `code` is carried | **REFUSAL-GAP** — c:444 uses `ok:false NOT_SUPPORTED`; `NOT_SUPPORTED` is not in the enum (c:457-458) | **REFUSAL-GAP** — same text as X11 | IMPLEMENTABLE [S] — c:444 `NOT_EXPOSED` / `NO_GLOBAL_WINDOW_LIST` both enumerated | **WRONG** — c:444 "✓ `performAction` on nodes (cross-app)" claims six window actions; a11y node actions cannot move/resize/minimise/close a window, and §12 has no principal model for it |
| **5a pointer click** | IMPLEMENTABLE [S] — src:platform_windows.go:430-454 honours left/right/middle | IMPLEMENTABLE [M] — c:445 XTEST; today's `Click` hard-codes `click 1` and drops `button` (src:platform_linux.go:237, live:ss-2) | IMPLEMENTABLE [M] — c:445 on Xvfb; no display → c:600 | IMPLEMENTABLE [S] — c:445 libei/portal + `CONSENT_REQUIRED` | IMPLEMENTABLE [M] — c:445 refusal `NO_BACKEND` |
| **5b double-click** | IMPLEMENTABLE [S] — c:363 `GetDoubleClickTime()` | IMPLEMENTABLE [M] — c:363 X multi-click interval; `xdotool click --repeat 2 --delay` | IMPLEMENTABLE [M] — same | IMPLEMENTABLE [S] — c:363 seat interval | **REFUSAL-GAP** — no §11 row, no backend named for multi-tap timing; c:448 lists only `ACTION_SET_TEXT` for text |
| **5c scroll** | **WRONG** — c:371 maps `delta=-1` (up, c:367) to `dwData=-120`; Windows' own positive wheel is up (stated in the same row), so up must be `+120`. The row contradicts itself and moves the list the wrong way | IMPLEMENTABLE [M] — c:372 button 4 = up (correct) | IMPLEMENTABLE [M] — same | IMPLEMENTABLE [S] — c:373 `ei_scroll`; sign unverified without a seat | **WRONG** — c:374 uses `ACTION_SCROLL_FORWARD` for `delta<0`; Android FORWARD moves toward the end, while c:367 defines negative as "toward the beginning" |
| **5d drag** | IMPLEMENTABLE [S] — c:447 down→moves→up | IMPLEMENTABLE [M] — c:447 XTEST sequence | IMPLEMENTABLE [M] — same | IMPLEMENTABLE [S] — c:447 libei + consent | IMPLEMENTABLE [S] — c:447 `dispatchGesture` stroke |
| **5e pointer move / hover** | IMPLEMENTABLE [S] — c:112, `SetCursorPos` | IMPLEMENTABLE [M] — c:112 | IMPLEMENTABLE [M] — same | IMPLEMENTABLE [S] — c:445 | **WRONG** — no move/hover backend exists in §11; c:445's `dispatchGesture` cannot hover and Android has no injectable cursor. Cell is silently absent from the matrix (U) and from §11 |
| **5f Unicode text** | IMPLEMENTABLE [S] — c:382 `KEYEVENTF_UNICODE`; today's byte loop is src:platform_windows.go:500-528, the defect being fixed | IMPLEMENTABLE [M] — c:383 keycode remap; live:ss-4 `type città è à 名前` reaches xdotool intact | IMPLEMENTABLE [M] — same | IMPLEMENTABLE [S] — c:384 conditional + `NO_TEXT_INPUT_PROTOCOL` | IMPLEMENTABLE [M] — c:385 `NO_INPUT_METHOD` |
| **6a capture full** | **WRONG** — c:95 defines px as the per-monitor DPI-aware **virtual screen** with negative origins legal; c:426 names `GetDC(0)/BitBlt`, and the only implementation is `SM_CXSCREEN` (primary) at src:platform_windows.go:316 with `BitBlt(...,0,0,w,h)` at :366. Multi-monitor is not captured and negative-origin screens are outside the bitmap | IMPLEMENTABLE [M] — c:426 `XGetImage(root)`; today's path is helper-gated (live:cap-plain) and reports `width:0,height:0` | IMPLEMENTABLE [M] — Xvfb root exists; same zeros today (src:platform_linux.go:204-206) | IMPLEMENTABLE [S] — c:426 portal + `CONSENT_REQUIRED` | IMPLEMENTABLE [M] — c:449 refusal `CONSENT_REQUIRED` |
| **6b capture window/region** | IMPLEMENTABLE [S] — c:427 `PrintWindow` + `source` fallback | IMPLEMENTABLE [M] — c:427 `XGetImage`/XComposite | IMPLEMENTABLE [M] — c:427 | IMPLEMENTABLE [S] — c:450 window `NOT_EXPOSED`, region portal+crop | IMPLEMENTABLE [M] — c:450 refusal `NO_BACKEND` |
| **6c hash-only** | IMPLEMENTABLE [S] — c:412 hash over decoded RGBA8 | IMPLEMENTABLE [M] — c:412; live:ss-5 shows `hash_only` ignored today (data returned) | IMPLEMENTABLE [M] — same | IMPLEMENTABLE [S] — c:412 | IMPLEMENTABLE [M] — c:449 refusal at capture level |

Count: **57 IMPLEMENTABLE, 6 WRONG, 7 REFUSAL-GAP**. A 70-cell table where 13 cells fail is not ratifiable as written.

---

## 3. Silent stubs — every place an ignored parameter returns success (highest-value axis)

All shapes below were **executed** on a HEAD build. `200 ok:true` is the literal response; `argv` is the shim log, i.e. what the platform actually did.

| # | Request | Response | What was ignored |
|---|---|---|---|
| **SS-1** | `POST /exec {"command":"hostname","session":7}` | `200 {"ok":true,... "exit_code":0, "stdout":"76cffb9490e2\n"}` | `session` dropped by `json.Unmarshal` into `ExecParams` (src:protocol/messages.go:230-280; parser is non-strict, :986-992). Same for `{"context":"x11::0"}` and `{"window":"0xdead","count":2}` — all exit 0 |
| **SS-2** | `POST /pointer-click {"x":10,"y":10,"button":"right","count":2,"window":"0x3c00007","context":"x11::0"}` | `200 {"ok":true,"data":{"success":true}}` · argv `mousemove 10 10 click 1` | All four fields dropped (`InputParams`, messages.go:557-564). A right double-click in a named window is reported as a successful left single-click anywhere. `button:"banana"` is also accepted silently |
| **SS-3** | `POST /pointer-click {}` | `200 {"ok":true,"data":{"success":true}}` · argv `mousemove 0 0 click 1` | `x`,`y` are required by c:136 but are zero-valued, not validated; the click lands at (0,0) |
| **SS-4** | `POST /text-input {"text":"abc","window":"0x3c00007","encoding":"utf16","context":"x11::0"}` | `200 {"ok":true,"data":{"success":true}}` · argv `type abc` | `window` (focus claim), `encoding`, `context` all dropped; the text goes to whatever holds focus |
| **SS-5** | `POST /capture {"hash_only":true}` | `200 {"ok":true,"data":{"data":"iVBORw0KGgo…","format":"png","width":0,"height":0,"size_bytes":96}}` | `hash_only` dropped (`ScreenParams` has only `display`,`quality`, messages.go:454-457); full base64 returned, no `hash` key ever exists. This is the primitive's stated point (c:158) returning the opposite |
| **SS-6** | `POST /capture {"format":"jpeg","quality":30}` | `200` · `"format":"png"` | `format`, `quality` dropped; Linux backend hard-codes png (src:platform_linux.go:204) |
| **SS-7** | `POST /capture {"window":"0x3c00007","rect":{"x":0,"y":0,"w":4,"h":4}}` | `200` · identical 8×8 full-screen body | `window` and `rect` dropped; capture is always full screen |
| **SS-8** | `POST /capture {"display":9}` | `200` · identical body | `display` **is** decoded (`ScreenParams.Display`) and then ignored by both backends (platform_linux.go:191; platform_windows.go:310). No refusal, no error — an operator asking for display 9 gets display 0 |
| **SS-9** | `POST /stream-start {"context":"x11::0","fps":2,"quality":50}` | `200 {"ok":true,"data":{"stream_id":"stream-1795542138815343239"}}` then `GET /stream-frame` → `200` frame | `context` dropped (`ScreenStreamParams` = display,fps,quality; messages.go:487-491). A stream starts in the agent's own context and the caller cannot tell. c:241 says this endpoint must `400 CONTEXT_NOT_ACCEPTED` |
| **SS-10** | `POST /stream-stop {"context":"x11::0"}` | `200 {"ok":true,"data":{"stopped":true,"stream_id":""}}` | `context` dropped; also stops **every** stream regardless of `stream_id` (empty id accepted) |
| **SS-11** | `POST /capture {}` (success path) | `"width":0,"height":0` | The agent does not parse the PNG it produced; a caller cannot size the result. c:415 requires pixels actually captured |
| **SS-12** | `POST /pointer-click` on an agent lacking `input` | `409 {"code":"CAPABILITY_MISSING",...}` (wrapper src:api_v1.go:174-181) | Not the contract's `409 UNSUPPORTED_AGENT` with `detail.feature` (c:235, c:534); a second refusal vocabulary already exists and the contract does not reconcile it |
| **SS-13** | `POST /exec {"command":"hostname","detach":true,"context":"x11::0"}` (future phase) | n/a today | E3 (c:237) refuses only command types **not** on the allow-list. `exec`, `capture`, `pointer-*`, `key-*`, `text-input`, `stream-*` are all allow-listed; their params are decoded into structs without a `context` field, so the selector is still dropped. Phase 1 (c:644) adds `Context` to `ExecParams`/`InputParams` only — `ScreenParams` and `ScreenStreamParams` are not listed. The contract's own enforcement does not close the endpoints it names |

**The structural finding:** every route in this server uses non-strict JSON (`json.Unmarshal`/`json.NewDecoder` into a struct). Unknown fields cannot produce an error anywhere. A contract that relies on "typed refusal" must add a strict-decode + allow-list gate **per handler**, not one dispatch-table check (c:237). SS-13 is the proof: the dispatch-table check is blind to unknown fields on known commands.

---

## 4. Internal contradictions in P10-01 (each forces a FIX before ratification)

| # | Lines | Contradiction |
|---|---|---|
| C-1 | c:77 vs c:124 | `session` is "`wts`-kind only; on any other kind refused" (77) but the parameter table says "`wts`/`a11y` kinds only" (124). Does `session` work on an `a11y` context or not? |
| C-2 | c:367 vs c:371 | Scroll sign: negative = up, yet Windows `delta=-1` → `dwData=-120`, which is down. Invert to `dwData = -delta*120` |
| C-3 | c:367 vs c:374 | Same inversion on Android: negative (toward the beginning) mapped to `ACTION_SCROLL_FORWARD` (toward the end) |
| C-4 | c:292 vs c:306 | The X11 `ui-tree` example carries `"hwnd":66102`; the field table says `hwnd` is absent on every non-`wts` context |
| C-5 | c:396 vs c:154 | Capture example sends `"window":66102` (integer); the parameter table types `window` as string in every other primitive. One of the two will 400 |
| C-6 | c:565-566 vs c:283-293 | Worked-sequence step 4 shows `ui-tree` returning a bare array; c:283-293 defines `{ok:true,data:{windows:[…]}}` |
| C-7 | c:325,c:351,c:444,c:578 vs c:457-458 | `FOREGROUND_DENIED`, `OCCLUDED`, `NOT_SUPPORTED` are used as refusal reasons but appear in **neither** closed enum. A caller cannot catch them; `ok:false` with no `code` is the "failed vs unsupported" ambiguity the brief asks about |
| C-8 | c:407 vs c:417 | `data` is "omitted when hash_only", but the response example contains `"data": "<base64 omitted when hash_only>"` — omitted vs empty |
| C-9 | c:418 | "encoded **server-side** … `detail.encoded_by:"agent"`" — two different encoders named for one operation |
| C-10 | c:241 | `display-info` is named as an endpoint that must refuse the selector. No such HTTP route exists (`grep -rn "display-info"` → 0 outside protocol constants; live:display-info → 404). The enforcement site is phantom |
| C-11 | c:440 vs c:475 | Android other-uid: `PRIVILEGE_NOT_HELD` (§11) vs `OTHER_USER` (§12) |
| C-12 | c:450 | Android a11y window capture "a11y node bounds + projection": a11y yields bounds, MediaProjection yields screen pixels. Cropping screen pixels by bounds is `screen_region` (occlusion-affected), not the `window_direct` the same section defines. The claimed source label is unobtainable |

Minor but real: c:42 cites handlers `api_v1_transfer.go:375,450` as "4 only" while key-press (:400) and key-combo (:425) also exist; c:442 "root-visible profiles" contradicts c:70.

---

## 5. Where P10-01 and P10-02 disagree (matrix M wins — except where M is wrong)

| # | Conflict | Ruling |
|---|---|---|
| M-1 | Matrix m:30 and m:50 (5g, **M-labelled**): "`POST /keypress`, `/keycombo` → 404 … no HTTP route wires them". Contract c:42 says both routes exist | **Matrix M cell is false.** Routes are registered at src:api_v1.go:490-491 as `/key-press` and `/key-combo`; live:keypress-nohyphen (`/keypress`) → 404, live:keypress-hyphen (`/key-press`) → `200 {"success":true}`. The matrix measured a URL that never existed and concluded a capability was absent. 2 of its 28 M cells are invalid |
| M-2 | Matrix m:23/B3 "Windows: full-screen JPEG … full virtual screen"; contract c:95 virtual screen with negative origins | Both wrong: src:platform_windows.go:316 uses `SM_CXSCREEN` and :366 `BitBlt(...,0,0)` → primary monitor only. Matrix "virtual" and contract "virtual" are both contradicted by the only implementation |
| M-3 | The word "headless" | Matrix headless = no `DISPLAY`, no X server (m:10). Contract headless = Xvfb (c:203, c:426). Every headless comparison between the two documents is between different substrates. Rename or reconcile |
| M-4 | Matrix m:44 labels the Linux click outcome "**M — REFUSED (dependency)**" | The live envelope is `200 ok:true` with `data.code:INTERNAL_ERROR` — nothing is refused at the HTTP or envelope level. "REFUSED" is the contract's vocabulary (403/501/409); using it for a 200 weakens the one measurement that shows the bug |
| M-5 | Matrix m:13: 5 linux agents, 0 windows; the dispatch brief's 16/8 fleet is "NOT REPRODUCED" | Confirmed independently: `GET /api/v1/agents` on the HEAD server listed exactly the agents connected to it (1 mine + peers' 4). No registry in this tree aggregates 16/8; the premise is unsupported, not merely unseen |
| M-6 | P10-02 close says it "Adds `docs/contracts/P10-02_PLATFORM_MATRIX.md`" | Commit adds `.workflow/analyst/…`; the close misdescribes its own commit (P-1) |

---

## 6. What cannot be decided without a Windows host (scope the ratification honestly)

Every sentence below is `[S]` at best and must not be ratified as verified. A ratification that covers them is a ratification of source reading, not of behaviour.

| # | Contract lines | Sentence that needs a Windows node |
|---|---|---|
| W-1 | c:184-193 | The whole V1-V6 ladder: `WTSEnumerateSessions` match, `State == WTSActive`, `WTSQuerySessionInformationW(WTSUserName)`, `WTSQueryUserToken (SYSTEM only)`, `CreateProcessAsUserW(lpDesktop="winsta0\\default")` — none of it exists in the tree; all of it is API-surface reasoning |
| W-2 | c:199 | "the same binary relaunched as `--context-worker` **in** that context, one JSON request on stdin, one JSON response on stdout" — the worker has never been run; the IPC shape is unproven |
| W-3 | c:215-222 | `ERROR_PRIVILEGE_NOT_HELD 1314` as the observed code for a non-SYSTEM agent addressing `wts:9` |
| W-4 | c:327-334 | All six `ui-window` actions: `ShowWindow`, `SetForegroundWindow` success rate on the Qt HMI, the `AttachThreadInput`/`BringWindowToTop` fallback, poll-300 ms verification, `PostMessage(WM_CLOSE)` + absence within 2000 ms |
| W-5 | c:348 | `WindowFromPoint` occlusion hit-test on a real overlapping HMI |
| W-6 | c:363 | `count:2` gap against the real `GetDoubleClickTime()` |
| W-7 | c:371 | Wheel direction end-to-end (the sign is already wrong on paper, C-2; the host would confirm the observed scroll) |
| W-8 | c:382, c:412-415 | `SendInput KEYEVENTF_UNICODE` for `àèìòù — € 名前 🙂`; `PrintWindow(PW_RENDERFULLCONTENT)` returning a usable surface for the Qt window; capture hash equality across png→jpeg |

Also unmeasured, though not Windows: the entire Wayland column (no seat; no Wayland code in tree) and Android (no node, no APK). They are `[S]` in §2 and cannot move to `[M]` from this host.

---

## 7. What must change (owner rules; no fix applied here)

1. **P10-01 C-2/C-3** — invert the Windows and Android scroll mappings; add the sign to the test list (c:652 already asserts "up on all five", which would catch it only if the expected value is derived from the convention, not the table).
2. **P10-01 C-7** — either add `OCCLUDED` / `FOREGROUND_DENIED` / `NOT_SUPPORTED` to the closed enums with codes, or convert those outcomes to `PRIMITIVE_REFUSED` with an enumerated `reason`. As written the caller still cannot distinguish "failed" from "unsupported".
3. **P10-01 C-1/C-11** — one answer per request: does `session` apply to `a11y`; is Android other-uid `OTHER_USER` or `PRIVILEGE_NOT_HELD`.
4. **P10-01 C-4/C-5/C-6/C-8/C-9/C-10** — fix the examples/types and delete the phantom `display-info` enforcement site (or add the route to phase 1).
5. **P10-01 SS-13** — extend phase 1 to strict decoding on **every** selector-bearing handler (`ScreenParams`, `ScreenStreamParams`, capture/stream/input), not one dispatch-table check. Without it the contract's own E3 cannot see the fields it promises to refuse.
6. **P10-01 P3 headless** — specify what `ui-tree` returns on an X server with no WM (`_NET_CLIENT_LIST` absent): refusal with a reason, not an empty list.
7. **P10-01 P4/6 Android a11y claims** — remove `performAction`-as-window-control and "a11y bounds + projection" as `window_direct`; they are not implementable as labelled.
8. **P10-02** — re-run all M cells against a HEAD build (`Semver 1.19.1`), correct `/keypress`→`/key-press` and `/keycombo`→`/key-combo`, relabel M(stale-binary) cells, and fix the artefact path in the close.
9. **P10-01 W-1..W-8** — scope ratification to exclude Windows behaviour until a node exists; do not mark the Windows column verified.

---

## 8. Evidence index

- Live run: `/opt/data/profiles/reviewer/cache/scratch/p1003/run/{live.json,live2.json,stream.json,xdotool.log,suite.log}`; server `127.0.0.1:17891`, agent `p1003-linux-headless` (HEAD build, `1.19.1`).
- Source: `internal/protocol/messages.go:230-280,454-457,467-473,487-491,557-564,986-992`; `internal/server/api_v1.go:154-182,489-492`; `internal/server/api_v1_transfer.go:275-300,375-470`; `internal/server/server_api.go:666-686,789-798`; `internal/server/enrollment.go:414-435,533-540`; `internal/platform/platform_linux.go:191-213,237-243`; `internal/platform/platform_windows.go:310-330,366,388,430-454,500-528`; `internal/version/version.go:21`.
- Commits: `d86e879` (contract), `79349a6` (matrix, single-file add), HEAD `79349a6958c96b72e1a0c24cbfb7987691d4c87c`.

## 9. Suite (this falsification's own gate)

`go build ./...` → BUILD_LINUX_OK · `GOOS=windows` → BUILD_WINDOWS_OK · `GOOS=linux GOARCH=arm64` → BUILD_LINUX_ARM64_OK · `GOOS=android GOARCH=arm64 CGO_ENABLED=0` → BUILD_ANDROID_OK · `go vet` four packages → VET_OK · `go test ./... -count=1` → **572 pass / 0 fail / 1 skip (573 tests, exit 0)** at `79349a6`.

**Verdict: FIX-FIRST.** 13 of 70 cells fail, 13 silent-success paths reproduce on a HEAD build, and the matrix's measured key-press cells are false. Nothing here is ratifiable as written; the contract's intent is sound and the corrections are enumerated above.
