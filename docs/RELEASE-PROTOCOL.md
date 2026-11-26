# RELEASE-PROTOCOL

**Status:** normative. Every release lands through this document. No exceptions without a conductor decision quoted in the tree.
**Scope:** the PROBE product (github.com/falke-ai-circuit/probe), including the four staged releases R0-R3 defined below.
**Provenance:** written against `r119-build` at `c1f7123` from the P24-01 brief, with the measured history in P15-01 and P23-00. All file:line and commit claims below were re-verified on disk before this document was committed.

---

## 1. What makes a version

Semver (`MAJOR.MINOR.PATCH`, no `v` prefix in the canonical form; `v` is display-only). The four staged releases are assignable without debate:

| Release | Version | Kind | Why |
|---|---|---|---|
| R0 | 1.19.2 | **patch** | Deadlock fix `60c5950` + rider `ea28a49` — bug fixes only, zero new surface, zero protocol change. Ships FIRST. |
| R1 | 1.20.0 | **minor** | Federation: peer backend, relaying, forward policy, inventory, matrix, live proof. New feature surface. **Must not carry encryption** — not configured out, ABSENT: no new encryption files, no new crypto endpoints, no crypto capability advertised. |
| R2 | 1.21.0 | **minor** | Encryption only — never bundled with R1. Diff vs R1 is the encryption feature alone. |
| R3 | 1.22.0 | **minor** | Frontend. Third new-surface release in sequence. |

Standing rules, written so the next release is assignable without asking:

