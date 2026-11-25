# P21-01 — one liveness truth: a listed agent is reachable or explains why not

**Unit:** P21-01 (workflow `wf_probe1`) · branch `r119-build`
**Repo:** `/opt/data/projects/PROBE_UPGRADE/v119wt` (worktree of `falke-ai-circuit/probe`)
**HEAD before this unit:** `e6d4bfd`
**Mode:** SURGEON (existing code, minimal diff)
**Scope:** `internal/server/` only — predicate, agents handler, refusal sites, tests. No `internal/web`.

---

## 1. The defect, restated in code terms

`/api/v1/agents` answered from the registry (`Registry.ListAgents`). Every dispatch
path answered from the conn-map (`s.conns`). Two surfaces, two truths, no
reconciliation: the list could report `status=active, health_score=0.95` for an id
that `exec` refused with `AGENT_UNAVAILABLE`. P12-03 had already made the *health*
row honest (`stale`/`stale_reason` from the conn-map), but it did not give the
question a single home — the list handler and each refusal site still built their
own answer.

## 2. The predicate — one home

`internal/server/agent_visibility.go`

```go
func (s *Server) AgentDispatchability(agentID string) (dispatchable bool, source string, reason string)
```

Read order, as specified:

1. **conn-map** (`s.conns`), the same map every dispatch path reads.
   `*virtualConn` → `(true, "relay", "")`; a direct conn **with** its per-agent write
   mutex → `(true, "ws", "")`.
