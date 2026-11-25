# P18-00 — PROBE Frontend Visual Audit (IA / duplication / dead surfaces)

profile=reviewer | gateway=hermes gateway (session api_server) | status=PASS-with-FIX-FIRST findings
tree=/opt/data/projects/PROBE_UPGRADE/v119wt (READ-ONLY, nothing changed) | live=139.99.209.235:7701 (v1.19.1, uptime 45897s at first probe)
unit_cap 55min | authored none of this UI (independent audit)

RECIPIENT EXPERIENCE (logged): drove the live SPA in Chromium at 7701 as an authenticated admin, all 10 nav routes + /agents/:id + 10 agent tabs, screenshots in `shots/`. Honest note on order: source was opened before this line was written — the deviation is mine, not hidden.
SHAPE none — this unit adds no code; no symbol was written or reused. (G-SHAPE-FINDING satisfied by absence of code addition.)

---

## 1. PAGE INVENTORY

Live population at capture: 26 agents (5 active, 2 stale), 12 builds, 1 flow, 0 profiles, 0 tasks, 0 transfers, 18 sensors in catalog, 0 operators, 0 enrollment tokens, 52 audit rows.

| route | nav label (Sidebar.tsx:7-16) | what loads (observed) | what mutates | API endpoints called (observed on the wire) |
|---|---|---|---|---|
| `/` | Dashboard | topology graph, 6 stat cards, full agent table (26 rows), Recent Tasks (empty) | nothing (graph −/+/Reset are view controls only) | GET /health, /agents, /tasks, /topology |
| `/agents` | Agents | 26-row table, search + status filter, Caps modal | redeploy (build + push) | GET /agents; POST /agents/:id/redeploy (`Agents.tsx:42`) |
| `/agents/:id` | — (nested, no nav item) | breadcrumb, 10 tabs, connection bar | exec / fs-* / proc-* / capture / tunnel / mitm / sniff / debug / mode / survey | GET /agents/:id, /agents/:id/audit (`AgentDetail.tsx:42`), then per-tab calls |
| `/builds` | Builder | 5-step wizard + Build History (12 rows) | create / delete / VT-scan build | GET /builds, POST /builds (`Builder.tsx:87`), GET /builds/:id, DELETE /builds/:id, POST+GET /builds/:id/vt-scan |
| `/profiles` | Profiles | 0 profiles, empty state | create / delete profile | GET /profiles (`Profiles.tsx:26`), POST (`:45`), DELETE (`:56`) |
| `/tasks` | Tasks | 0 tasks, empty state | create / cancel task | GET /tasks (`Tasks.tsx:28`), POST (`:48`), DELETE (`:63`) |
| `/transfers` | Transfers | 0 transfers, 3s poll | pause / resume / cancel | GET /transfers (`Transfers.tsx:14`), POST …/pause `/resume` `/cancel` (`:28,:33,:38`) |
| `/credentials` | Credentials | active-agent picker, manual-scan textarea, results table | regex scan over exec output | GET /agents (`Credentials.tsx:54`), POST /agents/:id/exec (`:95`) |
| `/flows` | Flows | 1 flow, node graph (4 steps), run history | create / update / run-now | GET /flows, /flow-runs, /agents; POST /flows (`Flows.tsx:99`), PUT /flows/:id (`:131`), POST run-now (`:150`) |
| `/sensors` | Sensors | agent picker (26 options incl. stale/inactive), 18-sensor table, category filters | enable / disable sensor | GET /agents, GET /agents/:id/sensors (`Sensors.tsx:58`), POST enable/disable (`:69,:78`) |
| `/settings` | Settings | operators (0), enrollment tokens (0), security cards, blacklist, audit (52 rows) | create/delete operator, mint enrollment token, blacklist add/clear | GET /operators, /enrollment-tokens, /agents/revoked, /audit?limit=50, /security/status; POST /operators, DELETE /operators/:id, POST /enrollment-tokens, POST /security/blacklist |
| *(no route)* | — | Login page shown instead of the app when no token (`App.tsx:21-23`) | login | POST /login |

Count correction to the brief's premise: **11 routed pages** (10 nav + `/agents/:id`) plus **one unrouted page component** (Login). "12 pages / 10 routes" is right only if Login is counted as a page and `/agents/:id` as a route; `/login` itself does not exist, so an unauthenticated deep link cannot be expressed (`App.tsx:41` wildcard → `/`).

---

## 2. DUPLICATION TABLE

