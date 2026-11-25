VERDICT PASS /opt/data/tmp/probe119/P16-01_REVIEW.md e46023f1cd074ce524f30d82eba54d95775cb350 11/11 packages ok, 0 FAIL, exit 0 (package-level run)

# P16-01R — independent adjudication of commit e46023f (arm64 multiarch + one version source)

reviewed_by: reviewer · verdict: PASS · verdict_sha_of_head: e46023f1cd074ce524f30d82eba54d95775cb350
UNIT: P16-01R (re-adjudication of P16-01, asked for by the author's PASS-SELF / reviewed_by PENDING)
OBJECT: commit e46023f1cd074ce524f30d82eba54d95775cb350, branch r119-build, single parent aad8938, ancestor of HEAD fdd5188, pushed to origin.
AUTHOR ARTEFACT: /opt/data/tmp/probe119/P16-01_MULTIARCH.md (8371 B).

CONSTRAINT HONOURED: no pull, no image run, no recreate, no restart; nothing touched on V6049A-EAS1 or the Radxa. Evidence is git objects, committed file reads, local Go compiles, local scratch-dir processes, and read-only registry/API GETs. The registry reads are distribution-API metadata requests (index, manifests, config blobs) — no layer pulled, no daemon involved.

RECIPIENT EXPERIENCE (G-USE-DONT-READ, constrained form): I did not grade the author's summary or the CI log. I executed the mechanisms: (a) the workflow's version-extraction step verbatim against the committed version.go and against mutations; (b) the Dockerfile's exact `go build -ldflags` line, with a distinguishable stamp (9.9.9-review) and a no-ldflags control, then ran the resulting binaries — `--version`, and `serve` on local scratch ports to read /health; (c) read the published image's index and both platform config blobs from GHCR. Everything called E1 below was observed in that execution.

PROVENANCE NOTE (recorded, not hidden): this file was rewritten on disk at 10:20:51Z by a writer other than the reviewer and committed as `147c7f5` ("adjudication closed on observation") with the reviewer's signature in place. Its two added evidence classes have been handled differently, because they are not equally checkable: (i) the registry claims — index mediaType/digests, four entries, `:latest` 404, and the `1.19.1` labels on both platform config blobs — I re-derived myself against GHCR's distribution API (read-only GETs, `HTTP 200`, body sha256 `1051de24…` matching the exported digest, config blobs `834646d6…`=amd64 and `8e144753…`=arm64), so they are adopted here as E1 with my own reads; (ii) the "627 pass / 0 fail / 2 skip in a detached worktree" suite figure I did not run, so it is replaced below with the run I did perform. Same verdict, but the evidence under it is now only what the reviewer observed.

## Claim adjudication

### 1. One manifest list for linux/amd64 + linux/arm64, sha-pinned tags, latest=false — PASS

Committed file (E1, `git show e46023f:.github/workflows/docker.yml`):
- `:88` `platforms: linux/amd64,linux/arm64`; exactly one `docker/build-push-action@v6` step (`:80`) with `push: true` (`:89`). One multi-platform build exports one index; no second push step exists that could emit a second single-arch index.
- QEMU `:49-52` (`platforms: arm64`) precedes buildx `:54-55`.
- Tags unchanged: `:74` `type=sha,format=long,prefix=`, `:75` `type=semver,pattern={{version}}`, `:76` `type=ref,event=tag`; `:71-72` `flavor: latest=false`. No `type=raw,value=latest` anywhere.
- Trigger, permissions, concurrency untouched (`:7-23`).

Registry (E1, read-only — the fact the earlier draft called unreachable):
```
GET ghcr.io/v2/.../manifests/e46023f1cd074ce524f30d82eba54d95775cb350 -> HTTP 200
index mediaType: application/vnd.oci.image.index.v1+json
body sha256:     1051de24cb6e0c8a7c4414ca1b97e38628af05de1c8dbf1c2d3c54a42a509373   (= the build's exported digest)
entries: 4
  linux/amd64       sha256:ea59e4474af3...
  linux/arm64       sha256:4c0fd6c56a4d...
  unknown/unknown   sha256:c7eb94e4d4c6...  reference.type=attestation-manifest  reference.digest=sha256:ea59e4474af3
  unknown/unknown   sha256:c67406a6c2af...  reference.type=attestation-manifest  reference.digest=sha256:4c0fd6c56a4d
GET .../manifests/latest -> HTTP 404
```
One index, two real architectures, sha tag resolves to it, `:latest` genuinely absent. The index digest equals both the CI-exported digest and the digest the author quoted — three independent statements of the same 32 bytes.

### 2. VERSION from internal/version/version.go, not steps.meta.outputs.version; fails loudly — PASS

- `docker.yml:96` `VERSION=${{ steps.appver.outputs.semver }}`; `:93` the same value is appended to the OCI label. `grep -n "meta.outputs.version"` over the committed file → no match: the tag-derived expression is gone, not shadowed.
- New step `:34-47`: `sed -n 's/^var Semver = "\([^"]*\)"$/\1/p' internal/version/version.go`, guard `grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'`, `::error::` + `exit 1`, inside `set -euo pipefail`.
- Executed that step body verbatim (E1) against the committed version.go: `parsed=[1.19.1]` → passes. Negative controls: no matching declaration → guard fails, `exit 1`; prefixed value `"v1.19.1"` → guard fails; whitespace drift `var Semver="1.19.1"` → guard fails. The regex is anchored, so all three fail closed rather than shipping a wrong value.
- M3 note (R-A): two `var Semver` declarations pass the guard (`grep -q` matches per line) and emit a two-line output, which GitHub rejects as an invalid step output — still a failed job, with the platform's message instead of the intended one. Exactly one `var Semver` exists today (`grep -rn "var Semver" --include=*.go` → 1 hit), so this is robustness, not a live defect.

### 3. Dockerfile consumes VERSION, ARG re-declared per stage, runtime label carries it — PASS

- Builder: `Dockerfile:19` FROM, `:25` `ARG VERSION=dev`, `:31-33` `RUN go build -trimpath -ldflags "-X github.com/falke-ai-circuit/probe/internal/version.Semver=${VERSION}" -o /probe ./cmd/probe/`.
- The `-X` target resolves: `go.mod:1` `module github.com/falke-ai-circuit/probe`. (A wrong `-X` target is a silent no-op — which is why it was compiled, not read.)
- Runtime: `:36` FROM, `:40-41` `ARG VERSION=dev` / `ARG GIT_SHA=` re-declared inside that stage (a CLI build-arg reaches only a stage that re-declares it — correct), `:42-44` `LABEL ...version="${VERSION}"` and `revision="${GIT_SHA}"`.
- Precise correction to the claim's wording: the ARG is re-declared in the two stages that consume it (builder, runtime); the `frontend` stage declares no VERSION because it consumes none. Correct construction, not an omission.
- Stamp binds (E1): `go build ... -X ...Semver=9.9.9-review` → `PROBE v9.9.9-review`; control with no `-ldflags` → `PROBE v1.19.1` (the literal).
- Published artifact (E1, amd64 config blob `sha256:834646d6...`): `org.opencontainers.image.version = 1.19.1`, `revision = e46023f1cd074ce524f30d82eba54d95775cb350`. The label on the shipped image is the Semver, not a tag.

### 4. Three version surfaces agree by construction — PASS (R-1 closed by observation)

- Label (E1, published config blobs): `1.19.1` on amd64, `1.19.1` on arm64.
- Banner (E1): stamped build prints `PROBE v9.9.9-review`; CI-equal build prints `PROBE v1.19.1`. Source path `cmd/probe/main.go:20` `var appVersion = version.Display()` → `:75` `fmt.Printf("PROBE %s\n", appVersion)` → `version.go:33` `Display() = "v" + Wire()`.
- /health (E1, two independent clean runs): binary stamped `1.19.1` served on 127.0.0.1:17799 → `{"server_version":"v1.19.1",...,"uptime_seconds":3}`; binary stamped `9.9.9-review` served on 127.0.0.1:17933 → `{"server_version":"v9.9.9-review",...,"uptime_seconds":5}`; `/api/v1/health` returns the same field. The distinct stamp proves the health field follows `Semver`, not a constant. Setting path: `cmd/probe/main.go:379` `opts.Version = appVersion` → `internal/server/server.go:282` `s.version = v` → `server_health.go:26` / `api_v1.go:558`.
- One number, 1.19.1; the `v` on banner/health is `Display()`'s prefix, the label records the canonical form. The author states this rather than glossing it.
- R-1 (the ordering doubt) closed by observation, not doctrine. `docker/metadata-action` also emits `org.opencontainers.image.version`, derived from the tag — on a `type=sha` push, the 40-char sha. The published label is `1.19.1`, so the appended line (`docker.yml:93`) is what survived. The same config blob proves the other half of the precedence question independently: `org.opencontainers.image.title` is `probe`, while `Dockerfile:42` says `PROBE` — the CLI `--label` set overrode the Dockerfile `LABEL`, which is what the version line requires. Two fields, two directions of one rule, both observed.

### 5. Nothing outside docker.yml and Dockerfile touched — PASS

`git show --stat e46023f` → `.github/workflows/docker.yml | 36 ++++`, `Dockerfile | 17 ++++`; 48 insertions / 5 deletions, 2 files. `--name-status` → `M` on both; nothing added, deleted or mode-changed. Single parent (`aad8938`), so no merge smuggled content in.

### 6. Open items honest, not hidden — PASS, with one omission named

- arm64 EXECUTION unproven: stated at MULTIARCH.md:145-148 with the observed failure (`exec /app/probe: exec format error`) and the missing measurement named. Self-limiting, in the direction that costs the author.
- Two attestation manifests: stated at :149-150 — confirmed exactly by the registry read (4 entries, 2 carrying `reference.type: attestation-manifest`, pointing at the amd64/arm64 digests). A naive `manifests[]` count reads four; real platforms are two.
- `:latest` absent by design: stated at :151-152 — confirmed, HTTP 404.
- R-2 omission (M2): §8 does not list that a `v*` tag push takes its tag from the tag and its label from source, so `v1.20.0` over a tree whose Semver is `1.19.1` yields an image tagged 1.20.0 and labelled 1.19.1. The workflow comment (`:36-38`) acknowledges it; the close list should too. No effect on this deliverable (workflow_dispatch on a branch).
- R-3 omission (M3): a hand `docker build` with no `--build-arg` stamps `dev` → label `dev`, banner `vdev` — self-consistent but wrong, while `Dockerfile:5-8` advertises that hand-build path. Compose carries no `build:` key, so no deploy path reaches it.
- §8.4 reported HEAD 5796c27 one commit ahead of origin: true at the author's close; the branch has since moved (fdd5188 plus this unit's document commit). Stale-at-read, not false-at-write — 5796c27 is an ancestor of current HEAD.

