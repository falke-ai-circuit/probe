# P18-05 — UI de-duplication: Dashboard tables out, Profiles into Builder, dead nav fixed

profile=coder · gateway=gateway-coder · status=PASS (self-verified, awaiting reviewer)
tree=/opt/data/projects/PROBE_UPGRADE/v119wt · branch=r119-build · HEAD=`f89fc64b89f4aafa57de634057dce8ee2aeddb10`
source=P18-00_VISUAL_AUDIT.md §2 (D2/D3), §3 (O3/O4/O6), §4 items 1/3/5 · owner ruling: "drop duplicate"
unit_cap=50min · research_leg=NONE (audit is the source, no external leg)

SHAPE — reuse, no new abstraction. Two blocks deleted (`Dashboard.tsx`), one page folded into an
existing form (`Builder.tsx` preset strip over the existing `/profiles` endpoints), two navigation
defects fixed in place, one dead component removed from disk. No endpoint added, removed or changed;
`BuildConfig` untouched; no new page, no new route concept (`/profiles` keeps a route that redirects).

---

## 1. WHAT CHANGED

| file | change | where (current HEAD) |
|---|---|---|
| `web/src/pages/Dashboard.tsx` | full agent table DELETED (was :81-115, the 26-row copy of `/agents`, audit D2) | gone; replaced by panel at :86-131 |
| `web/src/pages/Dashboard.tsx` | "Recent Tasks" table DELETED (was :117-149, copy of `/tasks`, audit D3) | gone |
| `web/src/pages/Dashboard.tsx` | "Needs attention" panel: only `status === 'stale' || 'inactive'`, 4 columns, capped at 8 rows (`ATTENTION_LIMIT`, :39-41) with a `+N more …` pointer, plus one link each to `/agents` and `/tasks` (:88-92) | :86-131 |
| `web/src/pages/Dashboard.tsx` | O3: `window.location.hash` → `useNavigate()` (:2, :9, :117 `onClick={() => navigate(to)}`) | :2, :9, :117 |
| `web/src/pages/Dashboard.tsx` | O4: `encodeURIComponent(a.agent_id)` in the row href and the navigate target | :111 |
| `web/src/App.tsx` | `/profiles` route now `<Navigate to="/builds?presets=1" replace />`; `Profiles` import removed | :35 |
| `web/src/components/Sidebar.tsx` | `/profiles` removed from the nav array (10 → 9 items); unused `Layers` icon dropped | :14-15 |
| `web/src/pages/Builder.tsx` | preset strip over the EXISTING endpoints `listProfiles`/`createProfile`/`deleteProfile`: select → "Apply to wizard" copies os/arch/capabilities/server_url/permissions/sandbox_dir/autostart/disguise into the form; "Save preset" stores the current form; "Delete preset" removes one; `?presets=1` highlights the strip | :54-62, :137-206, :258-281 |
| `web/src/api/client.ts` | `encodeURIComponent(id)` on 41 agent-scoped calls (`/agents/${id}` family) | 41 sites |
| `web/src/pages/Agents.tsx` | O4: `encodeURIComponent` in the name link and the Manage link | :87, :102 |
| `web/src/index.css` | `.preset-strip`, `.preset-strip-focus` (8 lines, existing tokens only) | :776-783 |
| `web/src/pages/Profiles.tsx` | absent from disk (was already absent from HEAD — see §5 finding); nothing to stage | — |
| `web/dist/**` | rebuilt bundle (committed by repo convention: `//go:embed all:web/dist`) | — |

Cap decision (not in the brief, inside its intent): with an all-inactive fleet an uncapped
"needs attention" panel becomes the 26-row table again — the exact duplicate this unit removes. The
panel is therefore capped at 8 with a pointer to `/agents`; the 26-agent screenshot below shows
8 rows + "+18 more".

