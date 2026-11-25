# P13-06 — independent verification of the P13-05 fix leg

Verified against the committed blob at `495df34` (`r119-build`). Head at verification:
`d9fc1cba13fb53acedf12ec40a0f6ba4fb95124d` — two documentation commits above the fix; `git diff
--stat 495df34 HEAD` = `reviews/P13-05_CLOSE.md`, `reviews/P13-05_REGATE.md` only, so the tree I
measured is the fix tree.

Worktree `/opt/data/projects/PROBE_UPGRADE/v119wt` (clean). Independent probe and the negative
mutation ran in a `git archive HEAD` copy under
`/opt/data/profiles/architect/cache/scratch/p1306/tree`; the three files compared byte-identical
to the worktree (`cmp`) before use. Go 1.22.5 (`/tmp/go/bin/go`).

Method note: every claim below is from the committed source or from a command I ran, not from
`P13-05_REGATE.md`/`P13-05_CLOSE.md`. Where I reuse the package's existing test harness
(`newV1TestServer`, `attachP1301Agent`, `postP1301`) I say so.

## V1 — F1 closed: PASS

Code, at `495df34`:

- `internal/protocol/messages.go:299-303` — `func (p ExecParams) Selector() (*string, *int) { return p.Context, p.Session }` (`:303`), on a struct carrying `Context *string`/`Session *int` at `:288-289`.
- `internal/server/server_api.go:170-179` — `handleAgentExec` calls `decodeStrict[protocol.ExecParams](w, r, allowExec)` (`:173`) then `s.preDispatchSelector(w, agentID, params)` (`:177`). `allowExec = protocol.AllowExec` (`internal/server/strict.go:29`).
- `internal/server/strict.go:70-76` — §5.1 range/conflict validation is behind `any(params).(protocol.SelectorCarrier)`, i.e. reachable now; `preDispatchSelector` `:142-181` returns 409 `ErrUnsupportedAgent` at `:171-179`; `assertContextRef` `:188-218` returns 502 `ErrProtocolViolation` at `:210-216`.
- E2 on the exec path: `server_api.go:430-435` — `assertContextRef` after the agent-error branch, before the response is written.

Behaviour, reproduced by me (own test file `internal/server/p1306_verifier_probe_test.go`, 6 cases, `go test ./internal/server/ -run TestP1306VerifierExecGateProbe -count=1 -v`):

```
1.19.0 exec+selector              /exec   status=409 code=UNSUPPORTED_AGENT detail.feature=context.selector requested=x11::0
1.19.0 exec no-selector (control) /exec   status=200 body={"ok":true,"data":{"replayed":false,"success":true}}
1.19.0 click+selector             /pointer-click  status=409 UNSUPPORTED_AGENT
1.21.0 exec+selector no echo      /exec   status=502 code=PROTOCOL_VIOLATION detail.missing=context_ref
1.21.0 click+selector no echo     /pointer-click  status=502 PROTOCOL_VIOLATION
exec context+session conflict     /exec   status=400 code=INVALID_PARAMS reason=SELECTOR_CONFLICT
```

The last row is the one the fix leg did not claim: §5.1 validation itself now reaches `/exec`
(`"context" ("x11::0") and "session" (7) select different contexts`), which is the same
interface gate, not a special case.

Prove-the-negative, reproduced (mutated copy only; `Selector()` renamed `selectorREMOVED`,
nothing else touched, then restored and `grep -c "func (p ExecParams) Selector()"` = 1):

```
--- PASS: TestP1301E1RefusesAgentWithoutSelectorFeature/pointer-click
--- FAIL: TestP1301E1RefusesAgentWithoutSelectorFeature/exec
    p1301_strict_test.go:177: status = 200, want 409 (body {"ok":true,"data":{"replayed":false,"success":true}})
--- PASS: TestP1301E2RefusesMissingContextRef/pointer-click
--- FAIL: TestP1301E2RefusesMissingContextRef/exec
    p1301_strict_test.go:212: status = 200, want 502 (body {...})
```

So the tests fail without the method, fail only on `/exec`, and the pointer-click subtests stay
green — route-discriminating, not vacuous.

## V2 — sweep complete: PASS

Re-derived structurally, not from the table: `go/ast` walk of the whole tree
(`/opt/data/profiles/architect/cache/scratch/p1306/main.go`) collecting every **named struct type
declaration** whose body has `Context *string` and `Session *int`, plus every `Selector()`
receiver:

```
STRUCTS with Context *string AND Session *int: 10
  ExecParams            internal/protocol/messages.go:249  Selector()=true
  ScreenParams          :485  =>true
  ScreenStreamParams    :542  =>true
  ScreenStreamStopParams:604  =>true
  InputParams           :627  =>true
  PointerMoveParams     :660  =>true
  PointerScrollParams   :673  =>true
  PointerDragParams     :687  =>true
  UITreeParams          :703  =>true
  UIWindowParams        :715  =>true
Selector() methods (10): InputParams PointerMoveParams PointerScrollParams PointerDragParams
ExecParams ScreenParams ScreenStreamParams ScreenStreamStopParams UITreeParams UIWindowParams
```

10 carriers, 10 methods, zero mismatched receivers — no second missing carrier.

Reachability of the carriers through a gate: `decodeStrict[T]` call sites are
`server_api.go:173` (`ExecParams`), `:672` (`ScreenParams`), `api_v1_transfer.go:280,324,415,445,475,505`
(`ScreenStreamParams`, `ScreenStreamStopParams`, `InputParams` ×4) and the generic
`v1StrictForward[T]` (`api_v1_strict.go:24`) instantiated at `:70,72,74,76,78` with
`PointerMoveParams`, `PointerScrollParams`, `PointerDragParams`, `UITreeParams`, `UIWindowParams`
— exactly the ten carriers. Every one of them passes through `decodeStrict`'s
`SelectorCarrier` assertion (`strict.go:70`).

The excluded anonymous decode struct — `internal/agent/contexts.go:342-345`, inside
`selectorGate`, `json.Unmarshal(env.Params, &sel)` at `:346` — is genuinely unreachable by any
gate: it is a function-local variable, never a `decodeStrict` type parameter, and
`SelectorCarrier` appears in the tree only at `internal/protocol/messages.go` (10 comments),
`internal/protocol/strict.go:141-145` (the interface) and `internal/server/strict.go:70,130,143`.
`grep -rn "Session \*int" --include=*.go` outside tests and `messages.go` returns that one line
and nothing else, so it is also the only anonymous candidate.

## V3 — F2 harness fixed, and for the named reason: PASS

Read at `495df34` (`internal/agent/p1303_xvfb_test.go`): `xvfbDisplayFree` `:36-48` (socket →
lock → a dial that must fail, so an abstract-namespace survivor counts as owned);
`x11AnswersProperty` `:56-64` (`d.Property(d.Root(), "_NET_SUPPORTING_WM_CHECK")`, accepting a
value *or* `errPropAbsent`); `waitXvfbReady` `:68-82` (20 ms poll, aborts on the child-exit
channel); `stopXvfb` `:87-104` (kill → wait on `exited` with 5 s cap → unlink socket and lock →
wait up to 1 s for the socket to vanish); start args `:121-122`
`Xvfb :N -screen 0 320x240x24 -ac -nolisten tcp -noreset`, scan bound `:28-31` (`90..179`), no pid
arithmetic anywhere in the file.

My measurements, sequential, nothing else of mine running:

Whole suite, `go test ./... -count=1 -json` (pass/fail/skip of test cases):

```
run 1: exit=0 12.8s 700 pass / 0 fail / 2 skip
run 2: exit=0 13.1s 700 pass / 0 fail / 2 skip
run 3: exit=0 12.5s 700 pass / 0 fail / 2 skip
run 4: exit=0 13.2s 700 pass / 0 fail / 2 skip
run 5: exit=0 11.8s 700 pass / 0 fail / 2 skip
run 6: exit=0 14.3s 700 pass / 0 fail / 2 skip
```

Xvfb test alone, `go test ./internal/agent/ -run '^TestP1303RealXvfbRefusesNoWindowManager$' -count=1 -json`:

```
10 run: 1 pass each, exit=0 each, 0 skip (1.2, 0.6, 1.3, 0.7, 0.6, 0.6, 0.5, 0.5, 0.6, 0.6 s)
```

Post-run host state: no stray `Xvfb`, `/tmp/.X11-unix/` back to the pre-existing `X90…X97` and
their locks — the new scan skips those and cleans what it starts.

The mechanism claim (reset-on-last-client-disconnect, not the display collision alone) was tested
directly rather than inferred from a green suite: own Xvfb on `:151`, one client performs the
setup handshake and disconnects, then 1500 immediate re-dials of the same socket, 4 times per
configuration (`.../p1306/reset_probe2.py`):

