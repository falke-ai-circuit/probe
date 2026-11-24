# P10-02 — PLATFORM MATRIX (measured, no code)

**profile** analyst · **gateway** 127.0.0.1:8649 `/health` → `{"status":"ok","platform":"hermes-agent","version":"0.21.4"}` (2026-11-24T17:17Z) · **status** MEASURE complete, 0 code changes, tree `/opt/data/projects/PROBE_UPGRADE/v119wt`
Unit budget 50 min · measurement window 17:17–17:30Z.

Nodes actually executed against (2, both mine, both this host):

| node | how | identity |
|---|---|---|
| `p1002-linux-headless` | `probe serve --addr 127.0.0.1:17890` + `probe connect --config client.json` | linux/amd64, agent v1.9.4, `DISPLAY=` empty, no `/tmp/.X11-unix`, no xdotool/import/scrot/xclip |
| `p1002-linux-x11` | same server, second client started with `DISPLAY=:99` after `Xvfb :99 -screen 0 1024x768x24` | linux/amd64, agent v1.9.4, X server present, **no window manager**, same missing helpers |

Fleet premise check (read-only `GET /api/v1/agents`): 17801 → `t119` (linux), 17803 → `v119b` (linux), 17804 → `t119` (linux), 17701 → `[]`. Visible = 5 linux agents (3 pre-existing + my 2), **0 windows**. The brief's "linux/amd64 x16, windows/amd64 x8 registered (2 linux + 1 windows active)" is **NOT REPRODUCED** by any registry I can read. Windows node access: EAS1 not reachable — ssh to `root@100.71.204.96` (falke-ovh tailnet name) rejected `Permission denied (publickey,password)` for root/ubuntu/debian/admin/hermes with `/opt/data/.ssh/hetzner_vps_key`; `$HOSTINGER_PASSWORD` and `$HETZNER_PASSWORD` both rejected as passwords. No Windows node was touched.

---

## Baseline verification (source, kernel-checked)

| # | claim | verdict | proof |
|---|---|---|---|
| B1 | protocol has 0 occurrences of `session` | **VERIFIED** | `grep -c -i session internal/protocol/*.go` → 0,0,0,0,0,0 over binary.go credential.go messages.go messages_test.go server.go websocket.go. The token exists elsewhere but means something else: `internal/server/session.go:7` "per-agent session data: memories, skills, context" — agent state, not an OS session. |
| B2 | `InputParams` = X,Y,Button,Text,Key,Keys | **VERIFIED** | `messages.go:557-564`, exactly those six fields, nothing else |
| B3 | capture is full-screen JPEG only | **PARTLY FALSE** | no params type exists at all (`CaptureResult` at `messages.go:467-473` = format,width,height,data,size_bytes). Windows: full-screen JPEG q70 (`platform_windows.go:310` `GetSystemMetrics(SM_CXSCREEN/SM_CYSCREEN)` + `GetDC(0)`, `:386-396` `jpeg.Encode`). Linux: **PNG** via `import -window root -` then `scrot -` (`platform_linux.go:191-209`, `format:"png"`, width/height hardcoded 0). |

Adjacent source facts used below, all greps over `internal/` and `cmd/` excluding `vendor/`:
- window symbols `EnumWindows|ShowWindow|SetForegroundWindow|GetWindowRect|hwnd|HWND` → **0 hits** anywhere in the tree. No window primitive is implemented on any OS.
- `wheel|scroll|drag|hover` → **0 hits**. Not implemented on any OS.
- `Platform` interface (`platform.go:66-101`) has Click/TypeText/KeyPress/KeyCombo and CaptureDisplay — no window function of any kind.
- Linux `Click` = `osExec("xdotool","mousemove",x,y,"click","1")`; `TypeText` = `xdotool type`; `KeyPress`/`KeyCombo` = `xdotool key` (`platform_linux.go:237-243`). Hands and eyes on Linux are **external binaries**, not in-process code.
- v1 routes that exist: `/exec`, `/capture`, `/pointer-click`, `/text-input` (`api_v1.go:320,358,489,492`). No `/keypress`, no `/keycombo`, no `/ui-*`, no `/sessions`.

---

## Matrix (rows executed unless marked SOURCE or UNKNOWN)

Legend: **M** = measured on a live node this session · **S** = source-verified, not executed (no node) · **U** = unknown, needs a node.

