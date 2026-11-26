# P18-03 — UI FALSIFICATION: RENDERED vs RAW (P18-01 /network, P18-02 via-relay column)

**CLOSE HEADER**
`reviewed_by: reviewer (P18-03 = review gate for P18-01 a60d02a and P18-02 9e68ca7)` · `verdict: PENDING-REVIEW` · `verdict_sha_of_head: d26c296fbb3f77144a7a4dfbafe469e11aa5d3af (pushed origin r119-build, 9e68ca7..d26c296, 0/0)`
`artefact: /opt/data/tmp/probe119/P18-03_UI_FALSIFY.md (+ repo copy docs/contracts/P18-03_UI_FALSIFY.md)`
`suite: 0 failed, 0 new red — falsification unit, read-only on code; no web/ or Go file touched, so no build floor re-run owed` · `open item: F1 topology-graph layout overlap (below) — the only genuine MISMATCH; it holds the P18-01/P18-02 gate flip`

**RECIPIENT EXPERIENCE (E1):** logged into the SPA through its own login form (admin/p1802pass) on the live P18-02 rig (:17710 + relay relay-ds :17711 + direct-01 + relayed-01, all loopback), drove every surface listed below in a real Chromium session, and paired each rendered assertion against the raw API JSON fetched inside the same session. Raw JSON pinned to disk before any verdict: `p1803_raw_topo.json`, `p1803_raw_agents.json`, `p1803_raw_peers.json`.

