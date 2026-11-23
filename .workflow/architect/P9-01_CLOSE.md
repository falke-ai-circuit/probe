# P9-01 — CLOSE record (parent bookkeeping; artefact is P9-01_UPDATE_CONTRACT.md)

**Commit:** 0352f3b71d801902ea32703299aebf18bcfe069f
head: 0352f3b71d801902ea32703299aebf18bcfe069f
repo: /opt/data/projects/PROBE_UPGRADE/v119wt
branch: r119-build
verdict: NEEDS-REVIEW
verdict_sha_of_head: 0352f3b71d801902ea32703299aebf18bcfe069f
verdict_scope: grades the CONTRACT DELIVERABLE; the graded tree is 3ad56e5 (source untouched by this unit). 0352f3b adds the v2 document plus its close record — no product code.
content_commit: b3c0b3c (contract v1), v2 corrections at 0352f3b
reviewed_by: none (no independent reviewer leg ran — the artefact's own header reads `reviewed_by | architect self-check v2 (independent reviewer: pending)`; WAIVED close)
artefact: /opt/data/tmp/probe119/P9-01_UPDATE_CONTRACT.md
suite: not applicable — design unit ran no product test suite. Tree read at 3ad56e5, working tree clean.

Why NEEDS-REVIEW rather than PASS: the author wrote `independent reviewer: pending` in its own header. A self-check is not a review, and the parent does not award the leg the author declined to claim.

Parent-measured / read from the artefact:
- artefact 62,841 B sha256 792c113c12463d563ca60b0c52552799d853c55310f0229bb34c81f57d373174 — matches the watcher's 792c113c1246
- commits resolve on r119-build: 3ad56e5 (tree read, source untouched), b3c0b3c (contract v1), 0352f3b (v2 corrections + close record, HEAD)
- the contract delivers what the brief demanded and the board's acceptance names: named state machine (requested → artifact-verified → canary-connected → promoted → old-killed → confirmed, plus failed/rolled-back), the FOUR confirmation checks as the definition of "confirmed" (version read from the artifact, same-name re-registration, old PID absent, one post-swap functional probe), a 90s watchdog with old-process restore, kill-old semantics with server-authority proc_kill, claim/lease idempotency+concurrency, and a channel decision — download_url primary at 256 KiB chunks with a 64 MiB artifact ceiling
- v2 corrects four measured defects found in v1 (§0.4 X1-X4): kill identity on real ProcessInfo, sandbox kill authorization, the Windows probe command, and server-authority proc_kill — a self-correction the author recorded rather than quietly folding in
- scope discipline held: reuses internal/upgrade + internal/agent/canary.go, no parallel swap mechanism, no agent-wire field the 1.18.6 fleet must learn, no frontend rewrite (additive verdict), no code changed
- ratification input pinned: P8-02_DESIGN.md 871 lines / 69,707 B

Inherited, NOT re-measured by parent: every file:line citation, the live probe runs the contract reports, and the X1-X4 corrections. The parent verified the artefact's sha, the three commits, and the header's own review status.

Open item: Goran's ratification is the gate — no coder dispatch on P9-02/P9-03 until he rules, and an independent reviewer leg is owed on the four confirmation checks (the load-bearing claim). The unratified state is now machine state, not prose.