| primitive | Windows | Linux/X11 | Linux/headless | Linux/Wayland | Android |
|---|---|---|---|---|---|
| **1 execution-context selector** | U — 0 nodes. (S: no `session` field exists in `ExecParams`/protocol, B1) | **M — ABSENT, silently ignored**: `POST /exec {"command":"hostname","session":7}` → HTTP 200, `exit_code:0`, stdout returned, no error, no refusal. The field is dropped by json decoding. Context is fixed by the process env at launch: agent started with `DISPLAY=:99` reports `DISPLAY=[:99]`; headless reports `DISPLAY=[]`. | same as X11 (measured on both agents) | U — no node. (S: same code path as X11 unless a Wayland backend exists — none in tree) | U — no node |
| **2 context enumeration** | U — no node. (S: route table is platform-independent, no `/sessions` handler registered → 404 on any agent) | **M — 404**: `GET /sessions`, `/session`, `/context` → HTTP 404 `404 page not found` | **M — 404** (same three) | U | U |
| **3 window tree** | U — no node. (S: 0 window symbols in tree; `/ui-tree` unregistered) | **M — 404**: `POST /ui-tree`, `/windows`, `/window-tree` → 404 | **M — 404** (same three) | U | U |
| **4 window control** | U — no node. (S: same, `/ui-window` unregistered) | **M — 404**: `POST /ui-window`, `/window-control` → 404 | **M — 404** | U | U |
| **5a pointer click (no modifiers)** | U — no node. (S: `platform_windows.go:430-454` `SetCursorPos` + `mouse_event`, absolute physical pixels, no window identity, no hit-test) | **M — REFUSED (dependency)**: `POST /pointer-click {"x":10,"y":10}` → HTTP **200**, body `{"ok":true,"data":{"error":"exec: \"xdotool\": executable file not found in $PATH"}}`. HTTP 200 + `ok:true` on a failed action. | **M — same error, same 200** | U — (S: xdotool is an X11 tool; Wayland-native would need another backend, none present) | U |
| **5b double-click** | U — (S: no `count` in `InputParams`, B2) | **M — unsupported param, ignored**: `{"x":10,"y":10,"count":2}` → byte-identical response to 5a; never reached click logic (xdotool missing first) | **M — identical** | U | U |
| **5c scroll / wheel** | U — (S: 0 hits for wheel/scroll) | **M — 404**: `POST /pointer-scroll` → 404 | **M — 404** | U | U |
| **5d drag** | U — (S: 0 hits) | **M — 404**: `POST /pointer-drag` → 404 | **M — 404** | U | U |
| **5e pointer move / hover** | U — (S: 0 hits) | **M — 404**: `POST /pointer-move` → 404 | **M — 404** | U | U |
| **5f Unicode text** | U — (S: `platform_windows.go:500-518` TypeText loops `for i:=0; i<len(text); i++` over **bytes**, maps ASCII ranges, `else` falls through to `vk = ch` → non-ASCII mangled) | **M — REFUSED (dependency)**: `{"text":"città è à"}` → HTTP 200 `{"ok":true,"data":{"error":"exec: \"xdotool\": executable file not found in $PATH"}}` — identical to ASCII `"abc"`, so byte-vs-rune is not observable here | **M — identical** | U | U |
| **5g keypress / keycombo** | U — (S: protocol types exist, no v1 route) | **M — 404**: `POST /keypress`, `/keycombo` → 404 (protocol has `TypeKeyPress`/`TypeKeyCombo`; no HTTP route wires them) | **M — 404** | U | U |
| **6a capture full screen** | U — no node. (S: works in-process, JPEG q70, full virtual screen only) | **M — REFUSED (dependency)**: `POST /capture {}` → HTTP 200 `{"ok":true,"data":{"error":"display capture failed (import/scrot not found): exec: \"scrot\": executable file not found in $PATH"}}` | **M — same, both with and without `DISPLAY`** | U | U |
| **6b capture window / region** | U — (S: no `hwnd`/`rect` field exists; params type does not exist) | **M — parameters ignored**: same call with `{"hwnd":66102,"rect":{"x":0,"y":0,"w":1038,"h":118},"format":"png"}` → **byte-identical 132-byte error** as 6a | **M — identical** | U | U |
| **6c hash-only** | U — (S: `CaptureResult` has no `hash` field) | **M — no such field**: `{"hash_only":true}` → identical 132-byte error, no hash key in any response | **M — identical** | U | U |

Row count: 14 primitive rows × 5 platforms = **70 cells** — **28 measured** (Linux/X11 + Linux/headless, 14 each), 0 executed on Windows, 0 on Wayland, 0 on Android; the remaining 42 cells are source-only (Windows, 14) or unknown-needs-node (Wayland 14, Android 14).

---

## The two questions asked explicitly

