VERDICT FIX-FIRST /opt/data/tmp/probe119/P8-06_TRANSFER.md+07_CREDENTIAL.md 586b67b 523P/0F/1S

# P8-17 — independent review of P8-06 (fa29134) and P8-07 (586b67b)

reviewed_by: reviewer (independent — I authored none of these commits)
unit: P8-17 · owner: reviewer=independent
repo: /opt/data/projects/PROBE_UPGRADE/v119wt · branch r119-build · HEAD 586b67b
shas verified: `git cat-file -t` = commit for both; `git merge-base --is-ancestor fa29134 586b67b` = true.
Suite (my runs, `go test ./... -count=1 -v -timeout 1500s`, GOFLAGS=-mod=vendor):
  fa29134: 514 PASS / 0 FAIL / 1 SKIP, EXIT=0
  586b67b: 523 PASS / 0 FAIL / 1 SKIP, EXIT=0
  (author's artefact says 466 PASS at fa29134 and 475 at 586b67b — different verbose subtest counting; my counts are the ones above.)
Build: `go build ./...` exit 0 at both shas after copying `web/dist` into the worktrees (gitignored; without it the 6 embed packages fail identically at both shas — environment artefact, not a regression).

RECIPIENT EXPERIENCE: ran a real 1.19.0 server + real agent on 127.0.0.1:17831 and uploaded/downloaded a
10,700,000-byte file in 262,144-byte chunks (41 chunks, interrupt + resume), read it back with/without
Range, capped and uncapped fs-read; ran a second server with PROBE_FS_READ_MAX_BYTES=0 and a third with
E2E enabled (scratch build), seeded a sentinel credential and executed `cat` with `credential:"sent1"`.

Scratch paths made (no product code changed in any tracked tree):
  worktrees: /opt/data/tmp/probe119/wt-p806 (fa29134), wt-p807 (586b67b), wt-base (af54467, pre-fix ref)
  copies:   /opt/data/tmp/probe119/scratch-c3 (test host), scratch-c3-mut (mutex removed),
            scratch-e2e (SetE2E(true) at startup only, to reach the credential channel)
  runs:     live586 (17831, default cap), live586u (17832, cap=0), live_e2e (17833, E2E on),
            live_noe2e (17834, E2E off), bin2/ (binaries built at 586b67b and from scratch-e2e)
  probes:   scratch-c4.py, internal/server/rev_p817_race_test.go, rev_p817_redact_test.go

SHAPE reuse internal/server/filetransfer.go:TransferManager.TrackRawUpload — upload reuses the existing
transfer manager and agent fs-write/fs-read primitives; no parallel transfer stack. P8-07 ssh:
SHAPE reuse internal/agent/agent.go:sshubcommand path — new `probe-client ssh` sits on x/crypto/ssh, no
second exec stack. One parallel helper noted: the server derives fs-read next_offset with its own
base64-length arithmetic (server_api.go:455-461) instead of reusing the agent's window accounting —
this is the C1 breach's mechanism.

Legend: E1 = I observed it; E2 = I re-ran it; E3 = tool output I executed; E4 = claim, never load-bearing.

---

## C1 — every fs-* carries sha256+sha256_scope, fs-list entry_count, server fallback for v1.18.6 — FAIL

New-agent wire (E1), live over the v1 surface, whole JSON quoted by key list:
```
fs-write -> {"written":28,"path":...,"sha256":"a4f1daed...","sha256_scope":"file","size":28,"verified":true}
fs-read  -> ["data","encoding","next_offset","offset","sha256","sha256_scope","size","total_size","truncated"]
fs-list  -> ["count","entries","entry_count","sha256","sha256_scope"]        (entry_count=3=count=len(entries))
fs-stat  -> ["exists","is_dir","mod_time","mode","sha256","sha256_scope","size"]
fs-hash  -> {"path":...,"sha256":"a4f1daed...","sha256_scope":"file","size":28}
```
So on a 1.19.0 agent the first half of C1 holds. `fs-stat`'s digest is the canonical-JSON record digest
declared `sha256_scope:"listing"` — the author discloses this in §6; it is inside the design's three-value
scope enum and does not hash the file (design §4.1 explicitly allows cheap hashing only).

The fallback clause is false at the wire (E1). Exact v1.18.6 agent (/opt/data/tmp/v119/bin/probe-client-1186,
version 1.18.6, registered as old1186) against the same 1.19.0 server:
```
old fs-read  -> {"data","encoding","size"}                      # no sha256, no scope, no truncated, no next_offset
old fs-read limit=10 -> {"data":<10 bytes>,"encoding":"base64","size":10}   # window reported as size
old fs-list  -> {"entries":[...]}                               # no count, no entry_count, no sha256, no scope
old fs-stat  -> {"exists","is_dir","mod_time","mode","size"}    # no sha256, no scope
old fs-hash  -> {"path","sha256","size"}                        # sha256 present, scope ABSENT
```
Server-side fallback does not exist for these fields. The only synthesis in the tree is
`internal/server/server_api.go:455-465`, and it sits inside `if truncated { ... }` — a field a v1.18.6
agent never sets, so the branch is dead for exactly the population it was written for. `grep -rn
sha256_scope internal/server/*.go` (non-test) returns only `api_v1_fs_transfer.go:236` and
`server_api.go:461-462`. Even inside that branch the offset is derived as `offset + len(base64)/4*3`
(server_api.go:456-460), a base64 upper bound that is not the decoded window length — a wrong
next_offset by up to 2 bytes, i.e. a gap/overlap source, had the branch ever fired.

Artefact §0 and §8 say the opposite ("a server-side fallback so a v1.18.6 agent cannot produce a 3-of-5
field set"); §8's own A7 evidence (`fs-read -> keys ['data','encoding','size']`) contradicts §0 and was
graded PASS. Design §4.1 says sha256_scope is "Never omitted" — the v1.18.6 path omits it on 4 of 5 ops.

Fix (numbered): 1. either implement the fallback in the server for every fs-* response (compute listing
digest / stat record digest / declare scope where the server holds the bytes), or delete the claim and
record the version-gated gap in the artefact and the design. 2. If the truncated-branch fallback stays,
derive the window from decoded bytes, not `len/4*3`.

## C2 — next_offset on truncation; cap 8 MiB; 0 = unlimited — PASS

E1, 10,700,000-byte file:
```
no limit      : size=8388608 total_size=10700000 offset=0 truncated=True next_offset=8388608 scope=window
follow-up@8388608: size=2311392 truncated=False next_offset=10700000; window sha == header sha
reassembled   : 10700000 bytes == file (byte-identical)
limit=999999999: size=8388608 truncated=True next_offset=8388608   (cap enforced, not caller-trusted)
PROBE_FS_READ_MAX_BYTES=0 server: size=10700000 truncated=False next_offset=10700000  (unlimited restored)
```
No gap, no overlap; the returned window's sha256 matches the bytes I decoded. `limit:4096` path was also
exercised by the full suite (TestFSReadWindowCarriesNextOffsetAndIsContiguous PASS in my run).

## C3 — the 8 MiB panic was a write/write race, fixed by taking the per-connection mutex — PASS (mutation-tested)

Pre-fix source (E1): `git show af54467:internal/server/server_ws.go` line 196 is `conn.WriteJSON(pong)`
with no lock, while every other writer goes through `connWriteMu`; same at 8a44426:203.

Mutation test (E2), scratch copy `/opt/data/tmp/probe119/scratch-c3`, same test both sides
(`internal/server/rev_p817_race_test.go`, mine — not the author's): real gorilla connection, one goroutine
writing a ~1.6 MB control frame through `writeToAgentLocked`, the agent side pinging in a tight loop:
- mutex present: `--- PASS: TestRevP817PongRace (4.21s)` — no panic (c3-fixed.log).
- mutex bypassed (`writeToAgentLocked` returns `conn.WriteJSON(v)` before the lock, scratch-c3-mut):
  `panic: concurrent write to websocket connection` at `gorilla/websocket.(*messageWriter).flushFrame`,
  EXIT=1 (c3-mut.log).
The green suite beside an unexercised race fix would have proved nothing; with the mutex removed the panic
returns inside 4 seconds under this load. The diagnosis and the fix are load-bearing and confirmed.
Scope limit repeated from the artefact: the Hetzner host and Windows were not reproduced (UNVERIFIED-ON-HOST).

## C4 — upload/download: 206+Content-Range, 416, resume byte-identical — PASS

E1, live:
```
A2 interrupted after 21 chunks offset=5505024 state=uploading
A2 gap probe: HTTP 409 INTEGRITY_MISMATCH detail.expected_offset=5505024
A2 final: state=completed file_sha256=c4a94390... size=10700000 chunks=41
A2 disk sha == source: True
A3 range: HTTP 206 Content-Range=bytes 1048576-2097151/10700000 len=1048576 bytes_exact=True
A3 unsatisfiable: HTTP 416 Content-Range=bytes */10700000
A3 3-chunk assembly equals source: True (10700000)
A3 no-range on 10.7 MB: HTTP 206, bytes 0-8388607/10700000, X-Probe-Next-Offset=8388608
```
Minor (M3): a malformed Range with a space (`Range: bytes 1-2`) is answered 416 rather than 400; RFC 7233
only defines `bytes=`, so this is a wrong status for a bad request, not a data defect.

## C5 — the secret appears in no sink; redactor bites — PASS (with sink coverage stated)

E1/E2. Sentinel `SENTINEL-P817-DO-NOT-LEAK-7f3a9c` seeded with `PUT /api/v1/credentials/sent1`; GET returns
metadata only (`name, created_at, times_used`) — no value field. Exec `{"command":"cat","credential":"sent1"}`
on the E2E-enabled scratch server: `exit_code:0, stdout:"[REDACTED]"`, response carries
`credential:{name,ref,used:true,redacted:0}` — no sentinel in the response body.
Sink greps (E1), all zero:
```
grep -F -c SENTINEL server.log -> 0 ; client.log -> 0 ; secrets.json -> 0
grep -rls -F SENTINEL live_e2e /var/lib/probe -> (no file)
```
Command line: agent's own test `TestExecWithStdin_SecretNotInArgv` re-run by me → PASS; `cmdline` grep on a
live child was not completed (my slow-exec probe aborted — no claim beyond the re-run). Audit log: the
live server ran with the default empty audit path, so no audit file existed to grep; the audit sink is
covered only by the author's `TestCredentialExec_RedactsAllSinks`, which I re-ran → PASS (E3).
Redactor bites — direct exercise (E2, mine): `NewRedactor(); Add(sentinel); Redact("login: SENTINEL… ok")`
→ `"login: [REDACTED] ok"`, hits=1; after `Remove`, the value passes through unchanged
(scratch-c3 `rev_p817_redact_test.go`, PASS). A filter that never fires is not evidence; this one fires.

## C6 — single-use, TTL, agent+job binding, 409 when E2E inactive — FAIL (job binding not enforced)

E1: with E2E off (server 17834, PROBE_SECRET_STORE set so the store opens):
```
HTTP 409 {"code":"CREDENTIAL_CHANNEL_INSECURE","message":"credential delivery requires an active E2E
          channel; refusing to send a secret in the clear"}
```
Single-use: `handleRegistry.Consume` (credentials.go:355-365) deletes on read; a second Consume returns
missing. TTL: `credHandle.Expired()` exists but is **dead code** (grep finds no caller); enforcement is in
the agent — delivery rejects an expired `ExpiresAtUTC` (credential.go:70-74) and use rejects it again
(credential.go:120). Effectively TTL holds at both ends, by a path the server's own field never checks.

Binding to agent_id: real — the sealed blob is derived from `sha256(token‖agent_id)` and the agent opens it
with its own id (openForAgent, credential.go:79). Binding to **job_id: not enforced anywhere**. The exec
path calls `sealCredentialForExec(agentID, name, "", mode)` with an empty job id (server_api.go:243);
`h.JobID` is minted, echoed in the delivery envelope, and never compared. `grep -rn JobID` in
internal/agent/credential.go and credential_api.go finds only the delivery echo. "Bound to agent_id +
job_id" as stated is false for the second half; severity M2 (single-use + inline delivery carry the risk),
but the claim is graded as written.

Fix: 3. either enforce job binding (mint with the real job id and verify it on consume/use) or strike
"job_id" from the claim; 4. delete or use `Expired()` so the server side has one enforcement point.

## C7 — vault at rest is ciphertext only — PASS

E1, store written by the live server at a scratch path (`PROBE_SECRET_STORE`):
```
{"version":1,"entries":{"sent1":{"ct":"hZiW88EIjZEUWqkJYVJnZToohLsWTFJ9a7ggzFHbA05SOJP/2SVwbIO/Kcc6eUCEtk/o+gVZwDyZjpfu",
 "created_at":"2026-11-23T08:55:23Z","times_used":0}}}
```
No plaintext, no `value` key; `ct` is base64 nonce‖ciphertext+tag (12-byte nonce for AES-GCM), key file
`secret.key` 0600 beside it. The secret is nowhere in the file (grep -c = 0).

## C8 — probe-client ssh — PASS on loopback / UNVERIFIABLE on the two named legs

E2: `TestSSHHelper_AgainstLoopbackFixture` re-run by me → PASS (banner, `>` vs `#`, stdin script, `show
version` ok / `% Invalid input` → exit 1, `--exec "exit 7"` → 7).
UNVERIFIABLE: the REAL-SWITCH leg (loopback fixture only) and Windows-on-host. I could not reach a device
from this host; per the brief these are not inherited as PASS.

---

## Nemesis gate (>=3 named attacks, 0 unaddressed)

1. Old-agent field-set attack — ran a real 1.18.6 binary against the 1.19.0 server: fallback absent. C1 FAIL. Addressed (fix 1/2).
2. Mutex-removal mutation — panic returns; fix is load-bearing. C3 PASS.
3. Claim-vs-artefact contradiction — §8 A7 evidence refutes §0's fallback sentence; author graded it PASS. Recorded, folds into fix 1.
4. Binding audit — job_id minted but never checked; TTL field dead on the server side. C6 FAIL (fix 3/4).
5. Cap-trust attack — client-supplied huge limit capped server-side; cap=0 restores unlimited. C2 PASS.
6. Redactor-never-fires attack — direct Add/Redact shows it fires and Remove deactivates it. C5 PASS.

Drift check: request-drift — the brief asked for wire dumps, a mutation test, and sink greps; all three were
produced against live processes, not from author logs. implementation-drift — two artefact sentences
(§0 fallback, job binding) describe behaviour the code does not have; named with file:line. verification-drift —
the author's A7 PASS was given on evidence that contradicts their own §0; corrected here.

## Verdict

FIX-FIRST. C2, C3, C4, C5, C7 hold under execution; C1 and C6 each carry a claim the code does not
implement; C8's two field legs remain UNVERIFIABLE. Rollout gate (P8-13) OPEN until fixes 1-2 (C1) and
3-4 (C6) land and are re-checked — C3 is cleared, and that was the release's crash claim.

P8-17 REVIEW — artefact /opt/data/tmp/probe119/P8-17_REVIEW.md · C1-C8: 5 PASS / 2 FAIL / 1 UNVERIFIABLE · suite at 586b67b 523P/0F · verdict FIX-FIRST · rollout gate OPEN