2. **relay channel** — a `virtualConn` present in a live `relaySession.channels`
   that has not reached the conn-map (the window between `channel_open` and the
   agent's own registration). Reported as `source="relay"`, `dispatchable=false`,
   with the relay id named in the reason.
3. **registry-only** — `registry.GetHealth`: `(false, "registry-only", reason)`.
   `reason` is the sentence, e.g. `registered (status=active), no live ws
   connection, last seen 4m ago` (age humanised: `4s`/`4m`/`3h`/`2d`).

A transport present **without** a write mutex is refused: forwarding would either
race another writer or deadlock (Go mutexes are not reentrant). `dispatchable=true`
is therefore never a promise the dispatch path cannot keep.

Both consumers derive from it:

* `handleV1ListAgents` / `handleV1GetAgent` → `decorateAgentLiveness` →
  `AgentDispatchability` (the list decorator no longer holds `s.mu` across the call;
  the predicate takes the lock itself, and Go locks do not nest).
* every refusal site below → `dispatchRefusal` / `writeDispatchRefusal` →
  `AgentDispatchability`.

`dispatchRefusal` splits the two cases that used to collapse into one code:

| predicate | status | code | message |
|---|---|---|---|
| `dispatchable=false` | 503 | `AGENT_UNAVAILABLE` | the predicate's reason |
| `dispatchable=true` | 502 | `AGENT_ERROR` | the agent's / link's own error, passed through |

The second row is the invariant in code: **an agent the endpoint lists as
dispatchable cannot answer `AGENT_UNAVAILABLE`**, because that code is only
produced on the `dispatchable=false` branch.

## 3. Surface on `/api/v1/agents` (additive only)

`AgentRecord` gained three derived fields, stamped on every read path that returns
a row (list and single-agent):

```go
Dispatchable   bool   `json:"dispatchable"`
LivenessSource string `json:"liveness_source"`          // "ws" | "relay" | "registry-only"
DispatchError  string `json:"dispatch_error,omitempty"` // present exactly when dispatchable=false
```

No existing field renamed, retyped or dropped. `stale`/`stale_reason` from P12-03
are unchanged in meaning: `stale=true, stale_reason=no_transport` whenever the
predicate says not dispatchable. The three fields are derived, never persisted —
`dispatchable` is not a fact about a previous server process, so they carry no
`omitempty` on the two that always answer.

## 4. Census — re-taken at HEAD `e6d4bfd` before any edit

The spec's line citations are stale, and the parent census does not match this
tree either. `grep -rn "AGENT_UNAVAILABLE" --include=*.go .` at `e6d4bfd`:

| # | Site at HEAD | Emits | Disposition after P21-01 |
|---|---|---|---|
| 1 | `credential_api.go:190` | 503 `AGENT_UNAVAILABLE` on credential delivery | **derives** — `dispatchRefusal`; 503+reason or 502 `AGENT_ERROR` |
| 2 | `server_api.go:369` | `code := "AGENT_UNAVAILABLE"` on the exec surface | **derives** — capability branch kept, else `dispatchRefusal(…,"exec",…)` |
| 3 | `api_v1_fs_transfer.go:196` | 503 on upload stat | **derives** — `writeDispatchRefusal(…,"fs-upload-stat")` |
| 4 | `api_v1_fs_transfer.go:216` | 503 on post-write stat | **derives** — `…"fs-upload-stat-after-write"` |
| 5 | `api_v1_fs_transfer.go:247` | 503 on whole-file hash | **derives** — `…"fs-upload-hash"` |
| 6 | `api_v1_fs_transfer.go:265` | 503 "finalize rename failed" | **derives** — `…"fs-upload-finalize"`, prefix preserved in the wrapped error |
| 7 | `api_v1_fs_transfer.go:347` | 503 on download stat | **derives** — `…"fs-download-stat"` |
| 8 | `api_v1_fs_transfer.go:396` | 503 on range read | **derives** — `…"fs-download-read"` |

Sites that refused in the same shape **without** the code (not in the parent's
list, found by `grep -rn "not connected"`):

| # | Site at HEAD | Emits | Disposition |
|---|---|---|---|
| 9 | `server_api.go:839` (`forwardToAgentWithTimeout` pre-send) | `ForwardRefusal{AGENT_NOT_CONNECTED, "agent X not connected"}` — the source every exec/fs path inherits | **derives** — message is the predicate's reason; if the predicate says dispatchable the code is `AGENT_DELIVERY_FAILED`, never not-connected |
| 10 | `tunnel.go:227` | 503 plain `"agent %s not connected"` | **derives** — predicate reason; status mapping unchanged (`SERVICE_UNAVAILABLE` via `errorCodeFromStatus`, which never yields `AGENT_UNAVAILABLE`) |
| 11 | `api_v1_phase4.go:82` (mode GET) | 404 plain `"agent not connected"` | **derives** — predicate reason for a registered agent; 404 status kept |

Sites deliberately **not** converted, each justified:

| # | Site | Why it stays |
|---|---|---|
| 12 | `credential_api.go:207` (`sendCredentialDelivery`) | transport primitive: returns the write error, decides no client code. Its text now lands in `detail.error` of a predicate-derived refusal (site 1). |
| 13 | `agent_visibility.go:96` (`pingAgent`) | background prober: answers "is there a channel to ping", logs its failure, and is not reachable from any HTTP response. |
| 14 | `server_token.go:199` (`InitiateTokenRotation`) | token rotation is not a control dispatch: the caller is the rotation goroutine and the error is a log line. If P7 routes rotation through a predicate, this becomes site 15. |

Documentation-only: `openapi.go:45` carries `"agent vegas-c2022 not connected"` as an
*example string*. No code path; left as-is.

Corrections to the brief's census, in writing:

* `server_api.go` has **one** `AGENT_UNAVAILABLE` occurrence at `e6d4bfd` (`:369`), not
  two. The spec's `:466` no longer exists; nothing else in that file manufactures
  the code. The v1 legacy-wrapper translator (`api_v1.go` `errorCodeFromStatus`)
  maps 503 → `SERVICE_UNAVAILABLE`, not `AGENT_UNAVAILABLE`, so it is not a factory.
* `server_token.go:199` and `tunnel.go:227` both exist but emit a plain "not
  connected" sentence, not the code. Counted above as sites 14 and 10.

## 5. The invariant, as a test in the API's terms

`internal/server/p2101_liveness_test.go` — every assertion reads the HTTP response
(decoded as JSON objects), not the struct, so the invariant is pinned in the shape
a client receives.

| Test | Acceptance | What it asserts |
|---|---|---|
| `TestP2101RegistryOnlyRowIsNotGreen` | 4(a) | registry row, no conn-map entry → `status=active`, `dispatchable=false`, `liveness_source=registry-only`, `dispatch_error` names "no live ws connection". Registry/conn-map state constructed directly — no live agent needed, and that is the honest smallest level |
| `TestP2101LiveWSRowIsDispatchable` | 4(b) | live conn + write mutex → `dispatchable=true`, `liveness_source=ws`, no `dispatch_error` |
| `TestP2101RelayChannelIsNamedNotIgnored` | 1 | relay channel present, agent not yet in conn-map → `liveness_source=relay`, `dispatchable=false`, reason names `relay-a` |
| `TestP2101InvariantHoldsOnEveryRow` | 4 | four rows (live, ghost, relayed, unregistered); for every row the three fields exist, source ∈ {ws, relay, registry-only}, and `status=active` ⇒ `dispatchable` ∨ non-empty `dispatch_error` |
| `TestP2101DispatchableAgentNeverAnsweredAgentUnavailable` | 3 + third acceptance bullet | the endpoint says `dispatchable=true`; exec on a failing link answers 502 `AGENT_ERROR` carrying "simulated transport failure" — asserted, not argued |
| `TestP2101RefusalStatesTheReason` | 3 | registry-only agent → 503 `AGENT_UNAVAILABLE` whose message is the predicate sentence, not `"agent X not connected"` |

### Red before / green after

Red at HEAD `e6d4bfd` (test file copied into a clean worktree of `e6d4bfd`,
`P21-01_red_before.log`, exit 1) — six failures, each naming the missing truth:

```
--- FAIL: TestP2101RegistryOnlyRowIsNotGreen
    row carries no dispatchable field: map[... status:active stale:true ...]
--- FAIL: TestP2101LiveWSRowIsDispatchable
    live ws agent reported dispatchable=false
--- FAIL: TestP2101RelayChannelIsNamedNotIgnored
    liveness_source = "", want relay
--- FAIL: TestP2101InvariantHoldsOnEveryRow
    p2101-relayed: liveness fields missing
--- FAIL: TestP2101DispatchableAgentNeverAnsweredAgentUnavailable
    precondition: row not dispatchable
--- FAIL: TestP2101RefusalStatesTheReason
    refusal states no reason: "agent p2101-absent not connected"
```

Green after (`P21-01_suite_v.log`):

```
--- PASS: TestP2101RegistryOnlyRowIsNotGreen
--- PASS: TestP2101LiveWSRowIsDispatchable
--- PASS: TestP2101RelayChannelIsNamedNotIgnored
--- PASS: TestP2101InvariantHoldsOnEveryRow
--- PASS: TestP2101DispatchableAgentNeverAnsweredAgentUnavailable
--- PASS: TestP2101RefusalStatesTheReason
```

## 6. Whole suite

```
GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1   → exit 0
```

`./internal/server` 106s; `./cmd/probe` 22.8s.

| | pass | fail | skip |
|---|---|---|---|
| baseline at HEAD (per brief) | 767 | 0 | 2 |
| this unit (all `--- PASS` incl. subtests) | **773** | **0** | **2** |

773 = 767 baseline + the 6 new P21-01 tests; top-level `--- PASS` count is 643
because `-v` indents subtests.

**Load during the run:** 16.44 / 15.93 / 15.10 (1/5/15) before, 16.29 / 16.35 / 15.39
after, on a host at 83 days uptime. The suite has no timing-sensitive failure here;
`./internal/server` ran 86s → 106s across two runs under that load.

## 7. Design decisions worth a reviewer's eye

1. **A relay channel that is not in the conn-map is reported `dispatchable=false`.**
   The brief allows `liveness_source="relay"` "when a relay channel genuinely
   exists" — it does, and it is named. But the dispatch path reads the conn-map, so
   promising `dispatchable=true` there would be the same lie one layer down. The
   window is short (between `channel_open` and the agent's registration) and the
   reason states exactly that.
2. **`AGENT_ERROR` (502) is a new observable code** on the exec/fs/credential
   surfaces, replacing `AGENT_UNAVAILABLE` (503) for a *live* agent whose dispatch
   failed. That is the point of requirement 3 — a 503 would claim absence — but it
   is a wire-visible change for any client that matched on 503 + `AGENT_UNAVAILABLE`
   as "retry later". Clients that match on the two remaining `dispatchable=false`
   paths are unaffected.
3. **Not implementing P5.** `exec` still routes only through the conn-map; a relayed
   agent reaches it because the relay's `virtualConn` *is* its conn-map entry
   (`relay_ws.go:207`). No new routing was added, per requirement 5.
4. **Registry format and conn-map persistence untouched.** All three fields are
   derived at read time; `registry.json` did not change shape, and the conn-map is
   still in-memory only.

## 8. Open items

* **P5 (control plane across relays)** is untouched by design. If a future relay
  holds an agent without publishing a conn-map entry, `dispatchable` will read
  false with the relay named — correct, but the operator still cannot exec it until
  P5 lands.
* Site 14 (`server_token.go:199`) will need the predicate if P7 routes token
  rotation through relay channels; today it only rotates over direct conns.
* `AGENT_ERROR` is worth documenting in `openapi.go` before the next release so a
  client author sees the new code; this unit did not touch the spec file.
