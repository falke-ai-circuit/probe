# P21-03 — CLOSE

**Commit:** `055b7fa` (unit commit: peer_hello, relay status, topology uplink, tests, artefact — 12 files)
**Unit:** P21-03 · workflow `wf_probe1` · branch `r119-build`
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: 055b7fa`
**Close record:** this document is a later document-only commit; a header cannot name the sha of the commit that contains it. The verdict binds to `055b7fa`, the tree the code and tests were read at.
**Scope:** `internal/relay/` (status storage + `GET /api/v1/relay/status`), `internal/server/relay_ws.go` (peer_hello on registration), `internal/server/uplink.go` + `api_v1_phase4.go` (topology uplink edge, bounded read, last-known retention), tests, and the `--relay-status` / `--advertised-addr` flags in `cmd/probe-server` and `cmd/probe serve`. No `internal/web`, no new dependencies.

## Delivered

1. On `relay_register` success the **server** writes one framed `peer_hello` on the same socket, channel 0: `{"type":"peer_hello","peer":{id,name,version,listen_addr}}`. `listen_addr` is dialable (host name + port), never the bind string; `--advertised-addr` overrides.
2. The **relay** stores it and serves `GET /api/v1/relay/status` on its own listen port: relay_id, max_agents, forward_policy, upstream{url,port,state,connected_at,peer,last_error}, downstream{listen,agents,channels}. `forward_policy` is `""` and `agents[]` are channel descriptors — both stated reasons, not invented values.
3. The **server** reads `127.0.0.1:<relay listen>` with a 300 ms bound and merges an `uplink` edge into `/api/v1/topology`, peer fields populated when up.
4. On loss the edge stays present with `state:"down"`, non-empty `state_reason`, and the cached last-known peer + `connected_at`. No uplink configured ⇒ no edge (absence ≠ down).
5. `/api/v1/topology` answered in 300.5 ms against a relay stalling 2 s — a test, not a claim.

## Evidence

- Wire frame quoted in `P21-03_PEERHELLO.md` §2 (observed in `TestP2103_PeerHelloAtRelayRegistration`; magic 0x42, channel 0, `listen_addr` = `<host>:7700`).
- Relay status body quoted in §3/§4 (up and down, peer retained).
- Red before: `P21-03_red_before.log` — relay tests 404 at `4be64ba`, server package build-failed on the missing API. Green after: `P21-03_suite.log`.
- Suite: `GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1 -v` → exit 0, **780 pass / 0 fail / 2 skip**; baseline 773 (HEAD `4be64ba`) + 7 new tests. Load 20.23/20.02/19.19 → 17.42/18.23/18.66.
- Binaries exercised: relay process code on a loopback listener (ephemeral port) and the server mux via `httptest`. **Radxa relay → OVH link: UNPROVEN-ON-HOST**; no Radxa host touched.

## Open item

The downstream server must be started with `PROBE_RELAY_STATUS=127.0.0.1:7701` (or `--relay-status`) for the uplink edge to exist; without it topology is silent by design. P3 consumes the new edge fields. Relay-side `agents[]` cannot name agents until the relay parses `AgentInfo`, which it deliberately does not today.

## Collision note

The worktree carried transient untracked files from another writer (`relay_uplink.go`, `protocol/peer_hello.go`, `zz_tmp_capture_test.go`). The unit commit was inspected with `git diff --cached --name-status` and contains exactly the 12 files above; no foreign file was staged.
