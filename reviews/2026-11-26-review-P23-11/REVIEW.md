# P23-11 — REVIEWER: REMEASURE. Independent re-measurement of five claims against P23-00

**Profile:** reviewer · **Gateway:** api_server :8654 · **Status:** remeasure complete
**Artefact:** /opt/data/tmp/probe119/P23-11_REMEASURE.md
**Baseline:** /opt/data/tmp/probe119/P23-00_ASIS.md (read first; its method copied exactly)
**Tree:** /opt/data/projects/PROBE_UPGRADE/v119wt · branch `r119-build` · HEAD `c1f7123` (`c1f71235f4ac82a860ef138d66c50619db3d2f48`, "P15-14: CI trigger"). Worktree CLEAN (0 modified/untracked) at measurement start — unlike P23-00's dirty tree, export == HEAD here.
**Measured artefact:** clean `git archive HEAD` export at `/opt/data/tmp/probe119/.p23src11` — NOT the worktree.
**Build:** `GOTOOLCHAIN=local GOFLAGS=-mod=vendor`, toolchain go1.24.4 (`/opt/data/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.24.4.linux-amd64/bin/go`). `go build ./...` → **BUILD_EXIT=0**. Binaries in `/opt/data/tmp/probe119/.p23bin11/` (probe-server, probe, probe-client). `go vet ./internal/{server,relay,agent,protocol}/` → exit 0.
**Lab (loopback only, no OVH/Radxa/EAS1, nothing pulled or deployed):** server `127.0.0.1:17920`, deny-all-allowlist server `:17930` (`-relay-allowlist ""` → log `[relay-allowlist] policy=deny-all`), relay `lab-relay` on `:17921`→:17920, capped relay `lab-relay-cap` on `:17941` (`-max-agents 1`), non-allowlisted relay `probe-nl` on `:17931`→:17930, agents direct1/a1/a1r/fill1/b2, echo target `127.0.0.1:17950`.
**Reviewed by:** reviewer · **verdict: FAIL (F4 still red, relay_all clause still red, T5.3 relay surfacing still red)** · **verdict_sha_of_head: c1f7123**
**Tally: T2 CHANGED (via= FIXED, relay_all STILL FAIL) · T5.2 PASS (F3 fix lands) · T5.3 FAIL (unchanged) · F4 FAIL (STILL RED, live worse: 10/20) · Lock-order PASS (no ABBA; package -race fails only on a pre-existing test-fixture race)**

---

## TL;DR delta against P23-00 (measured at c20427b)

| Cell | P23-00 @ c20427b | This run @ c1f7123 | Delta |
|---|---|---|---|
| T2 `via=<relay-id>` | absent (every row 0 `via` keys) | **present**: `relay/lab-relay/a1` row carries `"via":"lab-relay"`, direct row carries none | FIXED (P23-09 wiring, agent_visibility.go:385-391) |
| T2 cross-hop dispatch | worked | still works (`exec` → `T2-REMEASURE-VIA-OK`) | unchanged |
| T2 `forward_policy=relay_all` | unreachable (400/`bad handshake` paths) | **still unreachable**: `POST /api/v1/agents/a1/forward-policy {"action":"relay_all"}` → `HTTP=400 action must be 'relay' or 'local'` | STILL FAIL |
| T5.2 max_agents refusal | untyped 429 → `websocket: bad handshake` | **typed at the agent**: `[policy] admission refused: FORWARD_MAX_AGENTS — max_agents limit 1 reached on link lab-relay-cap (current 1) (limit=1 current=1)` | FIXED (F3) |
| T5.3 allowlist refusal at refused peer | relay sees `close 1006`, code lost | **identical**: relay log `upstream read error: websocket: close 1006 (abnormal closure): unexpected EOF`, retries; no `FORWARD_ALLOWLIST_DENIED` handler exists in the relay binary | STILL FAIL (P23-F5 unchanged) |
| F4 tunnel live 20x | 17/20 ok, 3 recv timeouts | **10/20 ok, 10 recv timeouts** — same shape, WORSE rate | STILL FAIL |
| F4 in-tree 10x (`-run 'Tunnel|P2106'`) | 9 fail / 50 (42 pass) | **0 fail / 50 (50 pass, 10.4s)** | this run's iterations were quiet — but see F4 cell (live is the recipient witness) |
| Whole tree | 822 pass / 0 fail / 2 skip | **840 pass / 0 fail / 2 skip** | +18 tests, no failures |
| Lock order (P15-11 ABBA) | 251 readers parked 145 min (production hang) | dedicated test 20x `-race`: 68 pass / **1 watchdog trip, dump proves log-mutex park, NOT a lock cycle**; 0 DATA RACE | no ABBA regression |