```
no -noreset #1..#4 : errors 30, 37, 45, 18  (SHORT / ConnectionResetError)
-noreset    #1..#4 : errors  0,  0,  0,  0
```

A failure rate of ~1.2-3% per dial in the window after the last client leaves, and zero with
`-noreset` — the same shape as the reviewer's ~1-in-10 harness failure and as the unit's
`probeX11(:98)=0, pixels=""` signature. The named mechanism is real, and `-noreset` is what
removes it. (Displays are per-run owned either way; that fix alone was the 22-run/3-fail
intermediate state, not the final one.)

## V4 — assertions not weakened: PASS

`internal/agent/p1303_xvfb_test.go:168-199`, quoted:

```go
if resp.Error == nil {
    t.Fatalf("ui-tree on a WM-less display answered a result: %s", resp.Result)
}
if resp.Error.Code != protocol.ErrPrimitiveUnsupported { ... }
d := resp.Error.Detail
if d["reason"] != protocol.ReasonNoWindowManager { ... }
if d["ran"] != false { ... }
...
// The negative proof: this body is not the empty-success body. A 200
// {count:0} has no error at all, and that is what this test forbids.
if len(resp.Result) != 0 {
    t.Errorf("a refusal carried a result payload: %s", resp.Result)
}
```

501 `PRIMITIVE_UNSUPPORTED` + `reason:NO_WINDOW_MANAGER` + `ran:false` are still required, and a
`200 {count:0}` still trips `:168-170` (`resp.Error == nil` → `Fatalf`). Not weakened by
inspection either: `git diff 1ad1afb 495df34 -- internal/agent/p1303_xvfb_test.go` is **one
hunk**, `@@ -13,42 +13,131 @@ import (` — imports and `startBareXvfb` only. The assertion function
is outside the hunk, byte-identical to the tree the reviewer FAILed.

## V5 — nothing else moved: PASS

`git show --name-only 495df34`:

```
internal/agent/p1303_xvfb_test.go
internal/protocol/messages.go
internal/server/p1301_strict_test.go
```

`--stat`: 3 files, +174/−60. `internal/protocol/messages.go` is +6 lines (the method and its
comment) and nothing else — checked hunk by hunk. `docs/contracts/*` untouched; the §7
`NO_BACKEND` routes are untouched (`internal/agent/uitree.go`, `uitree_linux.go` are not in the
commit). The `p1301_strict_test.go` diff converts both gate tests to route tables (adds the
`/exec` rows, widens the E1 agent's caps from `["input"]` to `["input","exec"]` so the refusal
measured is E1 rather than the generic `CAPABILITY_MISSING` gate); no assertion is dropped —
the 409/502, `feature`, `agent_version`, `missing` and the 200 controls all survive in the loop
bodies.

## Count line

V1 PASS · V2 PASS · V3 PASS · V4 PASS · V5 PASS — 5 PASS / 0 FAIL / 0 UNVERIFIABLE.
Suite 6/6 clean (700 pass / 0 fail / 2 skip each) · Xvfb alone 10/10 pass · exec gate probe 6/6
as specified · reset-window probe 8 configurations as specified · `go vet ./...` exit 0.

## Residuals (not closed by this unit)

1. **WM-present path UNPROVEN-ON-HOST.** Confirmed still labelled and not implied proved:
   `P13-03_WINDOW_TREE.md:108` ("WM-present path: UNPROVEN-ON-HOST"), `P13-04_REVIEW.md:103`,
   `P13-05_CLOSE.md:34`, `P13-05_REGATE.md:158`. No WM binary on this host; out of scope for
   P13-06. The §7.1 claim is quotable as "refusal branch proven on a real X server" — the host
   proof of the WM-present path is not quotable and must stay so labelled.
2. Carried from P13-04 §Residuals 4: `P13-03_WINDOW_TREE.md` says "two real-Xvfb tests"; only
   `TestP1303RealXvfbRefusesNoWindowManager` starts Xvfb. Prose, no behavioural claim. Unchanged.
3. A WM publishing neither `_NET_CLIENT_LIST_STACKING` nor `_NET_CLIENT_LIST` still yields
   `200 {count:0}` (`internal/agent/uitree.go:160-175`) — the unit's stated decision, carried.
