# P21-04 — CLOSE (written by the parent: the child left the work uncommitted and wrote no close record)

**Commit:** `6126516` — the unit's code and tests, authored by the coder child, committed by the parent.
**Why the parent committed:** the child wrote its artefact at 15:15 and then stopped with seven paths uncommitted in a shared tree. The parent verified the work from disk (vet clean, the unit's eight tests green, full suite green) and committed it, rather than closing a unit whose code existed only as someone's dirty working copy.
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: 6126516`
**Scope:** `internal/server/` — `forward_policy.go` (decision, chain, refusals, mirror), `forward_uplink.go` (uplink client), `p2104_forwardpolicy_test.go`, wiring in `server_ws.go`, `relay_ws.go`, `server.go`, `api_v1_phase4.go`. No `internal/web`, no host touched.

## Delivered

The policy that existed as a stored field is now enforced at the arrival point, for all three values. `local` registers on the server the client reached and forwards nothing; `relay` registers upstream and this server keeps a **read-only mirror** (`relayed=true`, `via=<forwarder>`); `relay_all` carries a relayed client further up the chain, recursively, terminating at a server with `local` or at a chain root. The chain-root rule is the load-bearing detail — without it the documented default (`relay`) would refuse every agent on a standalone server.

## Guards shipped in the same commit, as briefed

1. **`max_agents` per link** — checked against the limit the relay declared, falling back to the default 100; the refusal names the limit and the link.
2. **Loop prevention** — `Chain{Hops, Traversed}` advanced per hop, repeated id refused, `MaxForwardHops = 8` as backstop; the chain travels in `relay_register.metadata.chain` and a receiver refuses a chain naming its own id or its own forwarder link.
3. **Allowlist** — `RelayAllowlist.Allows` called before any session exists, with its own refusal. P7 owns the mechanics; an uninstalled allowlist is a **documented inactive gate**, stated rather than silently passing.
4. **Rate-limit parity** — one `admitInbound(agentID, relayed)` called by both the direct and the relayed path, with counters that make the parity measurable rather than assumed.

Five distinct refusal codes (`FORWARD_POLICY_DENIED`, `FORWARD_MAX_AGENTS`, `FORWARD_LOOP`, `FORWARD_ALLOWLIST_DENIED`, `FORWARD_MIRROR_HAS_NO_LIVE_UPSTREAM`) — the generic "forwarding failed" the brief existed to remove is gone, and the refusal reaches the client as a `forward_refused` control frame or an error envelope instead of a silent drop.

## Suite — the child's number is unusable, the parent's is on the record

The child's `P21-04_suite.log` ends in **`EXIT=143`** (SIGTERM) and contains **no `internal/server` line at all** — the package this unit changes never finished, so its run proves nothing about this code and is not adopted.

Parent measurements at `6126516`'s tree, load 17.97, clean of other lanes' work apart from this commit:
- `go vet ./internal/server/` — exit 0.
- `go test ./internal/server/ -run TestP2104 -v` — **8/8 PASS**, including `TestP2104_LoopIsRefusedAndChainTerminates` (S1↔S2 cycle refused, chain quoted, nothing registered on either server), `TestP2104_MaxAgentsPerLink` (refused at the limit, accepted below it), `TestP2104_RelayedClientHitsTheSameLimiter`, and `TestP2104_DecideForwardTaxonomy` (four refusal strings distinct).
- Full suite `go test ./... -count=1 -json` — **790 pass / 0 fail / 2 skip**, load 17.97. Baseline before this unit was 780/0/2; delta +10 is this unit's tests.

## Open items

- **Radxa↔OVH leg: UNPROVEN-ON-HOST.** Loopback harness only; no host was contacted, per the brief. The spec's acceptance names that leg and it remains unmet until the owner authorises host work (P20-01, P16-02 both still parked on that).
- **An uninstalled `RelayAllowlist` leaves the gate inactive** — stated in the artefact and here; P7 owns the allowlist mechanics, and until it lands a relay registration is not actually refused for want of an allowlist.
- A relayed client's policy is resolved by exact id first, then by trailing name segment, because a relayed agent's registry id is `relay/<link>/<name>`. That heuristic is a compromise and P7/P5 may supersede it.
- The child's own close was never written and its suite was killed; the parent's commit is a repair of the delivery, not evidence that the child finished cleanly.
