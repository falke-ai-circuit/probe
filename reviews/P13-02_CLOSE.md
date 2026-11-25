# P13-02 — CLOSE

Unit: P13-02 · workflow wf_probe1 · branch `r119-build`

**Commit:** `f243257` (repo `/opt/data/projects/PROBE_UPGRADE/v119wt`, branch `r119-build`, pushed — origin/r119-build == `f243257`, tree clean after the document commit that carries this file). Base `7c19a1a`; P13-01's code is at `7274ec2` and its substrate is reused, not duplicated.
**Review:** reviewed_by: `PENDING` — reviewer leg owed · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `f243257`
**Scope:** contract P10-07 v3.1 §6 only — read-only `GET /api/v1/agents/{id}/contexts`, linux enumeration (x11, wayland, uid), the single `ClassifyContext` predicate shared by discovery and the selector gate. No context entry, no window control, no write verb (OD-4), no `wts` row, contract document untouched. Artefact `/opt/data/tmp/probe119/P13-02_CONTEXTS.md` (8628 B).

## Implemented

`{ok,data:{agent_context,generated_at_utc,contexts[]}}` with the full §6 row shape; `input` scope
(viewer → 403); query parameter → `400 UNKNOWN_FIELD`; `active` nullable with `active_known`;
`idle_s:-1` / `logon_time:""` where unanswerable; `addressable` produced by `ClassifyContext` and
enforced by `ResolveContext` reading that row's verdict; §6 reason enum closed and fully produced;
per-context `features` with the `NO_WINDOW_MANAGER` refusal; linux-only enumeration, non-linux
answers `PRIMITIVE_UNSUPPORTED/NO_BACKEND`.

## Not implemented (out of scope by brief)

Context entry (`CONTEXT_UNAVAILABLE` enter path), window tree/control backends, `disconnect` /
`logoff` / `message` / `switch`, any Windows (`wts`) row.

## Tests

14 new (4 protocol, 6 agent, 4 server), each acceptance item named in the artefact.
`go test ./... -count=1 -json` → **687 pass / 0 fail / 2 skip** (baseline `7274ec2` 673/0/2; delta
+14). `go vet ./...` clean; `GOOS=windows` build of agent/protocol/server clean.

Open item: `treeBackendPresent=false` — `window.tree` cannot report `true` until the §7 EWMH
backend lands; the `NO_WINDOW_MANAGER` refusal is already correct, the flip is one constant.