| # | capability | surface A | surface B | same? | evidence |
|---|---|---|---|---|---|
| D1 | compile an agent binary | `Builder.tsx:65-96` wizard → POST /builds | `Agents.tsx:40-42` Caps modal → POST /agents/:id/redeploy | **partial** — same builder engine, different product | `builder_api.go:314-324`: redeploy constructs a `BuildConfig` and calls `s.builder.CreateBuild(cfg)` — the exact call `POST /builds` makes. Screenshots: `shots/P18-00_agents_caps_modal.png`, `shots/P18-00_builder_step5.png`, `shots/P18-00_builder_build_result.png` |
| D1a | build config fields | Builder: name, os, arch, 9 caps, server_url, permissions, sandbox, autostart, disguise (`Builder.tsx:70-86`) | Redeploy: name/os/arch inherited, caps chosen, **permissions hardcoded `"full"`** (`builder_api.go:321`), token = server token (`:320`) | different — redeploy cannot produce a sandboxed or disguised build | intercepted POST body: `{"name":"agent-windows-amd64","os":"windows","arch":"amd64","capabilities":["exec","filesystem"],"server_url":"wss://localhost:7700/ws","permissions":"full",...}` |
| D1b | delivery | Builder: manual Download button (`Builder.tsx:112-114`) | Redeploy: auto `agent_update` push (`builder_api.go:349-363`) | different outcome, one capability family | code + modal copy `Agents.tsx:115` ("rebuild … push it through the existing connection") |
| D2 | agent list | `Agents.tsx:83-107` (7 cols incl. Connected/Actions) | `Dashboard.tsx:81-115` (6 cols, no actions) — 26 rows rendered on both | **duplicate read view** | screenshots `P18-00_agents.png`, `P18-00_dashboard.png` |
| D3 | task list | `Tasks.tsx:127-163` (cancel action) | `Dashboard.tsx:117-149` "Recent Tasks" (no action) | **duplicate read view** | screenshots `P18-00_tasks.png`, `P18-00_dashboard.png` |
| D4 | create a file transfer | `FilesTab.tsx:116,128` → POST /agents/:id/transfer | `/transfers` lists them but cannot create; row shows `agent_id.slice(0,12)` with no name/link (`Transfers.tsx:121`), and the empty state says "Transfer files from the Files tab" as **plain text** (`Transfers.tsx:102`) | **split, not duplicated** — one control, two surfaces, no cross-link | screenshot `P18-00_transfers.png`; only one `createTransfer` call site in `web/src` |
| D5 | agent command execution | `Tasks.tsx:6` command types = exec/fs-*/proc-*/capture | `AgentDetail` tabs (Terminal/Files/Processes/Screen) run the same commands immediately | **same command set, different time model** (now vs scheduled) — legitimate split | `Tasks.tsx:6` vs `TerminalTab`/`FilesTab`/`ProcessesTab`; not a removal candidate |
| D6 | credentials | `Credentials.tsx` (scan hosts for secrets, regex `:7-16`) | `Profiles.tsx` (build templates) | **NOT duplication** — no shared endpoint, no shared object | endpoints: /agents + /agents/:id/exec vs /profiles only |
| D7 | settings | `Settings.tsx` (operators, enrollment tokens, revoked, blacklist, audit) | `Profiles.tsx` (build templates) | **NOT duplication** — no shared endpoint | screenshots `P18-00_settings.png`, `P18-00_profiles.png` |
| D8 | mint a credential | enrollment token (`Settings.tsx:182-217`, for agents) | operator token field (`Settings.tsx:139-142`, for humans) | **intra-page duplication of concept, not of endpoint** — labels never say who each token is for | both in `Settings.tsx`; no cross-reference copy |

Verdict on the owner's hypothesis: **two of the four suspected pairs are real (build family, and the list views), two are not (credentials/profiles, settings/profiles)**. The build pair is not a redundant copy — it is two products (new binary vs in-place rebuild) sharing one engine, and the UI never says so.

---

## 3. DEAD / ORPHAN SURFACES