---

## Cell 1 — T2 relay_all / via= reachability: **CHANGED** (one clause fixed, one still dead)

**via= clause: PASS (E1).** Live lab, relay `lab-relay` upstream → server :17920. A client arriving downstream (`a1` dials :17921) registers upstream as `relay/lab-relay/a1` and the row now names the carrier:

```
GET /api/v1/agents (:17920)
  direct1               via=<absent>            liveness_source=ws
  relay/lab-relay/a1    via=lab-relay           liveness_source=relay
  relay/lab-relay-cap/b2 via=<absent> (pre-cap drama; see wire notes below)
```

Topology node carries it too: `"id":"relay/lab-relay/a1","relayed":true,"via":"lab-relay","active":true`.

**Dispatchable across the hop (E1):**

```
POST /api/v1/agents/relay%2Flab-relay%2Fa1/exec {"command":"echo T2-REMEASURE-VIA-OK"}
{"ok":true,"data":{"duration_ms":2,"exit_code":0,"stdout":"T2-REMEASURE-VIA-OK\n","timed_out":false}}
```

Direct agent `direct1` has NO `via` key (omitempty, not empty string) — the direct-direction pin from the tree's own `TestP2309DirectRowHasNoVia` holds live. Wiring: `internal/server/agent_visibility.go:385-391` (P23-09) sets `rec.Via` from `agentCarrier()` when `liveness_source == relay`; carrier comes from `setRelayRoute` (`relay_routes.go:142`), written at registration (`relay_ws.go:431`) — the same entry the dispatcher resolves through, so the name in the row IS the transport.

**relay_all clause: STILL FAIL (E1).** Only setter rejects it, exactly as at c20427b:

```
curl -X POST .../api/v1/agents/a1/forward-policy -d '{"action":"relay_all"}'
  → HTTP=400  action must be 'relay' or 'local'
curl -X POST .../api/v1/agents/a1/forward-policy -d '{"action":"relay"}'
  → HTTP=200  {"agent":"a1","ok":true,"policy":"relay",...}
```

Source at c1f7123 confirms no other path: `forwardPolicyFor` still defaults `""` → `PolicyRelayAll`-semantics only inside `DecideForward` (forward_policy.go:184-187), but `uplinkConfigured` can never be true from any shipped binary — `SetUplinkForwardTarget` is still called from no `cmd/` file (grep: zero hits in cmd/). The recursion branch remains dead code from the operator's seat. **Note the asymmetry: the baseline's finding P23-F2 (no via) is fixed; P23-F1 (relay_all unreachable) reproduces verbatim.**

**Verdict Cell 1: CHANGED — via= half PASS, relay_all half FAIL.**

## Cell 2 — T5.2 max_agents refusal typed at the agent: **PASS (CHANGED)** (E1)

Capped relay live (`probe relay -listen 127.0.0.1:17941 -max-agents 1 -relay-id lab-relay-cap`), slot filled by `fill1`, then victim `b2` dials the capped relay:

```
agent.go:426: Connected.
agent.go:732: [policy] admission refused: FORWARD_MAX_AGENTS — max_agents limit 1
              reached on link lab-relay-cap (current 1) (limit=1 current=1);
              refused by policy, not a handshake/auth/TLS failure
agent.go:440: retrying in 1.467133565s (refused by policy, not a connection fault)
```

The refused agent READS THE CODE, the limit, the link id, and the refusal's class — no `websocket: bad handshake` anywhere (baseline showed raw `HTTP/1.1 429 Too Many Requests`, body `too many agents`, agent saw `dial: websocket: bad handshake`). The refusal now rides a socket that already upgraded (`internal/protocol/admission_refusal.go`) and the agent decodes it (`internal/agent/agent.go:policyRefusalLine`, ~:711). One harness note for reproducibility: the relay's cap counts its CURRENT channels, so the victim must be a SECOND client while a slot-holder is connected — my first victim took the free slot and connected normally.

**Verdict Cell 2: PASS — F3's fix lands and is operator-visible.** (Delta vs P23-00: FAIL → PASS.)

## Cell 3 — T5.3 allowlist refusal crossing the relay: **FAIL (UNCHANGED)** (E1)

Deny-all server live at :17930 (log: `[relay-allowlist] policy=deny-all entries=[]`). Relay `probe-nl` dials it:

