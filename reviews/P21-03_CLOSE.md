# P21-03 — CLOSE

**Commit:** `055b7fa` (unit commit: peer_hello, relay status, topology uplink, tests, artefact — 12 files)
**Unit:** P21-03 · workflow `wf_probe1` · branch `r119-build`
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: 055b7fae894ac2c35b3ad74cc9e2e6e29d95a025`
**Close record:** committed as a document-only commit that binds `055b7fa`; a header cannot name the sha of the commit that contains it, so the record trails the unit commit by one.
**Scope:** `internal/relay/` (status storage + `GET /api/v1/relay/status`), `internal/server/relay_ws.go` (peer_hello on registration), `internal/server/uplink.go` + `api_v1_phase4.go` (topology uplink edge, bounded read, last-known retention), tests, and the `--relay-status` / `--advertised-addr` flags in `cmd/probe-server` and `cmd/probe serve`. No `internal/web`, no new dependencies.

## Delivered

1. On `relay_register` success the **server** writes one framed `peer_hello` on the same socket, channel 0: `{"type":"peer_hello","peer":{id,name,version,listen_addr}}`. `listen_addr` is dialable (host name + port), never the bind string; `--advertised-addr` overrides.
2. The **relay** stores it and serves `GET /api/v1/relay/status` on its own listen port: relay_id, max_agents, forward_policy, upstream{url,port,state,connected_at,peer,last_error}, downstream{listen,agents,channels}. `forward_policy` is `""` and `agents[]` are channel descriptors — both stated reasons, not invented values.
3. The **server** reads `127.0.0.1:<relay listen>` with a 300 ms bound and merges an `uplink` edge into `/api/v1/topology`, peer fields populated when up.
4. On loss the edge stays present with `state:"down"`, non-empty `state_reason`, and the cached last-known peer + `connected_at`. No uplink configured ⇒ no edge (absence ≠ down).
5. `/api/v1/topology` answered in 301.289 ms against a relay stalling 2 s (probe timeout 300 ms), edge down with a reason — a test, not a claim.

## Evidence

- Wire frame quoted in `P21-03_PEERHELLO.md` §2 (observed in `TestP2103_PeerHelloAtRelayRegistration`; magic 0x42, channel 0, `listen_addr` = `<host>:7700`).
- Relay status body quoted in §3/§4 (up and down, peer retained).
- Red before: `P21-03_red_before.log` — behavioural red at `4be64ba` (tests copied into a detached HEAD worktree with a test-only no-op shim): relay status 404, no `peer_hello` frame on the wire, no `uplink` edge in either state; 6 FAIL / 1 PASS. Green after: `P21-03_green.log`, `P21-03_suite.log`.
- Suite: `GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1` → exit 0, **780 test-level pass / 0 fail / 2 test skip** (+5 packages with no test files); HEAD baseline `4be64ba` 773/0/2 → delta 7 = the new P21-03 tests. Load 17.76/18.27/18.66 at end. Toolchain go1.25.12 from the cached toolchain module (`GOTOOLCHAIN=local`, vendor).
- Binaries exercised: relay process code on a loopback listener (ephemeral port) and the server mux via `httptest`. **Radxa relay → OVH link: UNPROVEN-ON-HOST**; no Radxa host touched.

## Open item

The downstream server must be started with `PROBE_RELAY_STATUS=127.0.0.1:7701` (or `--relay-status`) for the uplink edge to exist; without it topology is silent by design. P3 consumes the new edge fields. Relay-side `agents[]` cannot name agents until the relay parses `AgentInfo`, which it deliberately does not today.

## Collision note

The worktree was written concurrently by more than one hand. Transient untracked files appeared
(`relay_uplink.go`, `protocol/peer_hello.go`) — competing drafts of the same feature — plus a
temporary capture test that was deleted. The canonical implementation is `internal/server/uplink.go`
plus `internal/relay/status.go`. The unit commit was inspected with `git diff --cached --name-only`
and contains exactly the 12 named files; no foreign file was staged.
