# P13-04 — independent review of the P13 change set (P13-01, P13-02, P13-03)

VERDICT FAIL /opt/data/tmp/probe119/P13-04_REVIEW.md 5d37f9f 695 pass / 1 fail / 2 skip (worst of 8 runs)

Reviewed commits: `7274ec2` (P13-01), `f243257` (P13-02), `b6a32f7` (P13-03). Head at review:
`5d37f9f` (documentation commits above `b6a32f7`; no code above the three). All three are ancestors of head
(`git merge-base --is-ancestor`, exit 0).

## Per-item verdict table — 4 of 5 PASS, 1 FAIL

| # | item | verdict | evidence (committed blobs) |
|---|---|---|---|
| 1 | §5.5 strict decode, 14 rows | **PASS** | `7274ec2:internal/protocol/strict.go:85-120` — the 14 allow-lists, compared element by element against the §5.5 table at `docs/contracts/P10-07_CONTRACT_V3_1.md:312-325`: all 14 match exactly, including the five new rows and the empty list for `stream-frame`. `:143-197` `ParseStrictBytes` — duplicate-key walk (`topLevelKeys` `:199-240`), allow-list membership, then `DisallowUnknownFields` (`:190-193`). `7274ec2:internal/server/strict.go:50-88` `decodeStrict` — one body read (1 MiB cap), refusal before dispatch. 13 agent call sites `ParseCommandStrict` (`internal/agent/agent.go:708,1091,1107,1118,1129,1140`, `agent_stream.go:39,90`, `agent_strict.go:28,37,46,55,63`) = 13/14 body routes; row 5 is a GET and refuses query keys (`api_v1_transfer.go:372-390`). The 14-row table in `P13-01_STRICT.md` matches the code row by row; the five `501 NO_BACKEND` rows are `agent_strict.go:66-84` at that sha (row 13 superseded later by P13-03 — correct at its own sha). |
| 2 | E1 / E2 enforcement | **FAIL** | E1 exists and is selector-conditioned (`7274ec2:internal/server/strict.go:138-180`, `hasCapabilityStrict` `server_api.go:476-483`), E2 correctly excludes pre-1.21 (`strict.go:183-225`, `agentVersionAtLeast` `:227-243`). But **`POST /exec` is not gated at all**: `ExecParams` carries `Context`/`Session` (`messages.go:246-295`) and has **no `Selector()` method** — the nine other param types do (`messages.go:502,545,606,644,662,677,692,705,718`). `preDispatchSelector`/`selectorPresent` type-assert `protocol.SelectorCarrier` (`strict.go:130-137,143-146`), so for exec they return "no selector": E1 does not fire, E2 does not fire, and `ValidateSelector` never runs. Probe (worktree at `b6a32f7`, agent advertising `exec` but not `context.selector`): `200 {"ok":true,"data":{"success":true}}` for 1.19.0, 1.21.0, and 1.21.0+feature cohorts; control `pointer-click` on the same agents → `409 UNSUPPORTED_AGENT`. `P13-01_STRICT.md` row 1 claims `E1 yes / E2 yes` — the cited code does not contain it. |
| 3 | §6 contexts endpoint | **PASS** | `f243257:internal/server/api_v1_contexts.go:27-84` — `input` scope (`:29`), query refusal (`:34`, `strict.go:111-127`, `allowed:[]`), §6 envelope with exactly three keys (`:79-83`); typed re-decode rejects a non-§6 body 502 (`:66-72`). `internal/protocol/contexts.go:95-112` `ContextRow` with `Active *bool` (null + `active_known:false`, never a fabricated zero), `idle_s:-1`/`logon_time:""` set in `internal/agent/contexts.go:124-125,164-165,198-199`. One predicate: `ClassifyContext` `contexts.go:151-179`; `ResolveContext:213-231` reads `row.Addressable`/`row.AddressableReason` and does not recompute. Closed 8-value enum `:52-70`, every value produced (`TestP1302EveryReasonHasAProducer`). Per-context features `agent/contexts.go:151,167-168` (`x11Features(state,…)`, `waylandRefusals()`). Tests exist for every claim cited in `P13-02_CONTEXTS.md` (13 test funcs across protocol/agent/server). |
| 4 | §7 window tree | **PASS** | `b6a32f7:internal/protocol/uitree.go:64-98` — row/result shape; **no `hwnd` field exists on the row type at all**, so it cannot be emitted on any row (§7 C-4). `internal/agent/uitree.go:53-113` — stacking order via `clientList` (`:160-175`, STACKING then CLIENT_LIST), top-level filtering (`:237-253` override-redirect, `popupTypes` `:180-187`, `visible_only` default true at `uitree_linux.go:66-69`), cap `maxTreeWindows=500` (`:27`) applied before row build with `truncated` (`:71-79`), foreground exactly one-or-none (`:93-101`), `iconic` from `WM_STATE` state 3 (`:226-233`), titles untruncated (`:300-308`). |
| 5 | §7.1 refusal | **PASS (code) / proof QUARANTINED** | WM check is ordered before any client-list read: `uitree.go:54-57` calls `wmCheck` and returns on refusal before `clientList` at `:59`; `wmCheck:119-153` refuses absent/zero/dead-window. Envelope `uitree_linux.go:71-85`: `PRIMITIVE_UNSUPPORTED`, `reason:NO_WINDOW_MANAGER`, `ran:false`, `display`, `context`, `tree_source_attempted`, `remediation` (`:18-20`). No `XQueryTree` fallback: the only occurrence in the tree is the remediation string (`grep -rn XQueryTree internal/` → `uitree_linux.go:19-20`). Empty `windows:[]` is reachable only after the WM check passes (`:59-62`, `:103-112`). The code path is PASS; see F2 — the on-host proof is intermittent, so the claim is not quotable as a green gate. |

