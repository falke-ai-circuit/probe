# P8-19 — RE-GATE of P8-17 C1 + C6 after P8-18

Unit: P8-19 · owner reviewer (independent) · repo /opt/data/projects/PROBE_UPGRADE/v119wt · branch r119-build
Head reviewed: 6f34193a179f594869f7578369faefb8843fe46d (P8-18)
Baseline: ccf4d55 (P8-17 review)
Baseline suite: 523P/0F at 586b67b · this sha: 529P/0F (my run)
reviewed_by: reviewer=independent
verdict: FIX-FIRST — C1 FAIL (one executed data-integrity defect in the new fallback; the documented
gap's stated reason is not borne out), C6 PASS (every leg executed)
verdict_sha_of_head: 6f34193a179f594869f7578369faefb8843fe46d
Scope: C1 and C6 only. C2/C3/C4/C5/C7 untouched and not re-litigated.

Legend: E1 observed directly · E2 re-ran/derived myself · E3 tool output I executed · E4 claim.

SHAPE: parallel `internal/server/fs_integrity_fallback.go:fillFSListingIntegrity` / `fillFSStatIntegrity`
re-implement the agent's canonical digest construction `internal/agent/agent.go:withListingDigest`
(791-807) and `handleFSStat` (826-833). Cross-process duplication is unavoidable (the server cannot
import the agent's body), and I verified equivalence empirically below (same digest for the same input
on both binaries), so this is a duplicated rule with a live equivalence check, not a fork.

Scratch paths created (all under /opt/data/tmp/probe119/):
- wt-p819/            detached worktree of 6f34193 (web/dist copied in from the primary checkout)
- scratch-c6/         copy of wt-p819 used for my own C6 test (rev_p819_c6_test.go, not product code)
- p819-bin/           probe-server + probe-client built at 6f34193
- p819-scratch/       live server/agents, logs, wire.py, seeds, operators.json
- p819_suite.log, p819_suite_v.log
No product file was edited. The primary checkout and the graded sha are unchanged.

---

## Suite at 6f34193 (my run, E3)

```
$ GOFLAGS=-mod=vendor go test ./... -count=1 -v      (go1.24.4; GOROOT=/tmp/go — see note)
exit=0   --- PASS lines: 529   --- FAIL lines: 0   ^ok packages: 10
```
Matches the author's 529P/0F claim. web/dist was copied into the worktree first: it is gitignored, and
without it the six embed packages fail identically at every sha (environment artefact, not a regression).
Toolchain note: /opt/data/go is both GOPATH and a partial GOROOT in this container and cannot build the
stdlib; I used /tmp/go (go1.24.4), which builds go.mod's go 1.22 target cleanly.

---

## C1 — every fs-* carries sha256+sha256_scope, fs-list entry_count, server-side fallback for v1.18.6 — FAIL

### (a) Real v1.18.6 binary against a 1.19.0 server — wire key lists (E1)

Server: `p819-bin/probe-server` built at 6f34193, 127.0.0.1:17872. Agents: `old1186` =
/opt/data/tmp/v119/bin/probe-client-1186 (real 1.18.6), `new119` = client built at 6f34193. Both
registered `protocol=2`; wire produced by my own script `p819-scratch/wire.py`.

```
== old1186 (1.18.6)
  fs-read   keys=['data','encoding','next_offset','sha256','sha256_scope','size']
            decoded=100 size=100 sha_ok=True next=100 scope=window truncated=False total_size=False
  fs-list   keys=['count','entries','entry_count','sha256','sha256_scope']   count=18 entry_count=18 len=18
  fs-stat   keys=['exists','is_dir','mod_time','mode','sha256','sha256_scope','size']
  fs-hash   keys=['path','sha256','sha256_scope','size']   sha_matches_disk=True scope=file
  fs-write  keys=['path','sha256','sha256_scope','size','verified','written']
            sha_matches_disk=True size=31 verified=True
== new119 (1.19.0)
  fs-read   keys=['data','encoding','next_offset','offset','sha256','sha256_scope','size','total_size','truncated']
  fs-list   keys=['count','entries','entry_count','sha256','sha256_scope']
  fs-stat   keys=['exists','is_dir','mod_time','mode','sha256','sha256_scope','size']
  fs-hash   keys=['path','sha256','sha256_scope','size']
  fs-write  keys=['path','sha256','sha256_scope','size','verified','written']
```
The 3-of-5 field set from P8-17 §C1 is gone for all five ops. The digests are not decorative: the
fallback's constructions equal the 1.19.0 agent's own for identical inputs (E1):
```
fs-list emptydir : old_sha=4f53cda18c2baa0c… new_sha=4f53cda18c2baa0c… same=True  (count 0/0)
fs-stat seed.bin : old_sha=5a59866ebcb41f45… new_sha=5a59866ebcb41f45… same=True
fs-hash seed.bin : old_sha=e96760a87768717b… new_sha=e96760a87768717b… same=True
```
`fs-write` on the old agent returns a digest matching the bytes on disk
(`sha_matches_disk=True`), i.e. the second-call read-back works, not a request echo.

### The blocking defect: `next_offset` is wrong for every non-zero offset on a v1.18.6 agent (E1)

A 1.18.6 agent does not echo the request `offset` in its response, and the fallback has no access to
the request offset (`applyFSIntegrityFallback(agentID, action, reqPath, resp)` — offset is not a
parameter; `fs_integrity_fallback.go:81-87` defaults `offset := 0`). Measured:

```
old1186 off=0    : bytes_match=True next=100    expected=100
old1186 off=1000 : bytes_match=True next=100    expected=1100   <-- WRONG
old1186 off=8192 : bytes_match=True next=100    expected=8292   <-- WRONG
new119  off=1000 : bytes_match=True next=1100   expected=1100   (control: the agent echoes offset)
```
The bytes are the correct window (server passed the offset through), so a consumer that follows
`next_offset` re-reads from 100 forever: no progress on any large file, and the re-read is presented as
a continuation. This is the same failure class P8-17 §C1 flagged (`len/4*3` gap/overlap source); the
arithmetic is fixed, but the offset input is now missing. The file comment claiming
`next_offset = offset + len(decoded)` is false at the wire, because `offset` is never observed.
Evidence: `p819-scratch/wire_out.txt` plus the offset probe (output quoted above).

Numbered fixes:
1. Pass the requested offset into the fallback (e.g. `applyFSIntegrityFallback(agentID, action, reqPath,
   reqOffset, resp)` from both call sites in server_api.go:463 and api_v1.go:267) and compute
   `next_offset = reqOffset + len(decoded)` when the agent's response does not carry `offset`.
2. Add a regression test at the wire level (old-agent shape, `offset>0`), not only the 1.19.0 shape:
   the existing `TestP818NewAgentWireUnchanged` cannot catch this.

### (b) Is the documented total_size/truncated gap honest? — FALSE as stated

The artefact's reason is "an old agent reports neither, and the server cannot distinguish 'the file
ends exactly at the cap' from 'it continues', so any value there would be a guess." Both halves are
measurable and both fail (E1, same run, same binary):
```
old1186 fs-stat seed.bin -> {"exists":true,"is_dir":false,...,"size":10240}
old1186 fs-read @offset=1000 limit=100 -> decoded=100  (file is 10240 bytes: window truly truncated)
```
`fs-stat` returns the file size on this exact agent, and the server already uses a second agent call as
a legitimate derivation for `fs-write` (fs-hash read-back). So `total_size = fs-stat.size` and
`truncated = (offset+len(window)) < size` are derivable server-side with the pattern the same file
already uses; and `truncated` alone is decidable by a one-byte read at `next_offset`, with no size
report at all. The gap is a deliberate scope cut, not a physical impossibility.

Nuance, stated plainly: the *disclosure* is truthful — the artefact says the two fields are absent and
they are absent (verified above: `truncated=False total_size=False` on old fs-read). What is not
truthful is the stated reason. Graded per the brief's binary: gap = false.

### (c) 1.19.0 agent unchanged (no regression) — confirmed (E1)

Key-for-key identical to P8-17 §C1's quoted 1.19.0 lists, all five ops (quoted in (a)); `sha_ok=True`,
`truncated=True`, `total_size=10240` at offset 0, and `next=1100` at offset 1000 — the pre-existing
1.19.0 behaviour is intact.

---

## C6 — single-use, TTL, agent+job binding, 409 when E2E inactive — PASS

All legs executed, not read.

### Job binding (E2: my own test, scratch-c6/internal/server/rev_p819_c6_test.go, real registry entry points)
```
R1 mint(J) use(J): ok=true code=""
R2 mint(J) use(K): code="CREDENTIAL_JOB_MISMATCH"   want="CREDENTIAL_JOB_MISMATCH"
R3 retry refused handle with J: code="CREDENTIAL_UNKNOWN"   (a refused use consumes the handle)
R4 replay used handle: code="CREDENTIAL_UNKNOWN"
R5 expired handle (ttl 30ms, waited 60ms): code="CREDENTIAL_EXPIRED"
R6 wrong agent: code="CREDENTIAL_UNKNOWN"
R7 unbound handle (minted with no job id) used with job-ANY: code=""   (documented caller-supplied limit)
--- PASS: TestRevP819C6Binding (0.06s)
```
Enforcement site: `internal/server/credentials.go:397` `if h.JobID != "" && h.JobID != jobID`, reached
through `Use` at server_api.go:259 before dispatch; the agent refuses independently at
`internal/agent/agent.go:721-725` (`CREDENTIAL_JOB_MISMATCH`, secret zeroed first).

### Single-use and expiry at the wire, and the 409 when E2E is inactive (E1)
Live server at 6f34193 with `PROBE_SECRET_STORE` set, credential `credsent` stored (PUT 200):
```
exec new119 {credential:credsent, job_id:job-J} ->
  HTTP 409 {"code":"CREDENTIAL_CHANNEL_INSECURE","message":"credential delivery requires an active E2E
            channel; refusing to send a secret in the clear"}
exec old1186 {credential:credsent, job_id:job-J} ->
  HTTP 409 {"code":"CAPABILITY_MISSING","message":"agent old1186 does not implement credential delivery
            (capability \"credential\" absent)"}
```
The 409 family on the live HTTP surface is confirmed; the old agent is refused before any secret moves.

### One server-side TTL decision (E1 grep)
```
$ grep -rn "Expired()\|Expires" --include=*.go internal/server internal/agent | grep -v _test.go
credentials.go:286  Expires time.Time
credentials.go:347      Expires: time.Now().Add(ttl)      (set at mint)
credentials.go:391  if time.Now().After(h.Expires) {      (the only comparison — inside Use)
credential_api.go:216   ExpiresAtUTC: h.Expires…          (echo in the envelope)
agent/credential.go:125 if !p.Expires.IsZero() && time.Now().After(p.Expires)   (agent's own clock)
enrollment.go:31/63/85  enrollment-token TTL — a different domain, not credential handles
```
`credHandle.Expired()` is gone (no definition, no caller — grep exit 1). The server has exactly one
decision, inside `Use`, and the agent's check is a separate process's clock, as claimed.

Verdict C6: PASS. Limitation I verified and record without blocking: on the live path the server mints
with `params.JobID` and calls `Use` with the same value (server_api.go:244 → 259), so the server-side
job check cannot fire from the HTTP surface; the exercised refusals are the registry calls above. The
binding is real where it matters (the agent compares the delivered claim to the exec's job id) and the
claim in P8-18 does not overstate it.

---

## Non-blocking observations (outside C1/C6 grading)

- `credHandle.Used` (credentials.go:287) is never set anywhere; single-use is enforced by deletion, so
  the field is now dead. Cosmetic.
- `applyFSIntegrityFallback` is applied to `fs-list` only through `v1Forward` (api_v1.go:267); the
  legacy `/api/agent/...` fs-list handler applies it too (server_api.go path), but I did not find a
  second v1 route that could bypass it. No live bypass observed.
- `fs-write` on an old agent costs an extra agent round-trip (fs-hash read-back) on every write whose
  response carries no digest. Correct, but the latency is undocumented.

---

P8-19 REGATE — C1 FAIL (gap false) · C6 PASS · suite 529P/0F at 6f34193 · verdict FIX-FIRST · rollout gate OPEN