**Rig honesty (E2):** rig_run1802.sh re-run (P18-02's rig was dead — all four pid.* unalive at session start). One env-only extension was required to drive the peer surfaces at all: `PROBE_SECRET_STORE=<rig>/peer-secrets.json`. Without it `POST /api/v1/peers` 500s `PEER_STORE_INIT_FAILED` (default store path `/var/lib/probe/` not creatable as non-root — `internal/server/credentials.go:64`). Environment limitation, not a code defect; no server file changed. A second server (:17712, `--relay-allowlist=`) was stood up as the typed-refusal upstream. Both are harness props (rig_run1803.sh), same deviation class as P18-02's admin-password flag.

---

## Surface results (each: raw JSON evidence + rendered DOM evidence + verdict)

### S1 — /network chain vs GET /api/v1/topology — MATCH (content), F1 MISMATCH (geometry)

Raw (`p1803_raw_topo.json`): nodes `server(127.0.0.1:17710)`, `relay-ds`, child `relay/relay-ds/relayed-01`, `direct-01`; edges `server→relay-ds (relay)`, `relay-ds→relay/.../relayed-01 (relayed)`, `server→direct-01 (direct)`.

Rendered (DOM, E1): svg labels exactly `{127.0.0.1:17710, direct-01, relay-ds, relay/relay-ds/relayed-01, v1.19.1}`.
- `nodes_missing_from_svg=[]` — every node and nested child the API returns renders.
- `invented_labels(non-version)=[]` — nothing rendered that the API does not report (v1.19.1 is the version sub-label of the server node).
- Meta card `name/version/relays/agents/viaRelay = 127.0.0.1:17710 / v1.19.1 / 1 / 2 / 1` — agrees with raw counts.
- Relays card: `relay-ds | — | relay/relay-ds/relayed-01` — the served-agent column names the nested shape from raw children.
- Edge geometry: svg lines `(450,50)→(450,160) dash=none` and `(450,160)→(450,270) dash=6,4` — relayed edge is dashed, direct edge solid, matching the component's visual language.

**F1 (the F1 probe + run5 line-pair dump, E1; code `web/src/components/TopologyGraph.tsx:44-62`):** with one direct agent and one relay (the exact P18-02 rig topology), both row-2 nodes land at the same x. The rendered line dump shows TWO logical edges drawn as the identical segment `(450,50)→(450,160)`: `server→direct-01` and `server→relay-ds` overlap, and the two node labels (direct-01, relay-ds) stack at the same point. Code: direct agents render at `cx` when `length==1` (line 47-52) and relays compute `cx - (relays.length-1)*spacing/2 + i*spacing` (line 58) = `cx` for one relay; the two branches never offset each other. The recipient cannot tell the direct agent from the relay on this topology — the very chain the page exists to show. **Verdict: MISMATCH (rendering), needs a code fix (offset the relay row from the direct-agent row).** This is a TopologyGraph defect that predates P18-01 (shared with Dashboard) but was invisible until paired against raw edge counts.

### S2 — state honesty: retrying renders retrying, never connected — MATCH

Raw (`p1803_raw_peers.json`, E3): peer `badtok` after PUT to dead-socket upstream and dial-now: `link_state:"retrying", state_detail:{}` earlier in its dial cycle; peer `wrongtok` (wrong bearer, live upstream :17712): `link_state:"retrying", state_detail:{}` — both wire shapes are the §4 retrying class (401-before-upgrade is transport failure: P17-03 note 1).

Rendered (DOM, E1): `badtok` badge text `retrying`, class `badge badge-yellow`; `wrongtok` badge `retrying`. Peers card body contains the literal string `connected`: **False**. Expansion of a retrying row renders ZERO `peer-state-detail` blocks (retrying has empty state_detail — correct absence, matches P18-01's deviation note 2).

### S2b — TYPED terminal path: auth-rejected never renders connected — MATCH (this closes P18-01's never-live-rendered gap)

Drive: peer `badtok` PUT to `ws://127.0.0.1:17712/ws` with the CORRECT bearer (`srv-token`), upstream-B runs `--relay-allowlist=` (deny all) → the upstream answers relay_register with a typed `FORWARD_REFUSED` (the §4 definition of auth-rejected), not a 401.

Raw (E3): `link_state:"auth-rejected", state_detail:{"refusal_code":"FORWARD_ALLOWLIST_DENIED","refusal_reason":"FORWARD_ALLOWLIST_DENIED: relay badtok is not in the downstream allowlist"}` — pinned in `p1803_raw_peers.json`.

Rendered (E1): badge text **`auth-rejected (terminal)`**, class **`badge badge-red`** — a distinct red terminal badge, never green/connected. Row expansion renders `peer-state-detail` with `refusal code: FORWARD_ALLOWLIST_DENIED` and the full refusal reason — **`refusal_code_raw='FORWARD_ALLOWLIST_DENIED' rendered_verbatim=True`**. The P18-01 gate item "terminal badge + state_detail expansion renders the refusal code" is now live-rendered evidence, not code-read.

### S2c — wrong bearer against a live upstream — MATCH (honest retrying)

`wrongtok` (wrong token, real upstream): stays `retrying` on wire and `retrying` in DOM across the capture window. Correct per the §4 machine: credential denial at the bearer gate is transport-class (retry), typed refusal is terminal. Neither rendering merges into `connected`.

### S3 — /agents via-relay column vs GET /api/v1/agents — MATCH

Raw (`p1803_raw_agents.json`, E3): `direct-01` has NO `via` key (omitempty); `relay/relay-ds/relayed-01` has `"via":"relay-ds"`.
Rendered (E1): headers include `Via relay`; `ROW0 name='direct-01' via_cell='direct'` (raw None → 'direct', MATCH), `ROW1 name='relayed-01' via_cell='relay-ds'` (raw 'relay-ds', MATCH). **Em-dash in the via column: False** (P18-02 deviation 3 honoured). Wire shape: bare relay id, never the nested `relay/.../agent` form (P18-02 deviation 4 honoured).

**Seeded flag (b) — liveness-path via vs topology-path via (P18-03 flags from P18-02 close):** for the relayed agent both surfaces say `relay-ds`: agents `via='relay-ds'` (agentCarrier read of the live relay route, `internal/server/agent_visibility.go:418-430`) and topology child `nodes[].children[].via='relay-ds'` (`p1803_raw_topo.json`) agree — `surfaces_agree=True` in the capture. Both views were fed from this rig's live relay-ds route; no divergence existed to attribute to either surface.

**Seeded flag (a) — mirror-forwarded agent with NO relay connected:** NOT DRIVABLE LIVE at HEAD, with E2 evidence: the binary's only wiring of the forward target is the test-hook `SetUplinkForwardTarget` (`internal/server/forward_policy.go:345`), which `cmd/probe-server/main.go` never calls (grep: zero call sites outside tests; flags are `--relay-status/--relay-allowlist/--forward-all` only, and none wire `forwardURL`). A forward mirror (and therefore its forwarder-link via, `agentCarrier`'s mirror branch, `agent_visibility.go:418-421`) can only be created by the in-process uplink forwarder, which no live flag configures. Falsified what IS reachable (the live-relay-route via, S3 above); the mirror case remains a seeded flag for the next chain unit that adds a live wiring flag. No mismatch claim is made on an undrivable path.

### S4 — no-fabrication: token never renders except as token_fp — MATCH

`plaintext_in_network_dom` = `{WRONG-token: false, probe-token-123: false, <operator token>: false}`; `plaintext_in_peers_raw` = all four secrets `false` including upstream-B's real bearer (`srv-token`). Token cells render fp only: `present (6b5b0f2e…)`, `present (9a06bb91…)` with `title="token fingerprint …"` (§6 shape: token_present + token_fp, nothing else).

### S5 — connect form: bad upstream renders PEER_UPSTREAM_INVALID verbatim — MATCH (reproduced post-restart, not a capture artifact)

Form submit `http://not-a-ws-url:80` → DOM `[data-testid="network-error"]` = **`PEER_UPSTREAM_INVALID: upstream must be a ws:// or wss:// URL`** (typed code verbatim, E1). Raw follow-up: peers list after the refused create = 0 rows — the typing reaches the operator AND nothing was persisted.

---

## Verdict

- P18-01 (`/network`, a60d02a) and P18-02 (via column, 9e68ca7): every acceptance item falsified above rendered MATCH against raw API — chain completeness, no invention, state honesty (incl. the terminal path P18-01 never live-rendered), via column, no-fabrication, envelope surfacing.
- **Overall: PENDING-REVIEW** — held open by exactly one genuine MISMATCH: **F1**, TopologyGraph row-2 coordinate collision (direct agents + relays share x positions; two edges render as one segment). Fix is a layout offset in `web/src/components/TopologyGraph.tsx` (sandboxed to that component; no server code). When F1 lands and re-falsifies MATCH, the P18-01/P18-02 gates flip CLOSED — this artefact is the record those gates pend on.
- G-BOT-RECALL note: P18-02's close claimed "DOM capture PASS … relay-ds + direct-01 both rendered" — TRUE per label dump, and my falsification adds what its capture never asserted: the two render **on top of each other** (F1). Their PASS was honest on what it asserted; the geometry gap is new evidence.

## Evidence inventory (all on disk)

- Screenshot set (vision aux down — same 400 as P18-01/02 recorded; DOM assertions are load-bearing): `p1803_shots/S1_network.png`, `S2_badtok_retrying.png`, `S2b_terminal_auth_rejected.png`, `S3_agents.png`, `S5_form_error.png`, `F1_geometry.png`
- Raw pinned JSON: `p1803_raw_topo.json`, `p1803_raw_agents.json`, `p1803_raw_peers.json`
- Capture: `p1803_capture.py` (run log `p1803_run5.log`), F1 line-pair dump inside run5 log; rig scripts `rig_run1802.sh` (original, reused), `rig_run1803.sh` (store/upstream-B extensions)
- Code cites: `web/src/components/TopologyGraph.tsx:44-62` (F1), `web/src/pages/Network.tsx:80-97` (terminal states), `internal/server/agent_visibility.go:400-430` (via derivation), `internal/server/credentials.go:64` (store default), `internal/server/forward_policy.go:345` (test-only forward wiring)

## Deviations

1. Rig re-run (P18-02's rig dead at pickup) + `PROBE_SECRET_STORE` rig-local env + second server :17712 as deny-all typed-refusal upstream — harness props only, no server code touched (rig_run1803.sh).
2. Seeded flag (a) reported NOT-DRIVABLE with code evidence instead of being forced through a test hook — falsifying through `SetUplinkForwardTarget` would have exercised a path no deployment can reach.
3. Vision aux down (400 invalid model id, same as P18-01/02) — screenshots captured for the record; DOM+raw pairings carried the verdicts.
