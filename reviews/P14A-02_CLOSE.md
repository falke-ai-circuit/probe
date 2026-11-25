# P14A-02 — CLOSE

**Commit:** `81546320c1bbc44145cf792655cda8d8b8def76b` (branch `r119-build`, pushed to origin, tree clean)
**Review:** reviewed_by: `PENDING` · verdict: `NEEDS-REVIEW` · verdict_sha_of_head: `81546320c1bbc44145cf792655cda8d8b8def76b`
**Scope:** R-4 only — the deploy path (`docker-compose.yml` + `README.md` container-deployment section) now names the multiarch index `e46023f1cd07…@sha256:1051de24…`, resolved from the registry, with the platform coverage stated in both places. No Go source, no contract text, no state machine. Artefact: `/opt/data/tmp/probe119/P14A-02_R4.md`.

## Delivered

Pin changed from the amd64-only `70de7fa…@sha256:466b2fc2…` to
`e46023f1cd074ce524f30d82eba54d95775cb350@sha256:1051de24cb6e0c8a7c4414ca1b97e38628af05de1c8dbf1c2d3c54a42a509373`.
Digest read from the registry, not inferred:

    $ curl -sSI -H "Accept: application/vnd.oci.image.index.v1+json" \
        https://ghcr.io/v2/falke-ai-circuit/probe/manifests/e46023f1cd074ce524f30d82eba54d95775cb350
    docker-content-digest: sha256:1051de24cb6e0c8a7c4414ca1b97e38628af05de1c8dbf1c2d3c54a42a509373

`docker manifest inspect` on the same tag: one OCI index, real members `linux/amd64`
(`ea59e447…`) and `linux/arm64` (`4c0fd6c5…`), plus two `unknown/unknown` buildx attestations. Both member
configs carry `version 1.19.1` / `revision e46023f1cd07…`. All five registry tags were inspected: the four
older ones are amd64-only, so `1051de24` only exists at the `e46023f` tag — tag and digest therefore move
together and still describe one manifest. Comment + README now state coverage and that the arm64 member has
been pulled and inspected but not run on arm64 hardware.

## Verification

- `466b2fc2` sites: 3 changed (`README.md:108`, `docker-compose.yml:8,42`), 12 left in `reviews/P11-05_ROLLOUT.md`,
  `reviews/P16-03_CLOSE.md`, `reviews/P16-03_REVIEW.md` as historical observations. `70de7fa` in
  `.workflow/architect/P9-08/P9-17` is a citation anchor, not an image pin, left. No other pin in the repo.
- `docker compose config` unavailable on this host (no compose plugin, no daemon, no `pct` from this
  container) — validated structurally: `yaml.safe_load` parses, `services == ['probe']`, `image` reads back
  as the new pin, and `git diff` confines the structural change to that one scalar.
- Suite `GOTOOLCHAIN=local GOFLAGS=-mod=vendor go test ./... -count=1`: **741 pass / 0 fail / 2 skip**, exit 0
  — identical to the `5b2498d` baseline, as expected for a docs+compose unit.

## Open item

The pin ships the `e46023f` build, the only published multiarch index, which is 71 files ahead of the
`70de7fa` release commit while `internal/version/version.go` still reads `1.19.1` — post-release code under
the 1.19.1 label. Staying on the release commit's code requires a new multiarch build of that commit, not a
different line in the compose file. arm64 execution on arm64 hardware still unproven (no qemu binfmt here).