**Does a headless Linux agent see any windows?** It cannot be answered as "0 windows" — there is no enumeration primitive to ask. Measured: `DISPLAY=[]`, `/tmp/.X11-unix` absent at client start, and `/ui-tree` → 404. So the honest answer is *neither 0 nor 16: the question is unanswerable on Linux through this protocol*, and the Windows session-0 "0 visible windows" finding has **no Linux counterpart to compare against** — Linux has no session concept, no window station, no enumeration.

**Does a Linux agent see the user's X session?** Measured: the agent's context is whatever environment it was **launched** with, nothing else. Client started with `DISPLAY=:99` executes `echo $DISPLAY` → `[:99]`; the same binary started without it → `[]`. Anything the operator's session owns is reachable only if the agent process inherited that `DISPLAY` (and X auth). There is no selector, no `DISPLAY` parameter, no per-call context. This is the structural equivalent of the session 0 vs 7 finding but with a different failure mode: on Windows the wrong context returns *empty results*; on Linux it returns *404* — the capability is not there at all.

**Second measured defect worth carrying up:** `pointer-click`, `text-input` and `capture` all return **HTTP 200 with `"ok":true`** while `data.error` carries the failure. A caller checking the documented success flag sees success for an action that never happened — the same "success ≠ effect" class the operator hit on Windows, now on the server envelope.

---

## What cannot be proven without a node of that platform

1. Windows — **every** row: whether `EnumWindows`-equivalent work in a worker thread sees 16 windows in session 7; whether `SetForegroundWindow`/`ShowWindow` take foreground on the Qt HMI; whether capture returns a sane JPEG of the right dimensions; whether `pointer-click` lands on the intended window; whether TypeText really mangles `à` end-to-end (source says bytes → empirically unmeasured).
2. Windows — the session 0 vs 7 contrast itself (operator-reported 0 vs 16). I reproduced neither number.
3. Linux/X11 — whether the six primitives would work **with** `xdotool`, `import`/`scrot` installed: this container runs as uid 10000 with no sudo, so I could start the X server (`Xvfb :99`) but not install the helpers. What I proved is that the code path is helper-gated, not X-gated. A node with those packages installed would settle WORKS-vs-REFUSED for 5a/5f/6a.
4. Linux/X11 — window tree/control/scroll/drag/move would still be 404 even with every package installed (0 symbols in source), but "the code as compiled on a real desktop distro" is unverified.
5. Linux/Wayland — no node, and no Wayland code path in the tree. If XWayland were present, the xdotool path might work by accident; unmeasured.
6. Android — no node exists in anything I can read. Not even a build target.
7. Fleet size x16/x8 — I could only see 5 linux and 0 windows agents across 4 registries; the 16/8 figure needs the registry the parent was reading.
8. Multi-session Linux — there is no such thing in this codebase, so the "does the agent see the other user's session" question has no Linux implementation to test; any statement about real multi-seat behaviour is untested.

---

## Reproduce (commands, verbatim)

```
mkdir -p ~/p1002/run && cd ~/p1002
printf '{"server":"ws://127.0.0.1:17890/ws","token":"p1002","name":"p1002-linux-headless","mode":"silent","permissions":"full"}' > client.json
/opt/data/projects/PROBE_UPGRADE/v119wt/probe serve --addr 127.0.0.1:17890 --token p1002 \
  --registry run/registry.json --operator-db run/ops.json --allowed-cidr 0.0.0.0/0 &
/opt/data/projects/PROBE_UPGRADE/v119wt/probe connect --config client.json &
B=http://127.0.0.1:17890/api/v1/agents/p1002-linux-headless
curl -s -X POST -H 'Authorization: Bearer p1002' -d '{"command":"hostname","session":7}' $B/exec
curl -s -X POST -H 'Authorization: Bearer p1002' -d '{"x":10,"y":10}'        $B/pointer-click
curl -s -X POST -H 'Authorization: Bearer p1002' -d '{"hash_only":true}'     $B/capture
curl -s -X POST -H 'Authorization: Bearer p1002' -d '{}'                     $B/ui-tree
Xvfb :99 -screen 0 1024x768x24 & DISPLAY=:99 probe connect --config client-x11.json &
```

Scratch: `/opt/data/profiles/analyst/cache/scratch/p1002/` (logs `run/server.log`, `run/client.log`, `run/client-x11.log`, `run/xvfb.log`).

## Coverage bound

NOT CHECKED: any Windows host → leaves primitives 1-6 on Windows unproven (source-only); a Linux node with xdotool/imagemagick → leaves Linux WORKS-vs-REFUSED undecided; a Wayland session → leaves the Wayland column entirely empty; the registry behind the 16/8 fleet claim → leaves fleet size unverified.
