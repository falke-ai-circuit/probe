# P13-02 — contract §6: `GET /api/v1/agents/{id}/contexts`

Unit P13-02 · workflow wf_probe1 · branch `r119-build` · repo `/opt/data/projects/PROBE_UPGRADE/v119wt`
Code commit `f243257` · base `7c19a1a` (P13-01 code landed at `7274ec2`; the commit above it is its close record).

Read-only enumeration. No context entry, no window control, no write verb (`disconnect`/`logoff`/
`message`/`switch` are OD-4 and untouched). Contract document untouched.

## What shipped

| file | role |
|---|---|
| `internal/protocol/contexts.go` | §6 wire types + `ClassifyContext` (the one predicate) + `ResolveContext` + §5.4 code mapping |
| `internal/agent/contexts.go` | `buildContexts` (pure), `handleContexts`, the pre-dispatch selector gate, 5 s enumeration cache |
| `internal/agent/contexts_linux.go` | linux backend: `/tmp/.X11-unix` scan with socket-inode owner uid, `$DISPLAY`, `$WAYLAND_DISPLAY`, `xprop`/`xdpyinfo` probes |
| `internal/agent/contexts_other.go` | non-linux: `PRIMITIVE_UNSUPPORTED/NO_BACKEND` — no invented row |
| `internal/server/api_v1_contexts.go` | the HTTP handler: `input` scope, query refusal, forward, §6 envelope |
| `internal/server/strict.go` | `refuseQueryParams` — the §5.6/§6 rule for a body-less GET |
| `internal/protocol/strict.go` | `AllowContextsQuery = []string{}` (named empty allow-list) |
| `internal/server/api_v1.go` | route registration |
| `internal/agent/agent.go` | `TypeContexts` in the command table + the selector gate before dispatch |
| `internal/protocol/messages.go` | `ErrPrimitiveRefused` (§11 403 class) |

Five modified files are additive only: `+47/−0` across `agent.go`, `messages.go`, both `strict.go`,
`api_v1.go` — no existing line changed.

## Acceptance, item by item

**1. Envelope.** `{ok, data:{agent_context, generated_at_utc, contexts:[…]}}` exactly; `data` has
three keys, no more. Rows carry `id, kind, name, user, uid, active, active_known, agent_own,
addressable, addressable_reason, surface{pixels,displays,dpi_scale}, features{window.tree,
window.control, input.pointer, input.text, capture.window, capture.region}` plus `idle_s`,
`logon_time`, `refusals` (§6 names them in the prose for the unknown rule; the row list in the
brief does not forbid them). Test: `TestP1302ContextsEnvelopeShape` — asserts the key count, every
required row/surface/feature key, and that `generated_at_utc` parses as RFC3339 with a `Z` suffix.

**2. No fabricated zero.** `active` is `*bool`: `null` + `active_known:false` when the platform did
not answer. Test: `TestP1302ActiveIsNullWhenUnknown` — the `uid` row is null/unknown (and the raw
JSON is asserted to contain `"active":null`, not an omitted field), the x11 row in the same answer
carries the boolean the probe returned. `idle_s:-1`, `logon_time:""` asserted on the unknown row.
The two paths are distinguishable: the null row's `Active` is a nil pointer, the known row's is a
set pointer — not one value relabelled.

**3. One predicate.** `addressable` comes from `protocol.ClassifyContext` when the agent builds each
row. The selector gate calls `protocol.ResolveContext`, which **reads the row's verdict** — it does
not recompute from facts. Test: `TestP1302SelectorGateCarriesTheRowReason` — takes the `uid:1000`
row reported `addressable:false, addressable_reason:"kind_has_no_surface"`, sends `ui-tree` with
`{"context":"uid:1000"}`, asserts the refusal is `CONTEXT_UNAVAILABLE` with
`detail.addressable_reason == row.AddressableReason` and `detail.reason ==
ContextUnavailableReason(row.AddressableReason)` (`KIND_NOT_APPLICABLE`). Same test also proves the
gate is not a blanket deny (an addressable `x11::0` passes) and that `session:2` refuses
`KIND_NOT_APPLICABLE` (wts alias on a host with no wts context). End-to-end through the server:
`TestP1302UnaddressableSelectorIs409WithRowReason` (HTTP 409, detail preserved).
Unit level: `TestP1302ResolveReadsTheRowsVerdict`.

**4. Closed enum.** `agent_own · service_principal · same_uid_other_process · other_user ·
no_user · not_interactive · privilege_not_held · kind_has_no_surface`, asserted verbatim in order,
with duplicates rejected (`TestP1302AddressableReasonEnumClosed`), and **every** value produced by
a fact set (`TestP1302EveryReasonHasAProducer`) so no value is dead vocabulary. The §5.4 mapping is
total (`TestP1302ContextUnavailableMappingIsTotal`).

