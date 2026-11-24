VERDICT FIX-FIRST /opt/data/tmp/probe119/P10-06_VERIFICATION.md 7f4823022b8f 572 pass / 0 fail / 1 skip

**CLOSE HEADER**
`reviewed_by: reviewer` · `verdict: FIX-FIRST` · `verdict_sha_of_head: 7f4823022b8f518cfc2c7d66e052b906331ba363`
`unit: P10-06` · `artefact: /opt/data/tmp/probe119/P10-06_VERIFICATION.md` (also committed at `docs/contracts/P10-06_VERIFICATION.md`)
`suite: go test ./... -count=1` at `7f48230` → **572 pass / 0 fail / 1 skip, exit 0** (go1.25.0; probe sha256 `807fb0fc32619487a1f4cbe67d8d559390bd00e538fba6dcee821e21df8ad3c8`)
`open item: F-1…F-5 — one false test claim, two citation-range defects, one SS-13 table defect, one failed literal grep. No contract semantics found wrong; no code changed by any of the three units.`

**RECIPIENT EXPERIENCE:** built the tree at HEAD (`PROBE v1.19.1`, sha256 `807fb0fc3261`), ran a server on `127.0.0.1:17893` with two agents (`p1006-headless` with `input`, `p1006-noinput` without) and executed both P10-05 §4 refutations myself — SS-12 returned `503 AGENT_UNREACHABLE` on a live connected agent; SS-10 returned `stopped:true` while the same stream's frames advanced 4→16.

`SHAPE none` — all three units are document-only: `git diff --name-only b342636..7f48230 -- '*.go'` → 0 files; `git diff --name-only b342636..7f48230` → the two documents only.

## Scope and method

Verified from committed blobs, not working copies: `git show 7f48230:docs/contracts/P10-04_CONTRACT_V3.md` (931 lines, sha256 `e15fb9909a6e…`), `git show 7f48230:.workflow/analyst/P10-05_MATRIX_V2.md`, `git show b342636:docs/contracts/P10-03_FALSIFICATION.md`. HEAD `7f48230`, branch `r119-build`, `git status --porcelain` empty. Line numbers below are blob line numbers in v3. No file in the tree was edited.

Row rule applied (brief item 1): a row PASSES when the change it exists to make is present and inside its cited range; a normative clause outside the range is recorded as a RANGE defect; a claim that is not true of the tree is a FAILED row.

## 1. The 14 correction rows

