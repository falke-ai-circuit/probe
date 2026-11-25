# P21-03 — `peer_hello`: the uplink learns the peer, and a dead link stays visible

**Unit:** P21-03 · workflow `wf_probe1` · branch `r119-build`
**Commit:** `055b7fa` (full `055b7fae894ac2c35b3ad74cc9e2e6e29d95a025`), pushed to `origin/r119-build`, tree clean.
**Scope:** `internal/relay/` (relay binary), `internal/server/relay_ws.go` + topology builder (server binary), `internal/server/uplink.go`, tests, and the two binary flags that point the server at its relay (`cmd/probe-server`, `cmd/probe serve`). No `internal/web`.
**Spec:** P2 of `/opt/data/probe-federation-spec.md`, read from source at `4be64ba`.

## 1. Which binary changed, and where it listens

Two programs are in scope and the split is the acceptance:

| Program | Change | Port exercised |
|---|---|---|
| relay (`internal/relay`, `probe relay` / `probe serve` relay mode) | stores `peer_hello`, serves `GET /api/v1/relay/status` | its own listen port (ephemeral `127.0.0.1:<port>` in tests; `:7701` in the field) |
| server (`internal/server`, `probe-server` / `probe serve`) | sends `peer_hello` at registration; reads the relay's status and merges an `uplink` edge | 7700 (server mux; `httptest` in tests) |

The status endpoint is registered on the relay's mux in `Run()`. It is not on the server mux: an implementation on the server would pass a unit test and fail this acceptance.

## 2. What the wire carries at registration

