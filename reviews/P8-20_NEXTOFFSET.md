# P8-20 — C1 fix: fs-read next_offset for non-zero offsets on v1.18.6 agents

Unit: P8-20 · owner coder · repo /opt/data/projects/PROBE_UPGRADE/v119wt · branch r119-build
Start HEAD: 70dc607 (tree clean) · fix commit: 0c3ed3e76057369ad9e887f449d8baca53dbaa4d
closed_by: coder=c1-fix
reviewed_by: none (this is the fix submission; independent re-gate is the reviewer's unit)
verdict: FIX SUBMITTED — A1–A6 6/6 PASS by my own execution (quoted below); no independent review yet
verdict_sha_of_head: n/a (no review of this sha yet; the sha under test is 0c3ed3e)
Input: /opt/data/tmp/probe119/P8-19_REGATE.md §C1 (mechanism, file:line, measured rows) — not re-derived
Toolchain: go1.22.5 (/tmp/go/bin/go), GOFLAGS=-mod=vendor, web/dist present (gitignored embed input)

Scope held: the transfer/upload path (`api_v1_fs_transfer.go`, `api_v1_transfer.go`, `filetransfer.go`)
was not modified. P8-08/P8-09/P8-10 files not touched. No refactor of adjacent code.

---

## What changed (3 files + 1 new test file)

1. `internal/server/fs_integrity_fallback.go`
   - `applyFSIntegrityFallback(agentID, action, reqPath string, reqOffset int, resp interface{})` —
     the request offset is now a required parameter. The old signature had no way to see it, and
     `fillFSReadIntegrity` defaulted `offset := 0`.
   - `fillFSReadIntegrity(m, reqOffset)`: `offset := reqOffset`, with the agent's own `offset`
     still winning when present (1.19.0 agents keep their value).
   - `fsReqOffset(params interface{}) int` — reads the offset out of a typed `protocol.FSParams`
     or a raw `json.RawMessage` body (v1Forward forwards the raw body).
   - file comment: the `total_size`/`truncated` sentence restated (see A4).
2. `internal/server/server_api.go` — all four call sites pass `params.Offset`.
3. `internal/server/api_v1.go` — `v1Forward` passes `fsReqOffset(params)`.
4. `internal/server/p820_nextoffset_test.go` — wire-level regression guard (new).
5. `internal/server/p818_remediation_test.go` — two test helpers get the new `0` argument
   (compile fix only; no assertion changed).

---

## A1 — old1186 at offset 0, 1000, 8192: next_offset == offset + len(bytes) — PASS

Executed at the fixed tree, through the same HTTP surface the reviewer measured
(`POST /api/v1/agents/old/fs-read`), agent shaped as 1.18.6 (no `offset` key on the response),
window = 100 bytes:

```
=== RUN   TestP820OldAgentNextOffsetUsesRequestOffset/off0
    old1186 off=0 wire={"data":"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P0BBQkNERUZHSElKS0xNTk9QUVJTVFVWV1hZWltcXV5fYGFiYw==","encoding":"base64","next_offset":100,"sha256":"bce0aff19cf5aa6a7469a30d61d04e4376e4bbf6381052ee9e7f33925c954d52","sha256_scope":"window","size":100}
=== RUN   TestP820OldAgentNextOffsetUsesRequestOffset/off1000
    old1186 off=1000 wire={"data":"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P0BBQkNERUZHSElKS0xNTk9QUVJTVFVWV1hZWltcXV5fYGFiYw==","encoding":"base64","next_offset":1100,"sha256":"bce0aff19cf5aa6a7469a30d61d04e4376e4bbf6381052ee9e7f33925c954d52","sha256_scope":"window","size":100}
=== RUN   TestP820OldAgentNextOffsetUsesRequestOffset/off8192
    old1186 off=8192 wire={"data":"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P0BBQkNERUZHSElKS0xNTk9QUVJTVFVWV1hZWltcXV5fYGFiYw==","encoding":"base64","next_offset":8292,"sha256":"bce0aff19cf5aa6a7469a30d61d04e4376e4bbf6381052ee9e7f33925c954d52","sha256_scope":"window","size":100}
--- PASS: TestP820OldAgentNextOffsetUsesRequestOffset (0.01s)
    --- PASS: .../off0 (0.00s)
    --- PASS: .../off1000 (0.00s)
    --- PASS: .../off8192 (0.00s)
```

Row by row: 0 + 100 = 100 ✓ · 1000 + 100 = 1100 ✓ · 8192 + 100 = 8292 ✓. `size` stays 100 and the
digest is unchanged across offsets because the window bytes in the fixture are the same 100 bytes —
the assertion is on `next_offset` only, as the brief specifies; the byte path was not touched.

## A2 — new119 at offset 1000 unchanged (next=1100) — PASS

```
    new119 off=1000 wire={"data":"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+P0BBQkNERUZHSElKS0xNTk9QUVJTVFVWV1hZWltcXV5fYGFiYw==","encoding":"base64","next_offset":1100,"offset":1000,"sha256":"agent-own-digest","sha256_scope":"window","size":100,"total_size":10240,"truncated":true}
--- PASS: TestP820NewAgentNextOffsetUnchanged (0.00s)
```
The agent's own `next_offset`, `offset`, `sha256`, `sha256_scope`, `total_size` and `truncated` are
all present and none is overwritten — the fallback still fills only absent fields.

## A3 — other derived fields audited (same failure class: a derived value silently assuming a
request parameter it never received) — one line per field

