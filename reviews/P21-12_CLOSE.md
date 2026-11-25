# P21-12 — CLOSE

**Commit:** `b53b0b1` — one commit on `r119-build`: the helper, 13 adoptions, 3 test files, `reviews/P21-12_ATOMIC.md`.
**Head move:** this close record is the document commit that follows `b53b0b1`; it changes no code, so the verdict binds to `b53b0b1` — the tree the suite ran against.
**Review:** `reviewed_by: PENDING` · `verdict: NEEDS-REVIEW` · `verdict_sha_of_head: b53b0b1`
**Scope:** `internal/server/` only. New `atomic_write.go` + tests; one-line adoptions in `registry.go`, `enrollment.go` (save + CA persist), `builder.go`, `profiles.go`, `operator.go`, `flows.go` (saveLocked + `writeFile`), `tasks.go`, `filetransfer.go`, `credentials.go` (secret.key + `flushLocked`), `idempotency.go`. No format change, no lock change, no on-disk path change.

## Delivered

One helper, `writeFileAtomic(path, data, perm)`: mkdir parent → write `<path>.tmp` in the same directory →
short-write check → `f.Sync()` → close → `os.Rename` → fsync the directory. Adopted by every state-file writer in
`internal/server/`; `credentials.go` (the previous "pattern of record") adopted it too, so there is no second
implementation left — its tmp+rename had no fsync, which is the specific gap §6.2 of the P21-02 contract names.

Census: 13 non-atomic persist sites found and fixed; 4 deliberate non-participants inside `internal/server/`
(2 append logs, transfer destination, downloaded-binary destination) and 6 outside it (`internal/agent`,
`internal/platform`) are listed with a reason each — nothing left silently unfixed. Full table:
`reviews/P21-12_ATOMIC.md` §1.

## Proof by interruption

- `TestRegistrySave_SurvivesFullDisk` drives the real `Registry.save()` under `RLIMIT_FSIZE` (256 B) with two
  agents already persisted. **Verified FAIL on the pre-fix tree** — `registry.json is unparseable after an
  interrupted write (truncated to 256 bytes): unexpected end of JSON input` — because `os.WriteFile` truncated
  the target before writing. **Verified PASS after** — the file still parses and still holds both agents.
- `TestWriteFileAtomic_InterruptedBeforeRenameKeepsPreviousFile` — stops at the exact seam (temp fsynced, rename
  not done): previous file byte-identical, no `.tmp` left.
- `TestWriteFileAtomic_InterruptedAfterRenameHasNewData` — pins the residual window instead of claiming general
  atomicity: after the rename the new content IS the file even though the call returns an error.
- `TestWriteFileAtomic_ModeAndDir` — parent created, `0600` survives the rename.

## What the helper does not fix

Crash between rename and directory fsync ⇒ stale-but-intact previous file, not a truncated one; the dir sync
narrows that window, it does not close it. No cross-file transaction (`registry.json` + `enrollment.json` can
still disagree after a crash between the two writes). `f.Sync()` is only as good as the storage stack. No locking
added — the existing single-writer contract is unchanged and remains unsupported beyond it. A file already
truncated by an older build stays truncated; this prevents the next occurrence.

## Suite

`GOTOOLCHAIN=local GOFLAGS=-mod=vendor go vet ./...` → exit 0. `go build ./...` → exit 0.
`GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1` → **767 pass / 0 fail / 2 skip**
(baseline `04b5039` = 763/0/2 + the 4 tests added here), run twice at load `7.79 7.47 7.26` and `6.41 7.15 7.18`.

## Open item

`internal/agent/jobs.go:107` `writeStateAtomic` is the same tmp+rename-without-fsync shape this unit removed from
the server; it is deliberately out of scope (`internal/server/` only) and is the one live instance of the pattern
this unit did not touch.
