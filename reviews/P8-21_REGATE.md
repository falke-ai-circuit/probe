VERDICT PASS /opt/data/tmp/probe119/P8-21_REGATE.md 0c3ed3e76057369ad9e887f449d8baca53dbaa4d 534P/0F at 0c3ed3e, 529P/0F at 70dc607

# P8-21 — narrow re-gate of the P8-20 next_offset fix

Unit: P8-21 · owner reviewer=independent · repo /opt/data/projects/PROBE_UPGRADE/v119wt · branch r119-build
Graded sha: 0c3ed3e76057369ad9e887f449d8baca53dbaa4d (fix). Unit head: 8706e0727864a99e9e4ecd3fd4f4b7293cb8ad34 (close header only, no product code).
reviewed_by: reviewer=independent
verdict: PASS — C1a PASS, C1b PASS, A6 guard bites, suite matches at both shas. Rollout gate CLEARED.
verdict_sha_of_head: 0c3ed3e76057369ad9e887f449d8baca53dbaa4d
Inputs: P8-19_REGATE.md §C1 (defect), P8-20_NEXTOFFSET.md (fix claim). Scope held to C1a + C1b + A6; C2–C7 not re-litigated, no artefact-wide read.

## Method / scratch paths (all recorded, no product code changed)

| Item | Path |
|---|---|
| Fix worktree 0c3ed3e (clean, `git status --porcelain` empty) | /opt/data/tmp/probe119/wt-p821 |
| Pre-fix worktree 70dc607 (clean before guard copy) | /opt/data/tmp/probe119/wt-p821base |
| Server built at 0c3ed3e | /opt/data/tmp/probe119/p821-bin/probe-server (sha256 113831087f5a8b27…) |
| Real 1.18.6 client (unchanged vendor binary) | /opt/data/tmp/v119/bin/probe-client-1186 (sha256 715f322582933abb…, banner `PROBE Client v1.18.6`) |
| 1.19.0 client built at 0c3ed3e | /opt/data/tmp/probe119/p821-bin/probe-client |
| Wire probe + server + registry + fixtures | /opt/data/tmp/probe119/p821-scratch (server on 127.0.0.1:17921; fixtures big.bin=10240 B, small.bin=31 B, utf8.txt=1000 B multi-byte) |
| Raw probe output | p821-scratch/wire_out.txt, p821-scratch/wire_out_b.txt |
| Suite logs | p821_suite_fix.log, p821_suite_base70.log |
| Guard log | p821_guard_70dc607.log |

Both worktrees had `web/dist` copied in from the repo (gitignored embed input); embed built cleanly, so the "6 packages fail" trap was not hit. Registry at probe time: `{'new119': '1.19.0', 'old1186': '1.18.6'}` — the old agent is the real 1.18.6 binary, not a shim.

Scope note on two detours: the router reading suggested reading filetransfer.go and M36-09_FIX.md first; both are outside this narrow brief (C2–C7 not in scope, M36-09 unrelated). I read P8-20's claim, the fallback source and its call sites instead.

## C1a — next_offset on a real v1.18.6 agent — PASS (E1, executed)

Server built at 0c3ed3e, `POST /api/v1/agents/old1186/fs-read` (the registered route goes through `v1WrapAgentHandler("fs-read", s.handleAgentFSRead)`, api_v1.go:314-315 → server_api.go:465). Wire dumps, verbatim:

```
old1186 off=0    {"data":"+5u4djEwzYmKt+Wqn2OsjDkCwNcDADDB/gr0X/KR7vAtK7XPUntik1OHWgyuSm1bsow6CpqZj6UXm+p7Sqr6G62L7t2Yg+ZtuIqCf+dplnoVdE6CJpm25CuzqKfTDjv5NxK9vw==","encoding":"base64","next_offset":100,"sha256":"b033add5cf066bf944eacbd9357817edced942a133bf133c0b205e2ea7c794ef","sha256_scope":"window","size":100}
old1186 off=1000 {"data":"zRmlyIqsHR5rhXl+PHeycqrQcKX+iX0FvJRs5oD6NeXZCnirUuOWNveGGFg3t5mE3d7OXB0jBrLYKNZAyQSh+9e30InEctUxjHPGaENV8nXWTtedanHo0xKo3yjVJ1lqIeyklg==","encoding":"base64","next_offset":1100,"sha256":"880bbb51d5771589cfe586d4c6066b0200caf9e5e934461cacf5e68a1427f4ed","sha256_scope":"window","size":100}
old1186 off=8192 {"data":"VrROrw2zdFD3WiPEB/Uck1uYH4u8Z9+OnSxsJGmcK0njaUHiLDDnG4dRaypwwwqvajTrum2l2A+cJodHtWjOGcZ937oebc5eoBQkoIUEa6roSgYWc9gOTgXSKlOSbkz03t7Apw==","encoding":"base64","next_offset":8292,"sha256":"c1449cd0d2513657671a5aec397a23c92f1681862bce028678d67d38c9c771e2","sha256_scope":"window","size":100}
```

