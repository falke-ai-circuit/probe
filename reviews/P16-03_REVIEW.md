VERDICT PASS /opt/data/tmp/probe119/P16-03_REVIEW.md e46023f 627 pass / 0 fail / 2 skip

**CLOSE HEADER**
`reviewed_by: reviewer` · `verdict: PASS` · `verdict_sha_of_head: e46023f1cd074ce524f30d82eba54d95775cb350`
`unit: P16-03` (independent review of P16-01) · `artefact: /opt/data/tmp/probe119/P16-03_REVIEW.md`
`claim under review: e46023f` — `.github/workflows/docker.yml` + `Dockerfile`. Repo HEAD is `5796c27`; `git diff --stat e46023f 5796c27` = `reviews/P12-06_CLOSE.md` + `reviews/P12-06_VERIFICATION.md`, 162 insertions, so the two reviewed files are byte-identical at HEAD.
`suite: go test ./... -count=1` at `e46023f` → **627 pass / 0 fail / 2 skip, exit 0** (11 packages `ok`; matches the parent's 627/0/2 exactly)

**RECIPIENT EXPERIENCE:** the recipient is an operator pulling `@sha256:1051de24…` on amd64 or arm64 and reading the version off the image, the banner and `/health`. I did that from the shipped bytes: fetched both platform configs from the registry myself, extracted `/app/probe` out of the arm64 layer and the amd64 layer, and **ran** the amd64 binary — `./app/probe --version` → `PROBE v1.19.1`; `./app/probe serve --addr 127.0.0.1:17999` → `GET /health` → `{"server_version":"v1.19.1",…,"status":"ok"}` and `GET /api/v1/health` → `{"ok":true,…"server_version":"v1.19.1"}`.

`SHAPE parallel .github/workflows/docker.yml:41-42 (sed parse + `^[0-9]+\.[0-9]+\.[0-9]+$` guard) vs internal/server/build_matrix.go:64 (versionStampKey)` — two places now name the version symbol: the workflow re-spells `github.com/falke-ai-circuit/probe/internal/version.Semver` at docker.yml:96 (via the Dockerfile's ldflag at Dockerfile:32) while the Go builder already owns that string in `versionStampKey`. A package move must touch both or the `-X` silently no-ops. Not blocking (YAML cannot import Go), named because it is the one place the "single source" is duplicated.

## 1. Label/version chain, verified in bytes — PASS

`git show e46023f:.github/workflows/docker.yml` (111 lines) and `git show e46023f:Dockerfile` (76 lines):

| element | where | what the bytes say |
|---|---|---|
| fail-fast guard | docker.yml:39-47 | `set -euo pipefail` (40); `v=$(sed -n 's/^var Semver = "\([^"]*\)"$/\1/p' internal/version/version.go)` (41); `if ! printf '%s' "$v" \| grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'` → `echo "::error::…"` (43) + `exit 1` (44); `echo "semver=$v" >> "$GITHUB_OUTPUT"` (46) |
| it fails the job, not just echoes | whole file | `exit 1` runs inside the step's `run:` block; `grep -c continue-on-error` on the blob → **0**, so a non-zero exit fails the step and the job |
| ldflag consumes it | Dockerfile:25, 31-33 | `ARG VERSION=dev` (25); `RUN go build -trimpath -ldflags "-X github.com/falke-ai-circuit/probe/internal/version.Semver=${VERSION}" -o /probe ./cmd/probe/` (31-33) |
| re-declared for the label | Dockerfile:40-44 | `ARG VERSION=dev` (40) and `ARG GIT_SHA=` (41) after the `FROM debian` line; `LABEL … org.opencontainers.image.version="${VERSION}"` (43), `…revision="${GIT_SHA}"` (44) |
| both feeding the same value | docker.yml:91-96 | labels include `org.opencontainers.image.version=${{ steps.appver.outputs.semver }}` (93); build-args `GIT_SHA=${{ github.sha }}` (95) and `VERSION=${{ steps.appver.outputs.semver }}` (96) — the label and the ldflag are the same expression, read once at docker.yml:46 |

Byte evidence taken from the registry, not from the artefact's transcription: index `sha256:1051de24…` is `application/vnd.oci.image.index.v1+json`, `annotations: {}`; `linux/amd64` config `sha256:834646d6…` → `Labels["org.opencontainers.image.version"]="1.19.1"`, `revision="e46023f1cd0…"`; `linux/arm64` config `sha256:8e144753…` → the same two values. Both `architecture` fields in the configs are correct (`amd64`/`arm64`), so the label is not carried on one platform and missing on the other.

Does any path remain where the label disagrees with the compiled `Semver`? Not in the CI path — every input resolves to `steps.appver.outputs.semver` or to the literal that expression came from. Three residual paths, none a divergence of label vs binary today:

- **R-1 (robustness, order-dependent):** `docker/metadata-action` also emits `org.opencontainers.image.version=e46023f1cd0…` for the sha tag — visible at log:806 and log:2313 (`DOCKER_METADATA_OUTPUT_LABELS`), with the same value echoed as `DOCKER_METADATA_OUTPUT_VERSION` at log:803 and log:2310. The workflow appends the correct line after it and the pushed config carries `1.19.1`, so last-wins holds; but the correctness of the label rests on that ordering, not on the metadata labels being free of the key.
- **R-2 (a tag can still disagree with the label):** on a `v*` tag push, `type=semver,pattern={{version}}` (docker.yml:75) tags the image from the tag while the label comes from source. A `v1.20.0` tag on a tree whose `Semver` is `1.19.1` gives an image tagged `1.20.0` labelled `1.19.1`. The three internal surfaces still agree — which is what the unit claimed — but the registry tag is not covered by the single source.
- **R-3 (local build default):** a hand `docker build` with no `--build-arg` stamps `VERSION=dev` → label `dev`, banner `vdev`: self-consistent, wrong number. The Dockerfile header (docker.yml/Dockerfile:5-8) advertises `docker compose up -d --build`; the repo's compose carries no `build:` key (docker-compose.yml:1-26), so this is a documentation path only.

Guard weakness observed, not blocking: `sed` prints every matching line and `grep -Eq` matches if **any** line matches, so two `var Semver` declarations would pass the guard and then write a two-line `$GITHUB_OUTPUT` value, which the runner rejects as `Invalid format` — loud failure, no wrong value, but a confusing error instead of the `::error::` message.

## 2. CI run 45507859352 — PASS

`GET /repos/falke-ai-circuit/probe/actions/runs/45507859352` (authenticated, 200): `name Docker Image`, `head_sha e46023f1cd074ce524f30d82eba54d95775cb350`, `head_branch r119-build`, `event workflow_dispatch`, `status completed`, `conclusion success`, `run_started_at 2026-11-24T23:11:51Z`, `updated_at 23:22:07Z`. Job `138847615303` (`image`): started `23:11:54Z`, completed `23:22:06Z`, success, 14 recorded steps (numbered 1-9 and 14-19, the trailing ones being Post steps) — all success.

Job log downloaded in full (188,317 B, 2,407 lines). Lines relied on:

```
127  23:12:04.4096480Z  app Semver: 1.19.1
1529 23:13:11.7890166Z  #43 [linux/amd64 builder 7/7] RUN go build -trimpath       -ldflags "-X github.com/falke-ai-circuit/probe/internal/version.Semver=1.19.1"       -o /probe ./cmd/probe/
1622 23:15:20.4339336Z  #53 [linux/arm64 builder 7/7] RUN go build -trimpath       -ldflags "-X github.com/falke-ai-circuit/probe/internal/version.Semver=1.19.1"       -o /probe ./cmd/probe/
1649 23:18:49.9105015Z  #59 exporting manifest list sha256:1051de24cb6e0c8a7c4414ca1b97e38628af05de1c8dbf1c2d3c54a42a509373 done
1662 23:18:59.3082265Z  #59 pushing manifest for ghcr.io/falke-ai-circuit/probe:e46023f1cd074ce524f30d82eba54d95775cb350@sha256:1051de24… 
1817 23:21:48.4594091Z  "build-arg:VERSION": "1.19.1",
1826 23:21:48.4600363Z  "label:org.opencontainers.image.version": "1.19.1",
2077 23:21:48.4718258Z  "label:org.opencontainers.image.version": "1.19.1",     (second provenance record — arm64 leg)
2310 23:21:48.4902615Z  DOCKER_METADATA_OUTPUT_VERSION: e46023f1cd074ce524f30d82eba54d95775cb350   (unused by the build step's inputs)
```

Both builder invocations carry `Semver=1.19.1`, the exported index digest is `sha256:1051de24…` at 23:18:49 and is the digest that was pushed at 23:18:59, and the two build records (one per platform) carry label `1.19.1` with `build-arg:VERSION 1.19.1`.

Artefact drift, cosmetic: P16-01_MULTIARCH.md:76 says "all 19 steps success"; the API reports **14** steps (step numbers run 1-9 then 14-19). The functional claims in that paragraph are all correct.

## 3. Production untouched — PASS (checkable scope)

Read on CT100 via `ssh root@139.99.209.235 -i /opt/data/.ssh/hostinger_key` → `pct exec 100`:

```
operator-op01-probe-1 | ghcr.io/falke-ai-circuit/probe:70de7fa03f879e0cdc48dc9048a5d25c69121211 | Up 3 hours (healthy)
StartedAt=2026-11-24T20:43:42.945191926Z  RestartCount=0  Running=true  Health=healthy
ImageID=sha256:466b2fc23e3349ba136a0a34867b44b48d24d9bd08ea7121fe324e24765d5504
Created=2026-11-24T20:43:41.108738787Z   ImgRef=…:70de7fa…@sha256:466b2fc2…
/docker/operator-op01/docker-compose.yml  mtime=2026-11-24 20:42:53.918180135 +0000  size=4879
```

Against the run window `23:11:51Z–23:22:07Z`: the container started 2h28m **before** the window, `RestartCount=0`, and it is running the pre-existing image `466b2fc2`, whose registry manifest I fetched myself — an OCI **index with one real platform** (`linux/amd64`) plus one attestation entry, i.e. not the new multiarch digest. The new image exists on that host only as an untagged local (`ghcr.io/falke-ai-circuit/probe:<none> 1051de24cb6e 2026-11-24 23:13:33`), pulled for verification. The compose file's mtime precedes the window, so the deployed definition was not edited during the unit.

Limit of this check, stated: an edit-to-compose followed by a revert inside the window would be invisible to mtime, and `RestartCount` proves no container-level restart, not that no file was written. "Not restarted, not rebuilt, not repointed" is proven to that degree; the child's promise is not what carries it.

## 4. arm64 gap stays a gap — PASS

Unproven is labelled where the claim lives:

- P16-01_MULTIARCH.md:145-148 — "**arm64 execution is still unproven** … `exec format error` … no arm64 binary was actually executed here. The Radxa E54C pull … is the one missing observation."
- P16-01_CLOSE.md:17 — "**arm64 execution** — … no arm64 binary was executed here … Not passing by omission."
- P16-01_MULTIARCH.md:140 — "No Radxa build was used as the deliverable; no Radxa operation was performed at all."
- CI step summary (log:2302) states `platforms: linux/amd64, linux/arm64 (single manifest list)` — a build fact, not an execution claim.
- docker.yml:84-87 ("an amd64 host and an arm64 Radxa pull the same reference") is a pull-identity statement about the index, true of the index; it does not claim a run.

Repo-wide check for a document that reads as if arm64 was executed: `git grep -c -iE 'arm64|radxa' e46023f -- '*.md'` → 12 files. Every hit is either a cross-compile/build claim (`ROADMAP.md:154`, `docs/contracts/P10-03_FALSIFICATION.md:155`) or an explicit "not probed on arm64" (`P9-08_CONTRACT_V5.md:601`). `reviews/P11-04_CHAIN_PROOF.md:136` labels android/arm64 bytes proven but its *download* UNPROVEN. No document claims arm64 execution. The artefact's own structural claim was independently confirmed: the arm64 layer's `/app/probe` is `ELF64`, `e_machine 183` (AArch64), 13,750,416 B, against amd64 `e_machine 62`, 14,280,075 B — the second platform is a real aarch64 binary, not a copy of the first.

**R-4 (named, not blocking, out of unit scope):** `README.md:104-107` and `docker-compose.yml:6-10` still pin `70de7fa…@sha256:466b2fc2…`, the single-platform index — so the repo's deploy path references neither `1051de24` nor any multiarch digest. Nothing there claims arm64 works, so this is silence rather than a false claim; it is the place where a future arm64 deploy would need the new index named and the gap restated.

## Counts

4 of 4 PASS (1 label/version chain · 2 CI run · 3 production untouched · 4 arm64 gap). Suite at `e46023f`: 627 pass incl. subtests / 0 fail / 2 skip, exit 0 — no drift from the parent's measurement. Residuals R-1…R-4 recorded above; none blocking, none a violation of the stated acceptance items.

**Verdict: PASS.**

P16-03 | e46023f1cd074ce524f30d82eba54d95775cb350 | reviewed_by: reviewer | verdict: PASS | verdict_sha_of_head: e46023f | open: R-4 — README/compose still pin the single-platform index 466b2fc2, so the multiarch digest is published but unreferenced by the deploy path; arm64 execution remains unproven
