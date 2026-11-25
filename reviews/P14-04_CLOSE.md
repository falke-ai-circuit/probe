# P14-04 — close

**Commit:** code under review `23cf971` (P14-01), `8cdbb18` (P14-02), `fd7d548` (P14-03) — all ancestors of the reviewed head `3b32753`; every commit above `fd7d548` is documentation only, and `git diff --stat fd7d548 HEAD -- internal/` is empty. This unit's own review documents are committed above that.
**Review:** reviewed_by: `reviewer` (independent; neither author `coder` nor sequencer) · verdict: `FIX-FIRST` · verdict_sha_of_head: `3b32753`
**Verdict vocabulary note:** the reviewer's ruling on the change set is **FAIL — 5 of 6 items PASS, item 1 FAIL, R1/R2 blocking** (`P14-04_REVIEW.md`, header line and per-item table). This row reads `FIX-FIRST` because that is the close gate's enum for "review delivered, defects listed and owned": the review is complete, and the change set does not close until P14-01 is re-gated.
**Scope:** independent review of P14-01/02/03 against `docs/contracts/P10-07_CONTRACT_V3_1.md` §9.1-9.4, §10, §11 — per-item verdicts from committed blobs, three deviation rulings, four falsification re-runs in throwaway copies, five Xvfb wire probes, three whole-suite runs. No fixes in the worktree; no CT100 or live agent touched; worktree clean.

## Verdict

| # | item | verdict |
|---|---|---|
| 1 | §9.1 geometry | **FAIL** — R1 `x11_point.go:41-43` decodes `TranslateCoordinates` four bytes early (child from the length word), so `PointIn` always returns 0, the descent stops at the root and every window-scoped pointer primitive answers OCCLUDED; R2 `x11.go:317-327` leaves 12 bytes of `GetWindowAttributes` unread, which desyncs the connection at the first descent step |
| 2 | §9.2 multi-click | PASS (R3) |
| 3 | §9.3 scroll sign | PASS |
| 4 | §9.4 unicode | PASS (R6) |
| 5 | §10 capture | PASS (R5 open item) |
| 6 | suite | PASS — 736 / 0 / 2, exit 0, 3/3 runs |

Deviations: §9.1 mechanism ACCEPTED as a question, FAIL as an implementation; §9.4 utf16 ACCEPTED with
R4 (lone surrogate → U+FFFD, unlabelled); §10 compositor ACCEPTED as a refusal, mislabelled as a 500.

## Evidence pointers

- Suite logs: `/opt/data/tmp/probe119/P14-04_run{1,2,3}.log` (736/0/2 each).
- Falsifications: P14-02 scroll flip → 3 subtests red; P14-01 gate disabled and refusal-also-clicks →
  both zero-event assertions red; P14-03 encoded-file hash → red with the unit's own reported digest.
- Blocking finding: Xvfb raw reply `child@8=0x200001` vs `PointIn` returning `child=0x0`, and
  `occludingWindow(requested=0x200001)` → `hit=0x21f` (root); with the two-line offsets fix plus the
  reply drain applied in a throwaway copy, `path=[root,0x200001]` and `hit=0`.