| # | item | claim (short) | verified from (v3 blob line) | row | range covers row |
|---|---|---|---|---|---|
| 1 | C-4 | `hwnd` gone from the X11 `ui-tree` row; `wts`-only row; field table says non-`wts` never emits it | c:413 (correction note), c:405-410 (X11 row, no `hwnd`), c:413 (`wts` row), c:425 (field table) — in range 396-458 | **FAIL** | F-1: "A test asserts no `hwnd` key on a non-`wts` context" — no such test exists: `grep -rl hwnd . --include=*_test.go` → 0 files; every non-vendor `hwnd` hit is production `platform_windows.go` or a document. The fix is present; the verification claim is not |
| 2 | C-5 | `capture` example now sends `window` as a string; a number is `400 INVALID_PARAMS/TYPE_MISMATCH` with `detail{field,want,got}` | c:574-578 (request `"window":"0x3c00007"`), c:201 (`type is string … a JSON number is 400 … TYPE_MISMATCH`), c:328-331 (refusal body) | PASS | RANGE (F-2): cited 570-615 contains the corrected example only; the `TYPE_MISMATCH` detail it describes is at c:201 and c:331, outside the range |
| 3 | C-6 | worked-sequence step 4 returns the envelope, never a bare array | c:754-758 (step 4 + "C-6 corrected: the response is the envelope") — in range 738-773; envelope struct `api_v1.go:27-31` | PASS | yes |
| 4 | C-8 | `hash_only:true` → `data` **and** `size_bytes` absent; two bodies shown; "absent, never `""`/`null`/`0`" | c:580, c:592, c:601-602 — in range 570-615 | PASS | yes |
| 5 | C-9 | one encoder: the agent; `detail.encoded_by:"agent"`; server holds no pixels | c:603 — in range 570-615; `CaptureResult` `messages.go:467-473` is base64 only | PASS | yes |
| 6 | C-10 | phantom enforcement site deleted; only a body-less wire command remains | c:346-351 (the name is not quoted, the wire command is distinguished), c:352-362 (rules that survive); `grep -rniE 'display[-]info' --include=*.go` → 0; `agent.go:1062` handler has no body decode | PASS | yes |
| 7 | SS-13 | E3 dispatch-table check deleted; E3a/E3b/E3c + per-route allow-list; table names the selector sites | c:282-292 (E3 deleted, E3a/E3b/E3c), c:294-315 (table + cross-parameter rationale), c:340 (closed reason enum) — in range 212-344 | PASS | DEFECT (F-4): site #5 is not selector-bearing; see §3 |
| 8 | P3 headless | `501 PRIMITIVE_UNSUPPORTED reason:"NO_WINDOW_MANAGER"` on absent `_NET_SUPPORTING_WM_CHECK`; empty list only after real enumeration; no `XQueryTree` fallback | c:430-458 — in range 396-458 | PASS | yes |
| 9 | P4/P6 Android a11y | node action → `NO_BACKEND`; a11y bounds+projection → `screen_region`; `uid.active` null; hover `NO_BACKEND` | c:625 (`active` null), c:627 (P4 `NO_BACKEND`), c:628 (hover), c:633 (window `NOT_EXPOSED`; region `screen_region`) — in range 617-649 | PASS | yes |
| 10 | W-1…W-8 | ratification scoped; Windows column `S` in all 11 cells | c:45-70 (scope; c:62 excludes Windows), c:623-633 (11 cells, all `S`), c:619 (marker rule) — in range 45-70 for the scope | PASS | note: the scope is in range; the 11-cell claim it adds is in §11. The literal phrase "planned, unverified (W-n)" appears at c:231, c:500, c:607, c:740; c:541 and c:562 carry `[S]` + W-number in other words ("claim only; never run") |
| 11 | route names | `/key-press`, `/key-combo`; register `api_v1.go:490-491`; wrong spellings 404 | c:118 ("registered as … `/key-press`, `/key-combo`; the spellings `/keypress` and `/keycombo` have no handler") — in range 112-138; router checked below | PASS | yes (grep item fails separately — F-5) |
| 12 | scroll sign | Windows `dwData = -delta*120`; Android `delta<0` → `ACTION_SCROLL_BACKWARD`; expectation derived from the convention | c:543-556 — in range 515-568 | PASS | yes |
| 13 | C-1/C-11 | `session` is `wts`-only; Android other-uid `OTHER_USER` in §11 and §12 | c:159-160 (governing text + C-1 note) — in range 140-161; `session` row c:170; §11 cell c:623; §12 ruling c:662 | PASS | RANGE (F-3): the C-11 landing is at c:623 and c:662, outside 140-161 |
| 14 | C-7 | `OCCLUDED`/`FOREGROUND_DENIED`/`NOT_SUPPORTED` closed as a third enum for 200+`ok:false`, separate from 403/409/501 | c:475-496 (shape, the three reasons, the mechanical distinction), c:645 (recap) — in range 460-513 | PASS | yes |

**Rows: 13 of 14 PASS as stated. 14 of 14 corrections are present in the document.** Row 1 fails because of a claim it makes about a test, not because the C-4 fix is missing.

## 2. The itemised greps

| item | command (on the committed blob) | result | verdict |
|---|---|---|---|
| 2a | `git show 7f48230:docs/contracts/P10-04_CONTRACT_V3.md \| grep -ciE 'display[-]info'` | **0** | PASS |
| 2b | `… \| grep -ciE 'perform[A]ction'` | **0** | PASS |
| 2c | `… \| grep -c '/keypress'` / `'/keycombo'` | **3** / **2** | **FAIL** as an acceptance check (F-5) |
| 2d | `git show 7f48230:internal/server/api_v1.go` lines 490-491 | `POST …/key-press` `s.handleV1KeyPress`; `POST …/key-combo` `s.handleV1KeyCombo` | PASS |
| 2e | `grep -c 'keypress\|keycombo' internal/server/api_v1.go` | **0** (the §19 command, c:875) | PASS |

F-5 detail: the 3 and 2 occurrences are all corrective prose, never a route citation — c:40 (correction row 11 quoting the old spellings as the thing replaced), c:91 (the falsification item marked "not touched"), c:118 (§2 saying the old spellings have no handler). The contract's own greps (c:875 router, c:883/890 contract) are scoped so they return 0; the brief's unscoped contract grep does not. No live route in the document uses the wrong spelling.

## 3. SS-13 — the 14 sites, resolved at HEAD

`git diff --name-only b342636..7f48230 -- '*.go'` → 0, so b342636 line numbers are HEAD line numbers.

