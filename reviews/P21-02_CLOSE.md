# P21-02 — CLOSE (written by the parent; the unit delivered the contract and no close record, and the registry half-close had no watcher ledger row)

**Commit:** `147c7f5` — the tree HEAD the contract's citations resolve against. This unit changed **no code and no tree file**: scope was a read-only contract document, so there is no unit head to bind to.
**Head-move waiver:** the documents for this unit land in the record commit that follows this file; the verdict binds to `147c7f5`, the tree every `path:line` was read at. Recorded rather than assumed — a document unit has no code commit and pretending otherwise would bind the verdict to a tree nobody read.
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: 147c7f5`
**Scope:** `GET /api/v1/servers` contract (federation P1), 64,774 B, 14 sections + appendix. No Go file, no config, no live agent touched. Downstream implementer: P18-06 (badge).

## Delivered

A contract for one endpoint that serves the operator's badge need — *"which server am I looking at"* — from live data with **no** second endpoint, local entry at index 0. Entry shape is a superset of the spec's P1 object (additive, nullable, nothing renamed or dropped), badge consumes exactly six fields (`id, name, listen_addr, version, role, state`), everything else expanded-view only. DOWN peers are records with `last_seen`/`down_since`/`state_reason` rather than absences, so a disconnect does not make a server vanish from the list.

## What the contract corrects in its own sources — all four verified by the parent

- **D1** — the spec's "same atomic-write pattern as `registry.json`" premise is **false**. Verified: `internal/server/registry.go:554` is a bare `os.WriteFile(..., 0644)` with no temp file, no rename, no fsync; the only atomic write in the server is the credential store's tmp+rename at `credentials.go:249-261` (which itself has no fsync, so the contract's pattern of record is that one plus fsync). Consequence adopted: `servers.json` must not copy the registry pattern, and retrofitting the registry is explicitly out of unit.
- **D2** — `relay_register.token` is parsed and never checked: with one shared token every peer's `token_fp` is byte-identical and identifies nothing, so the schema carries `token_fp_verified` and `token_fp_scope` rather than implying verification it cannot have.
- **D3** — the only existing "server name" is the bind string: verified `api_v1_phase4.go:142` `Name: s.addr`, which renders `localhost:7700` on the Radxa and is unbrowsable. `name` therefore gets a precedence chain and is forbidden from defaulting to `s.addr`.
- **D4** — `max_agents` is a relay-side flag with no server-side storage or enforcement, so it is `null` for local and for peers that never reported one — never `0`, never a guessed 100.

The pre-auth constraint is the sharpest finding: a badge in the shell is invisible on first open because `web/src/App.tsx:19-23` returns `<Login/>` without a token, so `/api/v1/health` carries a four-key `identity` block for the login screen, built by the same builder as `servers[0]` and byte-equal on shared keys — not `r.Host`, and switchable off. Option B (post-login only) is named and not chosen, with the reason stated.

## Open decisions — the owner's, not the architect's (§13)

Ten deferred decisions, each with options and a provisional answer: retention window (24 h), `name` default (`os.Hostname()` + override), local-host substitution when the bind addr is `localhost`/`0.0.0.0`, `max_agents` null-vs-zero, pre-P7 `token_fp` exposure, badge fallback when the endpoint is down, auth gate, new-endpoint-vs-topology, pre-auth identity exposure, and login-screen host format. **These are not closed by this unit** and P18-06 will need at least O2, O3, O6 and O10 answered before it renders.

## Suite

No suite claimed and none meaningful: this unit is a document, and the parent ran no test as evidence for it. The tree was verified clean and the four D-claims were re-read from source at the cited lines rather than trusted from the document.

## Open items

- The ten decisions above (§13) — owner/conductor.
- §12 gives re-runnable acceptance probes for the eventual implementer; they are untested here because no code exists yet.
- `registry.json` non-atomicity (D1) is named and deliberately **not** fixed by this unit; it is a live data-loss window on a crash mid-write and deserves its own unit.