**5. Features per context.** Built per row, not per agent. Test:
`TestP1302NoWindowManagerIsAdvertised` — a `wmAbsent` display gives `window.tree:false` and the
§13 entry `{feature:"window.tree", code:"PRIMITIVE_UNSUPPORTED", reason:"NO_WINDOW_MANAGER"}` in the
same row. `TestP1302FeaturesArePerContext` — same agent, same answer: the x11 seat advertises
`input.pointer:["click"]`, `input.text:["unicode"]`, `capture.region:true`; the `uid` row advertises
nothing and classifies `kind_has_no_surface`.

**6. GET only + RBAC.** `s.v1CheckAuth(w, r, "input")` — the same scope the selector-bearing reads
use, so a viewer token (not in `viewerActions`) gets 403 (`TestP1302ContextsViewerForbidden`).
POST on the path is 405 (`TestP1302ContextsQueryParameterRefused`). A query parameter is
`400 INVALID_PARAMS reason:"UNKNOWN_FIELD"`, `detail{field, allowed:[], ran:false}` — the strict
layer, not a silent drop.

**Wiring into the §5.5 strict layer / does the route carry a selector?** It does **not**: §6 has no
`context`/`session` input, so the route's strict form is the query refusal (there is no body to
allow-list) and `AllowContextsQuery` is a named empty list so the refusal prints `allowed:[]`.
`stream-frame` already solved the same shape inside its handler; the helper `refuseQueryParams` is
new and is used only by this route (refactoring `stream-frame` onto it would be off-brief).

## Honest limits (no fabricated capability)

- **`treeBackendPresent = false`.** The EWMH tree backend (§7) does not exist in this build, so
  `window.tree` is `true` **nowhere**. With a window manager present the row reports
  `window.tree:false` + `{window.tree, PRIMITIVE_UNSUPPORTED, NO_BACKEND}`; with no window manager
  it reports the environmental cause `NO_WINDOW_MANAGER` instead. One reason per cause (§7.6).
  When §7 lands, flipping the constant makes the `wmPresent` branch advertise `true`.
- `window.control` is `[]` with a `{window.control, …, NO_BACKEND}` refusal — the six actions are a
  later unit; claiming them now would be a claim the route answers 501 to.
- `surface.pixels` is `""` when `xdpyinfo` is absent or unparsable; `dpi_scale` stays `0` for an
  unmeasured surface. A fabricated resolution would be the same class of lie as a fabricated
  `active`.
- `active` for an x11 row is known only when `xprop` answered `_NET_ACTIVE_WINDOW` (non-zero id →
  true). No seat query (`loginctl`) is invoked, so a host where `xprop` is missing reports unknown.
- Wayland rows exist only when `$WAYLAND_DISPLAY` names a seat; they advertise
  `{window.tree, PRIMITIVE_REFUSED, NO_GLOBAL_WINDOW_LIST}` (403: the compositor's own model) and
  `{capture.window, PRIMITIVE_UNSUPPORTED, NOT_EXPOSED}`.
- The enumeration is not a privilege probe: another user's X socket is classified `other_user`
  (addressable only when the agent is root) from the socket inode's uid; no connect is attempted.
- The selector gate runs before dispatch for the 12 §5.5 selector-bearing commands. It does not
  re-validate a selector the server already refused, and it answers nothing for a host where
  enumeration is unavailable (the capability gate owns that refusal — inventing `NOT_FOUND` there
  would be a fabricated answer).

## Verification

- `go build ./...` → exit 0. `go vet ./...` → exit 0.
- `GOOS=windows go build ./internal/agent/ ./internal/protocol/ ./internal/server/` → exit 0 (the
  `contexts_other.go` path compiles and refuses, rather than being untested source).
- `go test ./... -count=1 -json` → **687 test-level pass / 0 fail / 2 skip** (baseline `7274ec2`:
  673/0/2). Delta +14 = this unit's 4 protocol + 6 agent + 4 server tests exactly. The 2 skips are
  the pre-existing `TestDownloadURL_LiveCrossProcessProof` and `TestV1_CreateTransfer`; five
  additional `Action:"skip"` events are Go's no-test-files package markers, not test skips.
- `go test ./internal/{protocol,agent,server}/ -run P1302 -v` → 14/14 PASS.

## Open item

`window.tree` cannot be advertised `true` until the EWMH tree backend (§7) ships —
`treeBackendPresent` is the single switch, and the `NO_WINDOW_MANAGER` refusal is already produced
when the WM is genuinely absent, so the flip is one constant, not a rewrite.