| # | surface | what happens | evidence |
|---|---|---|---|
| O1 | route with no nav item | `/agents/:id` — none; reachable from Agents "Manage" (`Agents.tsx:102`) and the name link (`:87`). Not orphan. | 10/10 nav items resolve, each rendered its own h1 and set `a.active`; screenshot set |
| O2 | nav item with no route | none found | `App.tsx:30-40` vs `Sidebar.tsx:6-17` — exact 10/10 set match |
| O3 | Dashboard row click | `Dashboard.tsx:100` sets `window.location.hash`; app uses `BrowserRouter`, so nothing navigates. Observed: click on a row cell → URL `/#/agents/relay/radxa-relay/eas1-via-radxa`, page still Dashboard. Cursor is `clickable` on all 26 rows. | live click test; `Dashboard.tsx:100` |
| O4 | Manage link for slash-bearing agent id | 1 of 26 ids is `relay/radxa-relay/eas1-via-radxa`; its `Link` becomes `/agents/relay/radxa-relay/eas1-via-radxa`, matches the wildcard, redirects to `/`. Observed: URL returned to `/`, h1 Dashboard. That agent has **no reachable detail page**. | live click test; `App.tsx:41`; agents inventory (1/26) |
| O5 | Screen → Stream / Interactive | client builds `/api/v1/api/v1/agents/:id/stream-*` (BASE prepended at `client.ts:28` to a path that already starts `/api/v1`, `client.ts:123-156`). Live: `GET /api/v1/agents/:id/stream-frame` → **404**, and the doubled path → **404**. The deployed v1.19.1 registers no stream routes at all, while source does (`api_v1.go:503-511`). Two independent breaks: client path bug + UI ahead of server. | curl 404 both paths; bundle contains `stream-start` ×2, `stream-frame` ×1; screenshot `P18-00_agentdetail_screen.png` |
| O6 | Profiles page | **write-only**: `listProfiles/createProfile/deleteProfile` are called only from `Profiles.tsx` (`:26,:45,:56`). Builder has no profile selector (wizard steps: OS & Arch, Capabilities, Connection, Permissions, Disguise) and `BuildConfig` carries no profile field. Saved templates can never be applied. Live: 0 profiles. | grep over `web/src` — only Profiles.tsx + `client.ts:234-242`; intercepted build body has no profile key |
| O7 | Agents empty-state "Build an Agent" link | `Agents.tsx:49-55` renders only when `agents.length === 0`; live has 26, so this link is unreachable in practice. | live: 0 occurrences of that button; `Agents.tsx:49` |
| O8 | Tasks agent target | free-text `placeholder="agent-uuid"` (`Tasks.tsx:90-91`) while every other page offers a 26-option picker (`Credentials.tsx:141`). A typo produces a task for an id that does not exist — no validation. | live form render; screenshot `P18-00_tasks.png` |
| O9 | placeholder / half-built UI | none found beyond O5/O6. Flow editor, sensors, VT scan, blacklist all render real controls and real data. | screenshots |

Checked and **not** defects (so they are not smuggled into the list): (a) Settings audit appeared empty at a 3s snapshot but populates at ≤6s — 52 rows, request `/audit?limit=50` 200; (b) `/agents/:id` showed "Loading agent…" at 3s and completed with 10 tabs at 9s — slow, not stuck; (c) pages rendering empty (tasks/transfers/profiles) are empty because the API returned `[]`, not 404.

---

## 4. IA PROPOSAL

Principle applied: remove surfaces, not capabilities. Each item names what survives.

**Nav order (10 → 9):** Dashboard, Agents, Flows, Sensors, Tasks, Transfers, Credentials, Builder, Settings.

1. **Fold Profiles into Builder; remove `/profiles` from nav.** Profiles is a preset store for the Builder's own fields. Builder gains a preset save/load strip using the same endpoints (`/profiles` stays). Removes 1 nav item, 1 route from the sidebar, 0 endpoints.
2. **Keep both build entry points; rename them so they stop looking identical.** Agents "Caps" → "Capabilities & rebuild"; modal button "↻ Redeploy Agent" → "Rebuild & push (keeps OS, arch, full permissions)". Builder button "Build Agent" → "Build new installer". No control removed; the distinction (`builder_api.go:321` hardcoded `full`, `:320` server token) becomes visible instead of hidden.
3. **Dashboard stops duplicating the two list pages.** Drop the 26-row agent table and "Recent Tasks" (D2/D3). Keep topology + stat cards, add "Needs attention" (stale/inactive only) and one link each to Agents/Tasks. Registry capability is unchanged — the full table already exists one click away with search/filter the Dashboard lacks.
4. **Transfers becomes agent-identifying.** Replace `agent_id.slice(0,12)` with the agent name linked to `/agents/:id` (`Transfers.tsx:121`), and turn the empty-state sentence (`:102`) into the link it already describes. One control stays (Files tab); the second surface gains the context it is missing.
5. **Fix the two dead navigations** (O3/O4): Dashboard row click must route (`navigate()`), and agent ids must be URL-encoded in every `Link` (`Agents.tsx:87,102`, `Dashboard.tsx:102`). Alternatively reject `/` in new agent ids at registration.
6. **Screen tab:** hide Stream/Interactive behind a server-capability check until the deployed server serves `stream-*`, or fix the doubled `/api/v1` prefix (`client.ts:123-156`). Keeping the tab visible while both paths 404 is the only place in the UI that promises a capability it cannot deliver.
7. **Settings language:** label the two token mints by audience — "Enrollment token (agent joins server)" and "Operator token (human logs in)" — same controls, no capability change.
8. **No merge** for Credentials/Settings (D6/D7): different objects, different endpoints; merging would be movement without removal.

