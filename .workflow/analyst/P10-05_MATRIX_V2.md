# P10-05 — PLATFORM MATRIX v2, re-measured against a HEAD build

**CLOSE HEADER**
`reviewed_by: reviewer` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: 251d9b45990badec3c3c1f4ec764c012bcbac108` (the commit that added this document; this header line itself was echoed in the next commit)
`artefact: /opt/data/tmp/probe119/P10-05_MATRIX_V2.md` · `also committed at .workflow/analyst/P10-05_MATRIX_V2.md` (repo `/opt/data/projects/PROBE_UPGRADE/v119wt`, branch `r119-build`)
`suite: go test ./... -count=1 → 572 PASS / 0 FAIL / 1 SKIP, exit 0 (go1.25.0, HEAD b342636)`
`open item: Windows, Wayland, Android columns remain U — no node of those platforms was reached (§7)`

Supersedes `.workflow/analyst/P10-02_PLATFORM_MATRIX.md` (102 lines, sha256 `1776bad08b69…`, commit `79349a6`) as the measurement record. The v1 file was not edited; this is a second document. The contract (`docs/contracts/P10-01_NEUTRAL_CONTRACT.md`) was not edited.

---

## 1. Build provenance (what every M cell in this document is attributable to)

| item | value | how obtained |
|---|---|---|
| repo / branch | `/opt/data/projects/PROBE_UPGRADE/v119wt` · `r119-build` | `git rev-parse --abbrev-ref HEAD` |
| build commit | `b342636e4ba22b788cabcafce223cc41c1956af3` | `git rev-parse HEAD`; tree clean (`git status --porcelain` → empty) |
| source semver | `internal/version/version.go:21` `var Semver = "1.19.1"` | `grep -n 'Semver = ' internal/version/version.go` |
| built binary self-report | **`PROBE v1.19.1`** | `probe version` on the binary built below |
| client self-report | `1.19.1` | `probe-client --version` |
| server self-report | `{"server_version":"v1.19.1"}` | `GET /api/v1/health` on 127.0.0.1:17892 |
| probe binary sha256 | `807fb0fc32619487a1f4cbe67d8d559390bd00e538fba6dcee821e21df8ad3c8` | `sha256sum bin/probe` |
| probe-client sha256 | `1545c4600d3d6a8f165de33d24828235c8c8b3f249051b7e7899699c54425228` | `sha256sum bin/probe-client` |
| toolchain actually used | `go1.25.0 linux/amd64` (`/opt/data/gopath2/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64`) | `$TC/bin/go version` |
| stale v1 binary (NOT used) | `./probe`, mtime 2026-11-22 22:38, self-reports `PROBE v1.9.4`, sha256 differs | `./probe --version` |

Build command (verbatim):

```
TC=/opt/data/gopath2/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64
GOROOT=$TC GOPATH=/opt/data/gopath2 GOMODCACHE=/opt/data/gopath2/pkg/mod GOTOOLCHAIN=local \
  $TC/bin/go build -o $B/probe ./cmd/probe
GOROOT=$TC GOPATH=/opt/data/gopath2 GOMODCACHE=/opt/data/gopath2/pkg/mod GOTOOLCHAIN=local \
  $TC/bin/go build -o $B/probe-client ./cmd/probe-client