| Field | Input actually used | Verdict |
|---|---|---|
| fs-read `sha256` | decoded window bytes from the response | correct — no request input needed; equals the 1.19.0 agent's own digest for the same window (P8-19 §C1a: `same=True`) |
| fs-read `sha256_scope`="window" | constant | correct — the digest covers the window, which is what it says |
| fs-read `size` | `len(decoded window)` | correct — the 1.18.6 agent's own `size` is also the window length (P8-19: `size=100` at offset 1000); it is not a file-size claim (that is `total_size`, which stays absent) |
| fs-read `encoding`="base64" | constant | correct |
| fs-read **`next_offset`** | **request offset + decoded length** (agent's own value wins) | **was the defect; now correct for off=0/1000/8192 (A1)** |
| fs-list `count` | `len(entries)` parsed from the response | correct — `handleFSList` takes only `Path` (`agent.go:767-784`); fs-list has no paging, so no derived value depends on a request offset |
| fs-list `entry_count` | same `len(entries)` | correct — same construction the agent uses (`withListingDigest`, `agent.go:791-807`) |
| fs-list `sha256` / `sha256_scope`="listing" | canonical JSON of the returned entries array | correct — byte-identical to the agent's digest for the same array (P8-19: `same=True`, empty dir `4f53cda1…`) |
| fs-stat `sha256` | the returned record re-marshalled with digest fields zeroed | correct — equals the agent's construction; note it round-trips through `FSStatResult`, so a field unknown to that struct would be dropped from the digest. The 1.18.6 record has no such field (P8-19 measured equal digests) |
| fs-stat `sha256_scope`="listing" | constant | correct by equivalence — `handleFSStat` (`agent.go:826-833`) also stamps `"listing"`; the label is odd for a stat record but it is the agent's own literal, so old and new agents agree. Naming only, no data error |
| fs-hash `sha256_scope`="file" | the agent's own digest, already present | correct — the value only labels coverage; the digest is never synthesised here |
| fs-write `sha256` / `sha256_scope`="file" | fs-hash read-back from the same agent after the write | correct — a real post-write read of the file on disk, not a request echo (P8-19: `sha_matches_disk=True`) |
| fs-write `size` | the fs-hash response's `size` (whole file) | correct — consistent with `sha256_scope="file"`; the chunk's `written` count stays the agent's own field, untouched |
| fs-write `path` (read-back target) | response `path`, else `reqPath` | correct — resolves to the file the request named |
| resume path `next` (`api_v1_fs_transfer.go:148-152`, `remoteReadWindow`) | `offset + len(raw)` with its own `offset` argument, agent's value preferred | correct — this path already had the request offset in hand, which is exactly why the defect was confined to the fallback |

Conclusion: `next_offset` was the only field in the fallback that consumed a request parameter it
was not given. Every other derived value is a function of the response or of a constant.

## A4 — the gap sentence now matches the code — PASS

Old text (false as stated; P8-19 §C1b measured that `fs-stat` reports the file size on 1.18.6 and
that `truncated` is decidable from the window):

> a field the server cannot derive is left absent and is a DOCUMENTED VERSION-GATED GAP
> (fs-read total_size/truncated on a pre-1.19 agent: an old agent reports neither, and the server
> cannot distinguish a file that ends exactly at the cap from one that continues)

New text in `fs_integrity_fallback.go`:

> fs-read total_size/truncated stay absent for a pre-1.19 agent. This is a SCOPE CUT, not a
> physical limit: both are derivable (fs-stat reports the file size on 1.18.6, and truncated =
> offset+len(window) < size), but that costs an extra agent round-trip on every read, so this
> fallback does not make it. The caller gets a correct window and a correct next_offset, and
> nothing here claims otherwise.

The restated reason is demonstrable in one grep: `fillFSReadIntegrity` makes no `fs-stat` or
`fs-hash` call (it cannot — it has no agent handle), so the only reason the fields are absent is
that this function does not spend a round-trip on them. The limitation itself is unchanged and
still truthful (the fields are absent); only the justification moved from "impossible" to "not done
here". A second read at `next_offset` — the cheap alternative P8-19 names — is likewise not made.

## A5 — suite at the fix commit — 534 PASS / 0 FAIL

```
$ GOFLAGS=-mod=vendor go test ./... -count=1 -v      (go1.22.5)
exit=0
--- PASS lines: 534
--- FAIL lines: 0
ok packages:   10
```
Baseline 529P/0F at 6f34193 (P8-19's run and mine). Delta: +5, exactly the new test and its three
subtests plus the 1.19.0 control. No previously passing test changed state. Full log:
`/opt/data/tmp/probe119/p820_suite_v.log`.

## A6 — the new test fails on the pre-fix code — PASS (guard, not a description)

Worktree `/opt/data/tmp/probe119/wt-p820` at `6f34193a179f594869f7578369faefb8843fe46d`, the new
test copied in unmodified (it is wire-level on purpose, so it compiles against the old signature
and fails on an assertion instead of a build error):

```
$ git rev-parse HEAD
6f34193a179f594869f7578369faefb8843fe46d
$ go test ./internal/server/ -run 'TestP820' -count=1 -v
=== RUN   TestP820OldAgentNextOffsetUsesRequestOffset/off0
    old1186 off=0 wire={...,"next_offset":100,...}
=== RUN   TestP820OldAgentNextOffsetUsesRequestOffset/off1000
    old1186 off=1000 wire={...,"next_offset":100,...}
    p820_nextoffset_test.go:72: next_offset = 100, want 1100 (request_offset 1000 + 100 decoded bytes) — wire={...,"next_offset":100,...}
=== RUN   TestP820OldAgentNextOffsetUsesRequestOffset/off8192
    old1186 off=8192 wire={...,"next_offset":100,...}
    p820_nextoffset_test.go:72: next_offset = 100, want 8292 (request_offset 8192 + 100 decoded bytes) — wire={...,"next_offset":100,...}
--- FAIL: TestP820OldAgentNextOffsetUsesRequestOffset (0.09s)
    --- PASS: .../off0 (0.05s)
    --- FAIL: .../off1000 (0.04s)
    --- FAIL: .../off8192 (0.00s)
=== RUN   TestP820NewAgentNextOffsetUnchanged
--- PASS: TestP820NewAgentNextOffsetUnchanged (0.00s)
FAIL	github.com/falke-ai-circuit/probe/internal/server	0.098s
```

This reproduces P8-19's measured rows on the pre-fix code (`off=0 → 100`, `off=1000 → 100`,
`off=8192 → 100`) and shows the control test already passing, i.e. the guard catches exactly the
defect the review found and nothing else.

---

## Not done / open

- `total_size`/`truncated` are still absent on pre-1.19 agents (A4: declared scope cut, now
  honestly stated). Implementing them costs one extra `fs-stat` per read; that is a separate unit,
  not snuck in here.
- No Windows/Vegas execution was performed for this unit and none is claimed; the evidence above is
  the Go suite plus the wire-level test, same as P8-19's own method.