| # | site | cited | found at HEAD (verified by line read) | selector-bearing |
|---|---|---|---|---|
| 1 | `/exec` | `api_v1.go:320`; `server_api.go:170,179`; `agent.go:668-669`; `messages.go:230-280` | 320-321 register + `v1WrapAgentHandler("exec")`; 170 handler, 179 `json.Unmarshal`; 668-669 `handleExec`/`ParseCommand`; `ExecParams` 230-272 | yes |
| 2 | `/capture` | `:358-359`; `server_api.go:666,675`; `agent.go:1050-1051`; `messages.go:454-457` | all exact — 666 handler, 675 `json.Unmarshal`; 1050-1051; `ScreenParams{Display,Quality}` | yes |
| 3 | `/stream-start` | `:484`; `api_v1_transfer.go:275,281`; `messages.go:487-491` | all exact — 275 handler, 281 `NewDecoder`; `ScreenStreamParams{Display,FPS,Quality}` | yes |
| 4 | `/stream-stop` | `:485`; `:314,328`; `messages.go:542-544` | all exact — 328 is `_ = json.Unmarshal(body, &params)` (error discarded); `ScreenStreamStopParams{StreamID}` | yes |
| 5 | `GET /stream-frame` | `:486`; `:353` | both exact — handler 353-370: `PathValue`, auth, agent memory; **no body read, no `r.URL.Query()`** | **no** (F-4) |
| 6 | `/pointer-move` *(new)* | — | absent from the mux block (297-503): `grep 'pointer-move'` → 0 | planned |
| 7 | `/pointer-click` | `:489`; `:375,381`; `agent.go:1066-1067`; `messages.go:557-563` | all exact; `InputParams{X,Y,Button,Text,Key,Keys}` | yes |
| 8 | `/pointer-scroll` *(new)* | — | absent → 0 | planned |
| 9 | `/pointer-drag` *(new)* | — | absent → 0 | planned |
| 10 | `/key-press` | `:490`; `:400,406`; `agent.go:1088-1089` | all exact | yes |
| 11 | `/key-combo` | `:491`; `:425,431`; `agent.go:1099-1100` | all exact | yes |
| 12 | `/text-input` | `:492`; `:450,456`; `agent.go:1077-1078` | all exact | yes |
| 13 | `/ui-tree` *(new)* | — | absent → 0 | planned |
| 14 | `/ui-window` *(new)* | — | absent → 0 | planned |

**Sites: 9 of 14 resolve to existing code, and every cited `file:line` among them is exact (9/9). 5 of 14 are declared new and verified absent at b342636 — correctly marked, not false citations. 13 of 14 are selector-bearing; #5 is not** — §5.6 c:359 says of it "can it carry a selector? no", and the handler confirms it: no body, no query decode. Counting it in "the 14 selector-bearing sites" (c:296) is the defect: the phase-1 gate is right to reject query keys on it, but it is a 13-site selector table plus one query-rejecting GET.

The mechanism itself survives the hard look: E3a (server, per handler, allow-list + `DisallowUnknownFields`), E3b (agent, same helper at `messages.go:986-991`, call sites `agent.go:669,1051,1067,1078,1089,1100` all verified), E3c (duplicate-key token walk, c:292). The 8 existing body-decoding handlers cover every one of SS-1…SS-11; the cross-parameter case is real (four routes share `InputParams`, c:315). E3 (dispatch table) is genuinely deleted from the enforcement text (c:284).

## 4. The two P10-05 §4 refutations — re-run