`internal/server/relay_ws.go` sends exactly one framed control message (channel 0, the relay's own magic) immediately after the session is stored and before the read loop starts. Observed in `TestP2103_PeerHelloAtRelayRegistration` on a real WebSocket:

```
frame magic 0x42, channel 0, payload:
{"type":"peer_hello","peer":{"id":"server-76cffb9490e2","name":"76cffb9490e2","version":"v1.19.1-test","listen_addr":"76cffb9490e2:7700"}}
```

- `listen_addr` is the dialable address, not the bind string: with `s.addr = "localhost:7700"` the frame carries the host name and port 7700. `dialableAddr()` substitutes the host name for wildcard/loopback binds; `--advertised-addr` / `PROBE_ADVERTISED_ADDR` overrides it. This is the P21-02 D3 finding applied to `peer_hello`.
- `version` is `s.version` (the same source `/api/v1/health` uses).
- `id` defaults to `server-<hostname>`, `name` to the host name; `SetServerIdentity` overrides both.
- Exactly one message: the test asserts no second frame within 250 ms after the hello (and the fake upstream in the relay test asserts one upstream connection — no second socket).
- If the write fails, registration is treated as failed: session cleanup, socket closed, no half-registered relay.

## 3. What the relay stores and serves

`TestP2103_RelayStatusServesPeerHello` runs the relay against a fake upstream and reads the relay's own listener:

```
GET http://127.0.0.1:<relay>/api/v1/relay/status
{"relay_id":"relay-542ecffd","max_agents":12,"forward_policy":"",
 "upstream":{"url":"ws://127.0.0.1:42131","port":42131,"state":"up",
   "connected_at":"2026-11-25T14:22:33.189624626Z",
   "peer":{"id":"server-ovh","name":"ovh-1","version":"v1.19.1","listen_addr":"ovh.example:7700"},
   "last_error":""},
 "downstream":{"listen":"127.0.0.1:36643","agents":[],"channels":0}}
```

Both requests in that test came from the same single upstream registration; the status is a snapshot served by the relay, not a poll of the server.

### Zero values, with the reason

- `forward_policy` — `""`. The relay `Config` has no forward-policy input; policy is a per-agent **server** setting. A guessed value would be an invented number.
- `downstream.agents[]` — channel descriptors `{channel, kind}` (`kind` = `agent` | `nested`). The relay forwards opaque frames and never parses `AgentInfo`, so it cannot honestly name agents. `channels` is the exact count.
- `upstream.port` — derived from the configured URL (`wss`→443, `ws`→80, explicit port wins); `0` if the URL is unparseable, never a guess.
- `relay_id` — the configured `--relay-id`, else the generated `relay-<hex>` the relay already uses upstream.
- `connected_at` — the instant the current/last `peer_hello` was received. Retained on loss; it means "last known", not "currently connected".
- `last_error` — the failure that took the link down, cleared by the next successful registration. `state:"down"` with an empty reason cannot occur: the server synthesises text if the relay reports none.

## 4. A dead link stays visible

`TestP2103_RelayStatusSurvivesUpstreamLoss`: after the upstream closes, the relay's status remains `HTTP 200` and reads:

```
"upstream":{"url":"ws://127.0.0.1:40641","port":40641,"state":"down",
  "connected_at":"2026-11-25T14:22:33.21046998Z",
  "peer":{"id":"server-ovh","name":"ovh-1","version":"v1.19.1","listen_addr":"ovh.example:7700"},
  "last_error":"websocket: close 1006 (abnormal closure): unexpected EOF"}
```

`connected_at` is byte-equal to the up-state value (asserted), and the peer is retained. Down is a state, not an absence.

## 5. Topology merge and the non-blocking bound

`/api/v1/topology` reads `http://<relay listen>/api/v1/relay/status` with a **300 ms** client/context deadline (`uplinkStatusTimeout`) and appends:

```
{"from":"server","to":"radxa-relay","type":"uplink","state":"up",
 "connected_at":"2026-11-25T10:00:00Z",
 "peer":{"id":"server-ovh","name":"ovh-1","version":"v1.19.1","listen_addr":"ovh.example:7700"}}
```

- Up: `TestP2103_TopologyUplinkUp` (peer fields populated, edge endpoints `server`→`radxa-relay`).
- Down after a previously-good read: `TestP2103_TopologyUplinkDownRetainsLastKnownPeer` — same edge id, `state:"down"`, non-empty `state_reason` (the connection-refused text), last known peer and `connected_at` retained from the cached snapshot.
- Relay reachable but its upstream down: the same code path uses the relay's own `state`/`last_error`, so the edge is down with the relay's reason (not a false green).
- Never reached: id falls back to `relay:<addr>`, `state:"down"`, peer omitted, reason non-empty.
- No relay configured: **no edge at all** (`TestP2103_TopologyNoUplinkConfiguredIsAbsent`). Absence and down are different answers.
- Non-blocking: `TestP2103_TopologyAnswersWithUnreachableRelay` points the server at a relay that accepts and stalls 2 s; the endpoint answered in **301.289 ms** (`topology answered in 301.289005ms with the relay stalling 2s (probe timeout 300ms)`) with `state:"down"` and a non-empty reason. The bound is the stated 300 ms plus local handler work, asserted `< 1 s` in the test.

Configuration: the downstream server needs `--relay-status 127.0.0.1:7701` (`PROBE_RELAY_STATUS`); `--advertised-addr` (`PROBE_ADVERTISED_ADDR`) sets the dialable address peers see. Both binaries (`cmd/probe-server`, `probe serve`) accept them.

## 6. Tests — red before, green after

Red at `4be64ba`, behavioural: the two relay tests and all four topology tests were copied into a
detached HEAD worktree together with a test-only no-op shim (`SetUplinkRelayStatus` + the timeout
constant) so the suite compiles against HEAD and fails on behaviour, not on a missing symbol
(`/opt/data/tmp/probe119/P21-03_red_before.log`, exit 1):

```
=== RUN TestP2103_RelayStatusServesPeerHello
    status never satisfied condition; last http=404
--- FAIL: TestP2103_RelayStatusServesPeerHello (4.01s)
--- FAIL: TestP2103_RelayStatusSurvivesUpstreamLoss (4.01s)
    no frame arrived after relay_register: i/o timeout
--- FAIL: TestP2103_PeerHelloAtRelayRegistration (2.00s)
    no uplink edge in topology edges: []
--- FAIL: TestP2103_TopologyUplinkUp (0.00s)
--- FAIL: TestP2103_TopologyUplinkDownRetainsLastKnownPeer (0.00s)
--- FAIL: TestP2103_TopologyAnswersWithUnreachableRelay (0.00s)
--- PASS: TestP2103_TopologyNoUplinkConfiguredIsAbsent   (true at HEAD by construction)
6 FAIL / 1 PASS
```

Green after (`/opt/data/tmp/probe119/P21-03_green.log`, `P21-03_suite.log`):

```
--- PASS: TestP2103_RelayStatusServesPeerHello (0.02s)
--- PASS: TestP2103_RelayStatusSurvivesUpstreamLoss (0.18s)
--- PASS: TestP2103_PeerHelloAtRelayRegistration (0.25s)
--- PASS: TestP2103_TopologyUplinkUp (0.00s)
--- PASS: TestP2103_TopologyUplinkDownRetainsLastKnownPeer (0.00s)
--- PASS: TestP2103_TopologyAnswersWithUnreachableRelay (2.00s)
--- PASS: TestP2103_TopologyNoUplinkConfiguredIsAbsent (0.00s)
```

## 7. Suite

```
GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1   → exit 0
780 test-level pass / 0 fail / 2 test skip   (+5 packages with no test files)
counted from -json: P21-03_suite.json; plain log: P21-03_suite.log
baseline at HEAD (4be64ba): 773 pass / 0 fail / 2 skip  (P21-01 close record) → delta 7 = the seven new tests
load 17.76 18.27 18.66 at end of run (shared 16-core host)
toolchain: go1.25.12 from /opt/data/go/pkg/mod/golang.org/toolchain@…-go1.25.12.linux-amd64 —
  the /opt/data/go GOROOT on this host is polluted (runtime `mallocHeaderSize redeclared`), so the
  cached toolchain was used with GOTOOLCHAIN=local and the vendor tree.
```

## 8. Host status

Deterministic half proven on this host: relay process code on loopback, server mux via `httptest`, real WebSocket handshakes in both directions. **UNPROVEN-ON-HOST: the Radxa relay → OVH server link.** No Radxa host was touched (owner authorisation pending). The Radxa-side acceptance is the same two reads: `curl 127.0.0.1:7701/api/v1/relay/status` on the Radxa, and `GET /api/v1/topology` on the server with `PROBE_RELAY_STATUS=127.0.0.1:7701`.

## 9. Collision note (shared tree)

This worktree was written concurrently by more than one hand during the unit. Transient untracked
files appeared (`internal/server/relay_uplink.go`, `internal/protocol/peer_hello.go`, a capture test);
they were competing drafts of the same feature. The canonical implementation is the one in the
commit: `internal/server/uplink.go` + `internal/relay/status.go`, and the drafts were removed before
staging. The capture test was temporary and is deleted. Nothing foreign was staged: the commit was
built from twelve explicit paths and `git diff --cached --name-only` was inspected first.

## 10. Open items

- P3 (`/network` page) consumes the new edge fields (`state`, `state_reason`, `connected_at`, `peer`); the component's `data` prop is P3's scope.
- Deployment must set `PROBE_RELAY_STATUS` on the downstream server. Without it topology has no uplink edge — by design, but silent unless the flag is in the unit's runbook.
- Relay-side `agents[]` carries channels only. Naming relayed agents on the relay's status page needs the relay to parse `AgentInfo`, which today it deliberately does not.
- The relay's `/api/v1/relay/status` is unauthenticated on the relay's listen port (like `/health`). It carries no secret — no token, no agent payloads — but it names the relay, the upstream URL/port and channel counts to anyone who can reach that port. If that port is publicly DNAT'd this is a disclosure decision for P7/P9, not something to paper over.
