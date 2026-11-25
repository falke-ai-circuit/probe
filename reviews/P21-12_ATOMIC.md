# P21-12 — a crash mid-write must not truncate persisted state

unit: P21-12 · workflow wf_probe1 · owner: coder=coder
tree: /opt/data/projects/PROBE_UPGRADE/v119wt · branch r119-build
base HEAD at unit start: `04b5039` (baseline suite 763 pass / 0 fail / 2 skip)
research leg: none (D1 verified in source by the parent; pattern of record already in credentials.go)

Verdicts in this document are labelled: **verified** (command + output quoted) or **hypothesis**.
Nothing below is recalled.

---

## 0. What was wrong, restated as a mechanism (not as a line number)

`os.WriteFile` is `O_WRONLY|O_CREATE|O_TRUNC` followed by `write(2)` on the target itself. The truncation is
the FIRST thing that happens, before a single byte of new data is written. So the failure is not "the new
state is lost" — it is "the old state is destroyed first and the new state may never arrive":

```
open(registry.json, O_TRUNC)   ← old file is now zero bytes; the inode is the same inode
write(...)                     ← interrupted here: disk full, SIGKILL, power loss
```

Every reader of these files (`registry.load`, `enrollment.load`, …) treats a parse failure as "no data"
(`if err := json.Unmarshal(...); err != nil { return }`), so a truncated file does not raise an alarm — it
silently produces an empty registry. That is the defect: total loss that looks like a fresh install.

## 1. Census — every non-atomic persist path, with a disposition

Search basis (re-runnable): `grep -rn "os.WriteFile\|ioutil.WriteFile" internal/ cmd/` plus
`grep -rn "os.OpenFile\|os.Create(" internal/server/*.go | grep -v _test` to catch non-WriteFile writers.
After this unit the first command returns **zero** hits in `internal/server/` outside tests.

| # | file:line (pre-fix) | function | what truncation destroys | disposition |
|---|---|---|---|---|
| 1 | `internal/server/registry.go:553` | `(*Registry).save` | every agent record — `registry.json` becomes unparseable, `load` returns an empty map (silent fresh-install) | **fixed** |
| 2 | `internal/server/enrollment.go:136` | `(*EnrollmentManager).save` | every enrollment token — new agents cannot enroll, existing tokens vanish | **fixed** |
| 3 | `internal/server/enrollment.go:344-345` | `(*CAManager).EnsureCA` (persist block) | the CA cert and key — truncated PEM reports as present on next boot and fails to parse; no prior copy exists | **fixed** (also stopped ignoring both write errors) |
| 4 | `internal/server/builder.go:526` | `(*BuilderManager).save` | the builds ledger; 5 call sites reach it (`:191, :241, :297, :332, :366`) — one writer, so one fix covers all five | **fixed** |
| 5 | `internal/server/profiles.go:177` | `(*ProfileManager).save` | all saved profiles | **fixed** |
| 6 | `internal/server/operator.go:318` | `(*OperatorManager).save` | every operator row **including `PasswordHash` and `Token`** — the highest-value file in the set | **fixed** |
| 7 | `internal/server/flows.go:460` | `(*FlowManager).saveLocked` | all flows | **fixed** |
| 8 | `internal/server/flows.go:497` (`writeFile`) | package helper, sole caller `internal/server/sensor_assignments.go:110` | sensor assignments (also reached from `:59` read) | **fixed** (helper body now delegates) |
| 9 | `internal/server/tasks.go:365` | `(*TaskManager).saveLocked` | the task queue | **fixed** |
| 10 | `internal/server/filetransfer.go:594` | `(*TransferManager).saveLocked` | transfer records (in-flight resume state) | **fixed** |
| 11 | `internal/server/credentials.go:257` | `(*CredentialStore).flushLocked` | the sealed credential store — this was the *pattern of record*, tmp+rename, but with **no fsync**: the rename could publish a name whose data was still in the page cache | **fixed** (adopted: the adoption is a clean 7-line replacement, so the exception clause was not needed) |
| 12 | `internal/server/credentials.go:117` | `credentialKey` | `secret.key` — and worse than losing it: a truncated key is <32 bytes so the read path (`len(raw) >= 32`) regenerates a *new* key, making every previously sealed credential permanently undecryptable | **fixed** |
| 13 | `internal/server/idempotency.go:127-133` | `(*idempotencyStore).saveLocked` | the idempotency ledger (replay protection) — already tmp+rename, no fsync | **fixed** (adopted) |

Non-JSON / deliberate non-participants inside `internal/server/` — left, with reason:

| file:line | shape | why it is not fixed here |
|---|---|---|
| `audit.go:105` | `O_APPEND` audit log | an append log has no whole-file invariant: a torn tail loses one record, never the file. Making it atomic per line would cost a rename+fsync per audit entry. |
| `flows_events.go:96` | `O_APPEND` event log | same as above. |
| `filetransfer.go:452-462` | `os.OpenFile`/`os.Create` on the transfer destination | the destination IS the artifact being streamed; it must be writable in place across chunks (resume), and its integrity is checked by the transfer protocol's own hash, not by JSON parse. |
| `server_update.go:405` | `os.Create(dst)` for a downloaded binary | download destination; verification is by hash/size of the staged binary before swap (existing `internal/upgrade` contract). Wrapping it in tmp+rename would collide with the staging logic that already owns those names. |

