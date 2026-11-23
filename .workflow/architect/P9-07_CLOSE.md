# P9-07 — CLOSE record (parent bookkeeping; artefact is P9-07_REVIEW.md)

**Commit:** 0352f3b71d801902ea32703299aebf18bcfe069f
head: 0352f3b71d801902ea32703299aebf18bcfe069f
repo: /opt/data/projects/PROBE_UPGRADE/v119wt
branch: r119-build
verdict: PASS
verdict_sha_of_head: 0352f3b71d801902ea32703299aebf18bcfe069f
verdict_scope: grades the review DELIVERABLE; the tree graded is 0352f3b (= 3ad56e5 source + the contract document; git diff --stat 3ad56e5..0352f3b = 1 file, 673 insertions, documentation only).
reviewed_by: none (reviewer IS the author of this artefact — WAIVED close)
artefact: /opt/data/tmp/probe119/P9-07_REVIEW.md
suite: reviewer-measured: go test ./internal/agent/ ./internal/upgrade/ ./internal/server/ ./internal/protocol/ -count=1 -v → 449 PASS / 0 FAIL, 4 packages ok. Full-repo suite NOT re-run by the reviewer, and not by the parent.

RESULT: FIX-FIRST — 6 blocking findings, ratification input NOT-READY.

BANNER SHA MISMATCH, recorded: the wake's REPO HEAD named 8da0580 ("P9-07: independent review…"), and `git cat-file -t 8da0580` returns "Not a valid object name" in /opt/data/projects/PROBE_UPGRADE/v119wt. The review document is on disk (22,391 B) but is NOT committed anywhere in that repo (`git log --all -- '*P9-07*'` is empty). The close therefore binds to 0352f3b, the tree the review actually graded, and the uncommitted state is a defect of the unit, not a reason to invent a sha.

Parent-measured / read from the artefact:
- artefact 22,391 B sha256 562cd5834eacd0fe3b21cffa174050beb586e0a8f2c583785838133035dc80e8 — matches the watcher's 562cd5834eac
- verdict_sha_of_head 0352f3b resolves and is repo HEAD; the contract sha the review binds to (792c113c1246…) matches the artefact P9-01 closed on
- six blocking findings, as the review names them: bootstrap kill authorization, check-c identity, the 60s fallback, watchdog/swap race, journal has no writer, 409 dialect
- the review framed the question correctly per brief — not "is the design good" but "is it testable, internally consistent, and true to the tree" — and stayed documentation-only (`SHAPE none`)
- declared limits, not silently passed: no Windows host (§9 runtime, taskkill under mixed principals, locked-exe claim graded on source + P8-10's UNVERIFIED marking); no live 1.18.6 binary, so R6 is a type-level derivation rather than a live run; suite limited to the four packages the claims touch
- scratch paths recorded: wt-p907 worktree, an untracked web/dist placeholder so embed.go compiles, and the suite log

Inherited, NOT re-measured by parent: every R1-R9 grade and the 449-pass run. The parent verified the artefact sha, the graded commit, the contract sha binding, and the banner-sha mismatch.

Open item: the contract needs a v3 fixing the six blocking items before ratification; P9-02..P9-06 stay parked behind it, and the review doc still needs committing.
