# P14-06 — close

**Unit:** P14-06 · **Workflow:** wf_probe1 · **Branch:** `r119-build`
**Commit:** `ee5e97a` — `P14-05: fix §9.1 wire decode — TranslateCoordinates offsets and the reply-tail drain` (the code gated; the gate's own commit follows this file)
**Review:** reviewed_by: `architect` · verdict: `PASS` · verdict_sha_of_head: `ee5e97a4c666330e8450640c7154415a4b02273f`
**Head-move waiver:** the verdict binds to `ee5e97a`, the code commit it rules on. `48bef39` and the gate's own document commit sit above it and change no code — `git diff --stat ee5e97a HEAD -- internal/` is empty and 0 commits in `ee5e97a..HEAD` touch `internal/`. The original row named `48bef39`, the head at verification time, which is not the sha the fix lives in — corrected so the verdict binds to the tree it judged.
**Scope:** §9.1 item 1 only — R1 `PointIn` reply offsets, R2 reply-tail drain, wire coverage. R3-R6 remain open and are named as later work, not closed; R7 (live desktop) stays UNPROVEN-ON-HOST.
**Artefact:** `/opt/data/tmp/probe119/P14-06_VERIFICATION.md`
**Suite:** `GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1` → 4 runs, exit 0 each, **739 pass / 0 fail / 2 skip** (`-v` count run)

## Verdicts

V1 PASS · V2 PASS · V3 PASS · V4 PASS · V5 PASS. **P14-04 item 1: FAIL → PASS.**

## Evidence that decided it

- Reply layout confirmed by a non-Go raw X11 client: raw header `0101 0400 00000000 01002000 0f00 1900 00…` → child `0x00200001` at `[8:12]`, dx 15 at `[12:14]`, dy 25 at `[14:16]`; `GetWindowAttributes` length word 3 (44 B) with a 12-byte tail.
- Through the package on Xvfb: `PointIn(root,15,25)` → `(15, 25, 0x200001)`; `PointIn(win,15,25)` → `(5, 5, 0)`; `PointIn(win,5,8)` → `(-5, -12)` (INT16 signed).
- One connection, five replies: geometry#1 ok / attributes ok / geometry#2 ok / property `errPropAbsent` / geometry#3 ok — the third call no longer dies.
- Non-vacuity, both reverts in a throwaway copy: old offsets → `child = 0x0`, `deltas = 1,32`, `hit = 0x21f`, `path=[543]` (2 tests red); drain removed → `geometry#2 after attributes: x11 error 0` (1 test red).
- `git show --name-only ee5e97a` → 3 files; no hunk under `geometry.go`, `input_pointer.go`, `capture*.go`; `p1401_geometry_test.go` has exactly one commit (`23cf971`), so no shipped assertion was weakened.

## Open items

- R3 (`gap_ms` echo, §9.2), R4 (lone surrogate, §9.4), R6 (`codepointUnsupported` names the wrong rune, §9.4 — its test accepts `U+0061` for `"a\U0001F600"`): later work, none blocking §9.1.
- R5 (composited window → 500 instead of 501 `NOT_EXPOSED`, §10): later work.
- R7: live-desktop behaviour under a WM/compositor unproven on this host; not claimed.