## SHAPE (G-SHAPE-FINDING)

SHAPE parallel — the unit adds two second definitions of facts that already exist in Go, in files that cannot import them:
 (a) the ldflags key is written literally at `Dockerfile:32`, while `internal/server/build_matrix.go:64` holds `versionStampKey = "github.com/falke-ai-circuit/probe/internal/version.Semver"`. A package rename breaks the Dockerfile silently (a wrong `-X` is a no-op) while the Go constant stays correct.
 (b) the Semver parse is a sed regex at `docker.yml:41`, while `internal/version/version.go:26 Normalize` is the canonical parser; they can disagree on accepted spellings (YAML is stricter — safe direction).
Neither is a defect in this commit; recorded so the duplication is a known cost at the next rename, not a surprise.

## Hostile attacks (G-NEMESIS-GATE) — 7 named, 0 unaddressed

A1 The `-X` stamp is a no-op (wrong symbol path) → mutation build 9.9.9-review vs control; banner and health both change. Addressed. This is exactly where the author's CI evidence was blind: VERSION equalled the compiled literal, so their log could not have distinguished stamped from unstamped.
A2 Runtime stage missing the ARG → `dev` label. Present at `:40`; published label reads 1.19.1. Addressed.
A3 The tag-derived `image.version` label wins over the intended one → published config reads 1.19.1 (not the sha); `title=probe` vs Dockerfile `PROBE` proves CLI-over-Dockerfile precedence. Addressed by observation.
A4 The arm64 entry could be amd64 bytes under a mislabelled platform → arm64 config reports `architecture: arm64, os: linux`, 8 layers, 209,753,086 bytes, label 1.19.1. Real arm64 image, not a relabel. Execution stays unproven (below) — that distinction is kept.
A5 Extraction silently accepts an empty or prefixed value → negative controls, `exit 1` in each. Addressed.
A6 The commit touches files outside the two named → name-status exactly two `M`, single parent. Addressed.
A7 A /health read can be answered by a foreign process on a scratch port → hit live during this adjudication: port 17801 was already occupied, my `serve` logged `bind: address already in use`, and the curl reply came from the other listener with `"server_version":""` and `uptime_seconds:214997`. A health observation is only valid with proof of responder identity (fresh uptime, successful bind line). Both /health results reported above carry that proof (3 s and 5 s, agents 0). Addressed; the check is now part of the method rather than assumed.

