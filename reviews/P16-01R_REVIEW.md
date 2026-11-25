VERDICT PASS /opt/data/tmp/probe119/P16-01_REVIEW.md e46023f1cd074ce524f30d82eba54d95775cb350 suite=11/11-pkg-ok-0-fail-exit0

# P16-01R — independent adjudication of commit e46023f (arm64 multiarch + one version source)

REVIEWED_BY: reviewer (adjudicator; authored neither the workflow nor the Dockerfile)
UNIT: P16-01R (re-adjudication of P16-01)
OBJECT: commit e46023f1cd074ce524f30d82eba54d95775cb350, branch r119-build, ancestor of HEAD fdd5188, tree clean, origin/r119-build == HEAD.
AUTHOR ARTEFACT: /opt/data/tmp/probe119/P16-01_MULTIARCH.md (8371 B), author verdict PASS-SELF with reviewed_by PENDING.
CONSTRAINT HONOURED: no pull, no image run, no recreate, no restart, nothing touched on EAS1 or the Radxa. Everything below is repo reads, git objects, local Go compile, and local shell execution of the workflow's own extraction step.

RECIPIENT EXPERIENCE (G-USE-DONT-READ, constrained form): I did not read the CI outcome and call it a review. I executed the three mechanisms this unit rests on, locally: (a) the workflow's version-extraction step, verbatim, against the real version.go plus four mutations; (b) the Dockerfile's `go build` RUN line, verbatim, with a distinguishable stamp (9.9.9-review) and a no-ldflags control; (c) the resulting binary served on 127.0.0.1:17791, read through /health and /api/v1/health. All three are recorded below as E1/E2. Registry bytes, image config labels and arm64 execution are outside this brief's permitted surface and are named as such, not glossed. The local serve was a local process against scratch data, not an image run, and it touched neither EAS1 nor the Radxa.

## Claim adjudication

### 1. One manifest list for linux/amd64 + linux/arm64, sha-pinned tags, latest=false — PASS
Evidence (E2, committed tree read + parse):
- `.github/workflows/docker.yml:88` (post-commit) `platforms: linux/amd64,linux/arm64`, single `docker/build-push-action@v6` step (`:80`). YAML parse of the post-commit file: 8 steps, exactly one with `platforms`, one `tags:`, one `push: true` — there is no second build/push step that could export a second, single-arch index.
- QEMU registered before buildx: `:49-52` (setup-qemu, `platforms: arm64`) precedes `:54-55` (setup-buildx). Order confirmed by parse indices 2 < 3.
- Tag scheme unchanged: `:74` `type=sha,format=long,prefix=` (bare 40-char sha always), `:75` `type=semver,pattern={{version}}` and `:76` `type=ref,event=tag` (v* only), `:71-72` `flavor: latest=false`. Forbidden-path check: no `type=raw,value=latest` anywhere in the file.
- Diff is exactly the platform line, the labels block, the build-arg value, two log lines, plus the QEMU step — no change to the trigger, permissions or concurrency.
What this does NOT prove: that the four-entry index the author quotes exists. That is a registry fact (E4 to me; no docker daemon reachable from this profile — `docker version` → "Cannot connect to the Docker daemon", and the brief forbids pulling).

### 2. VERSION from internal/version/version.go, not steps.meta.outputs.version; loud failure — PASS
Evidence (E2):
- `docker.yml:96` `VERSION=${{ steps.appver.outputs.semver }}`; `grep -n steps.meta.outputs.version .github/workflows/docker.yml` → no match. The tag-derived expression is gone from the file, not merely shadowed.
- The new step (`:34-47`) parses `internal/version/version.go` with `sed -n 's/^var Semver = "\([^"]*\)"$/\1/p'` and guards with `grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'` + `::error::` + `exit 1`.
- Executed that step body verbatim against five inputs (E1):
  ok (real file)            rc=0  semver=<1.19.1>
  value "v1.19.1"           rc=1  FAILED (<- no silent acceptance of a prefixed value)
  declaration renamed       rc=1  FAILED (got '')   <- the loud-failure claim, proven
  `var Semver="1.19.1"`     rc=1  FAILED (got '')   <- whitespace drift also fails loud, not silent
  two declarations          rc=0  value=<1.19.1\n2.0.0>
- M3 robustness note (R-A): the two-declaration case passes the regex because `grep -q` matches one line, then emits a two-line value into `$GITHUB_OUTPUT`, which GitHub rejects with "Invalid format" — still a failed job, but with an opaque message rather than the intended one. Cosmetic; the claim as stated ("fails loudly if no Semver parses") holds.

