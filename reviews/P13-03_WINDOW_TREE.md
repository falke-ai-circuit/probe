# P13-03 — §7 window tree on linux (X11 EWMH), with §7.1's refusal

Unit: P13-03 · workflow wf_probe1 · branch `r119-build` · repo `/opt/data/projects/PROBE_UPGRADE/v119wt`
Base: `f243257` (P13-02, `treeBackendPresent = false`)
Spec: `docs/contracts/P10-07_CONTRACT_V3_1.md` §7 (396-428), §7.1 (430-458)

Scope: `POST /api/v1/agents/{id}/ui-tree` on linux/X11. No §8 window control, no Wayland
`foreign_toplevel`, no Windows `wts` column; the other five `NO_BACKEND` refusals are untouched.

## What shipped

| file | role |
|---|---|
| `internal/protocol/uitree.go` | §7 wire types: `UITreeResult`, `UITreeRow`, `UITreeContextRef`, `UITreeRect`, `UITreeNative`; reason/source/kind constants |
| `internal/agent/uitree.go` | the enumeration: `buildUITree` against the `treeDisplay` seam; §7.1 check before enumeration; top-level filtering; 500 cap; foreground/iconic/title/class rules |
| `internal/agent/x11.go` | minimal X11 protocol client (linux): connection setup, MIT-MAGIC-COOKIE-1 from `$XAUTHORITY`, InternAtom, GetProperty, GetWindowAttributes, GetGeometry, TranslateCoordinates, GetAtomName |
| `internal/agent/x11_types.go` | the untagged value types the seam answers with (property bytes, attributes, geometry) — so the builder and its synthetic tests compile on every platform |
| `internal/agent/uitree_linux.go` | §7 handler: context resolution, `context_ref`, the §7.1 501 envelope with the contract's `detail` |
| `internal/agent/uitree_other.go` | non-linux: `NO_BACKEND`, never an empty tree |
| `internal/agent/contexts.go` | `treeBackendPresent = true`; the per-context answer is now observation-driven |
| `internal/agent/contexts_linux.go` | `probeX11` reads the WM check over the X protocol first, xprop only as fallback |

`handleUITree` (agent_strict.go) parses strictly, then dispatches to `a.uiTree` — the strict
layer is unchanged, and rows 6/8/9/14 still answer `NO_BACKEND`.

## Acceptance, item by item

### 1. Shape — `TestP1303Shape`

`{context_ref{requested,resolved,kind,user,source}, units:"px", dpi_scale, dpi_awareness:"x11_native",
truncated, count, tree_source:"ewmh_client_list", windows:[…]}`; row
`{id, id_kind:"xid", native{xid}, title, class, rect{x,y,w,h}, iconic, foreground, owner_pid, visible, kind}`.
The test asserts values, not just presence: `id:"0x3c00007"`, `class` = WM_CLASS's second field,
`rect` from TranslateCoordinates + GetGeometry (+`_NET_FRAME_EXTENTS` when set), `owner_pid` from
`_NET_WM_PID`.

### 2. `hwnd` never on a non-`wts` row (C-4) — `TestP1303NoHWND`

The row type has no `hwnd` field, and the test marshals the result and asserts the byte string
`hwnd` is absent from the body, then decodes and asserts the key is absent while `native` is
present. Presence is the failure, not the struct shape.

### 3. Enumeration rules — `TestP1303EnumerationRules`

- top-level only: an override-redirect window (attributes) and a `_NET_WM_WINDOW_TYPE_POPUP_MENU`
  window are both excluded; an unmapped, non-iconic window is excluded under `visible_only:true`.
- stacking order: `_NET_CLIENT_LIST_STACKING` order is preserved (`0x500001` then `0x500005`);
  `_NET_CLIENT_LIST` is the fallback when no stacking list is published.
- cap: 501 synthetic windows → `count:500, truncated:true`; exactly 500 → `truncated:false`.
  The cap test uses a synthetic list, not a real desktop.

### 4. `foreground` and `iconic` — `TestP1303Foreground`