```

Toolchain defect on this host, recorded because it changes what "built from HEAD" means: the installed GOROOT at `/opt/data/go` is corrupt — `go1.24.4` refuses every build with `roundupsize redeclared` (`src/runtime/msize.go:14` vs `msize_noallocheaders.go:17`) and `mallocHeaderSize redeclared` (`src/runtime/mbitmap.go:71`), i.e. build tags are not honoured in that tree. `GOPATH` and `GOROOT` are also the same directory. I did not use it. The two builds above use the clean downloaded toolchain in the module cache and both exit 0. Provenance for every M cell below is therefore: **PROBE v1.19.1, built from `b342636`, go1.25.0**, not the go1.24.4 the falsification used.

Nodes actually executed against (3 agents, one server, all this host):

| node | how | identity (live `GET /api/v1/agents`) |
|---|---|---|
| `p1005-headless` | `probe serve --addr 127.0.0.1:17892` + `probe connect --config run/headless.json`, no `DISPLAY` | linux/amd64, version `1.19.1`, capabilities include `input`,`capture` |
| `p1005-x11` | second client, `DISPLAY=:99` against `Xvfb :99 -screen 0 1024x768x24`, **no window manager** | linux/amd64, version `1.19.1` |
| `p1005-noinput` | third client, capabilities `["exec","filesystem","process","capture"]` (no `input`) | linux/amd64, version `1.19.1` |

Helper situation: `xdotool`, `import`, `scrot`, `xclip` are **absent** on this host (`which` → only `/usr/bin/Xvfb`); uid 10000, no sudo, so no install. Instead of installing, three **logging shims** were placed first on PATH (`cache/scratch/p1005/shims/`): each appends its argv to `run/argv.log` and exits 0; the two capture shims additionally emit a fixed 74-byte PNG. **A shim proves parameter plumbing through the agent to the process boundary — it does not prove that the real tool, X server or desktop would perform the action.** Cells resting on a shim say so.

---

## 2. Matrix — 14 rows × 5 platforms, three-state labels

Legend: **M** = measured on a live node this session · **S** = source-verified, not executed · **U** = unknown, needs a node. Every M cell carries `1.19.1@b342636` (binary version @ build commit) and its case id; every case id's verbatim command is in §5.

| primitive | Windows | Linux/X11 (`p1005-x11`) | Linux/headless (`p1005-headless`) | Linux/Wayland | Android |
|---|---|---|---|---|---|
| **1 selector on exec** | U — no node. (S: `ExecParams` carries no session/context field, `protocol/messages.go:230-280`) | **M 1.19.1@b342636 — selector silently dropped**: `session:7`, `context:"x11::0"`, `window+count` all → `200 ok:true exit_code:0`, stdout byte-identical to the no-selector call (`R1.exec.plain/session/context/window`). Context is fixed at launch: `echo DISPLAY=$DISPLAY` → `DISPLAY=:99` (`R1.exec.display-env`) | **M 1.19.1@b342636 — same, `DISPLAY=` (empty)** (`R1.exec.*` on `p1005-headless`) | U — no node. (S: same code path; no Wayland backend in tree) | U — no node |
| **2 context enumeration** | U — no node. (S: no `/sessions` handler registered) | **M 1.19.1@b342636 — absent**: GET `sessions`,`session`,`context`,`contexts` and POST `contexts` → `404` (`R2.*`) | **M 1.19.1@b342636 — same five 404s** | U | U |
| **3 window tree** | U — no node. (S: 0 window symbols in tree) | **M 1.19.1@b342636 — absent**: POST `ui-tree`,`windows`,`window-tree` → `404` (`R3.*`) | **M 1.19.1@b342636 — same three 404s** | U | U |
| **4 window control** | U — no node. (S: no window handle field exists) | **M 1.19.1@b342636 — absent**: POST `ui-window`,`window-control` → `404` (`R4.*`) | **M 1.19.1@b342636 — same** | U | U |
| **5a pointer click** | U — no node. (S: `platform_windows.go:430-454` `SetCursorPos`+`mouse_event`, absolute pixels, no window identity) | **M 1.19.1@b342636 — reported success, action reached the process boundary**: `{"x":10,"y":10}` → `200 {"ok":true,"data":{"success":true}}`; shim argv `mousemove 10 10 click 1` (`R5a.click.plain`). With `button:"right",count:2,window,context` → **same single left click at 10,10**, all four fields dropped (`R5a.click.full`). Empty body → argv `mousemove 0 0 click 1` (`R5a.click.empty`). `button:"banana"` accepted (`R5a.click.badbutton`). Shim = plumbing only | **M 1.19.1@b342636 — byte-identical to X11 for all four cases** | U — (S: xdotool is X11-only; no Wayland backend) | U |
| **5b double-click** | U — (S: no `count` field in `InputParams`) | **M 1.19.1@b342636 — `count` ignored**: `{"count":2}` → argv `mousemove 10 10 click 1`, one click, `success:true` (`R5b.dblclick`) | **M 1.19.1@b342636 — same** | U | U |
| **5c scroll / wheel** | U — (S: 0 hits for wheel/scroll) | **M 1.19.1@b342636 — absent**: `pointer-scroll` → `404` (`R5.pointer-scroll`) | **M 1.19.1@b342636 — same** | U | U |
| **5d drag** | U — (S: 0 hits) | **M 1.19.1@b342636 — absent**: `pointer-drag` → `404` (`R5.pointer-drag`) | **M 1.19.1@b342636 — same** | U | U |
| **5e pointer move / hover** | U — (S: 0 hits) | **M 1.19.1@b342636 — absent**: `pointer-move`,`pointer-hover` → `404` (`R5.pointer-move/hover`) | **M 1.19.1@b342636 — same** | U | U |
| **5f Unicode text** | U — (S: byte-wise loop `platform_windows.go:500-528`) | **M 1.19.1@b342636 — accepted, bytes reach the boundary intact**: `"città è à 名前 🙂"` → `200 success:true`, argv `type città è à 名前 🙂` (`R5f.text.unicode`); with `window`,`encoding:"utf16"`,`context` added the argv is `type abc` — **all three dropped** (`R5f.text.full`). Rendering/IME untested (shim) | **M 1.19.1@b342636 — same** | U | U |
| **5g key press / key combo** | U — (S: protocol types exist; no Windows node) | **M 1.19.1@b342636 — ROUTES EXIST AND EXECUTE**: `POST /key-press {"key":"Return"}` → `200 success:true`, argv `key Return` (`R5g.key-press`); `POST /key-combo {"keys":["ctrl","c"]}` → `200 success:true`, argv `key ctrl c` (`R5g.key-combo`); with `context` added → 200, field dropped (`R5g.key-press.ctx`). Routes registered at `internal/server/api_v1.go:490-491` | **M 1.19.1@b342636 — same three results** | U | U |
| **6a capture full screen** | U — no node. (S: primary-monitor `SM_CXSCREEN` + `BitBlt(...,0,0)`, `platform_windows.go:316,366`) | **M 1.19.1@b342636 — 200 with a frame, dimensions unknown to the caller**: `POST /capture {}` → `200 ok:true`, `format:"png"`, `width:0`, `height:0`, `size_bytes:74`, `data sha256[:16]=59edf55565bcecb9` (`R6a.capture.plain`). Shim frame = plumbing only, and the `0`s are the agent's own report | **M 1.19.1@b342636 — identical body and sha** | U | U |
| **6b capture window / region** | U — (S: no `hwnd`/`rect` field exists) | **M 1.19.1@b342636 — `window`/`rect`/`format` all ignored**: `{"window":"0x3c00007","rect":{4×4},"format":"png"}` → **byte-identical** to 6a, same sha (`R6b.capture.window`) | **M 1.19.1@b342636 — identical** | U | U |
| **6c hash-only** | U — (S: `CaptureResult` has no hash field) | **M 1.19.1@b342636 — `hash_only` ignored, full payload returned, no `hash` key**: `{"hash_only":true}` → identical body/sha, `data` present (`R6c.capture.hashonly`) | **M 1.19.1@b342636 — identical** | U | U |

Row count: 14 rows × 5 platforms = **70 cells — 28 measured** (14 × 2 Linux nodes), 14 S (Windows source-only), 28 U (Wayland 14, Android 14). v1 claimed the same 28 M; the cells are the same cells, but every one of them is now attributable to `1.19.1@b342636` rather than `v1.9.4`.

Additional measured rows that are not in v1's table (the streaming endpoints the falsification used):

| primitive | Linux/X11 | Linux/headless | note |
|---|---|---|---|
| **stream start** | **M 1.19.1@b342636** — `200`, `stream_id` returned; `context:"x11::0"` accepted and dropped (`ScreenStreamParams` = display,fps,quality) | **M same** | `R7.stream-start.ctx` |
| **stream frame** | **M 1.19.1@b342636 — `stream_id` is not read by the handler**: asked for stream S1, response carried **S2's** `stream_id`; asked for `bogus-id`, still `200` with S2's frame (`seq_num` present) | **M same** | handler reads per-agent memory `last_stream_frame` (`api_v1_transfer.go:353-368`); new defect NS-1 |
| **stream stop** | **M 1.19.1@b342636 — `stopped:true` is not an effect**: stop with exact id → `{"stopped":true,"stream_id":"<id>"}`; stop with `{}` → `{"stopped":true,"stream_id":""}` **and the frame sequence keeps advancing** (10→15 before, 25→35 after, fps 5) | **M same** | `agent_stream.go:88-121`; new defect NS-2 |

---

## 3. Diff against v1 — cell → old label → new label → reason

| cell | v1 label | v2 label | reason |
|---|---|---|---|
| **5g key press/combo — X11 and headless** | **M — "404"**, concluded "no HTTP route wires them" | **M — 200 `success:true`**, argv `key Return` / `key ctrl c` | v1 measured a URL that was never registered. Routes are `/key-press` and `/key-combo` at `api_v1.go:490-491`; the dashless spellings 404 for that reason alone. **A capability was reported absent that is present.** |
| 1 selector on exec — X11 | M — ABSENT, silently ignored | M — ABSENT, silently ignored, `1.19.1@b342636` | same verdict, re-confirmed on a HEAD build; now also confirmed on a launch-env-differing node (`DISPLAY=:99` vs empty) |
| 1 selector on exec — headless | M — same as X11 | M — same | re-confirmed |
| 2 contexts — X11 + headless | M — 404 | M — 404 (5 paths) | re-confirmed; two extra paths tested |
| 3 window tree — X11 + headless | M — 404 | M — 404 (3 paths) | re-confirmed |
| 4 window control — X11 + headless | M — 404 | M — 404 (2 paths) | re-confirmed |
| 5a click — X11 + headless | **M — "REFUSED (dependency)"**, `200` envelope carrying `xdotool not found` | **M — 200 `success:true`, action reached the boundary**, argv `mousemove 10 10 click 1` | helper replaced by a logging shim. The underlying defect did not move: `button`, `count`, `window`, `context` are dropped and a right double-click becomes a left single-click. v1's word "REFUSED" described a 200 response — the falsification's M-4 is right |
| 5b double-click — X11 + headless | M — unsupported param, ignored (identical to 5a) | M — ignored, argv proves one click | same verdict, first direct argv proof |
| 5c/5d/5e — X11 + headless | M — 404 | M — 404 | re-confirmed (5e tested on two route names) |
| 5f Unicode — X11 + headless | M — REFUSED (dependency), byte-vs-rune not observable | **M — 200, argv carries `type città è à 名前 🙂` intact**; `window`/`encoding`/`context` dropped | shim makes the parameter path observable; **glyph rendering still unproven** — no IME, no real xdotool |
| 6a capture — X11 + headless | M — REFUSED (dependency), `import/scrot not found` | **M — 200, `format:"png"`, 74-byte frame, `width:0 height:0`** | shim emits a valid PNG; the `0,0` is the agent's own sizing defect and is unchanged from v1's source reading |
| 6b capture window/region — X11 + headless | M — parameters ignored, identical 132-byte error | M — parameters ignored, identical **success** body (same sha) | same defect, opposite envelope: v1 could not distinguish "ignored" from "dependency failed" because both produced the same error |
| 6c hash-only — X11 + headless | M — no such field, identical error, no hash key | M — no such field, identical **full payload**, no hash key | same defect, now on a success path |
| SS-12 capability refusal | (falsification §3) `409 CAPABILITY_MISSING` | **REFUTED — `503 {"code":"AGENT_UNREACHABLE"}`** on a reachable agent | `handleV1PointerClick/KeyPress/KeyCombo/TextInput` call `forwardToAgent` directly (`api_v1_transfer.go:375-470`); the 409 translation at `api_v1.go:174-181` is only on the legacy wrapper path, so it never fires for these routes. A wrong code, not the code the falsification recorded |
| stream stop / frame | (falsification §3 SS-9/SS-10) context dropped; stop stops every stream | **`stream_id` ignored on read** (returns another stream's frame); **`{}` stop is a silent no-op that returns `stopped:true`** | new measurements, §4 NS-1/NS-2 |

Net: **one v1 verdict reversed** (5g: absent → present), **five cells changed envelope** (5a, 5f, 6a from failure-with-error to success-with-shim; 6b, 6c from error to success), **eight cells re-confirmed unchanged**, **two falsification claims refuted** (SS-12's code, SS-10's "stops every stream"), **two new defects** found in streaming.

---

## 4. The 13 silent-success paths — independently re-run at HEAD

`200 ok:true` is the literal response; `argv` is the shim log (what the platform actually did).

| # | re-run result | evidence |
|---|---|---|
| SS-1 `exec` + `session`/`context`/`window` | **CONFIRMED** | 4 calls, all `200 exit_code:0`, stdout identical `76cffb9490e2`; no refusal anywhere (`R1.exec.*`) |
| SS-2 `pointer-click` + `button,count,window,context` | **CONFIRMED** | argv `mousemove 10 10 click 1` — right double-click in a named window became a left single-click (`R5a.click.full`) |
| SS-3 `pointer-click {}` | **CONFIRMED** | argv `mousemove 0 0 click 1`, `success:true` (`R5a.click.empty`) |
| SS-4 `text-input` + `window,encoding,context` | **CONFIRMED** | argv `type abc` (`R5f.text.full`) |
| SS-5 `capture {hash_only:true}` | **CONFIRMED** | full `data` returned, no `hash` key in any capture response (`R6c.capture.hashonly`) |
| SS-6 `capture {format:jpeg,quality:30}` | **CONFIRMED** | `format:"png"`, sha identical to plain capture (`R6a.capture.quality`) |
| SS-7 `capture {window,rect}` | **CONFIRMED** | identical sha to plain capture (`R6b.capture.window`) |
| SS-8 `capture {display:9}` | **CONFIRMED** | identical sha to `display:0` (`R6a.capture.display9`) |
| SS-9 `stream-start {context,fps,quality}` | **CONFIRMED** (dropped) | `ScreenStreamParams` has no context field; `200` + stream_id, no echo of context (`R7.stream-start.ctx`) |
| SS-10 `stream-stop {context}` | **PARTLY REFUTED, defect is worse** | context is indeed dropped, and stop does **not** "stop every stream" — it stops **nothing** when the id is empty or unknown, while returning `stopped:true` (§4 NS-2) |
| SS-11 capture reports `width:0,height:0` | **CONFIRMED** | every capture response, both nodes (`R6a.*`) |
| SS-12 capability-missing returns `409 CAPABILITY_MISSING` | **REFUTED** | `503 AGENT_UNREACHABLE` on a live, connected agent that simply lacks `input` (`SS12.click.noinput`; the identical result on the key-press route is recorded in `run/battery.json` under the SS12 key-route case). Same call on a capable agent is `200`. The contract's `409 UNSUPPORTED_AGENT` vocabulary is absent from this path |
| SS-13 future-phase `context` on exec/capture/stream | **CONFIRMED by source, still unmeasurable** | handlers decode into structs with no context field; strict-decode absent. `exec` + `detach`+`context` today: fields dropped (SS-1) |

New silent-success paths found this session:

**NS-1 — `stream-frame` accepts any `stream_id` and returns the newest frame from any stream.** Source: the handler never parses the query string; it returns per-agent memory `last_stream_frame` (`internal/server/api_v1_transfer.go:353-368`, memory written at `server_ws.go:327`). Live: two concurrent streams S1, S2; asking for S1 → response `stream_id: S2`, asking for `bogus-id` → `200` with S2's frame. A caller cannot tell which stream a frame belongs to.

**NS-2 — `stream-stop` returns `stopped:true` without stopping.** `handleStreamEnd` (`internal/agent/agent_stream.go:88-121`) only takes the stop-all branch when parameter parsing **errors**; `{}` parses cleanly to `StreamID:""`, which matches no key in `streamMgr.streams`, and the handler still reports success. Live on a freshly restarted agent with exactly one stream: `seq 10` → `seq 15`, `POST /stream-stop {}` → `{"stopped":true,"stream_id":""}`, `seq 25` → `seq 35`. With the exact id the stop does take effect (that path closes `stopCh`), so the defect is specific to empty/unknown ids and to the unreachable stop-all branch.

---

## 5. Commands (each case id above maps to one of these, verbatim)

```
A=http://127.0.0.1:17892/api/v1/agents/p1005-headless   # or p1005-x11, p1005-noinput
H='Authorization: Bearer p1005'
curl -s -X POST -H "$H" -d '{"command":"hostname","session":7}'                          $A/exec            # R1.exec.session
curl -s -X POST -H "$H" -d '{"x":10,"y":10,"button":"right","count":2,"window":"0x3c00007","context":"x11::0"}' $A/pointer-click   # R5a.click.full
curl -s -X POST -H "$H" -d '{}'                                                          $A/pointer-click   # R5a.click.empty
curl -s -X POST -H "$H" -d '{"text":"abc","window":"0x3c00007","encoding":"utf16","context":"x11::0"}' $A/text-input # R5f.text.full
curl -s -X POST -H "$H" -d '{"key":"Return"}'                                            $A/key-press       # R5g.key-press
curl -s -X POST -H "$H" -d '{"keys":["ctrl","c"]}'                                       $A/key-combo       # R5g.key-combo
curl -s -X POST -H "$H" -d '{"hash_only":true}'                                          $A/capture         # R6c.capture.hashonly
curl -s -X POST -H "$H" -d '{"display":9}'                                               $A/capture         # R6a.capture.display9
curl -s        -H "$H"                                                                   $A/sessions        # R2.GET.sessions
curl -s -X POST -H "$H" -d '{}'                                                          $A/ui-tree         # R3.ui-tree
curl -s -X POST -H "$H" -d '{"context":"x11::0","fps":2,"quality":50}'                   $A/stream-start    # R7.stream-start.ctx
curl -s        -H "$H" "$A/stream-frame?stream_id=<id>"                                                     # R7.stream-frame
curl -s -X POST -H "$H" -d '{}'                                                          $A/stream-stop     # R7.stream-stop.ctx
```

Harness: `python3 cache/scratch/p1005/battery.py` (3 agents × 45 cases, raw responses in `run/battery.json`; 4 traffic-battery assertions above were re-run as single curls after a client restart to remove cross-stream interference). Reproduce:

```
cd /opt/data/profiles/analyst/cache/scratch/p1005
./bin/probe serve --addr 127.0.0.1:17892 --token p1005 --registry run/registry.json --operator-db run/ops.json --allowed-cidr 0.0.0.0/0 &
PATH=$PWD/shims:$PATH ./bin/probe connect --config run/headless.json &
DISPLAY=:99 PATH=$PWD/shims:$PATH ./bin/probe connect --config run/x11.json &   # after: Xvfb :99 -screen 0 1024x768x24
PATH=$PWD/shims:$PATH ./bin/probe connect --config run/noinput.json &
```

---

## 6. Route table check at HEAD (every route cited above)

`internal/server/api_v1.go`: `/exec` :320 · `/capture` :358 · `/stream-start` :484 · `/stream-stop` :485 · `/stream-frame` :486 · `/pointer-click` :489 · `/key-press` :490 · `/key-combo` :491 · `/text-input` :492. A grep of the entire mux block (:297-503) contains no session, context, window-tree, window-control, pointer-scroll, pointer-drag, pointer-move or pointer-hover registration — the 404s in §2 are absence, not capability. The dashless key spellings used by v1 do not exist in this table; that is the whole of v1's 5g result.

## 7. Coverage bound

NOT CHECKED: any Windows host → leaves all 14 Windows cells source-only, including the primary-monitor capture defect, the byte-wise text loop, and the whole WTS session ladder. NOT CHECKED: a real xdotool/ImageMagick install → leaves WORKS-vs-PLUMBING undecided for 5a/5f/6a; a shim proves argv, not effect. NOT CHECKED: any Wayland seat → Wayland column empty, no Wayland backend in tree. NOT CHECKED: any Android device or APK → column empty, no build target exercised here. NOT CHECKED: a window manager on the Xvfb node → leaves "empty window list vs no window manager" undecided, because the window-tree route does not exist at HEAD either way. NOT CHECKED: the registry behind the v1 16-linux/8-windows fleet premise → leaves fleet size unverified; this session saw only its own 3 agents.

`S` cells were not promoted to `M` anywhere. Every `M` above was executed this session against `PROBE v1.19.1` built from `b342636`; the stale `v1.9.4` binary was identified and not used.

## 8. Review request

Two acceptance items in the brief cannot be satisfied by measurement and are flagged here rather than asserted: (a) Windows/Wayland/Android remain `U` — no node; (b) the falsification's SS-12 and SS-10 claims did not survive re-measurement (refuted above with responses), so the falsification's count of 13 silent-success paths should be read as 11 confirmed + 1 partly refuted + 1 refuted + 2 new (NS-1, NS-2). Contract and v1 matrix were not edited.
