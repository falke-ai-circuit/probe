# P16-03 CLOSE — independent review of P16-01: multiarch index + one version source holds; arm64 execution still unproven

**Commit:** `e46023f` (repo `/opt/data/projects/PROBE_UPGRADE/v119wt`, branch `r119-build`). Repo HEAD is `5796c27`; `git diff --stat e46023f 5796c27` = P12-06's two review documents (162 insertions), so `.github/workflows/docker.yml` and `Dockerfile` are byte-identical at HEAD. No file in the tree was edited by this unit.
**Review:** reviewed_by: `reviewer` · verdict: `PASS` · verdict_sha_of_head: `e46023f1cd074ce524f30d82eba54d95775cb350`
**Head-move waiver:** repo HEAD is `13c31e2`, the documentation commit carrying this unit's two review files (`reviews/P16-03_REVIEW.md` + `reviews/P16-03_CLOSE.md`) on top of `5796c27`, which carries P12-06's two review documents. `git diff --stat e46023f 13c31e2` = four files, all under `reviews/`, 284 insertions, no code. The verdict binds to `e46023f`, the tree every item was resolved against.
**Scope:** read-only verification of the four brief items — label/version chain in bytes, CI run 45507859352, production container state, arm64 labelling. No fix, no rebuild, no image push, no write to CT100 or any live agent. Artefact: `/opt/data/tmp/probe119/P16-03_REVIEW.md` (12,139 B, 93 lines, sha256 `62804dd002303f680b3a5522a95f177fb9c7bcad4df58647ac6d08e5a09bfda0`).
**Suite:** `go test ./... -count=1` at `e46023f` (dedicated worktree) → **627 pass incl. subtests / 0 fail / 2 skip, exit 0**, 11 packages `ok`. No drift from the parent's 627/0/2. Skips: `TestDownloadURL_LiveCrossProcessProof`, `TestV1_CreateTransfer`.

## Result — 4 of 4 PASS

| # | item | verdict | what carried it |
|---|---|---|---|
| 1 | label/version chain, in bytes | **PASS** | docker.yml:39-47 guard (`set -euo pipefail`, `exit 1`, `grep -c continue-on-error` = 0), Dockerfile:25/31-33 ldflag, Dockerfile:40-44 re-declared ARGs + LABEL; both platform configs read from the registry carry `version=1.19.1`, `revision=e46023f…`; amd64 binary extracted from the shipped layer and **run**: `PROBE v1.19.1` and `/health` `server_version: v1.19.1` |
| 2 | CI run 45507859352 | **PASS** | API: head_sha `e46023f`, workflow_dispatch, success, 23:11:51Z→23:22:07Z; log 2,407 lines: `app Semver: 1.19.1` (127), amd64 builder ldflag (1529), arm64 builder ldflag (1622), `exporting manifest list sha256:1051de24…` (1649), push of tag@digest (1662) |
| 3 | production untouched | **PASS** | CT100: `operator-op01-probe-1` StartedAt `20:43:42.945Z`, `RestartCount=0`, ImageID `466b2fc2…`, compose mtime `20:42:53.918Z` — all 2h28m before the run window; new index present only as untagged `1051de24` |
| 4 | arm64 stays a gap | **PASS** | unproven labelled at MULTIARCH.md:145-148, CLOSE.md:17, MULTIARCH.md:140; no document at `e46023f` claims arm64 execution (12 arm64/radxa `*.md` hits checked); arm64 layer binary is `ELF64 e_machine 183`, amd64 is 62 |

## Residuals (recorded, none blocking)

- **R-1** metadata-action also emits `org.opencontainers.image.version=<sha>`; the workflow's explicit label wins (observed `1.19.1` in the pushed config), so the label's correctness rests on append ordering.
- **R-2** on a `v*` tag push the registry tag still derives from the tag while label/ldflag derive from source — a tag can disagree with the label even though the three internal surfaces agree.
- **R-3** a hand `docker build` with no `--build-arg` yields label `dev` + banner `vdev` (consistent, wrong); documentation path only, compose has no `build:` key.
- **R-4** `README.md:104-107` and `docker-compose.yml:6-10` still pin `70de7fa…@sha256:466b2fc2…`, the single-platform index — the multiarch digest is published but unreferenced by the deploy path.

## Open item

R-4: the deploy path (README + compose) pins the amd64-only index `466b2fc2`, so the new `sha256:1051de24…` multiarch list has no consumer in the repo; arm64 execution remains unproven (no qemu binfmt here, Radxa pull still missing). Nothing to fix inside P16-01's scope — the unit's two files are correct.

P16-03 | e46023f1cd074ce524f30d82eba54d95775cb350 | reviewed_by: reviewer | verdict: PASS | verdict_sha_of_head: e46023f | suite: 627 pass / 0 fail / 2 skip | open: R-4 compose/README still pin the single-platform index
