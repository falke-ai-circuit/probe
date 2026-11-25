# P21-01 — CLOSE

**Commit:** `6f94a3e` (unit commit: predicate, surface, refusals, tests, artefact)
**Unit:** P21-01 · workflow `wf_probe1` · branch `r119-build`
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: 6f94a3e`
**Close record:** committed as a document-only commit that binds `6f94a3e`; a header cannot name the sha of the commit that contains it, so the record trails the unit commit by one.
**Scope:** `internal/server/` only — `agent_visibility.go` (predicate + refusal derivation), `registry.go` (three derived fields), `server_api.go`, `api_v1_fs_transfer.go`, `credential_api.go`, `tunnel.go`, `api_v1_phase4.go`, `p2101_liveness_test.go`. No `internal/web`.

## Deliverable

1. `AgentDispatchability(agentID) (dispatchable bool, source string, reason string)` in
   `internal/server/agent_visibility.go` — conn-map → relay channel → registry-only,
   one answer for the whole server.
2. `/api/v1/agents` (and single-agent GET) rows carry `dispatchable`,
   `liveness_source` (`ws`|`relay`|`registry-only`), `dispatch_error`. Additive only.
3. Every refusal site that answered `AGENT_UNAVAILABLE` derives from the predicate:
   503 + the predicate's sentence when not dispatchable, 502 `AGENT_ERROR` with the
   agent's own error when dispatchable. `forwardToAgentWithTimeout`, the tunnel and
   mode handlers state the same reason.
4. Six tests in `p2101_liveness_test.go` pin the invariant in HTTP terms: red at
   `e6d4bfd`, green after. Includes both directions of acceptance 4 and the
   "dispatchable never answers AGENT_UNAVAILABLE" assertion.

Census (my own, at `e6d4bfd`), with a disposition per site: `P21-01_LIVENESS.md` §4.
Corrections to the brief's census are stated there: `server_api.go` has one
`AGENT_UNAVAILABLE` occurrence, not two; `server_token.go:199` and `tunnel.go:227`
emit a plain "not connected" sentence; `tunnel.go:227` and `api_v1_phase4.go:82`
were not in the inherited list and are converted.

## Suite

```
GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1   → exit 0
773 pass (all --- PASS incl. subtests) / 0 fail / 2 skip
baseline at HEAD: 767 / 0 / 2   (delta = the 6 new tests)
load 16.44 15.93 15.10 at start, 16.29 16.35 15.39 at end
```

## Open item

P5 (relay dispatch routing) is untouched by design: an agent held by a relay but not
in the conn-map now reports `dispatchable=false` with the relay named, instead of a
green row. Site 14 (`server_token.go:199`) needs the predicate if P7 routes token
rotation across relays. `AGENT_ERROR` should be added to the OpenAPI error list
before release.

## Evidence files

`/opt/data/tmp/probe119/`: `P21-01_LIVENESS.md`, `P21-01_red_before.log` (six
failures at `e6d4bfd`), `P21-01_suite2.log` (whole suite, exit 0),
`P21-01_suite_v.log` (verbose, counted), `P21-01_load.txt` (load at both ends).
