# P13-05 — re-gate blockers from P13-04 (F1 blocking, F2 harness)

Commit: `495df34` on `r119-build` (pushed, tree clean). Reviewed head was `5d37f9f`. Worktree
`/opt/data/projects/PROBE_UPGRADE/v119wt`.

## F1 — `POST /exec` escaped E1/E2 (§5.5 row 1)

### Fix

`internal/protocol/messages.go:299-304`:

```go
func (p ExecParams) Selector() (*string, *int) { return p.Context, p.Session }
```

`decodeStrict[ExecParams]` (`server_api.go:173`) now type-asserts `SelectorCarrier`, so §5.1
range/conflict validation (`strict.go:70-76`), E1 (`preDispatchSelector`, `strict.go:142`) and
E2 (`assertContextRef`, `strict.go:188`) all reach `/exec`.

### Sweep — every param type carrying `Context`/`Session`

Ten types in `internal/protocol/messages.go`, scanned structurally (struct body carrying both
`Context *string` and `Session *int`), not by name:

| type | Context *string | Session *int | `Selector()` |
|---|---|---|---|
| `ExecParams` | yes | yes | **added by this unit** |
| `ScreenParams` | yes | yes | yes (`:505`) |
| `ScreenStreamParams` | yes | yes | yes (`:547`) |
| `ScreenStreamStopParams` | yes | yes | yes (`:608`) |
| `InputParams` | yes | yes | yes (`:645`) |
| `PointerMoveParams` | yes | yes | yes (`:663`) |
| `PointerScrollParams` | yes | yes | yes (`:678`) |
| `PointerDragParams` | yes | yes | yes (`:693`) |
| `UITreeParams` | yes | yes | yes (`:706`) |
| `UIWindowParams` | yes | yes | yes (`:721`) |

10/10 carriers implement `SelectorCarrier` after the fix; `ExecParams` was the only one missing
it. `internal/agent/contexts.go:343` also declares `Context`/`Session` but is an anonymous
decode struct inside a function, not a command param type and not type-asserted anywhere — no
gate reaches it, so it is out of the sweep's scope by shape (named here so it is not silent).

### Gate tests — both directions, both routes

`internal/server/p1301_strict_test.go`:

- `TestP1301E1RefusesAgentWithoutSelectorFeature` — table over `/pointer-click` and `/exec`,
  each subtest asserting 409 `UNSUPPORTED_AGENT` with `feature=context.selector`,
  `agent_version=1.19.0`, plus the selector-free control on the same route answering 200.
- `TestP1301E2RefusesMissingContextRef` — table over `/pointer-click` and `/exec`, each
  asserting 502 `PROTOCOL_VIOLATION` `missing=context_ref`, plus the `context_ref`-carrying
  control answering 200.

Agent records advertise `["input","exec"]` so the refusal measured is E1, not the generic
`CAPABILITY_MISSING` gate that hid the hole in the first probe (P13-04 §F1 note).

### Prove the negative (non-vacuous)

Pre-fix, with the method removed (`selectorREMOVED`), same tree otherwise:

```
--- FAIL: TestP1301E1RefusesAgentWithoutSelectorFeature/exec
    p1301_strict_test.go:177: status = 200, want 409 (body {"ok":true,"data":{"replayed":false,"success":true}})
--- FAIL: TestP1301E2RefusesMissingContextRef/exec
    p1301_strict_test.go:212: status = 200, want 502 (body {"ok":true,"data":{"replayed":false,"success":true}})
```

Both `pointer-click` subtests passed in that same run — the test discriminates by route, which
is the defect it now guards.

Post-fix (`495df34`):

```
--- PASS: TestP1301E1RefusesAgentWithoutSelectorFeature/pointer-click
--- PASS: TestP1301E1RefusesAgentWithoutSelectorFeature/exec
--- PASS: TestP1301E2RefusesMissingContextRef/pointer-click
--- PASS: TestP1301E2RefusesMissingContextRef/exec
ok  github.com/falke-ai-circuit/probe/internal/server  0.013s
```

## F2 — the Xvfb harness