### 3. Dockerfile consumes VERSION, ARG re-declared after each FROM, label carries it — PASS
Evidence (E1 + E2):
- Builder stage: `Dockerfile:19` FROM, `:25` `ARG VERSION=dev`, `:31-33` `RUN go build -trimpath -ldflags "-X github.com/falke-ai-circuit/probe/internal/version.Semver=${VERSION}" -o /probe ./cmd/probe/`.
- Symbol path is real: `go.mod:1` `module github.com/falke-ai-circuit/probe` → the `-X` target resolves; a typo here would be a silent no-op, which is why I compiled it (below).
- Runtime stage: `:36` FROM debian, `:40-41` `ARG VERSION=dev` / `ARG GIT_SHA=` re-declared inside the stage (the only way a CLI build-arg reaches a stage — correct), `:42-44` `LABEL org.opencontainers.image.version="${VERSION}"` and `revision="${GIT_SHA}"`.
- I ran that exact RUN line against the committed tree with a distinguishable value (E1): `go build -trimpath -ldflags "-X ...Semver=9.9.9-review" -o probe-stamped ./cmd/probe/` → `probe-stamped --version` → `PROBE v9.9.9-review`. Control build with no `-ldflags` → `PROBE v1.19.1` (the literal). The stamp binds; it is not decoration.
- Consequence for the author's evidence: CI's VERSION was 1.19.1, identical to the compiled literal, so the CI output could not have distinguished "stamped" from "unstamped" if the `-X` path were wrong. Their log lines prove the arg was passed, not that it bound. The mechanism is nevertheless correct — proven by the mutation, not by their log.

### 4. Three surfaces agree by construction — PASS, with one link that is construction-only (R-1)
Evidence (E1/E2 for two surfaces, construction for the third):
- Banner: `cmd/probe/main.go:20` `var appVersion = version.Display()`; `:75` `fmt.Printf("PROBE %s\n", appVersion)`; `internal/version/version.go:33` `Display() = "v" + Wire()`. Observed directly: my stamped build printed `PROBE v9.9.9-review`.
- /health `server_version`: both server entry paths set it from the same var — `cmd/probe/serve.go:145` `srv.SetVersion(appVersion)` and `cmd/probe/main.go:379` `opts.Version = appVersion` → `internal/modes/server_mode.go:81` `s.srv.SetVersion(s.opts.Version)` → `internal/server/server_health.go:26` and `internal/server/api_v1.go:558` `"server_version": s.version`. There is no path where the health field is fed from a flag, config or tag. Observed directly (E1): the stamped binary started with `serve` on 127.0.0.1:17791 returned `{"active_agents":0,"server_version":"v9.9.9-review","status":"ok",...}` from `/health` and `"server_version":"v9.9.9-review"` from `/api/v1/health` — the same stamp as the banner, from the two endpoints the recipient reads.
- Image label: two committed paths carry the same value — `Dockerfile:42-43` LABEL from `ARG VERSION` (= `steps.appver.outputs.semver`) and `docker.yml:91-93` appending `org.opencontainers.image.version=${{ steps.appver.outputs.semver }}`. So the label cannot receive a value other than the Semver, unless ordering says otherwise:
- R-1 (named, not softened): `docker/metadata-action` also emits its own `org.opencontainers.image.version`, derived from the tag — on a sha push that is the 40-char sha (`docker.yml:75`). The correction line is appended after it (`:93`), so label correctness rests on last-wins among `--label` values and on `--label` overriding the Dockerfile LABEL. Both are docker's documented precedence, and the filed P16-03 review records registry bytes with `1.19.1` on both platform configs — but that is E4 to me (another agent's claim; I cannot reach a daemon or pull). Verdict on claim 4 stands on construction; the registry read is P16-02's, together with arm64.
- Prefix note: banner and health carry `v`, the label carries the canonical form. Same number, one source; the author states this rather than hiding it.

### 5. Nothing else touched — PASS
Evidence (E2): `git diff-tree --name-status -r e46023f` → `M .github/workflows/docker.yml`, `M Dockerfile`, nothing else. `git show --numstat` → 32/4 and 16/1, total 48 insertions 5 deletions, matching the commit body. No added/deleted paths, no mode changes.

