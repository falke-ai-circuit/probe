# P21-04 — `forward_policy` enforcement

Unit: P21-04 · workflow wf_probe1 · branch `r119-build`
Substrate: parent-verified at `e679b8b`

## What existed before this unit

`protocol.TypeForwardPolicy` and `POST`/`GET /api/v1/agents/{id}/forward-policy`
stored a policy per agent and pushed it to the agent. Routing never read it:
`handleWebSocket` registered every arriving client locally, and the relay binary
forwarded every downstream client upstream. `MaxAgents` was relay-config only.
No hop counter, no traversed set, no allowlist call on a relay registration.

## Policies, enforced

| Policy | Behaviour | Enforcement point |
|---|---|---|
| `local` | registers on the server the client reached; nothing forwarded even when an uplink exists | `forwardArrival` → `DecideForward` (server_ws.go, relay_ws.go) |
| `relay` | registers upstream; this server keeps a read-only mirror (`relayed=true`, `via=<forwarder id>`); no further recursion | `UplinkForwarder.ForwardAgent`, mirror in `forward_policy.go` |
| `relay_all` | as `relay`, and a client arriving through a relay is carried further up this server's uplink — recursion until a server with `local` or a chain root | `forward_policy.go:forwardPolicyFor`, `relay_ws.go` re-forward branch |

Chain root rule: a server with no uplink configured terminates `relay` and
`relay_all` by registering locally. Without this rule the documented default
policy (`relay`) would refuse every agent on a standalone server.

Implementation: `internal/server/forward_policy.go` (decision, chain, refusals,
mirror), `internal/server/forward_uplink.go` (the uplink client that dials the
upstream's `/ws` as a relay, one socket, one channel per forwarded agent),
wiring in `server_ws.go`, `relay_ws.go`, `server.go`, `api_v1_phase4.go`.

## Guards shipped in the same commit

1. **`max_agents` per link.** Server: `handleRelayControl` channel_open checks
   the open-channel count of that relay session against the limit the relay
   declared (`metadata.max_agents`), falling back to the relay default of 100
   (`DefaultLinkMaxAgents`). Relay binary: `handleDownstream` keeps its existing
   100-default check. Refusal names the limit and the link.
2. **Loop prevention.** `Chain{Hops int, Traversed []string}`, advanced per hop
   by `Chain.Next(relayID)`; a repeated id is refused, and `MaxForwardHops = 8`
   is the backstop for a chain that grows without a cycle. The chain travels in
   `relay_register.metadata.chain`; the receiving server refuses a registration
   whose chain names its own server id or its own forwarder link id.
3. **Allowlist.** `RelayAllowlist` interface (`Allows(relayID, fingerprint)`),
   called in `handleRelayConnection` before any session exists. P7 owns the
   mechanics; this unit owns the call and the distinct refusal. An uninstalled
   allowlist is a documented inactive gate, not a silent pass.
4. **Rate-limit parity.** `Server.admitInbound(agentID, relayed)` is the single
   admission function; the direct path (`handleMessages`) and the relayed path
   (channel registration) both call it. Counters (`forwardedStats`) make the
   parity measurable instead of assumed.

## Refusal taxonomy (distinct strings, no "forwarding failed")

| Code | String |
|---|---|
| `FORWARD_POLICY_DENIED` | `policy denies forwarding for agent <id> (policy=<p>)` |
| `FORWARD_MAX_AGENTS` | `max_agents limit <n> reached on link <link>` |
| `FORWARD_LOOP` | `loop refused — relay <id> already in the uplink chain [<ids>]` |
| `FORWARD_ALLOWLIST_DENIED` | `relay <id> is not in the downstream allowlist` |
| `FORWARD_MIRROR_HAS_NO_LIVE_UPSTREAM` | `mirror is read-only: agent <id> relayed via <relay> has no live upstream` |

Each is a `*ForwardRefusalError` with a stable `Code`, carried on the wire as a
`forward_refused` control frame (`channel_id` for per-channel refusals), and
surfaced to a direct client as an `{"type":"error","code":…,"error":…}`
envelope so the client is told why instead of being dropped.

## Mirror is read-only by construction

`MirroredAgent{AgentID, Via, Relayed, Policy, UpstreamLive}` in `Server.mirror`.
A forwarded agent has **no** entry in `s.conns` and **no** `Conn`, so no write
path exists to answer from the copy. `MirrorWriteGuard` returns
`FORWARD_MIRROR_HAS_NO_LIVE_UPSTREAM` when the uplink is gone and a
read-only-names-the-destination refusal when it is live. Topology renders
mirrored agents with `relayed:true` and `via:<relay id>` on a `relayed` edge.

## Evidence (loopback only)

Harness: `internal/server/p2104_forwardpolicy_test.go`, two real servers on
ephemeral `httptest` ports, real WebSocket clients. No host involved.

| Test | Proves |
|---|---|
| `TestP2104_LocalPolicyForwardsNothing` | local registers locally; upstream has no record; no mirror |
| `TestP2104_RelayPolicyRegistersUpstreamWithMirror` | agent appears upstream as `relay/relay-ds/agent-relay`; mirror `relayed=true via=relay-ds`; write guard refuses; after uplink loss the guard names no live upstream |
| `TestP2104_RelayAllAppearsUpstreamWithVia` | acceptance shape for relay_all with `via=relay-ds` |
| `TestP2104_LoopIsRefusedAndChainTerminates` | S1↔S2 cycle: terminal client receives `FORWARD_LOOP` over its own socket, quoting the chain; nothing registered on either server |
| `TestP2104_MaxAgentsPerLink/at_limit_refused` | limit 1: second channel refused, reason quotes `max_agents limit 1` and the link |
| `TestP2104_MaxAgentsPerLink/below_limit_accepted` | two channels under limit 2 accepted |
| `TestP2104_RelayedClientHitsTheSameLimiter` | with the only concurrency slot held, direct and relayed admissions both refused, relayed agent never registered; both admitted once freed |
| `TestP2104_AllowlistDeniesWithDistinctError` | unknown relay id refused with `FORWARD_ALLOWLIST_DENIED` |
| `TestP2104_DecideForwardTaxonomy` | four refusal strings distinct; unknown policy refused; chain root terminates |

## Honest labels

- Loopback only. **Radxa↔OVH leg: UNPROVEN-ON-HOST** — owner authorisation
  still pending; no host was contacted, per brief.
- An uninstalled `RelayAllowlist` leaves the gate inactive. That is stated, not
  implied: P7 owns the allowlist mechanics and this unit only calls them.
- A relayed client's policy is resolved by exact id first, then by its trailing
  name segment (`forwardPolicyFor`), because a relayed agent's registry id is
  `relay/<link>/<name>` while operators name the agent.
- The uplink edge in `/api/v1/topology` from P21-03 only appears when the server
  runs with `--relay-status`/`PROBE_RELAY_STATUS`; the enforcement path here
  does not depend on it (the harness does not set it).