- **PATCH** (x.y.N+1): bug fixes with no observable API or protocol surface change. Deadlock fixes, race fixes, counter-semantics corrections, typed-error wiring that was already specced. Existing agents and servers interoperate unchanged.
- **MINOR** (x.N+1.0): any new feature surface — endpoint, protocol frame, capability, frontend. Backward compatible within the same MAJOR: a 1.21 agent still connects to a 1.20 server in the degraded vocabulary the server speaks (see §2 of P23-00 for what "degraded, honest" means in this tree).
- **MAJOR** (N+1.0.0): a breaking change — agents and servers must be co-ordered across the fleet, or an old wire format is retired. Today this does not exist and should not be reached in the R0-R3 window.
- **A release may NOT bundle two independent feature axes** (e.g. federation + encryption). Reason, verbatim from the owner: "federation and encryption releases must be two separate not a single otherwise nothing works" — a bundled failure has two candidate causes and is not isolatable.
- Single-source rule: only files named in §2 may carry the version literal. `internal/server/capability_inventory.go` deliberately carries the *feature-introduction* version of each capability (e.g. still 1.19.0 for 1.19.0 intro'd capabilities); bumping those would mark real, in-fleet agents unsupported. Capability-intro versions are NOT release versions and are NOT the subject of a release bump.

## 2. The bump point

`internal/version/version.go:21` (`var Semver = "…"`), plus the companion literal `Makefile` line (`VERSION ?= …`). Both exist because `Makefile:38` stamps the release number via `-X` for `check-version-artifact` and a non-test source literal left at the wrong version would prove the wrong release (this exact correction is the delta measured in `70de7fa`).

- **Who:** the release owner (the operative lane).

- **Where and when:** in the SAME commit as the last fix / feature belonging to that release. Semantically: `release: bump Semver <old> -> <new>` is the tip of the release, immediately on top of the last code commit the release carries. Not before (pre-release commits would mis-report themselves in `--version` during reviews and stations). Not after (a post-bump fix would land on the release and force either a re-bump of the same number or a permissionless rewrite).
- Precedent at disk head: `af54467` (one version source), `8a44426` (1.18.6 -> 1.19.0), `70de7fa` (1.19.0 -> 1.19.1) — each is a bump-vs-fix relationship in a single commit pair, exactly the shape this section mandates. `60c5950` and `ea28a49` (R0's fix set) currently have NO bump commit on top; creating it is the R0 release step.
- CI is already the single-source consumer: `.github/workflows/docker.yml` steps `appver` reads `internal/version/version.go` and stamps both the image build arg and the `org.opencontainers.image.version` label from it (verified lines 46, 57, 86, 104, 107).

## 3. The tag

- **Format:** `v<semver>` — no suffix, no `-fud`, no build metadata, no platform. Annotated (-a). The display form (`v1.19.2`) and the canonical form (`1.19.2`) are two spellings of one value and both are in scope for tooling.
- **Subject:** the commit that IS the release bump commit from §2 (never a parent, never a tree position, always a concrete sha).
- **Rule:** a tag is cut only AFTER the release's proof artefact exists at its committed path (see §5). This path is the artifact the tag names, not the other way around — the tag indicts nothing and proves nothing on its own.
- Target used today is exactly the historic failure mode to not repeat: 18 tags exist, newest `v1.15.0-fud`. Neither `1.19.0` nor `1.19.1` ever got a tag, which is why P15-01 had to rebuild the anchor for the deployed image from `internal/version/version.go` seated by CI. This protocol removes that improvisation.

## 4. Proof the image came from a PUSH, not a manual dispatch

The P15-14 defect: until `c1f7123`, pushes to `r119-build` could not start a build — the branch was absent from the `docker.yml` push filter. Every image from that era that exists in ghcr is from a manual `workflow_dispatch` API call (the exact event that put `62a3b8e`'s image onto CT100 in P15-01 §1, evidenced as `run 45734302870` from a crafted dispatch). Manual dispatch is still permitted by CI but is an EMERGENCY lane, not a release lane.

A release claim pushes this chain:

1. `internal/version/version.go` bumped (§2).
2. Annotated tag `v<semver>` cut on the bump commit (§3), which ALSO triggers CI: `.github/workflows/docker.yml` `on.push.tags: ['v*']` (line 22 verified) fires a release-image build.
3. The image materializes in ghcr with provenance recorded by CI, not by hand:

   - `org.opencontainers.image.revision` = the pushed commit sha (full 40-hex, not a short form).
   - `org.opencontainers.image.version` = the semver read from `internal/version/version.go` at that commit.
   - `org.opencontainers.image.source` = the repo.

4. The DEPLOY step records, in the release artefact, the `docker manifest inspect` id and the `RepoDigests` entry for the exact reference pulled (`:v<semver>`, not a digest-in-place patch. P15-01 §2 at `62a3b8e` shows the read pattern).
5. A reviewer verifies the chain in ≤4 commands without trust:
   - `git rev-parse v<semver>` — the tag exists, and `git rev-parse v<semver>^{commit}` equals the claimed bump commit sha.
   - `git show <commit>:internal/version/version.go` — the semver literal at that sha is `<semver>`.
   - `docker manifest inspect ghcr.io/falke-ai-circuit/probe:v<semver>` — manifest exists.
   - GHCR image inspect — `org.opencontainers.image.revision` == the parse above and `org.opencontainers.image.version` == the literal above.

If any of the four commands fails or disagrees, the release is not a release — it is a manual build wearing a tag. The P15-14 defect is structural to this chain because the branch must be in the push filter AND the tag must fire the release build; both routes requisition the same labels.

## 5. The RELEASE MILESTONE contract

A release milestone (R0, R1, R2, R3 — one each, no amendments once closed) closes iff ALL of the following are on the board, each with a path or sha a reviewer can re-run:

1. **Idempotency of identity:** deployed sha == tagged sha == image revision label. Concretely: `git rev-parse v<semver>` on the bump commit; the compose file on every target host pins `ghcr.io/falke-ai-circuit/probe:v<semver>` (or the sha-tagged equivalent) whose image's `org.opencontainers.image.revision` equals the same sha. All three readings are run live at close time, not recited from an earlier read.
2. **Proof artefact path:** a file on the tree named by convention `docs/releases/R<n>-<semver>-PROOF.md` containing, at minimum, the four §4 verification commands and their verbatim output, the deploy record per target (timestamp, host, pre-image, post-image, health commitment), and the rollback plan with named images on the target (P15-01 §7 is the shape).
3. **Health/behaviour probe per target host:** for each host in the release's deploy scope, a live `GET /api/v1/health` at the deployed peer whose checked fields are named, and a behaviour check that catches what health cannot: e.g. for R0 — relay traffic resumes (agents reconnect, active count at pre-swap level or better, no 251-queue artefact in the log window structured per P15-10 patterns); for R1 — the peer inventory shows peers (the P23-00 T2 chain shape), and forward policy vocabulary returns typed errors (not `bad handshake`, per F3); for R2 — TLS refusal on the un-configured port, and `wss://`+client-CA handshakes accepted (P23-00 T1a-C shape); for R3 — the frontend loads without 500s through the new routes.
4. **A second person read it:** board entry carries `reviewed_by: <another-lane>` with verdict `GREEN | FAIL` and a date. A self-review does not count.
5. **Releases are append-only:** an R0 closure with a later defect requires R0.x (patch, not a re-open) — the same isolation logic that splits the four releases into R0-R3 in the first place.

## 6. The R2 precondition as an explicit gate

Encryption may not be deployed onto a host that has not already run R1 green on that same host. Not "R1 green somewhere in the fleet" — the SAME TARGET HOST, with its own R1 proof.

Enforcement is procedural on the release side and mechanical on the host side; this design section commits both paths (P24-03 implements the CI check; the host side is deployment discipline until the fleet can afford a build agent):

- Release-discipline gate (exists now, binding): `docs/releases/R2-1.21.0-PROOF.md` must contain, per host the release touches, the name of that host's R1 proof artefact (`docs/releases/R1-1.20.0-PROOF.md`, section named) and the health output of R1 on that host taken no earlier than the R1 milestone close. A reviewer reading only these two documents must be able to see the R2 host list ⊆ the R1 green host list, with no exceptions, no "we'll re-check on deploy".
- Host-mechanical gate (deploy-time pin): the R2 compose diff carries a pin comment naming the R1 image digest currently running on the entered host (compose already records the prior image line — see P15-01 §3 backup pattern — so this is the diff shape, not a new mechanism). If the host is running anything that is not an R1-family image, the R2 deploy halts before the swap.
- CI-design hook for P24-03 (see §8): the R2 image's `org.opencontainers.image.version` must be strictly greater than the image's feature-limit for federation; and CI-style asserts that the R2 image's `internal/server/` contains the R1 inventory endpoints of record (`/api/v1/servers` shape, e.g. P23-00's contract) so a broken-inheritance image fails rather than half-installs.

## 7. Anti-patterns — the 62a3b8e / e46023f collision as worked example

The measured state of the world before this protocol (all from P15-01 and the v119wt log at HEAD `c1f7123`):

- TWO builds are in production identity `v1.19.1` simultaneously: `e46023f` (reviewed 6/6 PASS) and `62a3b8e` (ABBA-deadlocked, hung OVH for ~2h). One version string, two opposite behaviours.
- Zero tags have ever been cut for 1.19.0 or 1.19.1 — the canonical anchor was `internal/version/version.go`'s literal, not a ref, so hosts referred to a version by asking the running binary, not by pulling a named build.
- Until `c1f7123` (P15-14), pushes could not build; the fix commit (`60c5950`) itself shipped no image and had to be coaxed through a manual workflow dispatch in P15-01 §1 — the released tool was from a manually-triggered run.

Named anti-patterns, each traceable to a specific line of the collision:

**A1 — one version, many identities.** `v1.19.1` as a string is true of both `e46023f` and `62a3b8e`; the string is broadcast to health, agent registry, and image labels (P15-01 §2 shows all three agreeing on the value `1.19.1` — because the literal was never bumped between the two WHATEVER-they-were branches that carried the same literal into both builds). The protocol kills this by making the bump commit the identity atom: bump once per release, tag only the bump, and the identity is a git object rather than a literal.
**A2 — version-literal bumps as afterthoughts** (`70de7fa` is the only clean bump, from an explicit P9-18 ask; nothing since). Without a bump commit, the R0 fix commits carry the LAST version they inherited (`1.19.1`), and R0 into production is invisible — operators read "1.19.1" in health output and have no way to know which fix set they are on. R0's absence of any bump commit is exactly this.
**A3 — release-by-narration instead of release-by-artefact.** The "release" currently exists as prose: commit messages say "one lock order for relayMu" and "P15-14: CI trigger". No board item closed with a proof path, no tag existed, no manifest was named. The protocol re-states this as §5's artefact, tag, probe, and second reviewer, not as anyone's word.
**A4 — manual dispatch as release channel.** P15-01 §1's crafted `POST /actions/workflows/docker.yml/dispatches` is usable, and was used for the `62a3b8e` image deploy on CT100 — it works, and working here means an image can materialize on a host that no push, no reviewed commit, no bump, and no tag is responsible for. The protocol says dispatch is emergency-only (§4) and the proof artefact must record the push event, not the dispatch event, as the origin of the deployed digest.
**A5 — bundling independent axes.** R1 and R2 are not bundled; the owner's reason, verbatim, is the anti-pattern reasoned in reverse (isolate the cause, don't multiply it). Federation failure must not be confusable with encryption failure — same logic as A1 but at the feature axis rather than the version axis.

## 8. CI check design for P24-03

Design only (P24-01 does not implement). Two guards, both driven off `internal/version/version.go` as the single authority:

**Guard A — version label vs version source.** The workflow already reads `internal/version/version.go` into `appver` and stamps it as both `labels: org.opencontainers.image.version` and `build-args: VERSION` (`.github/workflows/docker.yml` lines 104, 107). The check adds a hard post-build step:

- Build or inspect the image, then assert `org.opencontainers.image.version` == the value read from `internal/version/version.go` at the built sha.
- Pass condition: string equality modulo a leading "v" (canonical form in the file, display form allowed in labels).
- Fail loudly — non-zero step exit, workflow red — and PRIOR to any push, so a red build does not emit an image at all.

**Guard B — revision label vs tag existence.** For any image carrying `org.opencontainers.image.revision = <sha>`:

- The image must have a matching tag: `v<semver>` exists in the repo at the same sha — either the tag's commit is exactly the built sha, checked via `git rev-parse v<semver>^{commit}`.
- Fail when: no tag exists for the sha; the tag resolves to a different commit; the tag is present but the build was triggered on a push, not a tag, so the semver tag path and the pushed-commit path disagree.
- The `e46023f` / `62a3b8e` collision both die at this guard: two images could claim `org.opencontainers.image.version=1.19.1` with no tag anywhere in the chain — guard B refuses both, which is the whole point.
- Implementation surface: a new `check-release-tags` job in `docker.yml` (or a sibling workflow) whose inputs are the same `appver` output and the current github.sha; zero product-tree changes — P24-01 deliberately leaves CI untouched.

---

### R0-R3 quick reference (the whole document in one table)

| Rel | Ver | Content | May NOT carry | Tag on | Proof artefact | Gate to close |
|---|---|---|---|---|---|---|
| R0 | 1.19.2 | `60c5950` + `ea28a49` | anything new (patch discipline) | bump commit for 1.19.2 | `docs/releases/R0-1.19.2-PROOF.md` | deployed sha == tag == label, live health + relay-resumes probe on CT100 |
| R1 | 1.20.0 | federation (peers, relay, policy, inventory, matrix, live proof) | encryption (absent, not disabled) | bump commit for 1.20.0 | `docs/releases/R1-1.20.0-PROOF.md` | same identity chain + peer/relay/policy probe per targethost |
| R2 | 1.21.0 | encryption only | federation work, any other axis | bump commit for 1.21.0 | `docs/releases/R2-1.21.0-PROOF.md` | same identity chain + each target host MUST have R1 green on that host (§6 gate) |
| R3 | 1.22.0 | frontend | encryption, federation deltas | bump commit for 1.22.0 | `docs/releases/R3-1.22.0-PROOF.md` | same identity chain + frontend probe (no 500s, new routes live) |

Order is hard and non-negotiable: R0 FIRST (the fleet must be on a non-deadlocking 1.19.2 before federation multiplies the surface), R1 green per-host BEFORE any R2 entry on that same host, R2 before R3. The table lists the gate order: R0 -> R1 -> R2 -> R3.
