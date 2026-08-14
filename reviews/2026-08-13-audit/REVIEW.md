# Adversarial Audit — PROBE v1.15.0

**Date:** 2026-08-13
**Version:** v1.15.0 (CT100) / v1.9.4 (local repo build)
**Repo:** `/opt/data/workspace-operative/probe` (Go + React, remote agent infrastructure)
**Deployment:** `coder-cd01-mantle-1` style on CT100 (Proxmox); service `coder-cd01-mantle-1`-equivalent for probe; port 7701

---

## Recipient Model

**Who uses PROBE?** A red team operator / DevOps engineer managing distributed remote agents across heterogeneous infrastructure. They need:
1. Agents to actually connect and stay connected
2. Real-time visibility into topology, capabilities, transfers
3. The UI to NOT be broken (it currently is — the dashboard's topology graph is a red error)
4. Authentication to actually be enforced (it's not for the topology endpoint)
5. Fast, responsive controls for managing dozens of agents

**What frustrates them?** A broken dashboard graph that shows `b.edges is not iterable` instead of the agent topology. A topology endpoint that leaks the full network map without auth. A static 5-column stat grid that doesn't stack on mobile.

---

## Performance Profile (CT100 v1.15.0, 3 agents)

| Endpoint | Latency | Status |
|----------|--------:|--------|
| `/api/v1/health` | 0.76ms | ✅ fast |
| `/api/v1/agents` | 0.78ms | ✅ fast |
| `/api/v1/operators` | 1.02ms | ✅ fast |
| `/api/v1/audit?limit=50` | **255.6ms** | ⚠️ slow (SQLite scan) |
| `/api/v1/topology` | 0.84ms | ✅ fast |
| `/api/v1/agents/{id}/health` | 0.82ms | ✅ fast |
| `/api/v1/agents/{id}/capabilities` | 0.99ms | ✅ fast |
| `/api/v1/agents/{id}/tunnels` | 0.80ms | ✅ fast |
| Dashboard HTML | 0.62ms | ✅ fast |

**Server-side latency is excellent.** The 255ms audit endpoint will scale poorly with more events — needs pagination + index on `timestamp`.

---

## Findings

### F1 — CRITICAL — Broken dashboard topology graph (shows error in production)

**Evidence:** Dashboard renders this red error banner:
```
b.edges is not iterable
```

**Root cause** (verified in source `web/src/components/TopologyGraph.tsx`):
```ts
const raw = await api.getTopology() as { nodes: any[]; edges: any[] }
const parentMap: Record<string, string> = {}
for (const e of raw.edges) {     // ← CRASHES if edges is null
  parentMap[e.from] = e.to
}
```

The CT100 production API returns:
```json
{"edges": [{"from":"gorproxmox","to":"server","type":"direct"}, ...]}
```

But when edges is `null` (which happens when there are no relays), `for (const e of null)` throws. The error **only manifests when no agents are connected**, but the UI shows the error anyway on every page load.

**From the recipient's seat:** I open the dashboard. I see a red error banner where the topology graph should be. I have no idea what agents are connected. The main value of the dashboard — visualization — is broken.

**Severity:** CRITICAL — the dashboard's centerpiece visualization is broken in production.

---

### F2 — CRITICAL — `/api/v1/topology` has NO authentication

**Evidence:** Tested unauthenticated:
```
GET /api/v1/topology : 200 (unauth)   ← should be 401
GET /api/v1/agents   : 401 (unauth)   ← correctly protected
GET /api/v1/operators: 401 (unauth)   ← correctly protected
GET /api/v1/audit    : 401 (unauth)   ← correctly protected
```

**The topology endpoint leaks the entire agent network map to any unauthenticated request.** An attacker who discovers the endpoint can enumerate:
- All agent IDs and names
- Server bind address (e.g., `0.0.0.0:7701` in CT100's case)
- Connection topology (which agent connects to which)
- Software versions of every agent

**Root cause** — `internal/server/api_v1.go` registers `/api/v1/topology` but doesn't include it in the auth middleware list. Inconsistent application of `s.requireAPIAuth`.

**Severity:** CRITICAL — this is a reconnaissance goldmine for any adversary.

---

### F3 — MAJOR — Edge direction inconsistency between API and frontend

**Evidence:**
- API returns: `{"from": "gorproxmox", "to": "server"}` (child → parent direction)
- Frontend `computeLayout()` expects: `{"from": "server", "to": "agent"}` (parent → child direction)

**The two components agree on the edge semantics differently.** The frontend's `parentMap[e.from] = e.to` works because it's just building a parent lookup, but `computeLayout` reverses the direction. This is why the topology graph is broken even when edges exist.

**Fix:** Either flip the API direction (parent → child) to match the layout code, or flip the layout code's `edges.push({from, to})` calls.

**Severity:** MAJOR — even if F1's null-check is added, the graph will still draw wrong lines.

---

### F4 — MAJOR — `audit.go:2` error_count keeps incrementing for same error

**Evidence:** Agent `gorproxmox` health response:
```json
{
  "last_error": "download failed: Get \"http://139.99.148.90:7701/download/...\": dial tcp 139.99.148.90:7701: i/o timeout",
  "error_count": 2,
  "health_score": 0.8410440897066667
}
```

**The agent has been failing to download its update for 9 days** (uptime 791436s = ~9.2 days). The error message is identical each time. This means:
1. The auto-update mechanism has been broken for9 days
2. The agent is still showing as "active" (heartbeat works)
3. But it's stuck on an old version

**From the recipient's seat:** My agent has been silently failing to auto-update for 9 days. The error is the same every time. I have no alert — I'd only know if I manually checked.

**Severity:** MAJOR — silent operational degradation, no alerting on repeated same error.

---

### F5 — MAJOR — Mobile / responsive layout completely broken

**Evidence:** Tested at 375px viewport (iPhone size):
- Sidebar stays permanently expanded, eating ~25-30% of screen width
- Stats grid tries to render 5 cards in one row → text is cramped
- No hamburger menu
- No responsive breakpoints
- Topology graph controls overflow

**From the recipient's seat:** I'm checking probe from my phone. The dashboard is unusable on mobile. The sidebar takes up half my screen and I can't read the stats.

**Severity:** MAJOR — the tool is "responsive" only in name; functionally desktop-only.

---

### F6 — MAJOR — Capability management returns null for active agent

**Evidence:**
```bash
GET /api/v1/agents/gorproxmox/capabilities
→ {"ok":true,"data":{"agent_id":"gorproxmox","capabilities":null}}
```

The agent is active, has been connected for 9 days, has been used (audit log shows exec commands), but its capability list is null. This means:
- The UI shows nothing in the Capabilities column for this agent
- The UI's "Manage Capabilities" flow would break (no checkboxes to toggle)
- The `capabilities` field in the agent registration isn't being populated correctly

**From the recipient's seat:** I can't see what my active agent can do. The Capabilities column shows "—". When I click "Caps" to manage them, the modal is empty.

**Severity:** MAJOR — core feature (capability management) is non-functional for the only active agent.

---

### F7 — MINOR — Builder "Previous" button looks enabled on step 1

**Evidence:** First step of the Builder wizard shows `← Previous` button with green styling, but it's disabled (can't go back from step 1). Visual state suggests it's clickable.

**Fix:** Hide the button entirely on step 1, or grey it out more clearly.

---

### F8 — MINOR — Settings operator table shows ID truncated without tooltip

**Evidence:** Operator ID is shown as `op-16412562b...` truncated. Operators may need to copy the full ID for audit trails or API calls.

**Fix:** Add a copy-to-clipboard button or hover tooltip with full ID.

---

### F9 — MINOR — Dashboard Total Tasks card wraps awkwardly

**Evidence:** 6 stat cards in a row of 5+1 instead of a clean 2/3/6 layout. The "Total Tasks" card sits alone on the second row.

**Fix:** Use 2/3/6 column responsive grid.

---

### F10 — MINOR — Builder icon alignment inconsistent in sidebar

**Evidence:** Sidebar Builder (wrench) icon appears clipped at the left edge compared to other icons (Dashboard, Tasks, Credentials, Profiles, Settings).

---

## Visual Audit Scores

| Dimension | Score (0-5) | Notes |
|-----------|-------------|-------|
| First Impression | 4 | Cyberpunk terminal aesthetic, neon green glow, monospace typography — strong coherent theme |
| Layout & Responsiveness | 1 | **Mobile completely broken** — sidebar doesn't collapse, stats grid doesn't stack |
| Dark Mode | 4 | Dark mode IS the only mode; high contrast neon green works well |
| Interaction Polish | 3 | Buttons respond, navigation works, but topology graph is broken (F1) |
| Empty/Error States | 3 | Empty states are OK; **error state for graph is ugly red banner** (F1) |
| Typography | 4 | Consistent monospace throughout, good hierarchy |
| Color & Contrast | 3 | Neon green on black works but may be tough for color-blind users |
| Component Consistency | 4 | Consistent dark theme, iconography, button styles |
| **Overall Sexyness** | **3.3** | Strong aesthetic identity, but broken graph + no mobile = unusable in field |

### Pages screened
- ✅ Login — polished cyberpunk
- ❌ Dashboard — broken graph
- ✅ Agents — clean empty state
- ✅ Tasks — clean empty state
- ✅ Transfers — clean with stats
- ✅ Credentials — clean 3-section layout
- ✅ Builder — multi-step wizard
- ✅ Profiles — clean empty state
- ✅ Settings — operators table

---

## Performance & Scale Projections

Current (3 agents):
- All endpoints under 1ms except audit (255ms for 50 records)
- Single binary, ~12MB
- SQLite for audit log
- No apparent connection pooling issues

At 100 agents:
- Agents list query: will scale linearly with in-memory agent registry (sub-ms)
- Audit: 255ms × 2 = 510ms for 100 events — needs index/pagination
- Topology: edges scale O(n) — still fast
- WebSocket connections: 100 concurrent — needs testing

At 1000 agents:
- Audit endpoint becomes unusable without proper pagination + indexing
- WebSocket fan-out from server to N agents may become bottleneck
- File transfer metadata may need sharding

---

## What a Generic Audit Misses

A generic "check OWASP + run tests" would find F2 (no auth on topology) because it's a basic auth check. But it would MISS:

1. **F1 (broken graph)** — looks like a "small frontend bug" but is the centerpiece feature. A generic audit would mark it as "minor" instead of CRITICAL.
2. **F3 (edge direction inconsistency)** — only visible by tracing the data flow from API → frontend transform → render. Requires reading both sides.
3. **F4 (9-day silent auto-update failure)** — only visible by inspecting the agent's `last_error` and `error_count` fields. A generic audit wouldn't notice "the error has been the same for 9 days."
4. **F5 (no mobile support)** — requires actually testing at mobile viewport sizes. Generic audits check "does the API work" not "does the UI work on phones."
5. **F6 (capabilities null)** — requires checking the UI's "Manage Capabilities" flow against an active agent. Generic audits check endpoints, not the UI's data flow.
6. **The visual audit** — a generic audit doesn't screenshot the UI, navigate the pages, or score the design. The cyberpunk aesthetic looks great until you realize the centerpiece graph is broken.

---

## Recommended Fix Priority

| # | Finding | Severity | Effort |
|---|---------|----------|--------|
| 1 | F2: Add auth to /api/v1/topology | CRITICAL | Trivial (5 lines) |
| 2 | F1: Add null-check for edges in TopologyGraph.tsx | CRITICAL | Trivial |
| 3 | F3: Fix edge direction consistency | MAJOR | Small |
| 4 | F6: Fix capabilities null for active agents | MAJOR | Medium |
| 5 | F5: Mobile responsive layout | MAJOR | Medium |
| 6 | F4: Alert on repeated same error | MAJOR | Medium |
| 7 | F7-F10: UI polish | MINOR | Small |