`internal/agent/p1303_xvfb_test.go`. Test assertions unchanged: the refusal must be 501
`PRIMITIVE_UNSUPPORTED` / reason `NO_WINDOW_MANAGER` / `ran:false`, never a 200 with an empty
list.

Three changes, matching the three defects the review named:

1. **Display ownership.** `xvfbDisplayFree(num)` skips a number whose socket file, lock file,
   or answering server exists; `startBareXvfb` scans `90..179` and only starts Xvfb on a number
   nothing owns. No pid arithmetic.
2. **Readiness proves a property read.** `x11AnswersProperty` dials, then reads
   `_NET_SUPPORTING_WM_CHECK` on the root window and requires either a value or
   `errPropAbsent` — a definitive answer, not a handshake. `waitXvfbReady` also aborts when the
   child exits (display taken between scan and start).
3. **Cleanup unlinks its remains.** `stopXvfb` kills, waits for exit, removes
   `/tmp/.X11-unix/X<n>` and `/tmp/.X<n>-lock`, then waits until the socket is gone so a later
   run's scan cannot see it.

### Residual cause found by re-running, and closed

The first fixed harness (1+2+3) still failed in whole-suite runs:

```
suite run 7: exit=1 699 pass / 1 fail / 2 skip  failing=['TestP1303RealXvfbRefusesNoWindowManager']
    p1303_xvfb_test.go:154: probeX11(:98) = 0, want wmAbsent (active=<nil> pixels="")
```

Readiness had proved a property read, then `probeX11`'s own dial failed on the same display
with `pixels=""` (protocol dial failed, `xprop` absent, so `wmUnknown`). Cause: the test's
readiness connection is the server's only client; when it closes, an Xvfb started without
`-noreset` performs a server reset, which closes and re-opens its listening socket. Under suite
load the follow-up dial lands in that window → EOF/refused. The child was alive, which is why
the reviewer's instrumentation found "Xvfb alive at cleanup". Xvfb is now started with
`-noreset`, so the reset window does not exist. That change is the difference between the two
measurement sets below.

### Measurements — every run

Whole suite, `go test ./... -count=1 -json`, counts are pass/fail/skip of test cases:

| harness | isolated runs (`-run TestP1303RealXvfb…`) | whole-suite runs |
|---|---|---|
| old, at `5d37f9f` (reviewer) | 10 run / 1 fail | 8 run / 4 fail |
| unique display + property readiness + unlink cleanup | 12 run / 0 fail | 22 run / 19 clean, 3 fail |
| + `-noreset` (final, `495df34`) | **12 run / 0 fail** | **10 run / 10 clean** |

Per-run, final harness:

isolated: 12 × PASS.
whole suite: 10 × `exit=0 700 pass / 0 fail / 2 skip`.

Per-run, intermediate harness (kept because it is the evidence for the `-noreset` diagnosis):
suite runs `1-6`: 4 clean, 2 fail (`699/1/2`, `probeX11(:98)=0`, `pixels=""`); runs `7-12`: 6
clean; runs `13-22` (diag): 9 clean, 1 fail at `:98` — same signature.

`700 pass` vs the `696` baseline at `5d37f9f` is the four new subtests (2 routes × E1/E2).
Skip count unchanged at 2.

Host note: `/tmp/.X11-unix/X90…X97` and `/tmp/.X90…X97-lock` remain from the old harness
(locks hold dead pids: `X90 → 2379779`, `X97 → 2378702`, both `NO_PROCESS`). They are the
artefact the review described; the new scan skips them and starts on `:98+`, so no run attaches
to them. They were left in place rather than deleted: they are this unit's evidence, not its
state.

## Re-gate at `495df34`

| check | result |
|---|---|
| `go vet ./...` | exit 0 |
| `go test ./... -count=1` × 10 | 10 clean: `700 pass / 0 fail / 2 skip` each |
| `go test ./internal/agent/ -run TestP1303RealXvfbRefusesNoWindowManager` × 12 | 12 pass |
| `GOOS=windows go build ./...` | exit 0 |

## Open item

F2's §7.1 on-host proof is now green on 10/10 suite runs, but the WM-present path stays
UNPROVEN-ON-HOST (no WM binary on this host) — carried from P13-03, unchanged by this unit.
