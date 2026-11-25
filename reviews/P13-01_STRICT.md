# P13-01 — contract §5.5 phase-1 enforcement substrate (E1, E2, E3a, E3b, E3c)

Repo: `/opt/data/projects/PROBE_UPGRADE/v119wt`, branch `r119-build`
Base: `1e4e14f` · Code commit: `7274ec2` (§5.5 substrate + E2-on-exec assertion, one commit)
Contract read: `docs/contracts/P10-07_CONTRACT_V3_1.md` §5.1, §5.4, §5.5 (14-row work list), §5.6, §11.

## Per-item verdicts

| item | verdict | implementation | test that fails if removed |
|---|---|---|---|
| E3a server-side `decodeStrict[T](w, r, allowed)` | **PASS** | `internal/server/strict.go`: reads body once (1 MiB cap), duplicate-key walk, per-route allow-list, then `DisallowUnknownFields`; any failure → `400 INVALID_PARAMS`, `detail.ran:false`, before `forwardToAgent` | `TestP1301All14RoutesRefuseUnknownKey` (14 rows + row 5 query) |
| E3b agent-side `ParseCommandStrict[T](env, allowed)` | **PASS** | `internal/protocol/strict.go`; 14 agent call sites now strict (exec, capture, stream begin/end, 5 new primitives, click, key, combo, text, stream-stop) | `TestParseCommandStrictRefusesSmuggledField` |
| E3c duplicate-key refusal | **PASS** | inside `ParseStrictBytes`, token walk before the struct decode; `reason:"DUPLICATE_FIELD"`, `detail.field` | `TestStrictDecodeRefusesDuplicateField`, `TestP1301DuplicateSelectorRefused` |
| E1 pre-dispatch gate | **PASS** | `preDispatchSelector`: selector present + `context.selector` not advertised (strict check, not the empty-list-equals-all rule) → `409 UNSUPPORTED_AGENT`, `detail{feature, agent_version, ran:false}` | `TestP1301E1RefusesAgentWithoutSelectorFeature` (with selector → 409; without → 200 control) |
| E2 post-dispatch assertion | **PASS** | `assertContextRef`: context-scoped result without `context_ref` from a 1.21+ agent → `502 PROTOCOL_VIOLATION`; agent-error responses excluded (nothing ran) | `TestP1301E2RefusesMissingContextRef` (missing → 502; present → 200 control) |
| §5.1 selector validation | **PASS** | `protocol.ValidateSelector` at parse time (server) and in the agent helper: `OUT_OF_RANGE` (0..65535) then `SELECTOR_CONFLICT` | `TestP1301SelectorValidation`, `TestInvalidParamRefusalProducers` |

E1 is selector-conditioned on purpose: the contract's invariant is "absent selector = today's
behaviour byte-for-byte", so an agent that advertises nothing keeps answering selector-free calls
(`TestP1301NoSelectorMeansTodayBehaviour`). stream-stop is the one route that refuses **every**
selector (`CONTEXT_NOT_ACCEPTED`), so E1/E2 do not apply to it.

## The §5.5 table — honest row status (13/14 body routes + 1 GET)

| # | route | E3a | E3b | E1 | E2 | execution |
|---|---|---|---|---|---|---|
| 1 | `POST /exec` | yes | yes | yes | yes | unchanged |
| 2 | `POST /capture` | yes | yes | yes | yes | unchanged (P6 backends later) |
| 3 | `POST /stream-start` | yes | yes | yes | yes | unchanged |
| 4 | `POST /stream-stop` | yes | yes | n/a (selector refused) | n/a | `STREAM_ID_REQUIRED` added |
| 5 | `GET /stream-frame` | yes (query keys refused) | n/a (no body) | n/a | n/a | unchanged |
| 6 | `POST /pointer-move` | yes | yes | yes | yes | **501 NO_BACKEND** |
| 7 | `POST /pointer-click` | yes | yes | yes | yes | unchanged |
| 8 | `POST /pointer-scroll` | yes | yes | yes | yes | **501 NO_BACKEND** |
| 9 | `POST /pointer-drag` | yes | yes | yes | yes | **501 NO_BACKEND** |
| 10 | `POST /key-press` | yes | yes | yes | yes | unchanged |
| 11 | `POST /key-combo` | yes | yes | yes | yes | unchanged |
| 12 | `POST /text-input` | yes | yes | yes | yes | unchanged |
| 13 | `POST /ui-tree` | yes | yes | yes | yes | **501 NO_BACKEND** |
| 14 | `POST /ui-window` | yes | yes | yes | yes | **501 NO_BACKEND** |