Why `client.ts` (outside the brief's literal three line refs): the brief's O4 fix encodes the Link,
but the detail page then calls `GET /api/v1/agents/<id>` with a raw slash id. Measured on the local
instance built from this tree: raw id → `404 page not found`, `%2F`-encoded id → `200` with the agent
payload. Encoding the Link alone would open a detail page that cannot load its agent — cosmetic.
The 41 edits are mechanical and no-ops for ids without reserved characters.

---

## 2. ACCEPTANCE — evidence per criterion

| # | criterion | evidence (measured, not inferred) |
|---|---|---|
| A1 | no page renders the 26-row agent table twice | Dashboard at HEAD: `dash_table_count=1`, `dash_rows=8`, headers `["NAME","STATUS","HEALTH","LAST HEARTBEAT"]`; `dash_has_recent_tasks=false`; Agents: `agents_rows=26`. So the fleet table exists exactly once (7-col, searchable, on `/agents`); the landing page carries 8 attention rows of a 4-col projection. |
| A2 | `/profiles` absent from nav, old URL redirects to Builder | `profiles_url="http://127.0.0.1:7711/builds?presets=1"` after `goto /profiles`; `profiles_sidebar_entry=false`; `sidebar_items=["Dashboard","Agents","Flows","Sensors","Tasks","Transfers","Credentials","Builder","Settings"]` (9). |
| A3 | clicking a "needs attention" row opens the agent detail INCLUDING the slash-bearing id | Dashboard click → `dash_click_url="http://127.0.0.1:7711/agents/relay%2Fradxa-relay%2Feas1-via-radxa"`, `detail_name_visible=true`, `detail_agent_id_visible=true`, `detail_loading_stuck=false`, body renders the tab set (`TERMINAL FILES SCREEN PROCESSES TUNNELS MITM DEBUG MODES SURVEY AUDIT`) and the loaded record (`OS linux/arm64 VER 1.19.1 MODE relay HP 55% ERR 1`). Agents page: both hrefs are `/agents/relay%2Fradxa-relay%2Feas1-via-radxa`, and clicking Manage lands on the same URL. |
| A4 | a saved preset can be selected and applied in the Builder wizard | `select_option("#preset-select","prf-demo-windows")` → click `#preset-apply` → `apply_msg='Applied preset "windows-sandboxed-exec" to the wizard.'`; step 0 selects read `["prf-demo-windows","windows","amd64"]`; step 1 checked caps `["exec","filesystem","process"]`; `Selected: exec, filesystem, process`; permissions enforced `sandboxed` with `C:\sandbox`. Save path: `preset_save_msg='Saved preset "p1805-linux-arm64-full".'` and the option list grows to include it (store round-trip through `POST /profiles`). |
| A5 | `go vet` + `go build` clean | `VET_EXIT=0`, `BUILD_EXIT=0` at `f89fc64` (`go1.22.5`, `/tmp/go/bin/go`). |
| A6 | `go test ./... -count=1` with pass/fail/skip | `633 pass / 0 fail / 2 skip`, 11 packages `ok`, `TEST_EXIT=0` at `f89fc64`. |
| A7 | screenshots of Dashboard, Agents, Builder after the change | see §3. |

Runtime: local instance built from this HEAD (`probe3`, embeds the new `web/dist`) on `127.0.0.1:7711`,
fixture registry of 26 records (1 slash-bearing id) + 1 saved preset, token injected into localStorage
— no credentials typed, no writes to any deployed server. Zero console errors and zero `>=400`
responses during the whole probe run (`bad_responses=[]`).

---

## 3. SCREENSHOTS (live, 1500×1000, full page)

- `/opt/data/tmp/probe119/shots/P18-05_dashboard.png` — topology + 6 stat cards + capped "Needs attention" + both links, no Recent Tasks
- `/opt/data/tmp/probe119/shots/P18-05_agents.png` — full 26-row table, `%2F` Manage hrefs
- `/opt/data/tmp/probe119/shots/P18-05_builder_presets_redirect.png` — `/profiles` landed on `/builds?presets=1`, strip highlighted
- `/opt/data/tmp/probe119/shots/P18-05_builder_applied.png` — preset applied, wizard on step 1
- `/opt/data/tmp/probe119/shots/P18-05_preset_saved.png` — saved preset appears in the select
- `/opt/data/tmp/probe119/shots/P18-05_agentdetail_slash_id.png` — slash-id detail page loaded

Pixel sanity: 1500×1000, 3.1k–4.0k distinct colours each, non-blank bbox (no white/failed render).
Not visually inspected by a vision model — the vision endpoint returned `404 model no longer
available` in this session, so page content is asserted from DOM text quoted in §2, not from pixels.

---

## 4. DELIBERATELY NOT DONE (brief's DO-NOT list)

- build buttons not renamed (separate decision) — `Builder.tsx:301` still "Build Agent"
- Screen tab untouched (O5) — `client.ts:123-156` doubled `/api/v1` stays as it was
- no API endpoint changed: `POST/GET/DELETE /api/v1/profiles` and `/api/v1/agents/{id}` untouched;
  `BuildConfig` gained no field
- Transfers, Settings, Tasks (audit §4 items 4/7/8) untouched — other units

---

## 5. OPEN ITEMS / DISCLOSURES

1. **Prior-commit break, found while working (not caused here):** `2939cfc` deleted
   `web/src/pages/Profiles.tsx` from the tree but left `App.tsx:7` importing `./pages/Profiles`, so
   the source tree at `5c96560` could not typecheck — `git show HEAD:web/src/pages/Profiles.tsx` →
   `fatal: path … does not exist in 'HEAD'`, while `git show HEAD:web/src/App.tsx` still carries the
   import. The file survived only as an untracked on-disk leftover, which is why the local `tsc` run
   before this unit passed. This commit removes the import, so the tree typechecks again. Worth a
   reviewer note: a stale `web/dist` had been shipping a page the source no longer had.
2. **Deployed instance not updated.** `139.99.209.235:7701` still serves the v1.19.1 bundle; this is a
   UI-only change and reaches users on the next release. Screenshots are from the HEAD build, not the
   live host.
3. **Fleet snapshot loss is real and accepted** (audit §5): an operator who used the Dashboard table as
   a morning screen now clicks to `/agents`. Mitigation shipped: capped attention panel + both links.
4. **Fixture caveat:** the 26-record fixture loads as `inactive` (P12-03 registry semantics), so the
   screenshots show zero active agents. The non-duplication claim does not depend on that: it rests on
   a deleted block plus measured table/column/row counts on both pages.
5. **Id encoding is now enforced in one direction only.** New agents may still register ids containing
   `/`; the UI encodes, but any other consumer of the API (scripts, docs) must encode too. Rejecting
   `/` at registration was audit §4's alternative and is not in this brief.

---

reviewed_by: `coder` (self-verification — live DOM probes + suite at HEAD; independent reviewer still owed)
verdict: `NEEDS-REVIEW`
verdict_sha_of_head: `f89fc64b89f4aafa57de634057dce8ee2aeddb10`
suite: 633 pass / 0 fail / 2 skip · vet 0 · build 0 · HEAD `f89fc64` · pushed to `origin/r119-build`