Build: HEAD `7f48230`, go1.25.0, `bin/probe` self-reports `PROBE v1.19.1`, sha256 `807fb0fc3261` (identical to the matrix's provenance). Server `127.0.0.1:17893`; agents `p1006-headless` (capabilities include `input`,`capture`) and `p1006-noinput` (`exec,filesystem,process,capture`), both `status:active`.

| refutation | matrix claim | my run | reproduced? |
|---|---|---|---|
| SS-12 | not `409 CAPABILITY_MISSING`; `503 AGENT_UNREACHABLE` on a live, connected agent that lacks `input` | `POST /agents/p1006-noinput/pointer-click {"x":10,"y":10,"button":"left"}` → **HTTP/1.1 503** body `{"ok":false,"error":{"code":"AGENT_UNREACHABLE","message":"agent p1006-noinput lacks required capability \"input\" for command \"pointer_click\""}}`; the identical call on `p1006-headless` → `200 {"ok":true,"data":{"success":true}}` | **YES** |
| SS-10 | not "stops every stream"; empty/unknown id stops **nothing** and still returns `stopped:true` | stream `stream-1795544915825531497`: frames `seq 4`, `seq 8`; `POST /stream-stop {"stream_id":"<exact>"}` → `{"stopped":true,…}` and frames froze at `12,12,12,12` over 8 s. Fresh stream `…868337588979`: frame `seq 4`; `POST /stream-stop {}` → `{"ok":true,"data":{"stopped":true,"stream_id":""}}`; frames then `seq 16` on the **same** stream id — still running | **YES** |

Both refutations stand: the falsification's SS-12 code was wrong (a second vocabulary `CAPABILITY_MISSING` is not what this path returns today) and its SS-10 "stops every stream" was wrong (it stops nothing on an empty id; the exact id does stop). **Refutations: 2 of 2 reproduced.** NS-2 is confirmed by the same run.

## 5. The Windows column

| check | evidence | verdict |
|---|---|---|
| every §11 Windows cell is `S` | c:623-633, 11 rows, column 2: `S (W-1)`, `S (W-2)`, `S (W-1)`, `S (W-4)`, `S (W-4)`, `S`, `S (W-7)`, `S`, `S (W-8)`, `S (W-8)`, `S (W-8)` | PASS |
| no check-mark, "implemented", "verified" in that column | `grep -c '✓'` → 10 in the document, **0 inside §11 (617-649)**; `grep -n 'implemented'` → the correction row only | PASS |
| `## Ratification scope` excludes it | c:45-70; c:62 "**Windows behaviour** (all 14 cells, W-1…W-8) … no Windows node"; c:68 one-line reading | PASS |
| W-1…W-8 visibly honoured | W-1 c:231/235-240, W-2 c:248, W-3 c:275/656, W-4 c:500-509, W-5 c:523, W-6 c:541, W-7 c:549, W-8 c:562/607/631-633; every Windows table header carries `[S]` + W-number | PASS |

Two cosmetic residues, neither a Windows-cell verification: c:613 carries `✓ projection + crop` in the **Android a11y** column (§10) — outside §11, but it contradicts the P10-04 close's "a check-mark is now legal only where `M` is"; and §12 c:655-656 carry `✓ direct` on the two Windows `[S]` rows (a resolution claim inside a row already marked `[S]`).

## 6. Hostile attacks (recorded, all addressed)

- **A1 — the refutations were measured on someone else's stale binary.** Addressed: rebuilt from HEAD myself, same sha256 as the matrix's provenance; both paths re-derived from my build, not quoted from P10-05.
- **A2 — the 14 sites were checked by reading the document, not the tree.** Addressed: every cited `file:line` for existing code resolved by line read at HEAD (9/9 exact); the five new sites confirmed absent by grepping the mux block.
- **A3 — `stopped:true` could be the second stream's stop, or a stale frame.** Addressed: separated the cases; the `{}` stop left the same stream id advancing 4→16, the exact-id stop froze the same stream at 12.
- **A4 — the `✓` at c:613 or §12 proves a Windows cell is verified.** Addressed: c:613 is the Android a11y cell; §11 has no `✓`; §12's check-marks sit in rows marked `[S]`.
- **A5 — the row-1 test exists somewhere non-Go.** Addressed: whole-tree search; only vendored Windows syscall files and documents mention `hwnd`; no `*_test.go` does.
- **A6 — the greps were run on the working copy, which may differ from the blob.** Addressed: every contract grep ran on `git show 7f48230:…`, and the blob hash matches the file the close claims (`e15fb9909a6e…`).

## 7. Drift check

- **Request drift:** brief asks 14 rows + 3 greps + correct spellings in the router + 14 SS-13 sites + 2 refutations + the Windows column. All six are covered above; no scope added.
- **Implementation drift:** none — no code changed by any of the three units (`*.go` diff 0), so the contract describes the same tree the falsification measured.
- **Verification drift:** the one found is row 1's "a test asserts…" — a verification claim with no artifact behind it. It is the only claim in the three documents that asserts an executed check that does not exist.

## 8. Verdict

**FIX-FIRST** — documentation defects only; the contract's corrections are real, both refutations reproduce, and the Windows column is honestly scoped.

Failing items, numbered:

1. **Row 1 (C-4)** — "A test asserts no `hwnd` key on a non-`wts` context" is false at HEAD: 0 test files mention `hwnd`. Delete the sentence or add the test (§18 does not list it).
2. **Row 2 (C-5)** — cited range 570-615 does not contain the `TYPE_MISMATCH`/`detail{field,want,got}` change the row describes; it is at c:201 and c:331. Extend the range.
3. **Row 13 (C-1/C-11)** — cited range 140-161 does not contain the C-11 landing; it is at c:623 (§11) and c:662 (§12). Extend the range.
4. **SS-13 table, site #5** — `GET /stream-frame` is not selector-bearing (no body, no query decode; §5.6 says so). The table is a 13-site selector list plus one query-rejecting GET; re-label or drop the "14 selector-bearing" wording at c:296 (and in correction row 7, c:37).
5. **Grep item 2c** — the brief's unscoped contract grep returns 3 (`/keypress`) and 2 (`/keycombo`). No live route uses them; if an acceptance check must return 0, scope it (§19 already scopes its own to the router).

Not defects: the 14 corrections are all present; the 9 existing SS-13 citations are line-exact; E3a/E3b/E3c is a sound cure for SS-1…SS-11; both P10-05 refutations are correct; the Windows column is 11/11 `S` with ratification excluding it.