Rows 6/8/9/13/14 are new routes: registered, strictly decoded, gated, forwarded as new wire
commands. Their platform backends are contract phases 3–5 and out of this unit's scope, so after a
clean parse the agent answers `PRIMITIVE_UNSUPPORTED` / `reason:"NO_BACKEND"` (501) — no fabricated
success. **Not claimed: 14/14 execution.** Strict decode is 14/14; agent-side enforcement is 13/14
(row 5 has no body).

## Prove-the-negative (smuggling primitive)

Pre-fix tree `1e4e14f` (throwaway worktree), body `{"context":"x11::0","context":"wts:7","x":1,"y":2}`
on `/pointer-click`:

```
PRE-FIX forwarded pointer_click params: {"x":1,"y":2}
PRE-FIX status=200 body={"ok":true,"data":{"success":true}}
```

Both selector values vanished (unknown to `InputParams` at that commit) and the call was a success:
a caller could believe it selected a context while nothing was selected or audited.

At head, the same body:

```
400 INVALID_PARAMS reason=DUPLICATE_FIELD detail={field:"context", ran:false}
```

Unknown top-level keys are likewise 400 on all 14 rows (tested per row, including the GET's query).

## Closed enums

`protocol.InvalidParamReasons` pins the §5.5 enum verbatim (11) and `ContextUnavailableReasons`
pins §5.4 (9); both asserted by test. Produced by this unit (7, each with its own producer test):
`UNKNOWN_FIELD · TYPE_MISMATCH · DUPLICATE_FIELD · OUT_OF_RANGE · SELECTOR_CONFLICT ·
CONTEXT_NOT_ACCEPTED · STREAM_ID_REQUIRED`.
Declared with no producer: `MISSING_FIELD`, `DETACH_WITH_CONTEXT_UNSUPPORTED`, `REGION_TOO_LARGE`,
`ALIAS_KIND_NOT_APPLICABLE`, and all 9 `CONTEXT_UNAVAILABLE` reasons — no context resolution exists
in this unit, so nothing emits them and nothing pretends to.

## Suite — `/tmp/go/bin/go test ./... -count=1` at head

```
673 pass (incl. subtests) / 0 fail / 2 skip
```

Baseline **627 / 0 / 2**; delta +46 is this unit's new tests/subtests. Logs:
`P13-01_suite_head2.log` (final), `P13-01_suite_pre.log` (mid-work, 1 self-inflicted build break).

## Deviation from the brief, stated

One pre-existing test fixture changed: `TestV1_StreamStopAgentNotConnected` sent an empty
stream-stop body, which is now a `STREAM_ID_REQUIRED` refusal before dispatch. The fixture now
sends a real `stream_id` so it still exercises agent-not-connected; the contract break is intended
(OD-9 class).

## Residuals / open items

1. Rows 6, 8, 9, 13, 14 refuse `501 NO_BACKEND`: backends are phases 3–5.
2. A selector is validated, gated and echoed-checked but **not used** to enter a context — no
   context entry in scope. A 1.21+ agent that advertises the feature and ignores the selector is
   caught by E2 (missing `context_ref`), not by execution.
3. `KIND_NOT_APPLICABLE` (session alias on a non-wts kind) needs context enumeration; unreachable
   here.
4. Capture/exec keys (`window`, `rect`, `format`, `hash_only`, `count`, `window`, `focus`) are now
   accepted by the allow-list and carried in the structs, but the backends that honour them are
   phase 6/5 — the silent-drop defect is bounded to execution, not to the wire.
5. Phase 7 hygiene: the remaining ~60 routes keep non-strict decoding by contract.
