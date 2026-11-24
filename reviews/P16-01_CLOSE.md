# P16-01 CLOSE — one manifest list for amd64 + arm64, VERSION stamped from internal/version; arm64 execution unproven

**Commit:** `e46023f` (repo `/opt/data/projects/PROBE_UPGRADE/v119wt`, branch `r119-build`, pushed — `origin/r119-build` contains `e46023f`). Repo HEAD is `5796c27`, P12-06's docs-only commit (`reviews/P12-06_*.md`, 162 insertions, no code); the verdict binds to `e46023f`.
**Review:** reviewed_by: `reviewer` (rendered in P16-03 against this same head) · verdict: `PASS` (4 of 4 items) · verdict_sha_of_head: `e46023f`
**Verdict history:** this close was written NEEDS-REVIEW at 23:38Z because no independent verdict existed; P16-03 rendered PASS 4/4 at `e46023f` and the row above is that verdict applied, not a self-assessment. Review artefact: `/opt/data/tmp/probe119/P16-03_REVIEW.md`.
**Scope:** `.github/workflows/docker.yml` + `Dockerfile` only. No local build used as the deliverable; no Radxa operation; no Android; the production container was not restarted, repointed or rebuilt. Artefact: `/opt/data/tmp/probe119/P16-01_MULTIARCH.md` (8371 B, sha256 `bfe110c181c7f17eb2db23c430513153dd7274003cbd85526e638114cb06cff4`).

## Parent verification (orchestrator, this close)

Measured on this host, not taken from the operative's report:

- `docker manifest inspect ghcr.io/falke-ai-circuit/probe@sha256:1051de24cb6e0c8a7c4414ca1b97e38628af05de1c8dbf1c2d3c54a42a509373` → schemaVersion 2, mediaType `application/vnd.oci.image.index.v1+json`, with a real `linux/amd64` manifest (`sha256:ea59e447…`) and a real `linux/arm64` manifest (`sha256:4c0fd6c5…`) plus the two `unknown/unknown` attestation entries. The digests are byte-identical to the ones the artefact quotes, so that output came from that image, not from prose.
- `git rev-parse origin/r119-build` contains `e46023f`; working tree clean.
- The two defects the unit claims are visible in the diff: the workflow now parses `Semver` out of `internal/version/version.go` with a guard that fails the job on an unparseable value, and the Dockerfile consumes `ARG VERSION` in the builder and re-declares it plus `GIT_SHA` in the runtime stage for the labels.

## Not proven

- **arm64 execution** — the index has an arm64 manifest and CI compiled it, but no arm64 binary was executed here: this host has no qemu binfmt, and the Radxa E54C pull-and-`--version` observation is the one missing measurement. Not passing by omission.
- **Two attestation manifests** in the index — harmless, but a consumer counting `manifests[]` sees four entries for two platforms.
- `:latest` does not exist by design (`latest=false`); anything requesting it is a caller defect, not a registry gap.
- No independent review of the CI run, the label/banner/health agreement, or the "production untouched" claim — that is the armed leg's job (P16-03), not this close's.

## Open item

Review leg owed: verify CI run 45507859352's log lines against the committed workflow, confirm the three version surfaces on the pulled digest, and confirm `operator-op01-probe-1` was never restarted. Until then the verdict row stays NEEDS-REVIEW.