Outside `internal/server/` (census obligation, **not** fixed — this unit's scope is `internal/server/` only):

| file:line | what | reason left |
|---|---|---|
| `internal/agent/jobs.go:107` `writeStateAtomic` | agent job state: tmp+rename, **no fsync** (same residual as #11 before this unit) | agent-side spool; adopting the helper would mean exporting server internals into `internal/agent`. Flagged here so it is not "another instance nobody looked at": it is looked at and deferred. |
| `internal/agent/jobs.go:198` | `spec.json` written once into a freshly created job dir | a torn spec fails that one job at read; it cannot destroy a previous job's state — the file has no predecessor by construction. |
| `internal/agent/agent.go:1430` | `TokenFile` | single short line, no predecessor invariant beyond "the rotated token"; losing it forces re-enrolment, not data loss. Out of scope. |
| `internal/agent/agent.go:1723` | agent config rewrite after reconfigure | config is operator-supplied and re-writable; a torn config is recoverable by hand. Out of scope. |
| `internal/platform/platform.go:274` | `FSWrite` RPC handler | **must not** become atomic: writing caller-specified bytes at a caller-specified path is the feature. Tmp siblings and renames would change the RPC's contract (and break symlink-target semantics). |
| `internal/platform/platform_{linux,darwin,windows}.go:103/107/215` | asset extraction to disk | out of scope; extraction re-runs on next start. |
| `internal/**/*_test.go` hits | fixtures | not persistence. |

## 2. The helper — one implementation

`internal/server/atomic_write.go` (new, 97 lines):

```
writeFileAtomic(path string, data []byte, perm os.FileMode) error
  1. MkdirAll(filepath.Dir(path), 0755)          same-directory temp ⇒ same filesystem ⇒ rename is atomic
  2. OpenFile(path+".tmp", O_WRONLY|O_CREATE|O_TRUNC, perm)
  3. Write; short write ⇒ io.ErrShortWrite
  4. f.Sync()                                    data reaches the disk BEFORE the name is published
  5. f.Close()   (close error is reported; tmp removed on any error)
  6. os.Rename(tmp, path)
  7. open dir, dir.Sync(), close                 best-effort: the rename is not durable until the entry is
```

Design notes that matter for review:

- **Same `.tmp` name as before** (`<path>.tmp`): `credentials.go` already used it, and any leftover from a real
  crash is ignored by readers and overwritten by the next write. No new garbage name to learn.
- **Permissions** are applied to the temp file and survive the rename, so `0600` on the credential store and
  `0644` on the JSON state files are unchanged byte-for-byte (`TestWriteFileAtomic_ModeAndDir`, verified).
- **Error text is wrapped** (`atomic write <path>: <cause>`), and every caller keeps its own logging line, so
  log lines still start with `[registry]`, `[enrollment]`, … — no operator-facing change.
- **No lock is taken.** Every current caller already serialises its own saves (`save()` under `mu`,
  `saveLocked()` with the caller holding it). The unit does not move that contract.
- The two stage hook constants exist only for tests (below). `atomicWriteStageHook` is nil in production and
  is not safe for concurrent use by design — it is a fault injector, not a feature.

## 3. Proof by interruption (not by inspection)

### 3.1 The test that runs on the OLD tree — full-disk injection

`internal/server/atomic_write_fsize_test.go` (build tag `linux`) drives the real `Registry.save()` path and
forces the next write to hit `RLIMIT_FSIZE` (256 bytes) after two agents are already persisted. That is the
kernel-level shape of a full disk: `write(2)` returns `EFBIG` **after** a partial transfer, at exactly the
point where a bare `os.WriteFile` leaves a truncated file. `SIGXFSZ` is ignored for the window so the write
error is reported normally instead of killing the process, and the limit is restored before any assertion.

The test compiles and runs against the pre-fix tree, because it calls only `NewRegistry` / `Register` —
no new symbol. **Verified failure on the current tree, before the fix** (`04b5039` + test file only):

```
=== RUN   TestRegistrySave_SurvivesFullDisk
[registry] save write error: write .../registry.json: file too large
    atomic_write_fsize_test.go:81: registry.json is unparseable after an interrupted write (truncated to 256 bytes): unexpected end of JSON input
        bytes on disk: "{\n  \"agent-1\": {\n    \"agent_id\": \"agent-1\",\n    \"name\": \"alpha\",\n
        \"version\": \"v1.19.1\",\n    \"os\": \"linux\", ... \"connected_at\": \"2026-11-25T12:27:1"
--- FAIL: TestRegistrySave_SurvivesFullDisk (0.02s)
FAIL	github.com/falke-ai-circuit/probe/internal/server	0.023s
```

Note what the failure is: the *error was logged* and the file was still destroyed. A test asserting only
"save returned an error" would have passed on the broken tree.

**Verified pass after the fix:**

```
=== RUN   TestRegistrySave_SurvivesFullDisk
[registry] save write error: atomic write .../registry.json: write .../registry.json.tmp: file too large
--- PASS: TestRegistrySave_SurvivesFullDisk (0.11s)
PASS
ok  	github.com/falke-ai-circuit/probe/internal/server	0.278s
```

The assertion is on the artifact: the file still unmarshals and still names `agent-1` and `agent-2`.

### 3.2 Interruption at the two stages a crash can land in

`internal/server/atomic_write_test.go`:

- `TestWriteFileAtomic_InterruptedBeforeRenameKeepsPreviousFile` — stops the write with the data fsynced under
  the temp name and the rename not done. Asserts (a) the call errors, (b) the previous file is byte-identical,
  (c) no `.tmp` survives. This is the failure mode `registry.go` had, proven absent at the seam where it
  happens.
- `TestWriteFileAtomic_InterruptedAfterRenameHasNewData` — pins the residual window instead of claiming
  atomicity in general: once the rename landed, the new content **is** the file even though the call reports
  an error. A reviewer can read the honest boundary off a test rather than off a comment.
- `TestWriteFileAtomic_ModeAndDir` — parent directory created, `0600` survives the rename.

All three verified PASS in the same run quoted above.

## 4. What the helper cannot fix (stated, not papered over)

1. **A crash between `os.Rename` and the directory fsync.** The new name may be lost, leaving the previous
   file. That is a *stale* state, not a truncated one — the old file is intact and parseable, which is the
   property the unit promises. But it is not "atomic in general", and the directory sync only narrows it: on
   most filesystems the entry is already durable within a journal commit, yet no portable guarantee exists.
2. **No ordering against a reader in another process.** A rename gives readers old-or-new; it does not give a
   snapshot across multiple files (e.g. `registry.json` and `enrollment.json` are not written in one
   transaction, so a crash between them can leave the two mutually inconsistent). Cross-file consistency is a
   different unit.
3. **`f.Sync()` is only worth what the storage stack honours.** On a filesystem that lies about flushes
   (some consumer SSDs with volatile write cache, or `cache=unsafe` for a virtual disk) a power cut can still
   lose the fsynced data. The code cannot detect that.
4. **It does not add locking.** Two processes writing the same path still race; each rename is atomic, the
   last one wins. The existing single-writer contract is unchanged — and unchanged means unsupported.
5. **It cannot recover a file already truncated by an older build.** A `registry.json` destroyed before this
   commit stays destroyed; the fix prevents the next occurrence, it does not restore the last one.
6. **A full disk during the temp write leaves `<path>.tmp`** (in a real crash, no cleanup runs). Readers
   ignore it and the next write truncates it; nothing garbage-collects it. Bounded at one file per path.

## 5. Format and permissions — unchanged

- Same JSON: no schema key added, removed, renamed or retyped; `json.MarshalIndent(..., "", "  ")` untouched
  in all 13 sites.
- Same paths: `<path>` is what the caller passed (`registry.json`, `enrollment.json`, …); the only new name on
  disk is the transient `<path>.tmp`, which `credentials.go` already produced.
- Same modes: `0644` for the state JSON, `0600` for the credential store / secret key / CA key / idempotency
  ledger — see the per-site table and `TestWriteFileAtomic_ModeAndDir`.
- No migration, so this ships in one commit.

## 6. Verification block

```
GOTOOLCHAIN=local GOFLAGS=-mod=vendor go vet ./...            → exit 0, no output
GOTOOLCHAIN=local GOFLAGS=-mod=vendor go build ./...          → exit 0
GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1  → 767 pass / 0 fail / 2 skip
                                                               (baseline 763/0/2 + the 4 tests added here)
host load at the final suite run: /proc/loadavg 6.41 7.15 7.18
```

The suite was run twice after the last code edit (the CA-persist block): once at load `7.79 7.47 7.26` and once
at `6.41 7.15 7.18`, both `767 pass / 0 fail / 2 skip`.

`internal/server` wall time 68.4 s (baseline register), `-json` recount used for the pass/fail/skip tally.
No test was skipped that was not skipped at baseline (2 skips, unchanged — they are the pre-existing
platform-gated skips, not new `t.Skip` calls in this unit's tests).

## 7. Files

new: `internal/server/atomic_write.go`, `internal/server/atomic_write_test.go`,
`internal/server/atomic_write_fsize_test.go`
modified (one-line adoption each unless noted): `registry.go`, `enrollment.go` (2 sites: save, CA persist block),
`builder.go`, `profiles.go`, `operator.go`, `flows.go` (2 sites: saveLocked, writeFile), `tasks.go`,
`filetransfer.go`, `credentials.go` (2 sites: secret.key, flushLocked), `idempotency.go`
