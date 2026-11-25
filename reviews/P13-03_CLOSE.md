# P13-03 — CLOSE

Unit: P13-03 · workflow wf_probe1 · branch `r119-build`

**Commit:** `b6a32f7` (repo `/opt/data/projects/PROBE_UPGRADE/v119wt`, pushed — origin/r119-build == `b6a32f7`, tree clean). Base `f243257`.
**Review:** reviewed_by: `PENDING` — reviewer leg owed · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `b6a32f7`
**Scope:** contract P10-07 v3.1 §7 + §7.1 only — `POST /api/v1/agents/{id}/ui-tree` on linux/X11 EWMH; the backend behind P13-02's `treeBackendPresent`. No §8 window control, no Wayland `foreign_toplevel`, no `wts` row, no change to the other five `NO_BACKEND` routes. Artefact `/opt/data/tmp/probe119/P13-03_WINDOW_TREE.md`.

## Parent verification (orchestrator, this close) — one claim does not reproduce

Measured at `b6a32f7`, not quoted from the child:

- **The suite is not reliably green.** Whole-suite `go test ./... -count=1` was run five times: **two runs failed `TestP1303RealXvfbRefusesNoWindowManager`, three passed** (695/1/2 vs 696/0/2). The child reported 696/0/2 and did not see the failure. In isolation the test passes every time.
- **Cause, read in the code:** `startBareXvfb` (`internal/agent/p1303_xvfb_test.go:30`) picks its display as `90 + os.Getpid()%8`, so two parallel test packages can select the same display number; and its readiness loop (`:43`) accepts `os.Stat` of `/tmp/.X11-unix/X<n>`, which a stale socket from a previously killed server satisfies, so the test can dial a server that is not the one it started. This is a display-allocation defect in the test, not flakiness in the product code.
- Everything else in the claim table held, read in the code: `treeBackendPresent = true` (`internal/agent/contexts.go:314`); `buildUITree` calls `wmCheck` at `uitree.go:54` **before** `clientList` at `:59`, so the §7.1 refusal genuinely precedes enumeration; `hwnd` appears in `internal/protocol/uitree.go` only in the C-4 comments (lines 10, 57), never as a field.
- Baseline arithmetic holds: 687 at `f243257` → delta +9 is the seven seam tests plus the two Xvfb tests.

**Verdict consequence:** `NEEDS-REVIEW`, and the flake is the first thing the review leg must falsify. A test that fails one run in two or three is not evidence for the §7.1 claim the unit rests on — the claim is probably true (it passes in isolation and the code order is right), but the *proof* is intermittent, which is the same class of defect as a fabricated pass.

## Implemented

§7 result and row shape; `context_ref` echo; `id_kind:"xid"`, `native{xid}`, no `hwnd` on any
non-`wts` row (asserted against the serialised body); top-level only (override-redirect and popup
types excluded, unmapped non-iconic excluded under `visible_only`); stacking order from
`_NET_CLIENT_LIST_STACKING`, `_NET_CLIENT_LIST` fallback; hard cap 500 with `truncated:true`;
`foreground` exactly one or none; `iconic` from `WM_STATE`; titles untruncated. §7.1: WM check
read before enumeration, absent/dead → `501 PRIMITIVE_UNSUPPORTED` `reason:"NO_WINDOW_MANAGER"`
with `detail{context, display, tree_source_attempted, ran:false, remediation}`, no
`XQueryTree(root)` fallback. `treeBackendPresent = true`; `window.tree` now follows observed WM
state.

## Not implemented (out of scope by brief)

§8 window control; Wayland enumeration; Windows `wts` rows. Those keep refusing.

## Verification

- `go test ./... -count=1 -json` → **696 pass / 0 fail / 2 skip** (baseline `f243257` 687/0/2, +9).
- `go vet ./...` clean; `GOOS=windows go build ./...` clean.
- §7.1 negative proved on a real Xvfb on this host (code + reason + `ran:false` asserted, not the
  absence of windows). WM-present path is **UNPROVEN-ON-HOST** — no window manager binary on this
  host; exercised through the seam with synthetic EWMH data only.
- Strict layer unchanged: `p1301_strict_test.go:119` (ui-tree unknown key `nope`) still passes.

Open item: WM-present enumeration has no live-desktop capture on this host — a re-run on a host
with an EWMH WM is the only missing evidence, and the seam tests stand in for it by construction.