### 6. Open items honest, not hidden — PASS, with one omission
Evidence (E2 diff + E4 author artefact read):
- arm64 EXECUTION unproven: stated in `§8.1` with the observed failure (`exec /app/probe: exec format error`) and the missing measurement named. A self-limiting disclosure, in the direction that costs the author.
- two attestation manifests: stated in `§8.2`, and consistent with buildx default provenance (`mode=min`) attaching one attestation manifest per built platform — 2 platforms → 2 unknown/unknown entries alongside 2 real ones, so a naive `manifests[]` count reads four. The count itself is E4 to me; the disclosure is honest either way.
- `:latest` absent by design: demonstrable from the committed diff (`docker.yml:71-72 latest=false`, no raw latest tag) — the one open item I can prove without a registry.
- OMISSION (M2, R-2): the artefact's open-items list does not mention that on a `v*` tag push the registry tag comes from the tag while the label comes from source, so `v1.20.0` on a tree whose Semver is `1.19.1` yields an image tagged 1.20.0 and labelled 1.19.1. The workflow's own comment acknowledges it (`docker.yml:36-38`), so it is disclosed in code but not in the close list. Filed P16-03 had already recorded it as R-2; it should be in §8.
- Also disclosed nowhere in §8 (M3, R-3): a hand `docker build` with no `--build-arg` stamps `VERSION=dev` → label `dev`, banner `vdev` — self-consistent, wrong number — while `Dockerfile:5-8` advertises exactly that hand-build path.

## SHAPE (G-SHAPE-FINDING)
SHAPE parallel — the unit adds two second definitions of facts that already exist in Go:
 (a) the ldflags key is written literally in `Dockerfile:32` while `internal/server/build_matrix.go:64` holds it as `versionStampKey = "github.com/falke-ai-circuit/probe/internal/version.Semver"`; a package rename breaks the Dockerfile silently (a wrong `-X` target is a no-op) and leaves the Go constant correct.
 (b) the Semver parse is a sed regex in `.github/workflows/docker.yml:41` while `internal/version/version.go:26` `Normalize` is the canonical parser; the two can disagree on accepted spellings (the YAML one is stricter, by design — see claim 2 mutation `v1.19.1`).
Neither is a defect in this commit (a Dockerfile and a YAML step cannot import Go constants), and the stricter YAML parse is the safe direction. Recorded so the duplication is a known cost, not a surprise at the next rename.

## Hostile attacks (G-NEMESIS-GATE) — 6 named, 0 unaddressed
A1 The stamp is a no-op / `-X` path wrong → control vs stamped build, banner changes. Addressed; and this is where the author's own evidence was insufficient (their VERSION equalled the literal).
A2 `ARG VERSION` missing in the runtime stage → `dev` label. Present at `:40`, verified.
A3 Two `image.version` labels, tag-derived one could win → R-1, ordering dependency named; registry confirmation deferred to P16-02, not assumed.
A4 arm64 leg could ship amd64 bytes under emulation → unverifiable here (no daemon, no pull). Named in §8.1 and carried to P16-02; not passed by omission.
A5 Extraction step silently passes an empty or prefixed value → mutation table, rc=1 in both. Addressed.
A6 Commit silently touches files outside the two named → diff-tree name-status. Addressed.

Triple-loop drift check: request-drift — the unit asked for multiarch + one version source; both landed, no scope substitution. Implementation-drift — the Dockerfile half is the fix for the defect the commit message names, not extra invention. Verification-drift — the author verified with a value equal to the literal (blind spot); the mechanism was re-verified here by mutation, which is the drift this adjudication exists to catch.

## Residuals
R-1 label ordering (metadata-action's tag-derived `image.version` precedes the appended semver line) — non-blocking, closure = registry config read in P16-02.
R-2 v* tag can disagree with label — tag scheme is deliberately not the version source; should be in §8.
R-3 hand `docker build` yields `dev`/`vdev` — documentation path only (compose carries no `build:` key).
R-4 the CI evidence cannot distinguish stamped from unstamped; the local mutation can. No action.
R-A two `var Semver` declarations produce a multi-line output → GitHub's "Invalid format" failure, not the intended `::error::`. Cosmetic.

## Missing observation (not performed here)
The Radxa E54C pull of `ghcr.io/falke-ai-circuit/probe@sha256:1051de24cb6e0c8a7c4414ca1b97e38628af05de1c8dbf1c2d3c54a42a509373` followed by `/app/probe --version` (and, if the board's client is running, a `/health` read). That single observation closes arm64 execution, the registry label bytes and the index shape at once. Unit: P16-02. Not performed — this brief forbids touching the Radxa.

## Suite
`go test ./... -count=1` at HEAD fdd5188 (Go toolchain 1.24.4, /opt/data/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.24.4.linux-amd64/bin/go): 11 of 11 test packages `ok`, 5 packages `no test files`, 0 `FAIL` lines, 0 panic/race markers, `real 2m2.771s`, exit 0. Nothing in this commit is Go code, so this run is a no-drift control, not evidence for claims 1-4.

P16-01R | e46023f1cd074ce524f30d82eba54d95775cb350 | reviewed_by: reviewer | verdict: PASS | verdict_sha_of_head: e46023f | open: P16-02 must observe arm64 execution + registry label bytes on the Radxa; R-1 label ordering unproven from this profile