## F1 — blocking: the `exec` selector is not enforced by E1/E2 (§5.5 row 1)

What the contract requires: §5.5 E1 — "the server refuses before dispatch when the agent's advertised features
lack `context.selector`"; E2 — every context-scoped result must carry `context_ref`. Row 1 of the §5.5 work list
is `POST /exec`, allow-list `…,context,session`.

What the code does: `decodeStrict[protocol.ExecParams]` (`server_api.go:173`) parses fine, then both gates bail out
because `ExecParams` does not implement `SelectorCarrier`. A selector sent to `/exec` is therefore accepted, never
validated for range/conflict (`strict.go:69-76` is also behind the interface), forwarded to the agent, and the
response is passed through without the E2 echo check. On a pre-1.21 agent — the exact population E1 exists to
protect — nothing downstream catches it: the command runs in the agent's own context while the caller believes it
selected `wts:7`. That is the silent fallback §5.4/§5.5 was written to kill, and `P13-01_STRICT.md`'s row-1
`E1 yes / E2 yes` is a claim about code that is not there.

Evidence class E1 (executed). Probe output, worktree at `b6a32f7`:

```
EXEC pre121-no-selector         status=200 body={"ok":true,"data":{"replayed":false,"success":true}}
CLICK pre121-no-selector        status=409 body={"ok":false,"error":{"code":"UNSUPPORTED_AGENT",...}}
EXEC 121-no-selector            status=200 ...
CLICK 121-no-selector           status=409 ...
EXEC 121-with-selector-no-echo  status=200 ...
```

Also note: an agent that lacks the `exec` capability entirely is refused `409 CAPABILITY_MISSING` by the generic
`forwardToAgent` check — a different gate with a different code, which is why the first probe (caps `["input"]`)
did not expose the hole. With `exec` advertised, the selector passes through untouched.

Minimal fix list (not applied — out of scope):
1. `func (p ExecParams) Selector() (*string, *int) { return p.Context, p.Session }` in `internal/protocol/messages.go`.
2. Add `exec` to `TestP1301E1RefusesAgentWithoutSelectorFeature` and `TestP1301E2RefusesMissingContextRef` (both currently use `pointer-click` only, `p1301_strict_test.go:162-212`), so a future regression cannot hide.
3. Re-run the suite and re-gate this item.

## F2 — the §7.1 on-host proof is intermittent; product code is not at fault

Runs at head `5d37f9f`, `go test ./... -count=1`, nothing else of mine running concurrently:

| measurement | result |
|---|---|
| whole-suite runs | **8 run, 4 failed, 4 clean** (50%) |
| every failure | `TestP1303RealXvfbRefusesNoWindowManager` only; 695 pass / 1 fail / 2 skip |
| clean runs | 696 pass / 0 fail / 2 skip |
| single-test runs (`-run TestP1303RealXvfb…`, package alone) | **10 run, 1 failed** |
| `go vet ./...` | exit 0 |

So the parent's premise ("passes every time in isolation") does not hold here: it also fails alone, ~1 in 10.
Every failure message is `probeX11(:N) = 0, want wmAbsent` — state 0 is `wmUnknown`, i.e. "could not ask".