Triple-loop drift check: request-drift — the unit asked for multiarch + one version source; both landed, no scope substitution, and the source is the Go var rather than a new one. Implementation-drift — the Dockerfile half fixes the defect the commit message names; nothing invented beyond it. Verification-drift — the author's PASS-SELF rested on log lines whose version value equalled the literal, i.e. evidence that could not fail; this adjudication re-derived the mechanism by mutation and read the published bytes instead.

## Residuals (none blocking)

R-1 label ordering — CLOSED by registry observation.
R-2 `v*` tag can disagree with the label — tag scheme is deliberately not the version source; belongs in §8.
R-3 hand `docker build` yields `dev`/`vdev` — documentation path only.
R-4 CI evidence cannot distinguish stamped from unstamped; the mutation can. No action.
R-A two `var Semver` declarations → multi-line output, opaque platform failure instead of the intended `::error::`. Cosmetic.
R-B scratch-port collisions are common in this profile's shared container; health checks must assert responder identity (see A7).

## The one observation still missing

The Radxa E54C pull of `ghcr.io/falke-ai-circuit/probe@sha256:1051de24cb6e0c8a7c4414ca1b97e38628af05de1c8dbf1c2d3c54a42a509373` followed by `/app/probe --version` (and a `/health` read if the board's client is running). That single observation closes arm64 execution — the only claim in this unit that neither CI nor this review could settle. Unit: P16-02. Not performed here: this brief forbids touching the Radxa.

## Close state (repo cleanliness)
My commit contains one path: `reviews/P16-01R_REVIEW.md` (`git show --stat` → 1 file changed, 91 insertions). Nothing else in the tree is mine.

At close, `/opt/data/projects/PROBE_UPGRADE/v119wt` carried five paths belonging to another unit's live lane (P14B-05 / P14-02: `internal/agent/input_pointer.go`, `internal/protocol/input_geometry.go` modified; three untracked `*_test.go`). They appeared between 10:18 and 10:21Z, after my commit, and are not touched here — clearing them would be a lane violation and would destroy another agent's in-flight work. Consequence: the close check reports NOT CLOSABLE against the shared tree for reasons outside this unit. The same check against a clean checkout of commit 8146e64 is CLOSABLE; both outputs are quoted in the close report.

## Suite
INTEGRITY NOTE: at 10:23:37Z this file was modified on disk by a writer other than the reviewer (the committed copy at 10:20:51Z is intact in git). The inserted text was a suite result — "`-v` ... dedicated detached worktree ... 627 PASS / 0 FAIL / 2 SKIP" — that this review did not run and cannot evidence (it matches the earlier P16-03 review's numbers). It is removed here, not adopted: an unverified claim inside a review artefact is the defect this role exists to catch.

What this review actually ran (`go test ./... -count=1` at HEAD fdd5188, Go toolchain 1.24.4, /opt/data/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.24.4.linux-amd64/bin/go): 11 of 11 test packages `ok`, 5 packages `no test files`, 0 `FAIL` lines, 0 panic/race markers, `real 2m2.771s`, exit 0. No `-v`, no worktree, so no subtest-level pass count is claimed here. Nothing in this commit is Go code, so the run is a no-drift control, not evidence for claims 1-4.

P16-01R | e46023f1cd074ce524f30d82eba54d95775cb350 | reviewed_by: reviewer | verdict: PASS | verdict_sha_of_head: e46023f | open: arm64 execution (Radxa pull + `--version` banner) belongs to P16-02; R-2/R-3 belong in the author's §8
