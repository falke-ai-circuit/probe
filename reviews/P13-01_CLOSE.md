# P13-01 — CLOSE

Unit: P13-01 · workflow wf_probe1 · branch `r119-build`

**Commit:** `7274ec2` (repo `/opt/data/projects/PROBE_UPGRADE/v119wt`, branch `r119-build`, pushed — `origin/r119-build` == `7274ec2`, tree clean). Base `1e4e14f`. The close documents were committed above this sha afterwards, so the gate was run at `7274ec2`; the docs commit changes no code and is deliberately not named (a close cannot name the commit that carries it).
**Review:** reviewed_by: `PENDING` (no independent verdict rendered — a review leg is owed and armed) · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `7274ec2`
**Scope:** contract §5.5 phase-1 substrate only — strict decode, the five enforcements, selector validation, stream-stop `STREAM_ID_REQUIRED`. No context entry, no forking/setuid, no platform backends, contract document untouched. Artefact `/opt/data/tmp/probe119/P13-01_STRICT.md` (6851 B, sha256 `0de88720eac93708`).

## Parent verification (orchestrator, this close)

Measured independently at `7274ec2`, not quoted from the child:

- Suite `go test ./... -count=1 -json` (go1.22.5) → **673 pass incl. subtests / 0 fail / 2 skip**, identical to the child's count; baseline `1e4e14f` was 627/0/2, so the +46 is this unit's tests.
- Enforcements exist in code and are called, not merely described: `decodeStrict[T]` (`internal/server/strict.go:50`), `ParseStrictBytes`/`ParseCommandStrict`/`ValidateSelector` (`internal/protocol/strict.go:143,192,341`), `ReasonDuplicateField = "DUPLICATE_FIELD"` (`:22`), `preDispatchSelector` (`internal/server/strict.go:122`, called at `internal/server/api_v1_transfer.go:284,419`), `assertContextRef` (`internal/server/strict.go:168`, called at `:310,431`).
- E1 semantics read at the body: a selector-bearing call to an agent without `context.selector` writes 409 `UNSUPPORTED_AGENT` with `detail{feature, agent_version, requested, ran:false, remediation}` and returns false **before** forwarding. E2 writes 502 `PROTOCOL_VIOLATION` only when the agent is at least 1.21 and the result carries no `context_ref`; an agent-error response is excluded, so nothing is invented for a pre-1.21 agent.
- `git show --stat 7274ec2` → 14 files, +1588/−72, matching the scope: strict helpers, one shared allow-list, the five new routes, and the stream-stop fixture change the artefact declares as a deviation.

## Implemented / not implemented

Implemented: `protocol.ParseStrictBytes` / `ParseCommandStrict` (duplicate-key refusal, per-route
allow-list, `DisallowUnknownFields`), one shared allow-list table for server and agent, server
`decodeStrict[T]` wired into the 9 existing selector-bearing handlers and 5 new routes
(pointer-move/scroll/drag, ui-tree, ui-window), E1 pre-dispatch gate, E2 post-dispatch assertion,
§5.1 conflict/range validation, stream-stop `STREAM_ID_REQUIRED` + `CONTEXT_NOT_ACCEPTED`.

Not implemented (out of scope by brief): context entry, forking/setuid, platform backends for the
five new primitives (they refuse `501 NO_BACKEND`), contract document untouched.

Suite: `go test ./... -count=1` → **673 pass / 0 fail / 2 skip** (baseline 627/0/2).

Open item: rows 6, 8, 9, 13, 14 have strict decode + gates but no backend yet — `501
PRIMITIVE_UNSUPPORTED/NO_BACKEND`, stated in the artefact, not a silent success.