Removals, exact: nav entry `/profiles`; Dashboard agent table block (`Dashboard.tsx:81-115`); Dashboard Recent Tasks block (`:117-149`); Agents "Caps" label text; Screen Stream/Interactive exposure. Every removed *view* still exists elsewhere; every removed *control* is a rename or relabel.

---

## 5. WHAT A USER CAN NO LONGER DO

| after removal | honest cost | mitigation |
|---|---|---|
| see all 26 agents with version+health on the landing page | one extra click to Agents, and the Dashboard no longer serves as a single-screen fleet snapshot | "Needs attention" panel keeps the operational signal; Agents has search/filter the Dashboard never had |
| see last 10 tasks on the landing page | lose at-a-glance task activity without a click | stat card "Total Tasks" + link |
| reach build presets from the sidebar in one click | presets live inside Builder, one level down; a `/profiles` bookmark 404-redirects to `/` | redirect `/profiles` → `/builds?presets=1` |
| tell "rebuild this agent" from "build a new installer" by button wording | none — wording only; both still exist | the rename is the fix |
| (proposal 4) nothing | agent identity is added, not removed | — |
| (proposal 6) use Stream/Interactive *as they are today* | **already not usable**: both 404 on the live server | nothing is lost by hiding; something is gained by not lying |
| (proposal 8) nothing | no endpoint or control merges | — |

Remaining real loss, stated plainly: if any operator used the Dashboard agent table as their morning screen, proposals 3 costs them that habit. It is the one removal with a behavioural cost, and it is the one most worth confirming with the owner before P18-01 builds it.

---

## NEMESIS GATE (hostile attacks on this audit, all addressed)

| attack | result |
|---|---|
| "You reported a bug that was just a slow render" | Self-caught twice: AgentDetail "Loading agent…" and Settings "No audit entries" were 3s snapshots. Re-ran at 6-9s: 10 tabs and 52 audit rows. Both moved to the not-a-defect list. |
| "Your duplication claim is a code-reading claim, not an observed one" | Redeploy was re-proven live: intercepted POST (no side effect) returned a build id, and `builder_api.go:314-324` shows `CreateBuild`. Builder POST body captured with the same fields. |
| "Prove the dead link, don't infer it from the code" | Clicked both: Dashboard row → hash only, h1 unchanged; slash-id Manage link → back to `/`. O5 proven by two 404s on distinct paths. |
| "Profiles might be used by the server-side build path" | Checked: `BuildConfig` has no profile field, the intercepted POST has none, and no client file outside `Profiles.tsx` touches `/profiles`. Write-only stands. |
| "Charitable read: maybe the owner is right about all four pairs" | Two pairs disproved (D6/D7) against the brief's own framing. The complaint was tested, not adopted. |

## Fix list for P18-01 (numbered, no capability lost)

1. Move Profiles into Builder as presets; drop the nav entry; redirect `/profiles`.
2. Rename both build entry points to state their difference (rebuild-in-place vs new installer).
3. Reduce Dashboard to topology + stats + needs-attention + links (drop agent table, drop Recent Tasks).
4. Link Transfers rows to the agent (name, not truncated id) and link the empty-state sentence.
5. Route Dashboard row clicks; URL-encode agent ids in every Link.
6. Hide or fix Screen Stream/Interactive (doubled `/api/v1` at `client.ts:123-156`; server routes absent on v1.19.1).
7. Relabel the two token mints in Settings by audience.
8. Validate Tasks agent target against the registry (picker or existence check).

## Evidence index

- raw capture: `/opt/data/tmp/probe119/P18-00_capture.json`, `P18-00_capture2.json`
- screenshots (live, 1500×1000, full page): `/opt/data/tmp/probe119/shots/P18-00_{dashboard,agents,agents_caps_modal,agents_redeploy_result,builder,builder_step5,builder_build_result,profiles,tasks,transfers,credentials,flows,flows_open,sensors,settings,agentdetail,agentdetail_screen}.png`
- side effects: **none**. Redeploy and build POSTs were intercepted and fulfilled locally; no agent was rebuilt, no build was created, no profile/operator/task was written, no code or repo file was touched.

VERDICT FIX-FIRST — IA defects O3/O4/O5/O6/O8 are real and blocking for a "duplicate areas" cleanup; D6/D7 disprove half the owner's complaint; no code changed (prohibited this unit).
reviewed_by=reviewer (independent visual audit) | sha of tree HEAD=e5d7440 | live server v1.19.1
