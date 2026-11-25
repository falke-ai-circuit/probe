# P21-02 — INVENTORY CONTRACT: `GET /api/v1/servers` (P1 of the federation spec)

profile: architect · gateway: n/a (no dispatch from this unit) · status: contract delivered, no code written
unit: P21-02 · parent: P21 (federation P1) · spec of record: `/opt/data/probe-federation-spec.md` § P1, with § P0 read as a dependent contract
tree (read-only): `/opt/data/projects/PROBE_UPGRADE/v119wt` · branch `r119-build` · HEAD `147c7f5`
owner's need driving this unit: a top-right badge showing **which server is being viewed** — name/host, host:port, version, role — from live data, never a hardcoded label. The LOCAL entry must be sufficient to render that badge with **no second endpoint**.
downstream implementer: P18-06 (badge). P21-02 defines the shape; P18-06 draws it.

Citation form in this document: `path:line` against the tree above at the stated HEAD. Every concrete claim below was read from that tree during this unit; nothing is recalled.

---

## 0. Contract in one page

1. One endpoint: `GET /api/v1/servers` → `data.servers[]`, **local entry at index 0**, then peers. No second endpoint for the badge, no badge-specific endpoint.
2. Entry shape = the spec's P1 object, **superset** (added keys are additive and nullable; no spec key renamed, retyped or dropped).
3. Badge consumes exactly 6 fields: `id, name, listen_addr, version, role, state`. Everything else is expanded-view only.
4. `servers.json` beside `registry.json`, atomic tmp+fsync+rename (the spec's "same atomic-write pattern as registry.json" premise is **false** — see D1 — so the pattern of record is `credentials.go:249-261` plus fsync).
5. DOWN peers are **records, not absences**: retained after socket close with `last_seen`, `down_since`, `state_reason`, evicted only by the retention rule (§6).
6. Local entry fields have exactly one source each, named with `file:line` in §7. `name` is **not** `s.addr` (D3) and the host is **not** taken from `window.location`.
7. P0 reconciliation: peer `state` and agent `dispatchable` are different questions. Inventory re-implements **neither** (§9, one sentence + two tests).
8. **Pre-auth identity (§3.1, added constraint):** a badge in the shell is invisible on first open because `web/src/App.tsx:19-23` returns `<Login/>` without a token. `/api/v1/health` therefore carries a **four-key** `identity` block (`name, listen_addr, version, role`) for the login screen — same builder as `servers[0]`, byte-equal on shared keys, never `r.Host`, `PROBE_PREAUTH_IDENTITY=off` turns it off. Option B (post-login only) is named and not chosen.

---

## 1. Verified substrate — what actually exists at HEAD

Everything in this section is a read of the tree, not a plan. It is here because the contract's field sources are only as good as these facts.

| # | Fact | Evidence |
|---|---|---|
| S1 | No inventory exists anywhere: zero hits for the endpoint or the file | `grep -rn "api/v1/servers\|servers.json"` over `internal/`, `web/src` → **0 hits** |
| S2 | Server keeps its configured bind string and its version on the struct | `internal/server/server.go:31` (`addr string`), `:32` (`token string`), `:40` (`version string`) |
| S3 | Version is a single source: `Semver → Wire → Display`; server sets it once at start | `internal/version/version.go:21,30,33`; `cmd/probe-server/main.go:18`; `SetVersion` at `cmd/probe-server/main.go:154` → `server.go:281` |
| S4 | Process uptime is a package-level `startTime`, already published by health | `internal/server/server.go:206`; consumed `internal/server/api_v1.go:564`; version published `api_v1.go:558` |
| S5 | Bind address resolution: flag → env → default | `cmd/probe-server/main.go:23` (`--addr`), `:56-61` (`PROBE_ADDR`, then `localhost:7700`), wired into `NewServer` at `:146,:148` |
| S6 | The `/ws` route and its auth are the only ingress a relay uses | `internal/server/server.go:449`; auth at `internal/server/server_ws.go:63-68` |
| S7 | A relay registers as `relay_register` on channel 0 and is stored in `s.relays[relayID]` | `internal/server/relay_ws.go:100-127`, `:129-133` (id generation), `:134-140` (session construction), `:144` (map insert) |
| S8 | Relay metadata carries exactly three facts: listen addr, max agents, upstream URL | `internal/server/relay_ws.go:16-21`; sender `internal/relay/relay.go:143-152`; type `internal/relay/mux.go:29-34` |
| S9 | Relayed agents live in the **same** conn map as direct ones, as `*virtualConn` | `internal/server/relay_ws.go:207`; map type `internal/server/server.go:48`; interface `internal/server/conn_interface.go:9-13` |
| S10 | Relay cleanup removes the session map entry and every channel | `internal/server/relay_ws.go:289-310` |
| S11 | Agent liveness is stamped from the conn map by one decorator pair | `internal/server/agent_visibility.go:154-162`, `:166-175` |
| S12 | Identity of a server in topology today is `s.addr` | `internal/server/api_v1_phase4.go:140-144` (`Name: s.addr`) |
| S13 | Per-agent forward policy exists as a map + two endpoints; no server-level default exists | `internal/server/server.go:120-121`; `internal/server/api_v1.go:525-526`; node field `api_v1_phase4.go:121` |

### 1.1 Defects in the premises — the spec assumes four things that are not true at HEAD

**D1 — "same atomic-write pattern" as `registry.json` is false.** `registry.json` is written with a bare `os.WriteFile` — no temp file, no rename, no fsync (`internal/server/registry.go:534-557`, write at `:554`). Enrollment is the same (`internal/server/enrollment.go:126-137`, write at `:136`). A crash mid-write truncates the registry. The **only** atomic write in the server is the credential store's tmp+rename (`internal/server/credentials.go:249-261`). Consequence: `servers.json` must not copy the registry pattern; the contract fixes the pattern in §6.2 and does **not** retrofit `registry.json` (out of unit).

**D2 — `relay_register.token` is parsed and never checked.** The field is declared (`internal/server/relay_ws.go:98-104`, `Token string`) and has **zero** readers in the handler. Peer authentication happens only one layer earlier, at the `/ws` upgrade, against the *shared* server token (`server_ws.go:63-68`), and the relay presents exactly that shared token (`internal/relay/relay.go:119-122`). Consequences, both load-bearing for this schema: (a) every peer's `token_fp` is byte-identical to the local entry's while one shared token is in use — it identifies nothing; (b) a `token_fp` for a peer cannot be presented as verified. The schema therefore carries `token_fp_verified` and `token_fp_scope`, and P7 is what makes them meaningful. This is stated, not papered over.

**D3 — the only existing "server name" is the bind string.** Topology names the server node `s.addr` (`api_v1_phase4.go:142`). On the Radxa that renders `localhost:7700`; on OVH it renders whatever the flag said. That string is a *bind configuration*, not a host a browser can dial, and it is the reason a badge fed from topology would show a wrong label. `name` therefore gets a defined precedence chain (§7.2) and is explicitly forbidden from defaulting to `s.addr`.

**D4 — `max_agents` is a relay-side flag with no server-side storage or enforcement.** Declared flag default 100 (`cmd/probe/relay.go:20`), sent in metadata (`internal/relay/relay.go:147-151`), received into `relayMetadata.MaxAgents` (`relay_ws.go:18`) and read by nothing else. A plain server has no value at all. Hence `max_agents` is `null` for the local entry and for peers that never reported one — never `0`, never a guessed 100. P4 owns enforcement.

**D5 — a relay session has no `connected_at`.** `relaySession` holds relayID, magic, conn, writeMu, channels, channelsMu, metadata (`relay_ws.go:24-32`) — no timestamp. `transports[].connected_at` therefore cannot be populated at HEAD by any honest means. Implementation must set it at session construction (`relay_ws.go:134-140`). Until then the field renders `null`; it must not be back-filled from `metadata` or from file mtime.

**D6 — P0 has not landed in this tree.** `grep -rn "dispatchable\|liveness_source\|dispatch_error"` over `internal/` and `web/src` → **0 hits**. What exists is the *seed* of the predicate: `decorateAgentLiveness` / `decorateAgentListLiveness` (`agent_visibility.go:154-175`), which reads `s.conns` only and therefore already counts relayed agents as connected (because S9 puts them there) — but it cannot name the transport (`ws` vs `relay` vs `registry-only`). The inventory therefore treats P0's predicate as an **external dependency by name**, not as something to invent (§9).

**D7 — "within one heartbeat interval" has no heartbeat to run on.** `handleRelayControl` accepts `"heartbeat"` and discards it (`relay_ws.go:283-285`); the relay package declares the message type (`internal/relay/mux.go:20`) and **never sends one** (no ticker in `internal/relay/relay.go`). So today a peer can only be observed down when its socket closes. The contract defines peer liveness on WebSocket ping/pong (§8.3) rather than pretending an application heartbeat exists — because an idle relay sends no traffic and a silence-based rule would flip a healthy idle relay to `down`.

---

## 2. Endpoint

```
GET /api/v1/servers
auth: s.v1CheckAuth(w, r, "list") like other v1 reads (api_v1.go:324) — note this gate is
     auth-OPTIONAL unless --require-api-auth (api_v1.go:104-112; flag default false,
     cmd/probe-server/main.go:34), so in the default configuration the real boundary is
     --allowed-cidr (server.go:419-432), not a credential. This contract names that posture, it
     does not change it. The pre-auth identity block (§3.1) is the only identity surface
     reachable without a token.
response envelope: writeJSON → {"ok":true,"data":{...}}  (internal/server/api_v1.go:41-46)
errors: writeError → {"ok":false,"error":{"code","message"}} (api_v1.go:49-53)
```

Rules that make it non-ambiguous:

- **R1** The endpoint never returns 503 and never returns an empty `servers` array. The process answering the request is proof that the local entry is `up`; `servers[0]` is always the local entry.
- **R2** `data.local_id` equals `servers[0].id`. Exactly one entry has `role="local"`. Enforced by test (§12 T1), because two consumers will otherwise disagree about which is "me".
- **R3** No field is omitted. Unknown → `null` with a `*_source`/`state_reason` that says why. A missing key is unreadable; an explicit `null` is a fact. (Additive rule: this schema may gain keys in later phases; it may never lose or retype one.)
- **R4** Ordering of peers: `state="up"` before `state="down"`, then `name` ascending, then `id` ascending — deterministic so a UI never reorders between polls and diffing two responses is meaningful.

### 2.1 Response schema (normative)

```jsonc
{
  "ok": true,
  "data": {
    "generated_at": "2026-11-25T10:41:07Z",     // RFC3339, UTC, server clock
    "local_id": "srv-9f2c1a7b",
    "retention_seconds": 86400,                  // §6.3, echoed so the UI can explain an absence
    "servers": [
      {
        "id": "srv-9f2c1a7b",
        "name": "ovh-falke-1",
        "role": "local",
        "listen_addr": "ovh-falke-1:7700",
        "listen_addr_raw": "localhost:7700",
        "listen_addr_source": "hostname",        // "configured" | "hostname"
        "version": "v1.19.1",
        "uptime_seconds": 41322,
        "state": "up",
        "last_seen": "2026-11-25T10:41:07Z",
        "state_reason": "serving",
        "transports": [
          {
            "kind": "listen",
            "url": "ws://ovh-falke-1:7700/ws",
            "port": 7700,
            "ws_state": "listening",
            "connected_at": "2026-11-24T23:12:25Z",
            "source": "local_config"
          }
        ],
        "counts": { "direct_agents": 1, "relayed_agents": 3 },
        "forward_policy": null,
        "max_agents": null,
        "token_fp": "3f9a1c02",
        "token_fp_verified": true,
        "token_fp_scope": "shared"
      },
      {
        "id": "radxa-relay-443",
        "name": "radxa-relay-443",
        "role": "peer",
        "listen_addr": "radxa:7701",
        "listen_addr_raw": ":7701",
        "listen_addr_source": "hostname",
        "version": null,
        "uptime_seconds": null,
        "state": "up",
        "last_seen": "2026-11-25T10:40:58Z",
        "state_reason": "socket_open",
        "transports": [
          {
            "kind": "listen",
            "url": "ws://radxa:7701/ws",
            "port": 7701,
            "ws_state": "open",
            "connected_at": "2026-11-25T09:14:02Z",
            "source": "peer_metadata"
          },
          {
            "kind": "uplink",
            "url": null,
            "port": 443,
            "ws_state": "unknown",
            "connected_at": null,
            "source": "unreported"
          }
        ],
        "counts": { "direct_agents": 0, "relayed_agents": 1 },
        "forward_policy": null,
        "max_agents": 100,
        "token_fp": "3f9a1c02",
        "token_fp_verified": false,
        "token_fp_scope": "shared"
      },
      {
        "id": "relay-a41f0c9e",
        "name": "relay-a41f0c9e",
        "role": "peer",
        "listen_addr": "radxa:7701",
        "listen_addr_raw": ":7701",
        "listen_addr_source": "hostname",
        "version": null,
        "uptime_seconds": null,
        "state": "down",
        "last_seen": "2026-11-25T09:52:11Z",
        "state_reason": "socket_closed",
        "down_since": "2026-11-25T09:52:11Z",
        "transports": [ /* last known, ws_state="closed" */ ],
        "counts": { "direct_agents": 0, "relayed_agents": 0 },
        "forward_policy": null,
        "max_agents": 100,
        "token_fp": "3f9a1c02",
        "token_fp_verified": false,
        "token_fp_scope": "shared"
      }
    ]
  }
}
```

### 2.2 Field table (type, source, nullability, meaning)

| key | type | null? | source | meaning / rule |
|---|---|---|---|---|
| `id` | string | no | local: persisted first-run id (§6.4); peer: `relayID` (`relay_ws.go:129-133`) or `peer_hello.peer.id` (P2) | stable identity for diffing. Never the address (addresses change). |
| `name` | string | no | local: precedence chain §7.2; peer: `peer_hello.peer.name` → else `id` | human label. **Never** `s.addr` (D3). |
| `role` | enum `local\|peer` | no | derived: the process serving this response | exactly one `local` (R2). |
| `listen_addr` | string `host:port` | no | §7.3 resolution | always dialable-shaped: non-empty host, explicit port. Badge field. |
| `listen_addr_raw` | string | no | configured string (`s.addr` local; `relayMetadata.ListenAddr` peer) | preserved verbatim so a human can see what was actually configured. |
| `listen_addr_source` | enum `configured\|hostname` | no | §7.3 | says whether the host was configured or substituted from `os.Hostname()`. This is the anti-invention receipt. |
| `version` | string | yes | local: `s.version`; peer: P2 `peer_hello` | `"v1.19.1"` shape (`version.Display()`); `null` when unreported (peer at HEAD). |
| `uptime_seconds` | int64 | yes | local: `time.Since(startTime)`; peer: P2 | seconds; `null` for peers until P2. Must equal `/api/v1/health.uptime_seconds` for the local entry (T3). |
| `state` | enum `up\|down` | no | §8 | peer **transport** liveness, not agent liveness (§9). |
| `last_seen` | RFC3339 | no | §8.2 | last positive evidence of the peer (inbound frame, pong, or session creation). Local: response time. |
| `state_reason` | enum | no | §8.4 | closed vocabulary; never free text. |
| `down_since` | RFC3339 | yes | §6.3 | present iff `state="down"`. First transition into down; preserved across repeated failures. |
| `transports[]` | array | no (may be `[]`) | §7.4 / §8.5 | one entry per observed transport; `kind` separates the peer's listen side from its uplink side. |
| `transports[].kind` | enum `listen\|uplink` | no | as above | `listen` = where the peer accepts agents; `uplink` = how the peer reached us. |
| `transports[].url` | string | yes | derived: `{ws|wss}://{host}:{port}/ws` for `listen`; P2 for `uplink` | `null` for an unreported uplink — never synthesised from `listen_addr`. |
| `transports[].port` | int | no | listen: port of `listen_addr`; uplink: peer-reported dial port | **the port the peer listens on or dialled**, never the ephemeral source port of the inbound socket (§8.5). |
| `transports[].ws_state` | enum `listening\|open\|closed\|connecting\|unknown` | no | §8.5 | `listening` only for the local entry; `unknown` is legal for an uplink we cannot see. |
| `transports[].connected_at` | RFC3339 | yes | D5 new field at `relay_ws.go:134-140`; local: `startTime` | `null` when unknown. Never back-filled. |
| `transports[].source` | enum `local_config\|peer_metadata\|peer_report\|inbound_socket\|unreported` | no | as above | provenance of that transport row. |
| `counts.direct_agents` | int | no | §9.1 | agents whose conn is a direct websocket (conn map, `server.go:48`). |
| `counts.relayed_agents` | int | no | §9.1 | agents whose conn is a `*virtualConn`. |
| `forward_policy` | enum `local\|relay\|relay_all` | yes | §7.5 | server default. `null` today (no server-level default exists, S13); P4 fills it. Per-agent policy stays on `/api/v1/agents` and is **not** aggregated here (aggregation would be a second truth). |
| `max_agents` | int | yes | local: `null`; peer: `relayMetadata.MaxAgents` | `null` = unknown/not applicable — never a default (D4). |
| `token_fp` | string (8 lowercase hex) | yes | `sha256(token)[:8]` of the credential observed for that node | `null` when auth is disabled (`s.token == ""`, `server_token.go:120`) or when `PROBE_TOKEN_FP_MODE=off`. Never the token, never a prefix of it. |
| `token_fp_verified` | bool | no | local: `true` (we hold the secret); peer: `false` until P7 allowlist | says whether the fingerprint is *trusted* or merely *observed* (D2). |
| `token_fp_scope` | enum `shared\|node` | no | today `shared` for every entry (D2); `node` after P7 | tells the reader that identical fps across peers are expected, not a leak. |

**Additive-key discipline.** `listen_addr_raw`, `listen_addr_source`, `down_since`, `transports[].kind/source`, `token_fp_verified`, `token_fp_scope`, `retention_seconds`, `local_id` are additions to the spec's P1 object. Every spec key keeps its name and type. Nothing in the spec's list is removed, renamed, retyped, or made nullable that the spec declares present.

---

## 3. The badge subset (what P18-06 may read)

**Badge consumes exactly these six keys, all from `servers[0]`:**

| key | use in the badge |
|---|---|
| `name` | the label — "which server am I looking at" |
| `listen_addr` | `host:port` sub-label (the address a human can type) |
| `version` | the version chip |
| `role` | rendered as `LOCAL`; a peer entry is never used to label this badge |
| `state` | dot colour + `down` styling; the badge must render `state` even when it disagrees with its own optimism |
| `id` | stable React key + cache key for the last-known-good label |

**Implementation constraint for P18-06:** read them from `/api/v1/servers` only. `servers[0]` is guaranteed local (R2), so the badge needs no filtering and no `role === "local"` search.

**Expanded-view only (must NOT appear in the badge):** `uptime_seconds`, `last_seen`, `state_reason`, `down_since`, `transports[]` (all fields), `counts`, `forward_policy`, `max_agents`, `token_fp*`, `listen_addr_raw`, `listen_addr_source`, peers at any index. The badge is a 1-line identity chip; uptime/policy/fingerprints belong to `/network` (P3) and the server detail view.

**What the badge must NOT do:**

1. **No `window.location.host` as the primary source.** The UI is reachable through a proxy/DNAT/localhost while the server's identity is a different host entirely (`localhost:7700` behind a tunnel is the normal case, S5). `window.location` may only be used as a *fallback when the request itself failed* and then it must render visibly degraded (e.g. label = `unknown (connection lost)`), never as a silent substitute — because a substituted host is indistinguishable from a real one.
2. No DNS/`fetch` probing to "discover" the host, no reverse-lookup from the socket, no `navigator` hints.
3. No hardcoded label, no build-time constant, no `title` fallback from `document.title`, no guessing from the agent list.
4. No derivation of `name` from `listen_addr` (the badge must not re-implement the naming rule; that rule lives in one place, §7.2).
5. No client-side join with `/api/v1/health` to assemble the badge. Health may be polled for its own cards; if health is up and `/servers` is not, the badge is `unknown`, not `health.server_version` + a guessed host — a partial badge assembled from two endpoints is the second truth this contract exists to prevent.
6. No optimistic `state="up"`. Initial render is `unknown` (neutral), then the measured value; a stale response must age out visibly (`generated_at` older than 3 poll intervals ⇒ render as stale), not stay green.

**Polling:** one poll per UI refresh of the header; `ETag`/`If-None-Match` is *not* specified in this unit (see §11 non-goals), so P18-06 polls plainly and keeps the last good response for the degraded render.

---

### 3.1 Pre-auth identity — what `/api/v1/health` may expose UNAUTHENTICATED (added constraint, orchestrator mid-flight)

The owner's need is "visible **when probe is opened**". Verified substrate for this subsection — every line read from the tree, none assumed:

| fact | evidence |
|---|---|
| The app shell is not rendered without a token; the login screen replaces it entirely | `web/src/App.tsx:19-23` (`const token = getToken()` → `if (!token) return <Login />`) |
| The login screen has no host or identity line today — logo, subtitle, form | `web/src/pages/Login.tsx:19-45` |
| `/api/v1/health` performs **no** auth check at all | `internal/server/api_v1.go:541-566` (handler body contains no `v1CheckAuth`) |
| `/api/v1/agents/{id}/health` **does** auth-check | `internal/server/api_v1.go:621-625` (`v1CheckAuth(w, r, "health")`) |
| `v1CheckAuth` is itself auth-optional unless `--require-api-auth` is set | `internal/server/api_v1.go:87-112` (fall-through at `:104-112`), flag default `false` at `cmd/probe-server/main.go:34` |
| `/api/v1/*` is network-filtered by `--allowed-cidr`; only `/ws` bypasses | `internal/server/server.go:419-432`; default ranges `cmd/probe-server/main.go:41` |
| The frontend has no configured server address — relative base only | `web/src/api/client.ts:8` (`const BASE = '/api/v1'`) |

Consequence, stated without softening: **a badge placed in the shell is invisible at the exact moment the owner named**, because `App.tsx:19-23` returns `<Login />` before the shell exists. The requirement therefore has two satisfiable shapes, and the choice is named rather than assumed.

#### Option A — chosen: a four-key public `identity` block on `/api/v1/health`

```jsonc
// GET /api/v1/health  — existing keys unchanged (api_v1.go:556-565), one addition
{
  "ok": true,
  "data": {
    "status": "ok",
    "server_version": "v1.19.1",
    "uptime_seconds": 41322,
    "identity": {
      "name": "ovh-falke-1",
      "listen_addr": "ovh-falke-1:7700",
      "version": "v1.19.1",
      "role": "local"
    }
  }
}
```

Boundary, in the strongest terms this contract has:

1. **Exactly four keys:** `name`, `listen_addr`, `version`, `role`. **Not** `token_fp`, `token_fp_verified`, `token_fp_scope`, `id`, `counts`, `transports`, `last_seen`, `state`, `state_reason`, `down_since`, `forward_policy`, `max_agents`, `uptime_seconds` (uptime stays where it already is, as its own existing key). The identity block is a label, not a status surface: if it grows past these four it has become `/api/v1/servers` without that endpoint's semantics.
2. **One projection, two surfaces.** `identity` is produced by the **same** builder that produces `servers[0]` (§7), projected through a hard-coded four-key allowlist. For the three shared keys, values must be **byte-identical** to `servers[0].{name,listen_addr,version}` — enforced by test T10. Two independently computed identities is exactly the second truth §9 forbids, arriving through a different door.
3. **`role` is the constant `"local"`.** Pre-auth there is exactly one server: the one that answered. A login screen that names peers tells an unauthenticated caller the topology.
4. **The host is never `r.Host`, never the `Host` header, never `X-Forwarded-Host`.** Source is §7.3 only (`s.addr` → `os.Hostname()` substitution). A login form is the last place in the product where a request-controlled string may become the displayed server name — that is Host-header content injection into a credential prompt (D8).
5. **`version` here is not new information.** `/api/v1/health` already returns `server_version` unauthenticated (`api_v1.go:558`); the block re-labels an existing leak for the login screen rather than widening it.
6. **Nothing cached client-side.** The login screen renders `identity` from the response it just received, and a neutral `unknown` if the request fails. No `localStorage` copy of the name: a cached label survives a server switch and then lies in precisely the way this unit exists to prevent.
7. **Off-switch: `PROBE_PREAUTH_IDENTITY=on|off`** (default `on`). `off` ⇒ `"identity": null` (key present, per R3), login renders generic `PROBE`, post-login badge unaffected.

**Exposure accepted, stated plainly.** With Option A any client that can reach the login page learns deployment name, `host:port`, role and version. That audience is bounded by `--allowed-cidr` (`server.go:419-432`), **not** by credentials — and on the Radxa that flag is documented plant-wide (`192.168.0.0/16`, `10.70.0.0/16`, spec §P7), so "anyone on the plant network" is the honest description of who sees this. Version disclosure is CVE-mapping surface; the name discloses a naming convention (`ovh-falke-1` ⇒ provider + role + index). Accepted because the requirement is identity-at-a-glance on the first screen, the alternative fails the stated need, and the knob exists for deployments that disagree.

#### Option B — named, not chosen: identity post-login only

`/api/v1/health` unchanged; login screen shows no host line; the badge appears only after authentication. Cheapest, leaks nothing, but it satisfies "visible once signed in", **not** "visible when probe is opened". Recorded here so the trade is visible to whoever reads the requirement later instead of being re-litigated as an oversight.

#### Data flow P18-06 implements (both states)

```
no token → App.tsx:19-23 → <Login/> → GET /api/v1/health → data.identity.{name,listen_addr,version,role}
token    → shell + badge → GET /api/v1/servers → servers[0].{id,name,listen_addr,version,role,state}
```

Pre-login there is no `state` and no `id` (not published pre-auth), so the pre-auth chip renders **neutral with no status dot** — `state` is a measurement and there is none. Post-login the six-key subset of §3 applies unchanged. If a client holds both payloads, `servers[0]` is authoritative; a divergence between `identity` and `servers[0]` on a shared key is a defect (T10), never something the UI silently resolves.

---

## 4. What the endpoint must never contain

- No token, token prefix, or any reversible form of a secret. `token_fp` is `sha256(token)` truncated to 8 hex — see §6.5 for the entropy caveat and the off-switch.
- No agent rows. Agent liveness is P0's surface. The inventory carries *counts only*; a per-agent list here would be the second truth (§9).
- No filesystem paths, no registry path, no environment dump, no command lines.
- No free-text error strings in `state_reason` (closed vocabulary, §8.4) — free text is where two implementations start to disagree.
- No `null` for `state`, `role`, `id`, `name`, `listen_addr`, `last_seen`, `state_reason`, `counts`, `transports`.

---

## 5. Failure paths (designed before the success path)

| F | condition | required behaviour |
|---|---|---|
| F1 | `servers.json` missing | not an error. Build local entry, treat peer set as empty, create the file on first peer event. `state_reason` unaffected. Log once at info. |
| F2 | `servers.json` unreadable/corrupt JSON | **fail-closed toward truth, not toward invention**: serve local entry + zero peers, log one line with the parse error, and move the bad file to `servers.json.corrupt-<unix>` (never silently overwrite evidence). Do not fabricate peers from the damaged file. |
| F3 | torn write (crash between write and rename) | impossible by construction (§6.2): readers see either the old file or the new. `.tmp` leftovers are ignored on read and overwritten on next write. |
| F4 | duplicate peer id from two live sockets | last registration wins for identity, but the older socket is not deleted from the map by id-collision — the newer session records `state_reason="duplicate_id_replaced"` on the displaced record (down) so an operator sees the collision rather than a silent swap. |
| F5 | relay never reports metadata | `listen_addr` uses the same hostname substitution as the local entry, `listen_addr_source="hostname"`, `max_agents=null`, `version=null`. Never invent `:7701`. |
| F6 | local `addr` has port `0` (ephemeral) | `listen_addr` carries the **actual bound port** if known (from the listener), else `listen_addr=null` and `state_reason="listen_port_unknown"`. Do not print `:0`. |
| F7 | clock skew (peer `last_seen` in the future) | clamp to `generated_at`, `state_reason` unchanged; log once. Age math must never go negative. |
| F8 | TLS enabled (cert+key) | local transport `url` scheme is `wss` (`cmd/probe-server/main.go:135`), `port` unchanged, `ws_state="listening"`. |
| F9 | auth disabled (`s.token == ""`) | `token_fp=null`, `token_fp_verified=false`, `token_fp_scope="shared"`. Key must be present (R3). |
| F10 | handler panics building the peer list | recover per-peer; a peer whose record cannot be marshalled is emitted as `{id, role:"peer", state:"down", state_reason:"record_unreadable", …}`. One bad record never blanks the list — the badge's own server must still render. |

---

## 6. Persistence — `servers.json`

### 6.1 Location and name

`servers.json` lives in the **same directory** as the registry file, resolved from the registry path actually in use: `filepath.Join(filepath.Dir(registryPath), "servers.json")`. `registryPath` is resolved at `cmd/probe-server/main.go:25,65-69` (flag → `PROBE_REGISTRY` → `/tmp/probe-registry.json`) and handed to `NewServer` at `:146,:148`, which passes it to `NewRegistry` (`server.go:210`). Deriving from that one value is what makes "beside registry.json" true in production (`/data/...` on the container) and in tests (`t.TempDir()`), without a second path knob. A `PROBE_SERVERS_DB` override exists for tests and for the multi-instance case; the default must be derived, never hardcoded to `/tmp`.

Reason for a separate file (not a block inside `registry.json`): the registry is written on every agent heartbeat through the non-atomic path (D1); adding server records there would couple peer state to agent churn and widen the write-amplification window. Separate file, separate cadence, atomic write.

### 6.2 Write pattern (the one the spec meant)

```
1. raw := json.MarshalIndent(doc, "", "  ")
2. tmp := path + ".tmp"                      // same directory ⇒ same filesystem ⇒ rename is atomic
3. f := os.OpenFile(tmp, O_WRONLY|O_CREATE|O_TRUNC, 0600)
4. f.Write(raw); f.Sync()                    // fsync the data before publishing the name
5. f.Close(); os.Rename(tmp, path)           // atomic replace
6. optional: open dir, dir.Sync()            // best-effort, log-only on failure
```

Shape taken from `internal/server/credentials.go:249-261` (tmp + rename, the only atomic writer in the tree — D1) with `Sync()` added and mode `0600` (peer records include fingerprints, not secrets, but the file has no reason to be world-readable). **Not** `registry.go:534-557`.

Write coalescing: dirty-flag driven, flushed at most once per second and always within 5 s of a state transition; a state transition (`up→down`, `down→up`, new peer) forces an immediate flush so a crash cannot lose the distinction between "down" and "never existed". `last_seen` is persisted at 5-second granularity (rounding down) to keep write volume bounded — 20 s of ping interval (`registry.go:28`) never produces a write per agent.

### 6.3 Down retention — the distinction the owner asked for

| rule | value | why |
|---|---|---|
| retention window | `retention_seconds=86400` (24 h) from `down_since` | long enough that a relay rebooted overnight is still explained in the morning, short enough that a decommissioned peer disappears within a working day. Knob: `PROBE_SERVERS_RETENTION` (duration string), echo the effective value in `data.retention_seconds`. |
| max retained records | 64 peers | a bounded file. On overflow evict the **oldest `state="down"`** record first; a `state="up"` record is never evicted by pressure (that would hide a live peer, which is the failure mode this whole unit exists to prevent). If all 64 are up, refuse new *down* records and log; do not grow unbounded. |
| absence vs down | absence = never observed, or observed and aged out past retention. down = observed, then lost, and still inside the window, carrying `last_seen` + `down_since` + `state_reason` | this is the owner's requirement in one line: *"Killing a relay flips that peer to `state=down` … preserving `last_seen` and a `state_reason`"* (spec § P1 acceptance). |
| deletion | only by retention, or by an explicit future endpoint (not in this unit) | a disconnect must never delete the record (S10 deletes the *session*; the inventory record is created first, then marked down). |

### 6.4 On-disk document

```jsonc
{
  "version": 1,
  "updated_at": "2026-11-25T10:41:07Z",
  "local": { "id": "srv-9f2c1a7b", "first_seen": "2026-11-24T23:12:20Z" },
  "peers": {
    "radxa-relay-443": {
      "id": "radxa-relay-443",
      "name": "radxa-relay-443",
      "listen_addr_raw": ":7701",
      "max_agents": 100,
      "version": null,
      "first_seen": "2026-11-25T09:14:02Z",
      "last_seen": "2026-11-25T09:52:11Z",
      "down_since": "2026-11-25T09:52:11Z",
      "state_reason": "socket_closed",
      "last_transports": [ { "kind": "listen", "url": "ws://radxa:7701/ws", "port": 7701, "ws_state": "closed", "connected_at": "2026-11-25T09:14:02Z", "source": "peer_metadata" } ],
      "token_fp": "3f9a1c02",
      "token_fp_scope": "shared"
    }
  }
}
```

`local.id` is the only local fact persisted: it must survive restarts so a UI's cached choice of "which server" stays stable, and it cannot be derived from `addr` (which changes when the box moves). Generated once as `"srv-" + hex(4 random bytes)` on first run; if the file exists, the persisted id wins and is never regenerated (a regenerated id would look like a different server to any consumer that keyed on it).

**No secrets in this file.** `token_fp` only, `token_fp_scope` only. No `token`, no relay token, no enrollment material. Mode `0600` regardless.

### 6.5 `token_fp` entropy caveat (named, not hidden)

`sha256(token)[:8]` is 32 bits. For the current shared, human-chosen node token (`falke-admin-2026` per spec §P7) that is a *verifier*: 4 billion guesses offline against 32 bits is minutes on one GPU, and the plaintext space of a human-chosen token is far smaller. The spec mandates the fingerprint, so it ships — but: (a) `token_fp_scope="shared"` tells the reader it is not a node distinguisher; (b) `PROBE_TOKEN_FP_MODE=off` (knob) makes `token_fp` `null` for deployments that treat it as a leak; (c) P7 replaces the shared token with per-node credentials and raises both the entropy and the meaning of the field. This note exists so nobody "fixes" the fingerprint by lengthening it and calls the entropy problem solved.

---

## 7. Local entry population — exactly one source per field

### 7.1 Identity

| field | source | note |
|---|---|---|
| `role` | constant `"local"` for the entry built by this process | the process answering is the local server |
| `id` | persisted `local.id` (§6.4); on first run `"srv-" + hex(4 random)` | never `s.addr`, never hostname — both change |
| `state` | constant `"up"` | the response's existence is the evidence; a killed process cannot answer |
| `state_reason` | constant `"serving"` | closed vocabulary |
| `last_seen` | `time.Now().UTC()` at response build | local clock is the reference clock |

### 7.2 `name` — the chain, so nobody invents a hostname

```
1. PROBE_SERVER_NAME (env)                        // explicit, operator intent
2. --server-name flag                             // new flag, same precedence as other flags
3. os.Hostname()                                  // kernel truth; pattern already used at
                                                  // internal/platform/platform.go:101 and
                                                  // internal/server/server_ws.go:121
4. "probe"                                        // only if os.Hostname() errors
```

Forbidden as sources: `s.addr` (`server.go:31`) — that is the topology's current mistake (D3); `version.Semver`; the UI title; the registry path; the first agent's name.

`os.Hostname()` is the default because it is the only name that is *the machine's own* — it cannot disagree with reality, and it is exactly what the owner asked for ("which server am I looking at"). It is also the same value that appears in the local `transports[].url` host, so name and address agree by construction when the substitution path is active.

### 7.3 `listen_addr` / `listen_addr_raw` / `listen_addr_source`

Input: `s.addr` — set from `NewServer(addr, …)` (`server.go:209`, field `:31`), fed by `--addr` → `PROBE_ADDR` → `localhost:7700` (`cmd/probe-server/main.go:23,56-61`).

```
raw := s.addr
host, port := split(raw)
if host is "" | "localhost" | "127.0.0.1" | "::1" | "0.0.0.0" | "::" :
      host = os.Hostname(); source = "hostname"
else: source = "configured"
listen_addr = host + ":" + port        // port from the configured string, or the bound port (§5 F6)
```

Why substitute at all: `localhost:7700` is *true* for a browser on the same box and *false* for every other consumer, and the badge is precisely the consumer that must not show a false label. Why `os.Hostname()` and not an interface IP: the hostname is a fact the OS reports; picking an interface IP is a guess about routing, and the box may have five. The substitution is declared in `listen_addr_source`, so nothing is hidden. A deployment behind DNAT (the OVH 443 case) resolves this properly only through P2's peer-reported uplink — a *peer* row may name a public URL; a *local* row does not pretend to know its own public address.

### 7.4 `version`, `uptime_seconds`, `transports`

| field | source | note |
|---|---|---|
| `version` | `s.version` (`server.go:40`), set once at `cmd/probe-server/main.go:154` → `server.go:281`, value `version.Display()` (`main.go:18`, `internal/version/version.go:33`, `Semver` at `:21`) | identical string to `/api/v1/health.server_version` (`api_v1.go:558`) — assert equality (T3). |
| `uptime_seconds` | `int64(time.Since(startTime).Seconds())` — `startTime` at `server.go:206`, same expression as `api_v1.go:564` | process uptime, not listener uptime. Must equal health's value (T3). |
| `transports[0].kind` | constant `"listen"` | |
| `transports[0].url` | `ws://` or `wss://` + `listen_addr` + `/ws` — scheme from `useTLS := *certFile != "" && *keyFile != ""` (`cmd/probe-server/main.go:135`), path from the route registration `server.go:449` | Building the URL from `listen_addr` (not `s.addr`) keeps name/host/URL consistent. |
| `transports[0].port` | port component of `listen_addr` (or the bound port, F6) | |
| `transports[0].ws_state` | `"listening"` | the local listener is by definition listening |
| `transports[0].connected_at` | `startTime` (`server.go:206`) | the process began listening at start |
| `transports[0].source` | `"local_config"` | |

If this server *also* runs as a relay (server-as-relay, S13), its own uplink is an additional `kind="uplink"` row sourced from P2 (`source="peer_report"`), never inferred from `metadata` — an uplink we have not been told about is `ws_state="unknown"`, `url=null`.

### 7.5 `forward_policy`, `max_agents`, `counts`, `token_fp`

| field | local value today | where it comes from | when it changes |
|---|---|---|---|
| `forward_policy` | `null` | no server-level default exists (S13): the only policy is per-agent (`server.go:120-121`, endpoints `api_v1.go:525-526`) | P4 adds a server default; then this field carries it and the per-agent map stays on `/api/v1/agents` |
| `max_agents` | `null` | server has no max-agents concept (D4); the flag lives in relay mode (`cmd/probe/relay.go:20`) | P4 enforces it; then a real int |
| `counts.direct_agents` | computed, §9.1 | conn map `server.go:48` minus `*virtualConn` | every connect/disconnect |
| `counts.relayed_agents` | computed, §9.1 | `*virtualConn` count, which is the relayed agents (S9) | every `channel_open` / `channel_close` (`relay_ws.go:242-281`) |
| `token_fp` | `sha256(s.token)[:8]` lower hex; `null` if `s.token == ""` (`server_token.go:120`) | `token_fp` computed at response build, never stored (a stored fp drifts from a rotated token) | token rotation (`server_token.go` rotate path) |
| `token_fp_verified` | `true` (local holds its own secret) | — | — |
| `token_fp_scope` | `"shared"` | D2 | P7 sets `"node"` |

---

## 8. Peer population and peer state

### 8.1 Where peers come from at HEAD

`relaySession` is created at `internal/server/relay_ws.go:134-140` and inserted into `s.relays[relayID]` at `:144`; `relayID` is `reg.RelayID` or `"relay-<unixmilli>"` (`:129-133`); `metadata` holds `listen_addr`, `max_agents`, `upstream` (`:16-21`, sender `internal/relay/relay.go:143-152`). This is the complete peer identity available without P2 — id, listen addr, max agents, upstream URL. **`version` is not in the metadata** and must stay `null` until P2's `peer_hello` (`spec § P2`, `relay_ws.go:112` anchor).

Peer record lifecycle:

```
on relay_register (relay_ws.go:100-145)
    upsert peer {id, name=id, listen_addr_raw=metadata.ListenAddr, max_agents=metadata.MaxAgents,
                 state="up", state_reason="socket_open", last_seen=now,
                 connected_at=new field (D5), transports=[listen row from metadata]}
    flush immediately (§6.2)

on inbound frame from peer (relay_ws.go main read loop)
    last_seen = now

on pong / successful ping (§8.3)
    last_seen = now; if state=="down": state="up", state_reason="pong", down_since=null

on socket close (relay_ws.go:154-160 → cleanupRelaySession :289-310)
    BEFORE deleting the session: state="down", down_since=now, state_reason="socket_closed",
    last_seen unchanged, transports[].ws_state="closed", counts zeroed, flush immediately
```

The ordering in the last item is the whole point: `cleanupRelaySession` currently deletes the session and its channels (`:289-310`) with no record written — so an inventory that reads the live map alone could only report *absence*, exactly what the owner says is not enough. The down-record write must happen on the close path, before deletion.

### 8.2 `last_seen` — positive evidence only

`last_seen` is stamped by events that prove the peer was alive: registration, any inbound frame, or a completed ping/pong. It is **never** stamped by (a) our own read from disk, (b) our own response generation, (c) a scan over the peer map. A file-loaded record keeps the `last_seen` it was saved with — a restart must not refresh an absent peer into freshness (that would manufacture liveness out of a file read).

### 8.3 Peer liveness — ping/pong, because app-level heartbeat does not exist (D7)

- Server pings each relay socket on an interval (WebSocket control frame; gorilla exposes `SetPongHandler`/`WriteControl`). Interval: reuse `pingProbeInterval` (20 s, `internal/server/registry.go:28`) — one cadence for the process, not a second one.
- `state="down"` when either (i) the socket closes, or (ii) no pong within `2 × interval` (40 s), with `state_reason="pong_timeout"`.
- An **idle relay with no agents must not be flipped down**: ping/pong is answered by the peer's WebSocket stack without application involvement (`internal/relay` already speaks gorilla), so silence on the agent channels is irrelevant. This is why the rule is not "no frames for 40 s" — that rule would mark a healthy idle relay down, and the spec's own acceptance (`kill a relay → flips to down`) would pass while the more common case (idle relay) silently lied.
- If `handleRelayControl`'s dead `"heartbeat"` case (`relay_ws.go:283-285`) is later given a sender, it becomes an *additional* `last_seen` stamp, not a replacement for ping/pong; the two must not diverge in meaning.

**D8 — the two strings a naive badge would use for "which server is this" are both wrong, and one is attacker-controlled.** The frontend has no configured server address: `const BASE = '/api/v1'` (`web/src/api/client.ts:8`), so the browser knows only the URL the user typed — behind a tunnel or DNAT (`localhost:7700` in one case, `139.99.209.235:443 → 10.10.10.100:7701` in another, spec §P9) that URL is not the server's identity. On the server side the symmetric shortcut, `r.Host`, is request-controlled: rendering it on a **login page** turns a Host header into displayed "which server am I on" text, i.e. content injection into a credential prompt. Rule: identity host comes from §7.3 only. `r.Host`, `Host`, `X-Forwarded-Host`, and `X-Real-Host` are forbidden in every identity path, pre- and post-auth (test T11).

### 8.4 `state_reason` — closed vocabulary

`serving` · `socket_open` · `pong` · `socket_closed` · `pong_timeout` · `duplicate_id_replaced` · `uplink_unreported` · `listen_port_unknown` · `record_unreadable` · `retention_expired` (rendered for the last poll before eviction, if a UI held it).

Anything else is a bug. `state_reason` never carries an error string — the string belongs in the server log, next to the peer id.

### 8.5 `transports[]` for a peer — two different links, never merged

A peer has two distinguishable transport facts, and merging them is how a UI ends up labelling a 443 relay as 7701:

| row | meaning | source | port comes from |
|---|---|---|---|
| `kind="listen"` | where the peer accepts **agents** | `relayMetadata.ListenAddr` (`relay_ws.go:17`) | the metadata string's port (`:7701` default, `cmd/probe/relay.go:20`) |
| `kind="uplink"` | how the peer reaches **us** | P2 `peer_hello` / peer status; `null` at HEAD | the peer's own dial port — this is the only honest source for the spec's `port=443` case (§P1 acceptance) |

Explicitly rejected: the inbound socket's `conn.RemoteAddr()`. It gives the *ephemeral source port* of the peer's dialer (e.g. `51234`), which would (a) contradict the spec's acceptance values and (b) change on every reconnect, making the field undiffable. `RemoteAddr` may be used only as a last-resort host hint when metadata is absent, and then `listen_addr_source="hostname"`-style provenance applies (`transports[].source="inbound_socket"`), with `port` still taken from the peer's listen side, never the source port.

At HEAD, therefore, the second acceptance row (`radxa-relay-443` with `port=443`) is **not satisfiable** — `port` must be `null` or the listen port with `source="unreported"`. This is a P2 dependency stated here, not a gap to be closed by guessing: the `443` exists only in the relay's own configuration (`--upstream wss://…:443/ws`, `internal/relay/relay.go:143-152`).

---

## 9. P0 reconciliation — one truth per question

**The sentence (acceptance quote):**

> `/api/v1/servers[].state` answers *is this peer's transport to me alive* and is derived from the relay session map that P0 reads for `liveness_source="relay"`; `/api/v1/agents[].dispatchable` answers *can I dispatch to this agent* and is P0's single predicate over the same `s.conns` map (seed: `decorateAgentLiveness`, `agent_visibility.go:154-162`, which already counts relayed conns because `relay_ws.go:207` puts them there). The inventory **reuses** that predicate for `counts` and **reimplements neither** — it publishes no per-agent liveness field, no `dispatchable`, no second predicate; and it never derives peer `state` from agent rows, because a peer can be `up` with zero agents and an agent can be `dispatchable` on a peer we have not been told about (P2 absent).

Concretely, the rules the coder must implement:

1. **P1 (inventory) does not own liveness.** `counts.direct_agents` and `counts.relayed_agents` are computed by calling whatever P0 exposes (once P0 lands: its predicate/field; today: the existing decorator's own source of truth, the conn map with the `*virtualConn` type assertion — `server.go:48`, `relay_ws.go:207`). No new function that answers "is agent X alive".
2. **Peer `state` is not agent state** and never appears on `/api/v1/agents`; agent liveness never appears here. The names are deliberately different (`state` on servers, `dispatchable`/`liveness_source` on agents) so a reader cannot confuse them.
3. **No aggregation across surfaces.** `counts` is a count of rows already classified for another purpose; it is not a health verdict and must not be used by the UI to colour anything agent-related.
4. **Dependency ordering.** If P0 lands first, the inventory calls it. If P1 lands first (likely — P0 is a blocker but this unit is being specified now), the conn-map read is the *single* site that P0 later replaces: one function, named in the implementation, marked with a `// P0: replace with liveness predicate` comment, and no second site anywhere. Under no ordering may a third read of `s.conns` be introduced into the tree (`agent_visibility.go:154-175` already has two; P1 adds at most one, in the counts builder).

Two tests enforce this (T4/T5, §12).

---

## 10. Observability + control (G-TRACEABLE-CONTROLLABLE)

| knob | default | effect |
|---|---|---|
| `PROBE_SERVERS_DB` | derived `filepath.Dir(registryPath)/servers.json` | path override (tests, multi-instance) |
| `PROBE_SERVERS_RETENTION` | `24h` | down-peer retention; echoed as `data.retention_seconds` |
| `PROBE_SERVERS_MAX_PEERS` | `64` | retained peer cap (§6.3) |
| `PROBE_SERVER_NAME` | unset → `os.Hostname()` | local `name` (§7.2) |
| `PROBE_TOKEN_FP_MODE` | `on` | `off` → `token_fp=null` everywhere (§6.5) |
| `PROBE_PREAUTH_IDENTITY` | `on` | `off` → `health.data.identity=null`; login screen renders generic `PROBE` (§3.1) |
| `PROBE_SERVERS_WRITE_MIN_INTERVAL` | `1s` | write coalescing floor (§6.2) |

Log lines (one per event, peer id in every one, no secrets):
- `[servers] peer up id=%s listen=%s max_agents=%d`
- `[servers] peer down id=%s reason=%s last_seen=%s`
- `[servers] retained %d peers (%d down, %d up) — oldest down=%s`
- `[servers] servers.json corrupt: %v — moved to %s, serving local only`
- `[servers] token fingerprint disabled by PROBE_TOKEN_FP_MODE=off`

Decision log (in the blueprint, not the code): every departure from the spec's literal wording is recorded in §1.1 (D1–D7) with the reason; the reader of a later phase can see *why* `max_agents` is null rather than 100 without re-deriving it.

---

## 11. Non-goals (explicit, per unit boundary)

1. **No code.** This unit writes no Go, no TS, no handler, no frontend. P21-02 is the contract; implementation is a separate unit.
2. **No badge work.** P18-06 implements the badge against §3. This unit does not draw, style, or mount it.
3. **No new liveness semantics.** P0 owns agent liveness (D6). This unit consumes it.
4. **No P2.** `peer_hello`, `/api/v1/relay/status`, uplink edges, downstream topology merge — all out. Consequently `version`/`uptime_seconds` for peers ship as `null` and the 443-port acceptance row is unsatisfiable until P2.
5. **No P4.** `forward_policy` enforcement, `max_agents` enforcement, loop prevention, per-link policy — out. Fields stay `null` rather than carrying a made-up default.
6. **No P7.** Per-node credentials, relay allowlist, rotation. `token_fp_scope` stays `"shared"`; the fingerprint is honest about being a non-distinguisher.
7. **No registry/enrollment atomicity retrofit.** D1 is recorded here and fixed elsewhere (retrofitting `registry.json` touches every write path and is not this unit).
8. **No new persistence framework.** Plain JSON + tmp/rename, same as credentials. No SQLite, no lock file protocol, no daemon, no background service beyond the existing process.
9. **No `EDIT`/`DELETE` endpoints** (no peer deletion, no renaming, no policy mutation over this surface). Read-only resource, matching the local+peers requirement.
10. **No `ETag`/`If-None-Match`, no WebSocket push for inventory** in this unit. Polling is sufficient for a badge and a network page; caching is an optimisation with its own truth risks (a stale 304 would freeze a `down` transition).
11. **No client-side host discovery** of any kind (§3, badge prohibitions 1–2).
12. **No topology rewrite.** `/api/v1/topology` keeps its current shape (`api_v1_phase4.go:100-160`); P3 merges, it does not replace.
13. **No login-screen redesign.** P18-06 adds the host line to `web/src/pages/Login.tsx`; this unit defines the payload it reads (§3.1) and nothing about its layout.
14. **No new pre-auth endpoint.** Identity rides on the existing `/api/v1/health`; a `/api/v1/whoami` would be a third identity surface and is refused by design.
15. **No auth-posture change.** Adding `identity` does not move `/api/v1/health` behind `v1CheckAuth` and does not tighten `--allowed-cidr`; both are separate decisions (§13 O9).

---

## 12. Acceptance — re-runnable probes (a reviewer executes these)

Preconditions: a server started with `--registry /tmp/p21/registry.json` and a relay pointed `--upstream ws://127.0.0.1:7700/ws`.

**T1 — shape and local-first**
```
curl -s -H "Authorization: Bearer $TOKEN" localhost:7700/api/v1/servers | jq -e '
  .ok and (.data.servers|length>=1)
  and (.data.servers[0].role=="local")
  and (.data.servers[0].id==.data.local_id)
  and ([.data.servers[]|select(.role=="local")]|length==1)
  and (.data.servers[0]|has("name") and has("listen_addr") and has("version")
       and has("uptime_seconds") and has("state") and has("state_reason")
       and has("transports") and has("counts") and has("token_fp"))'
```
PASS = exit 0. Also: `jq -r '.data.servers[]|.role'` → local first.

**T2 — local field sources agree with the surfaces that own them**
```
curl -s -H "Authorization: Bearer $TOKEN" localhost:7700/api/v1/health | jq -r '.data.server_version,.data.uptime_seconds'
curl -s -H "Authorization: Bearer $TOKEN" localhost:7700/api/v1/servers | jq -r '.data.servers[0].version,.data.servers[0].uptime_seconds'
```
PASS = version identical; uptime within 2 s (same `startTime`, `server.go:206`).

**T3 — `name` is not the bind string, and the raw string is preserved**
```
curl -s … /api/v1/servers | jq -r '.data.servers[0] | [.name,.listen_addr,.listen_addr_raw,.listen_addr_source] | @tsv'
```
Start the server with `--addr localhost:7700`. PASS = `name` equals `os.Hostname()` (or `PROBE_SERVER_NAME`), `listen_addr` is `<hostname>:7700`, `listen_addr_raw=="localhost:7700"`, `listen_addr_source=="hostname"`. FAIL if `name=="localhost:7700"` — that is D3 reproduced.

**T4 — peer appears up with its listen port; the badge-relevant local entry never depends on peers**
```
# kill all relays, then:
curl -s … /api/v1/servers | jq -e '.data.servers[0].role=="local" and .data.servers[0].state=="up"'
```
PASS with zero peers. The badge must be renderable from this response alone.

**T5 — down is retained, absence is different**
```
start relay → jq '.data.servers[]|select(.role=="peer")|{id,state,listen_addr,transports}'
kill -9 relay → wait 2× interval (40 s)
jq '.data.servers[]|select(.role=="peer")|{id,state,state_reason,last_seen,down_since}'
jq '.data.servers|map(select(.role=="peer"))|length'   # record still present
```
PASS = `state=="down"`, `state_reason=="socket_closed"` (or `pong_timeout`), `last_seen` preserved and older than now, `down_since` present, record count unchanged. Then truncate `PROBE_SERVERS_RETENTION=1s`, restart, wait: PASS = record gone and `retention_seconds==1`.

**T6 — one truth: the inventory has no liveness implementation of its own**
```
grep -rn "dispatchable\|liveness_source" internal/server/ | grep -v _test   # only P0's sites, never a new one in the inventory file
grep -c "s.conns\[" internal/server/servers*.go                              # ≤1 (the counts builder)
grep -rn "decorateAgentLiveness\|decorateAgentListLiveness" internal/server/  # inventory must CALL these, not reimplement
```
PASS = at most one conn-map read in the inventory file, no new predicate function, no per-agent fields in the response.

**T7 — no secret, no path**
```
curl -s … /api/v1/servers | grep -Ei 'bearer|token":|/tmp/|/data/' ; echo exit=$?
ls -l $(dirname /tmp/p21/registry.json)/servers.json   # mode 0600
python3 -c "import json;d=json.load(open('/tmp/p21/servers.json'));print('token' in json.dumps(d).lower())"
```
PASS = no match for a raw token or path in the response; file mode `0600`; on-disk doc contains `token_fp` but no `"token"` key.

**T8 — atomic write / corruption policy**
```
printf '{not json' > /tmp/p21/servers.json ; restart ; curl … /api/v1/servers | jq '.data.servers|length'
ls /tmp/p21/servers.json.corrupt-*
```
PASS = server answers (local entry present, 1 server), one `.corrupt-<unix>` file exists, log line `servers.json corrupt` present exactly once.

**T9 — badge contract**
Static check on P18-06's implementation (review-time): no `window.location.host` as primary source; only `servers[0].{id,name,listen_addr,version,role,state}` read; no `/api/v1/health` join. PASS = grep for `window.location` returns either nothing or one fallback site guarded by a failed-request branch.

**T10 — pre-auth identity equals the authenticated identity (one projection, §3.1 rule 2)**
```
# no credential at all:
curl -s localhost:7700/api/v1/health | jq -e '.ok and (.data.identity|keys_unsorted|sort == ["listen_addr","name","role","version"])'
curl -s localhost:7700/api/v1/health | jq -r '.data.identity|[.name,.listen_addr,.version,.role]|@tsv'
# with credential:
curl -s -H "Authorization: Bearer $TOKEN" localhost:7700/api/v1/servers | jq -r '.data.servers[0]|[.name,.listen_addr,.version]|@tsv'
```
PASS = exactly four keys; `role=="local"`; the three shared values byte-identical across both responses; the unauthenticated call returns 200 with `identity` present. FAIL if `identity` exists only with a token (that is Option B, not A) or if any value differs between surfaces.

**T11 — identity cannot be steered by the request (D8)**
```
curl -s -H "Host: evil.example.com" localhost:7700/api/v1/health | jq -r '.data.identity.listen_addr,.data.identity.name'
curl -s -H "X-Forwarded-Host: evil.example.com" localhost:7700/api/v1/health | jq -r '.data.identity.listen_addr,.data.identity.name'
```
PASS = neither response contains `evil.example.com`. FAIL = the header reached the payload.

---

## 13. Open decisions (need conductor/Goran, not architect)

| # | decision | options | contract's provisional answer |
|---|---|---|---|
| O1 | retention window | 1 h / 24 h / 7 d | 24 h (§6.3); knob exists either way |
| O2 | `name` default | `os.Hostname()` / operator must set `PROBE_SERVER_NAME` / DNS FQDN | `os.Hostname()` with explicit override (§7.2) |
| O3 | local host substitution when addr is `localhost`/`0.0.0.0` | hostname (proposed) / first non-loopback interface IP / refuse and null | hostname + `listen_addr_source` receipt (§7.3) |
| O4 | `max_agents` for a non-relay server | `null` (proposed) / `0` = unlimited / omit | `null` (D4) |
| O5 | peer `token_fp` before P7 | expose as observed (`verified=false`, proposed) / hide (`null`) until per-node creds | observed, unverified (D2) |
| O6 | badge fallback when `/api/v1/servers` is down | render `unknown` degraded (proposed) / keep last-good silently / fall back to `window.location` | `unknown` + visibly degraded (§3) |
| O7 | auth gate for `/api/v1/servers` | same as `/api/v1/agents` → `v1CheckAuth(...,"list")` (proposed) / stricter | `"list"` |
| O8 | does the inventory belong in `/api/v1/topology` instead of a new endpoint | new endpoint (spec P1 mandates it) | spec wins; topology unchanged (§11.12) |
| O9 | pre-auth identity exposure | (A) four-key block on `/api/v1/health` / (B) post-login only / (A) + tighten `--allowed-cidr` | **A**, `PROBE_PREAUTH_IDENTITY` knob (§3.1) — B fails "visible when probe is opened" |
| O10 | login-screen host line format | `name` + `listen_addr` on two lines / host only / name only | `name` primary, `listen_addr` secondary (§3.1 data flow) |

---

## 14. Handoff

**To the coder (implementation unit):**
- File: new `internal/server/servers.go` (endpoint + record store); type extension in `relay_ws.go` for `connectedAt` (D5); registration hook at `relay_ws.go:100-145`; down-record hook **before** deletion in `cleanupRelaySession` (`relay_ws.go:289-310`, called from `:154-160`); route registration next to `api_v1.go:324`; `--server-name` flag beside `main.go:23`.
- Start from §2 (schema), §6 (persistence), §7 (local sources), §8 (peer lifecycle). Each section is self-contained; §9 may be read last.
- Do not touch `registry.go`'s write path (D1 belongs to another unit), do not add a second liveness predicate (§9), do not add a peer field that P2 has not delivered (§11.4).

**To P18-06 (badge):** §3 + §3.1. Post-auth: six fields, `servers[0]`, degraded state defined, prohibitions 1–6 binding. Pre-auth: four fields from `data.identity` on `/api/v1/health`, rendered on `web/src/pages/Login.tsx` (no `state`, no `id`, no status dot), `servers[0]` authoritative if both are held. The server side of §3.1 is `handleV1Health` (`api_v1.go:541-566` — currently no auth call) plus the identity builder shared with §7; the shell badge and the login line are the same chip component with a different data source.

**To P3 (`/network`):** §2 is a superset of what the page needs; peers, `transports[].kind/port/state`, `counts`, `down_since` are all present so the page needs no second source.

**Reviewed_by / verdict fields:** filled by the reviewer's leg of this unit — this document is the spec, not its verification.

---

## Appendix A — verifier commands used to build this document (re-runnable)

```
cd /opt/data/projects/PROBE_UPGRADE/v119wt
git rev-parse --short HEAD                          # 147c7f5
grep -rn "api/v1/servers\|servers.json" internal/ web/src      # 0 hits
grep -rn "dispatchable\|liveness_source\|dispatch_error" internal/ web/src   # 0 hits
sed -n '534,557p' internal/server/registry.go       # non-atomic WriteFile (D1)
sed -n '249,261p' internal/server/credentials.go    # tmp+rename (pattern of record)
grep -n "reg.Token" internal/server/relay_ws.go     # no readers (D2)
sed -n '119,122p' internal/relay/relay.go           # shared token on the wire (D2)
sed -n '140,144p' internal/server/api_v1_phase4.go  # Name: s.addr (D3)
grep -n "MaxAgents" internal/server/relay_ws.go cmd/probe/relay.go           # metadata only (D4)
sed -n '24,32p' internal/server/relay_ws.go         # no connectedAt (D5)
sed -n '283,285p' internal/server/relay_ws.go       # heartbeat discarded (D7)
grep -n "NewTicker" internal/relay/relay.go         # no heartbeat sender (D7)
sed -n '206p;564p' internal/server/server.go internal/server/api_v1.go       # startTime + uptime
grep -n "getToken" -A 5 web/src/App.tsx              # shell replaced by <Login/> without token
sed -n '541,566p' internal/server/api_v1.go         # health: no v1CheckAuth
sed -n '621,625p' internal/server/api_v1.go         # agent health: v1CheckAuth present
sed -n '104,112p' internal/server/api_v1.go         # v1CheckAuth auth-optional fall-through
sed -n '419,432p' internal/server/server.go         # ipFilterMiddleware; /ws exempt
grep -n "BASE = " web/src/api/client.ts             # relative base, no configured host
```

Verdict of this appendix: every `file:line` in §1–§9 resolves in the tree at HEAD `147c7f5`; the four spec premises that do not hold are named as D1–D7 rather than silently satisfied.