Cause, instrumented in a throwaway worktree (Xvfb stderr captured, connection diagnostics added; the main tree
was not touched): the Xvfb child is **alive at cleanup in every failing run** and its stderr carries only
`_XSERVTransmkdir: Owner of /tmp/.X11-unix should be set to root` — the server did not die. What fails is the
connection: `x11Dial` can return `EOF` (server accepted, then closed during initialisation) and readiness returns
after exactly one 50 ms poll (`after=50.6ms`), i.e. the gate in `p1303_xvfb_test.go:41-52` accepts "a socket
exists and one handshake succeeded", not "the server I started answers a property read". The display space is
`90 + os.Getpid()%8` — eight values (`:30`) — and SIGKILL cleanup (`:37-40`) leaves the socket and
`/tmp/.X<n>-lock` behind: at review time all eight were present (`/tmp/.X11-unix/X90…X97`, locks holding dead
pids). A later run therefore attaches to a display whose socket state it did not create.

Product vs test: **test only.** The product answers honestly in the failing case — `probeX11` returns
`wmUnknown`, which maps to `NO_BACKEND`, never to an empty success; the §7.1 branch itself is proven
independently by `TestP1303NoWindowManagerRefusal` (`p1303_uitree_test.go:414-452`) against the seam, and the
failure never reaches `buildUITree`.

Ruling on quotability: **the §7.1 claim is NOT quotable as a green-gate result.** The branch is proven by the
seam test; the sentence "proved on a real X server" rests on a test that is red in half of whole-suite runs.
Re-gate after the harness is fixed (unique display, cleanup that unlinks the socket, readiness that proves a
property read). Until then, cite §7.1 as "seam-proven, host-proof quarantined".

## Adversarial attempts

1. **Make E1 not fire.** Tried `/exec` with a selector on agents that advertise `exec` but not
   `context.selector` — E1 does not fire (F1, 200 with selector). Also probed the same call on a 1.21 agent that
   advertises the feature and returns no `context_ref`: no E2, 200. Control (`pointer-click`) refuses 409 both
   times, so the probe is discriminating.
2. **Make `addressable` and the selector disagree.** Built a fact set where `ClassifyContext` returns
   `other_user` with `Privileged:false` and read the gate's answer: `ResolveContext` (`contexts.go:213-231`)
   returns the row's own `AddressableReason` and `ContextUnavailableReason` maps it to `OTHER_USER`; there is no
   second predicate in the gate (`agent/contexts.go:377-390`). No drift found. The row and the gate read one
   stored verdict, as claimed.
3. **Break the real-Xvfb negative.** Repeated runs (above) plus connection instrumentation: the failure is in
   the harness's readiness/cleanup, not in the refusal path.

## Residuals

1. **WM-present path UNPROVEN-ON-HOST** — `P13-03_WINDOW_TREE.md` says so explicitly ("WM-present path:
   UNPROVEN-ON-HOST"), and no live WM binary exists here. Confirmed: it is labelled, not implied as proved.
2. `P13-03_WINDOW_TREE.md` says "two real-Xvfb tests"; only one test starts Xvfb
   (`TestP1303RealXvfbRefusesNoWindowManager`). `TestP1303NonX11ContextKeepsRefusing` is a non-X11 refusal test.
   Prose inaccuracy, no behavioural claim attached.
3. A WM that publishes neither `_NET_CLIENT_LIST_STACKING` nor `_NET_CLIENT_LIST` yields `200 {count:0}`
   (`uitree.go:160-175`). That is the unit's stated decision (WM check passed = successful enumeration of zero),
   but such a WM is non-compliant and the answer is indistinguishable from an empty desktop — the exact
   distinction §7.1 protects. Recorded as a residual, not a blocking row.
4. `P13-01_STRICT.md` row 13 (`ui-tree` → `501 NO_BACKEND`) is accurate at `7274ec2` and superseded at head by
   P13-03. Not a defect; noted so a reader of the table at head is not surprised.

SHAPE parallel internal/server/api_v1_contexts.go:handleV1Contexts ↔ internal/server/api_v1_strict.go:v1StrictForward
— the same forward → agent-error → HTTP-status mapping block is written twice (and a third time in
`api_v1_transfer.go` handlers). No shared helper; the P13-02 unit adds a copy of it. Not the unit's killing
defect, but it is the code the unit adds and it is a parallel site, so it is recorded.

## Evidence discipline

- Every line range above was read with `git show <sha>:<path>`; none is quoted from a child's prose.
- Suite counts are from `go test ./... -count=1 -json` at `5d37f9f` (695/1/2 on the failing run; 696/0/2 on clean
  runs of the same tree).
- The E1/E2 probe and the Xvfb instrumentation ran in a separate worktree at `b6a32f7`; `git status --porcelain`
  in `/opt/data/projects/PROBE_UPGRADE/v119wt` showed no changes from this review before the close commit.