`foreground` is true for the window equal to `_NET_ACTIVE_WINDOW`, exactly one, and only if that
window is in the enumerated list. Three cases asserted: active → exactly one true; no
`_NET_ACTIVE_WINDOW` → none true; active window not in the client list → none true (no fabricated
single). `iconic` is `WM_STATE` state 3 (`IconicState`); an iconic window survives
`visible_only:true` with `visible:false` rather than disappearing from the tree.

### 5. Title not truncated — `TestP1303TitleIsNotTruncated`

An 800-character title comes back byte-identical, and a UTF-8 title (`caffè — ☕ — naïve`) round-trips.

### 6. §7.1 refusal — the load-bearing item

`wmCheck` reads `_NET_SUPPORTING_WM_CHECK` on the root window **before** any client-list read.
Absent, zero, or pointing at a window the server answers BadWindow for → refusal. `windows: []`
is returned only when the WM check passed and the WM published an (empty) client list.

`TestP1303NoWindowManagerRefusal` (builder): absent property → `NO_WINDOW_MANAGER`; check window
dead → `NO_WINDOW_MANAGER`; WM present with no clients → `200` shape, `count:0`, no refusal.

`TestP1303RealXvfbRefusesNoWindowManager` (real X server on this host): starts
`Xvfb :N -screen 0 320x240x24 -ac`, asserts `probeX11` answers `wmAbsent` (not `wmUnknown`) with
`pixels:"320x240"`, then asserts the handler envelope:

```
Error.Code             = PRIMITIVE_UNSUPPORTED
Error.Detail.reason    = NO_WINDOW_MANAGER
Error.Detail.ran      = false
Error.Detail.display  = ":N"
Error.Detail.context  = {"id":"x11::N","kind":"x11"}
Error.Detail.tree_source_attempted = "ewmh_client_list"
Error.Detail.remediation           = non-empty string
Result                             = empty (no success payload)
```

The test does not pass on `200 {count:0}`: it requires an error body with that code and reason.

### 7. The switch flips — `TestP1303SwitchFlips`

`treeBackendPresent = true` is one constant; the two facts come from observation, not from the
constant: `wmPresent` → `features["window.tree"] = true` and no `window.tree` refusal entry;
`wmAbsent` → `false` plus `{window.tree, PRIMITIVE_UNSUPPORTED, NO_WINDOW_MANAGER}`. `wmUnknown`
(a display we could not ask) still says `NO_BACKEND` — the reason names what the operator can act on.

### 8. Strict layer — unchanged, still refusing

`internal/server/p1301_strict_test.go:119` (`13 ui-tree`, body `{"visible_only":true,"nope":1}`)
still passes in the full suite; the route is the same `v1StrictForward` with the same allow-list,
and `handleUITree` still calls `ParseCommandStrict` before dispatch.

## Host facts (what is and is not proved here)

- No window manager binary on this host (`openbox`, `fluxbox`, `i3`, `metacity`, `twm`, `xfwm4`:
  none installed). `xprop`, `xwininfo`, `xdpyinfo` are also absent — hence the protocol client,
  which is why the §7.1 negative is provable here at all.
- **WM-present path: UNPROVEN-ON-HOST.** It is exercised through the `treeDisplay` seam with
  synthetic EWMH property data only. No live desktop capture is claimed.
- **Refusal path: PROVEN-ON-HOST** against a real Xvfb, as above.

## Decisions worth naming

- `visible_only` (default true) drops windows that are neither viewable nor iconic. An iconic
  window stays in the tree with `iconic:true, visible:false`; otherwise the `iconic` field would
  be unreachable under the default.
- A WM that publishes neither client list returns `200 {count:0}`: the WM check passed, so this is
  a successful enumeration of zero clients, not the §7.1 case.
- `owner_pid` is omitted when `_NET_WM_PID` is unset — an unknown owner is not pid 0.
- A window that vanishes between the client-list read and its own property reads is skipped, not
  fatal: the enumeration reports what exists at read time, and one dead id must not fail the tree.

## Tests

`/tmp/go/bin/go test ./... -count=1 -json` → **696 pass / 0 fail / 2 skip** (baseline `f243257`
687/0/2; delta +9: seven seam tests, two real-Xvfb tests). `go vet ./...` clean;
`GOOS=windows go build ./...` clean.
