# P14B-02 — CLOSE

**Commit:** `f439b0d` (pushed to `origin/r119-build`)
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: f439b0d`
**Scope:** `internal/agent/input_pointer.go`, `internal/agent/p1402_input_test.go`, `internal/protocol/input_geometry.go` (label keys), `internal/agent/agent.go` (3 result sites), `internal/server/api_v1_transfer.go` (raw-params forward, deviation argued in R4 §5), `internal/server/p14b02_raw_text_test.go`. Artefact: `/opt/data/tmp/probe119/P14B-02_R4.md`.

Suite: 753 pass / 0 fail / 2 skip (baseline 745/0/2 at 27e5b74; PASS-set diff: 0 removed, +4 tests / +4 subtests).

Decision: LABEL, on the contract's "Never silently lossy" (P10-07 v3.1 §9.4:566) — the line forbids the
silence and licenses a `detail` report, and nowhere authorises refusing text it can type.

Done:
- `"a\ud800b"` → `detail.repaired_rune_count:1, repaired_at:[2]`; `"a\ufffdb"` → no such key. Same decoded
  string, two outcomes — quoted in R4 §3.
- `"\U0001F600"` literal and `"\ud83d\ude00"` escaped: not labelled, one keysym `0x0101F600`.
- Red before green after for the lone surrogate (R4 §4): FAIL "a\\ud800 was repaired in silence" → PASS.
- `P14-02`'s UTF-16 test still passes unchanged (transcoded:true, 5 codepoints).

Open item: `R4b` — invalid UTF-8 **bytes** in `text` are repaired to U+FFFD by `encoding/json` and are still
reported as "no repair": the detector answers in code-unit positions, and a byte-level replacement has none.
Named, not absorbed (R4 §7).