100 / 1100 / 8292 — the three rows P8-19 measured as 100/100/100 are now offset+len(bytes) on the wire (E1). `offset` is absent from every old-agent response, as expected: the value the agent never echoes is the one the server now carries.

Boundaries the fix does not mention, all measured on the same binary (E1):

| Case | Request | Wire | Expected | Result |
|---|---|---|---|---|
| file smaller than limit | off=0 limit=100 on 31 B file | `decoded=31 next_offset=31` | file size 31, not offset+limit | PASS |
| short read at tail | off=10200 limit=1000 on 10240 B | `decoded=40 next=10240` | 10240 | PASS, bytes == file[10200:] |
| huge limit | off=0 limit=1000000 | `decoded=10240 next=10240` | clamped window end, not 1000000 | PASS |
| odd offset + tight window | off=1 limit=7 | `decoded=7 next=8` | 8, bytes == file[1:8] | PASS |
| at EOF | off=10240 limit=100 | `data":"" size=0 next=10240` | no progress past EOF | PASS |
| full window | off=0 limit=8192 on 10240 B | `decoded=8192 next=8192` | 8192 | PASS |

The decisive test: a walk that follows only the returned next_offset and concatenates. Byte-exact equality with the original file proves no gap, no overlap, no dropped byte — including windows that split multi-byte characters:

```
-- walk old1186 big.bin limit=100  file=10240 steps=103 collected=10240 exact_match=True
-- walk old1186 big.bin limit=8192 file=10240 steps=2   collected=10240 exact_match=True
-- walk old1186 utf8.txt limit=7   file=1000  steps=143 collected=1000  exact_match=True
-- walk old1186 big.bin limit=7    file=10240 steps=1463 collected=10240 exact_match=True
-- walk new119 big.bin limit=100   file=10240 steps=103 collected=10240 exact_match=True
-- walk new119 utf8.txt limit=7    file=1000  steps=143 collected=1000  exact_match=True
```

A next_offset that was arithmetically tidy but landed mid-character or skipped a byte would break exact_match on the limit=7 utf8 walk (143 windows, every one 7 bytes). It does not. E1, executed, not derived.

Agent's own value still wins (E1): `new119 off=1000 → next_offset=1100, offset=1000` with `total_size=10240, truncated=true` intact; identical at off=0 and off=8192. fs-list/fs-stat/fs-hash digests at the fix are byte-identical between old and new agents (`same=True` for all three), and fs-write on the old agent returns a digest matching the file on disk (`sha_matches_disk=True`, size 30, verified true).

## C1b — "next_offset was the ONLY fallback field consuming a request parameter it never received" — PASS (re-derived, E2)

Function signature (fs_integrity_fallback.go:40):

```go
func (s *Server) applyFSIntegrityFallback(agentID, action, reqPath string, reqOffset int, resp interface{}) interface{}
```

Reaches it via four call sites, all quoted (E3, grep over the tree):
- server_api.go:465 `applyFSIntegrityFallback(agentID, "fs-read", params.Path, params.Offset, resp)`
- server_api.go:523 fs-write, :556 fs-stat, :583 fs-hash — same shape, `params.Path`, `params.Offset`
- api_v1.go:269 `applyFSIntegrityFallback(agentID, action, "", fsReqOffset(params), resp)` (v1Forward: fs-list/move/mkdir/delete only; fs-read/write/stat/hash are registered through v1WrapAgentHandler, api_v1.go:314-321)

Every field the fallback synthesises, its actual input, and whether that input is a request parameter that does not reach the function:

| Field | Input actually used | Request param unreceived? |
|---|---|---|
| fs-read sha256 | sha256 of decoded response bytes | no request input |
| fs-read sha256_scope | constant "window" | no |
| fs-read size | len(decoded window) | no (this is window length, not file size; total_size is the file-size claim and stays absent) |
| fs-read encoding | constant "base64" | no |
| fs-read next_offset | reqOffset + len(decoded), agent `offset` preferred | **was the defect — reqOffset is now a parameter and reaches the function** |
| fs-list count / entry_count | len(entries) parsed from the response | no |
| fs-list sha256 / sha256_scope | canonical JSON of returned entries; constant "listing" | no |
| fs-stat sha256 / sha256_scope | returned record re-marshalled with digest fields zeroed; constant "listing" | no |
| fs-hash sha256_scope | constant "file", digest is the agent's own | no |
| fs-write sha256 / scope / size / verified | fs-hash read-back from the same agent after the write | no |
| fs-write read-back target path | response `path`, else `reqPath` | `reqPath` IS a parameter of the function and is passed by every call site (params.Path on the live fs-write route) |

No second field of the same class exists. The absence claim survives re-derivation: the only request-derived value in the fallback is the position a return value is relative to, and that position is now supplied.