```
srv-deny.log: [server] relay probe-nl refused: FORWARD_ALLOWLIST_DENIED: relay probe-nl is not in the downstream allowlist
relay-nl.log: [relay] connected to upstream ws://127.0.0.1:17930/ws (id=probe-nl, ...)
              [relay] upstream read error: websocket: close 1006 (abnormal closure): unexpected EOF
              [relay] upstream disconnected — attempting reconnect (backoff=1s)
              [relay] connected ... [relay] re-registered 0 channels after reconnect
              [server] relay probe-nl refused: FORWARD_ALLOWLIST_DENIED: ...   (2nd refusal on the retry)
```

The refused peer cannot name why. Source at c1f7123: `FORWARD_ALLOWLIST_DENIED` is produced in `internal/server/*` and typed in `internal/protocol/admission_refusal.go:23`-family, but grep shows **zero handlers in the relay binary or agent code for the allowlist code** — the typed-frame consumer written in P22-03 (`policyRefusalLine` in `agent.go`) serves the AGENT socket, not the relay's upstream socket. The relay meets EOF and retries into the same refusal (loop). Same shape P23-00 recorded at c20427b; no HOUSE commit addressed it. Also F4-family in general: the P23-00 finding P23-F5 stands unchanged.

**Verdict Cell 3: FAIL — the frame never reaches the process that needs it.** (Delta: none.)

## Cell 4 — F4 tunnel determinism ≥20 iterations: **FAIL — STILL RED**, live worse (E1)

Live, 20 sequential create→round-trip→teardown against `direct1`→echo `:17950`:

```
LIVE20_REMEASURE ok=10/20
FAIL   5  TimeoutError timed out port=36037  create_ms=1.6 verified=True create_code=200
FAIL   8  TimeoutError timed out port=36155  create_ms=1.8 verified=True create_code=200
FAIL   9  TimeoutError timed out port=42915  create_ms=1.7 verified=True create_code=200
FAIL  10  TimeoutError timed out port=40177  create_ms=2.0 verified=True create_code=200
FAIL  11  TimeoutError timed out port=37617  create_ms=1.7 verified=True create_code=200
FAIL  13  TimeoutError timed out port=46779  create_ms=2.3 verified=True create_code=200
FAIL  14  TimeoutError timed out port=37505  create_ms=3.6 verified=True create_code=200
FAIL  15  TimeoutError timed out port=38507  create_ms=1.8 verified=True create_code=200
FAIL  16  TimeoutError timed out port=41139  create_ms=1.9 verified=True create_code=200
FAIL  17  TimeoutError timed out port=36393  create_ms=1.9 verified=True create_code=200
```

(raw per-iteration record: /opt/data/tmp/probe119/p2311/live20.json)

Every failure is at RECV, not create: create returned `200 verified:true` with a published port (`loopback.ok latency_ms=0`), the TCP connect to the published port succeeded, and the read then timed out. Identical recipient-visible shape to P23-00's 17/20 — only the rate moved (worse: 10/20). The API entry point is `POST /api/v1/agents/{id}/tunnel` (`{"target_host":"127.0.0.1","target_port":17950}`) — `target_host`+`target_port`, not `target` (a 400 with `target_host and target_port required` documents the correct v1 shape).

