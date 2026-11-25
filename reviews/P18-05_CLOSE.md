# P18-05 — CLOSE (written by the parent; the unit produced the artefact and no close record)

**Commit:** `f89fc64b89f4aafa57de634057dce8ee2aeddb10` (HEAD at this close, pushed to `origin/r119-build`, tree clean)
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: f89fc64`
**Scope:** `web/src` only — `pages/Dashboard.tsx`, `pages/Builder.tsx`, `pages/Agents.tsx`, `App.tsx`, `components/Sidebar.tsx`, `api/client.ts`, one dead page deleted. No endpoint added, removed or changed; `BuildConfig` untouched.

## Delivered

The duplicate read views the owner complained about are gone rather than hidden: the 26-row agent table and the "Recent Tasks" table are deleted from `Dashboard.tsx` (audit D2/D3), replaced by a "Needs attention" panel that shows only `stale`/`inactive`, capped at 8 rows with a `+N more` pointer and one link each to `/agents` and `/tasks`. `/profiles` stops being a page of its own — the route redirects to `/builds?presets=1` and the nav drops from 10 items to 9, with Builder gaining a preset strip over the **existing** `listProfiles`/`createProfile`/`deleteProfile` endpoints rather than a new concept. Two latent UI defects fixed in passing: `window.location.hash` replaced by `useNavigate()` (O3), and `encodeURIComponent` applied to 41 agent-scoped call sites plus the name/Manage links (O4).

## Disclosure that matters — a break the unit found and fixed, not caused

The unit records that `2939cfc` deleted `web/src/pages/Profiles.tsx` while `App.tsx:7` still imported it, so the tree at that commit could not typecheck; the file survived only as an untracked leftover, which is why a local `tsc` had passed. **That commit is the parent's** — a document commit that swept in a deletion another lane had staged, and its message claiming "no code" was therefore false. Verified here: `git show --name-status 2939cfc` → `A reviews/P21-02_CLOSE.md`, `A reviews/P21-02_INVENTORY_CONTRACT.md`, `D web/src/pages/Profiles.tsx`. `f89fc64` removes the stale import and the tree typechecks again. The process failure is the parent's, is recorded on the board, and the rule (never bare `git commit` in a shared tree) is captured in the skill library.

## Suite — parent's measurement, and one number that does not reproduce

Parent ran `go test ./... -count=1 -json` at `f89fc64` with host load 6.67: **763 pass / 0 fail / 2 skip**, 16 packages. The unit's own last line reported **633 pass / 0 fail / 2 skip**; that does not reproduce at this head and is not adopted. The likely cause is a differently-scoped run, but a count that cannot be reproduced is a claim, not evidence, so the parent's number is the one on the record. `go vet`/`go build` clean per the unit; the tree was clean at close.

## Open items

- **Deployed instance not updated.** `139.99.209.235:7701` still serves the v1.19.1 bundle; this is a UI-only change that reaches users on the next release. The artefact's screenshots are from the HEAD build, not the live host — so nobody has seen this in production.
- **Fleet snapshot loss is real and accepted**: an operator whose morning screen was the Dashboard table now clicks to `/agents`. Mitigation shipped (capped attention panel + both links), but the loss is a deliberate trade, not an oversight.
- Fixture caveat: the 26-record fixture loads as `inactive` under P12-03 registry semantics, so the attention panel's behaviour against a healthy fleet is untested with realistic data.