One near-miss, stated because it is the shape of the original bug: api_v1.go:269 passes `reqPath` as a literal `""` — a placeholder for an unknown, exactly what `offset := 0` was. It produces no wrong value today because no action registered through v1Forward consumes reqPath. Non-blocking, listed below.

## A6 — the guard — BITES (E2, ran it myself at 70dc607)

New test copied unmodified into the pre-fix tree `/opt/data/tmp/probe119/wt-p821base` at `70dc607113d3122f5741837053af76059d20834b`:

```
p820_nextoffset_test.go:72: next_offset = 100, want 1100 (request_offset 1000 + 100 decoded bytes) — wire={...,"next_offset":100,...}
p820_nextoffset_test.go:72: next_offset = 100, want 8292 (request_offset 8192 + 100 decoded bytes) — wire={...,"next_offset":100,...}
--- FAIL: TestP820OldAgentNextOffsetUsesRequestOffset (0.01s)
--- PASS: TestP820NewAgentNextOffsetUnchanged (0.00s)
FAIL github.com/falke-ai-circuit/probe/internal/server 0.010s
```

It fails for the right reason: an assertion at line 72 on the value, with the wire quoted; no compile error, no missing fixture, and the 1.19.0 control already passes on the old tree. The guard catches the defect and nothing else. (The author ran it at 6f34193; I ran the brief's tree, 70dc607 — same result.)

## Suite — my own runs, both shas

```
GOFLAGS=-mod=vendor go test ./... -count=1 -v   (go1.22.5)
0c3ed3e: 534 PASS / 0 FAIL, 10 ok packages, exit 0   (p821_suite_fix.log)
70dc607: 529 PASS / 0 FAIL, 10 ok packages, exit 0   (p821_suite_base70.log)
```

Counts include subtests (`--- PASS` at any indent). Delta +5 = the new wire test, its three subtests and the control, matching the author's claim. No previously passing test changed state. Exit 0 at both (I checked exit codes, not just the log tails).

## Shape line (G-SHAPE-FINDING)

SHAPE parallel `internal/server/fs_integrity_fallback.go:fsReqOffset` vs `internal/server/server_api.go:439-443` (inline `json.Unmarshal(body, &params)` into `protocol.FSParams`). The unit adds a second parameter-decoding site rather than reusing one decode helper; both sites are named. Consequence in the non-blocking list: three of fsReqOffset's four type branches are unreachable in production.

## Nemesis — three hostile attacks, all addressed

1. **Arithmetic-only fix.** A fix computing `next = reqOffset + windowLen` regardless of what the agent returned would satisfy the three headline rows. Broken by: small file (31 B → 31, not limit), tail short read (40 B → 10240), huge limit (clamped to 10240), EOF (0 bytes → 10240). Correct in all four because the term is `len(decoded)`.
2. **Fallback tramples the new agent.** Broken by the 1.19.0 control: next_offset 1100, offset 1000, total_size/truncated preserved; digests unchanged for fs-list/stat/hash.
3. **Guard passes for the wrong reason.** Broken by running it on 70dc607: assertion failure at line 72 with the wire attached, control passing — not a build or fixture error.

Drift check. Request-drift: none — two claims graded, no artefact-wide read, C2–C7 untouched. Implementation-drift: the fix lands on the typed route the wire actually uses (server_api.go:465), not on the raw v1Forward path; the offset reaches `fillFSReadIntegrity` (fs_integrity_fallback.go:119-125). Verification-drift: both suite runs and the pre-fix guard run are mine, logs on disk.

## Non-blocking (recorded, not inflated)

1. `fsReqOffset` (fs_integrity_fallback.go:67-87) has four type branches; its only call site is api_v1.go:269, where `params` is always `json.RawMessage` or nil, and the actions on that route (fs-list/move/mkdir/delete) carry no offset. The `protocol.FSParams`, `*protocol.FSParams` and `[]byte` branches are dead and untested. No wrong value today; a future offset-bearing action on v1Forward would exercise them unverified.
2. The A6 guard pins only the three headline offsets. The boundaries I measured (small file, tail short read, huge limit, EOF, mid-character windows) are correct on the wire but not pinned by the suite — a regression in the agent-side short-read path would not be caught.
3. api_v1.go:269 passes `reqPath` as `""` — the same placeholder-for-unknown shape as the C1 defect, harmless only because nothing on that route reads it.
4. total_size/truncated remain absent on pre-1.19 agents (declared scope cut in the restated comment; outside this re-gate). The restated reason is now truthful against the code, which is what P8-19 asked for.

## Verdict

C1a PASS (executed on a real 1.18.6 binary at 0c3ed3e, three rows plus six boundaries, byte-exact walks) · C1b PASS (absence claim re-derived, signature and all four call sites quoted, every synthesised field enumerated) · A6 guard bites at 70dc607 for the assertion reason · suite 534P/0F at the fix, 529P/0F pre-fix, my counts.

VERDICT: PASS. The rollout gate P8-13 is CLEARED — the defect that parked it is closed on the wire, not merely in prose.