In-tree repetition THIS run was quiet — `go test ./internal/server -run 'Tunnel|P2106' -count=10 -v` → **50/50 PASS, TUNNEL10_EXIT=0** (the same command that failed 9/50 at c20427b). I record that honestly as a possible fix-in-progress to the stale-route half of F4 (`TestP2106_LoopbackFailureIsRefused` passed all 10). But the live cell (the recipient's witness) still fails at half the rate P23-00 measured. The in-tree confirmation-too-fast assertion (`p2106_tunnelproof_test.go:235`) also did not fire this run. Verdict cell 4: **FAIL — still nondeterministic from the recipient's seat; live rate not improved (10/20 vs 17/20), in-tree repetition happens to be quiet this run.**

## Cell 5 — lock-order regression probe (cleanup vs agent-visibility read): **PASS** (E1/E2 two witnesses)

1. **Dedicated test exists and ran 20x under -race:** `go test ./internal/server -run 'P1512' -count=20 -race -v` → `--- PASS: TestP1512CleanupVsDecorateDeadlock` 68 of 69 executions (includes the churn/count tests), **0 DATA RACE**, no hang, no watch-pattern abort — a regression FAILS with a dump instead of hanging (that is the test's design), and at count=1 in the package run below it passed.
2. The ONE trip (execution 4.56s, `p1512_lockorder_test.go:123: cleanupRelaySession stalled >3s (passes=16592 sweeps=2)`) is **NOT a lock cycle**: the attached goroutine dump shows every parked `cleanupRelaySession` frame parked inside `peerLost → log.Printf → log.(*Logger).output → lockSlow` (std log mutex on servers.json-atomic-write log lines) — relay_ws.go:574 → servers.go:585 → stdlib log.go:243. No goroutine holds a relayMu/channelsMu half-cycle; the reader side made 16,592 passes while the writer side did 2 sweeps. Verdict on the trip: watchdog sensitivity to log contention under `-race` scheduling, not the P15-11 ABBA shape.
3. **Package -race** (`go test ./internal/server -count=1 -race`, 182s): **FAIL, exit 1** — but the sole failing test is `TestP2105_RouteMapLifecycleNamedError`, with **4 DATA RACE warnings whose read AND write stacks are both test-fixture goroutines in `p2105_controlplane_test.go` (`p2105RelayResponder` writing at :238, the test itself writing at :270 — two unsynchronized `conn.WriteMessage` calls on the same fixture conn)**. Reproduced in isolation (`-run 'TestP2105_RouteMapLifecycleNamedError' -count=3 -race` → FAIL in 1.3s), zero production frames in any trace. This is exactly the class ea28a49's own message records as "p2105/p2108 fixture-internal races proven pre-existing at 60c5950 baseline" — **not a P15-12 lock-order regression and not one of the four fixes in this unit's scope.** It nonetheless means the suite under `-race` is not green at this HEAD; the baseline's P23-00 suite run was WITHOUT -race, and the same fixture race would have been there (pre-existing at 60c5950 per the repo's own record).

**Verdict Cell 5: PASS for the specific P15-11 question (cleanup concurrent with a visibility read does not deadlock — the dedicated test plus 475-test package run complete, no ABBA stack in any dump); the package -race red belongs to pre-existing fixture territory and is named here so nobody mistakes it for a new regression.**

---

## (b) Whole tree — build pass, suite pass (E2)

```
go build ./...  → BUILD_EXIT=0
go test ./... -count=1 -json  → SUITE_EXIT=0
  probe 17 · probe-client 6 · agent 184 · crypto 5 · lifecycle 11 · platform 6
  protocol 43 · relay 21 · server 475 (2 skip) · upgrade 70 · version 2
  TOTAL 840 pass / 0 fail / 2 skip   (baseline was 822/0/2 — +18 tests from the four fixes)
```
No failing test name in the plain `-count=1` run. The 2 skips are the same named tests as baseline (`TestDownloadURL_LiveCrossProcessProof`, `TestV1_CreateTransfer`). `go vet` on server/relay/agent/protocol → exit 0.

## Verdict block (reviewer)

**verdict: FAIL** — not because the fixes failed (two of the three red reachability findings are verifiably fixed), but because the gate question is "only what is still red", and red remains:

- **RED 1 — F4 tunnel:** live 10/20 (baseline 17/20), all recv-timeout after verified create; ports listed cell 4. Worse, not fixed.
- **RED 2 — T2 relay_all clause:** still unreachable through any shipped binary (400 + no uplink wiring); P23-F1 reproduces verbatim.
- **RED 3 — T5.3 relay surfacing:** refused relay still sees only `close 1006`; `FORWARD_ALLOWLIST_DENIED` never leaves the refusing process; P23-F5 unchanged.
- **Named caveat (not a red of this unit's scope):** `internal/server` under `-race` fails on the pre-existing p2105 fixture-internal race (ea28a49's own baseline record matches).

**Fixed and now live-verified at c1f7123: F3/`via=`** — a refused agent reads `FORWARD_MAX_AGENTS … link … (limit=1 current=1)` instead of `bad handshake`, and every relayed agent row now carries `via=<relay-id>` that matches the transport the dispatcher actually uses.

**Nothing was fixed or edited in the tree, and nothing was pushed or deployed. No production host touched. All binaries came from the `git archive` export; the review artefact was committed doc-only (`aa6ac84`, `reviews/2026-11-26-review-P23-11/REVIEW.md`).**

## Worktree moved during this unit (recorded so the sha is not misread)

At measurement start the worktree was CLEAN at `c1f7123`. While this unit ran, a sibling lane
placed five uncommitted files in the shared tree: `internal/relay/mux.go`, `relay.go`, `status.go`
(modified) and `internal/relay/p2205_refusal_test.go`, `internal/relay/refusal.go` (new,
timestamps 10:35–10:48 — inside this unit's window). **Every number above describes `c1f7123` =
`git archive HEAD` and nothing else; those files are P22-05 in flight, were not read as source for
any verdict, were not run, and are not mine to commit** (committing another lane's WIP would cross
a live lane and the unit's no-fixes/no-commits rule on source).

Raw evidence: /opt/data/tmp/probe119/p2311/ (logs: srv/relay-nl/relay-cap/agent-refused, live20.json, tunnel_f4.log, lockorder_race20.log, pkg_race.log, .p23_suite11.json).
