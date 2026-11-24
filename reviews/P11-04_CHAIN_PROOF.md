# P11-04 — CHAIN PROOF: request → artifacts → metadata ↔ bytes → token download → health/version

reviewed_by: reviewer
verdict: PASS
verdict_sha_of_head: cda0a0a681579e71dff9387d437db06e34361c73
unit: P11-04
artefact: /opt/data/tmp/probe119/P11-04_CHAIN_PROOF.md
repo: /opt/data/projects/PROBE_UPGRADE/v119wt · branch r119-build · HEAD cda0a0a (tree clean before and after)
suite: `go test ./... -count=1 -timeout 1500s` → 15 packages, 0 failures, 591 pass incl. subtests, 2 skip, `SUITE_EXIT=0` (counts from a parallel `-json` run, same tree)
open item: F1 — `cmd/probe-server` serves `"server_version":""` on `/api/v1/health` (shipped entrypoint `probe serve` is correct). One-line fix, does not block this chain.

Units under test are unmodified by this session: P11-01 `eb9d93b`, P11-02 `e09928d`, P11-03 `cda0a0a`.

---

## 0. Method and ground truth

Server binary built from the committed tree at HEAD, not from a worktree copy:

```
$ git rev-parse HEAD
cda0a0a681579e71dff9387d437db06e34361c73
$ git status --porcelain | wc -l
0
$ /tmp/go/bin/go build -o …/p1104/probe ./cmd/probe && …/probe --version
PROBE v1.19.1
```

Run (from the repo root so the builder resolves `./cmd/probe-client/`; the packaged image uses the same entrypoint, `Dockerfile:44-45` → `/app/probe serve`):

```
probe serve --addr 0.0.0.0:17894 \
  --builder-db …/state/builds.json --builder-output-dir …/out \
  --builder-go /tmp/go/bin/go --builder-source …/v119wt/cmd/probe-client/ \
  --admin-password … --allowed-cidr 0.0.0.0/0
```

Login → bearer token; all build requests below carry it. Every number in this document came from a command run in this session; no figure is copied from P11-01/02/03.

---

## 1. The chain, link by link

| # | link | claim under test | evidence (command → output) | verdict |
|---|------|------------------|-----------------------------|---------|
| 1 | request → artifacts | one matrix request produces one artifact per supported target | `POST /api/v1/builds` `{targets:[linux/amd64, windows/amd64, android/arm64], wait:true}` → `HTTP=201 t=28.1s`, `"status":"complete"`, `"matrix_version":2`, `"artifacts"` length 3, one `{os,arch,version,sha256,size,filename}` each (`matrix_response.json`) | PROVEN |
| 2a | metadata ↔ bytes (sha256) | reported `sha256` equals the digest of the file the server wrote | 3/3 equal, both values in §2 (`sha256sum` + Python `hashlib` over `out/<filename>`) | PROVEN |
| 2b | metadata ↔ bytes (size) | reported `size` equals the file's size | 3/3 equal: 10465666 / 11048960 / 10771750 | PROVEN |
| 2c | metadata ↔ bytes (version) | reported `version` is read from the artifact, not from a builder constant | `out/…_linux-amd64 --version` → `1.19.1`, exit 0, equal to reported `1.19.1`; mutation probe in §3 proves the reader reads the stamp (`9.9.9`), not `version.Semver` (`1.19.1`) | PROVEN |
| 2d | the *original* P11-01 acceptance run | its published digests match the files it published | retained run at `/opt/data/profiles/coder/cache/scratch/p1101/live/` — 3/3 match, §2 table B | PROVEN |
| 3 | download WITH token, cross-host | the artifact can be fetched with a token from another machine, byte-identical | from CT100 `falke-stack` (Proxmox `139.99.209.235`, `pct exec 100`): `curl -H "Authorization: Bearer …" http://172.26.0.4:17894/api/v1/builds/<id>/download` → `HTTP=200 bytes=10465666`, `sha256 b587aa2d…` (source), ELF magic `7f 45 4c 46`; same URL without token → `HTTP=401` | PROVEN |
| 4a | health ↔ build metadata version | `/api/v1/health.server_version` equals the version the build published | server health `"server_version":"v1.19.1"` vs artifact `"version":"1.19.1"` (wire form carries no `v`, `internal/version` doc) | PROVEN |
| 4b | agent count ↔ registry | the count the surfaces report equals the registry itself | live stack health `total_agents:24 active_agents:3 stale_agents:2`; registry.json → `24` keys, statuses `{inactive:19, active:3, stale:2}` → 24 = 3 + 2 + 19 | PROVEN |
| 5 | refusals are typed | an unbuildable target yields a typed refusal, not a 500 or an empty list | `targets:[darwin/arm64]` → `HTTP=409`, `code BUILD_TARGET_UNSUPPORTED`, `detail.ran:false`, `targets[0].reason NOT_IN_PLATFORM_MATRIX`, remediation cites §11 (`refusal.json`) | PROVEN |
| 5b | mixed request | a refused target is published beside the built ones, not hidden | `targets:[linux/amd64, darwin/arm64]` → `HTTP=200`, `status:complete`, 1 artifact + `refusals[0]` naming darwin with the same code/reason (`refusal_mixed.json`) | PROVEN |

Contract cross-check for the refusal's citation: `docs/contracts/P10-07_CONTRACT_V3_1.md:621` lists five columns — Windows `wts`, Linux X11 (+Xvfb), Wayland, Android Termux, Android a11y APK — and no darwin column. The reason string is accurate.

Resolving 24 vs 25: the registry on the live stack holds **24** keys (24 = 3 active + 2 stale + 19 inactive); `/api/v1/health` reports **24**. The 25-agent figure is the stale P8-16 note, not current state. P11-03's measurement is correct.

---

## 2. Digest comparison — both values, per produced artifact

### A. This session's live matrix build (`build-c2800a3f-d65e-42dd-8557-4b30c4e973e4`)

| target | API `sha256` | recomputed from the file on disk | match | API `size` | disk size |
|---|---|---|---|---|---|
| linux/amd64 | `b587aa2de10c0e818a41e1be251d2f66ec7a37187c8089fdfe274caa8411a0a2` | `b587aa2de10c0e818a41e1be251d2f66ec7a37187c8089fdfe274caa8411a0a2` | yes | 10465666 | 10465666 |
| windows/amd64 | `e8f92c5c96a00830a668b3c4b1afc879c81a7fc5883a4941aa188edd7df6c5c9` | `e8f92c5c96a00830a668b3c4b1afc879c81a7fc5883a4941aa188edd7df6c5c9` | yes | 11048960 | 11048960 |
| android/arm64 | `7f33f862e231e3cb97c68e5335a6328225ca8d9d9dcadacb56ff69b80e5d1b70` | `7f33f862e231e3cb97c68e5335a6328225ca8d9d9dcadacb56ff69b80e5d1b70` | yes | 10771750 | 10771750 |

Architecture is real, not a copied file — headers read directly:

```
…_linux-amd64    ELF 64bit e_machine 62   (x86-64)
…_android-arm64  ELF 64bit e_machine 183  (AArch64)
…_windows-amd64.exe  PE\0\0 machine 0x8664 (x86-64)
```

Version self-report, the linux binary (runs on this host):

```
$ …/build-c2800a3f-…_p1104-matrix_linux-amd64 --version
1.19.1
exit=0
```

The downloaded copy is the same bytes: `sha256sum dl_linux.bin` → `b587aa2d…`, identical to the reported value and the disk file (`HTTP=200`, `Content-Length: 10465666`).

### B. P11-01's own acceptance run — the comparison the brief says was never made

The brief states P11-01's acceptance artifacts "were not retained". They are: `/opt/data/profiles/coder/cache/scratch/p1101/live/out/` holds all three, next to `matrix_response.json`. Recomputed now:

| target | published `sha256` (P11-01 §3) | recomputed from the retained file | match | published size | disk size |
|---|---|---|---|---|---|
| linux/amd64 | `bb196586c78d003bfadbc636fdaec8b340738862aa3683f29de87eb52288f564` | `bb196586c78d003bfadbc636fdaec8b340738862aa3683f29de87eb52288f564` | yes | 11018459 | 11018459 |
| windows/amd64 | `922c0c3adb360a26b52e39f682ca59add2bf565381de3ef501be19803ef48fe8` | `922c0c3adb360a26b52e39f682ca59add2bf565381de3ef501be19803ef48fe8` | yes | 11613696 | 11613696 |
| android/arm64 | `0e04584972871d9f1a81348b7b82e0d766848dd17d45bdea1dcb25fde93601c2` | `0e04584972871d9f1a81348b7b82e0d766848dd17d45bdea1dcb25fde93601c2` | yes | 11417377 | 11417377 |

```
$ ./out/build-e34034d6-…_p1101-matrix_linux-amd64 --version
1.19.1
```

P11-01's headline claim — published metadata equals the published bytes — is true for the bytes it actually published, verified after the fact.

---

## 3. Mutation probe: is `version` really read from the bytes?

P11-01's own test `TestBuildMatrixReportedVersionComesFromArtifact` asserts `a.Version == version.Wire()` (`internal/server/p1101_matrix_test.go:134`) — the same value the builder stamps, so a reader that returned the constant would pass it. That test cannot fail on the defect it names. Probe instead: build the client with a *different* stamp and read the metadata back through the production reader.

```
$ GOOS=linux  GOARCH=amd64 go build -ldflags "-X …/internal/version.Semver=9.9.9" -o artifact_v999
$ GOOS=windows GOARCH=amd64 go build -ldflags "…9.9.9" -o artifact_v999.exe
$ GOOS=android GOARCH=arm64 go build -ldflags "…9.9.9" -o artifact_v999_android

$ go test ./internal/server -overlay=…/overlay.json -vet=off -run TestP1104VersionComesFromBytesNotConstant -v
    p1104_probe_test.go:18: artifact_v999         -> version="9.9.9" os=linux   arch=amd64
    p1104_probe_test.go:18: artifact_v999_android -> version="9.9.9" os=android arch=arm64
    p1104_probe_test.go:18: artifact_v999.exe     -> version="9.9.9" os=windows arch=amd64
--- PASS
```

`version.Semver` in the tree is `1.19.1` (`internal/version/version.go:21`). The reader returned the compiled-in `9.9.9` for ELF x86-64, ELF AArch64 and PE — so `readArtifactMetadata` (`internal/server/build_matrix.go:488`) reads the stamp from the file, on all three formats.

The probe was injected with `go test -overlay`; the worktree stayed clean (`git status --porcelain` → 0 lines).

---

## 4. Coverage — what was built, refused, downloaded, from where

- **Built (3):** `linux/amd64`, `windows/amd64`, `android/arm64` — from one `POST /api/v1/builds`, `wait:true`, 28.1 s.
- **Refused (1):** `darwin/arm64` — `409 BUILD_TARGET_UNSUPPORTED / NOT_IN_PLATFORM_MATRIX`, `ran:false`. In a mixed request it is published in `refusals[]` while the supported target still builds.
- **Downloaded (1):** `linux/amd64` only — `GET /api/v1/builds/{id}/download` serves the record's `binary_path`, i.e. the record's own OS/arch. There is no per-artifact selection parameter (P11-01 §7.1, still open); windows/exe and android/arm64 were verified from disk bytes, not over HTTP. Their bytes are proven; their *download* is UNPROVEN by construction of the endpoint, not by failure.
- **Cross-host fetch:** from CT100 `falke-stack` (Proxmox host `139.99.209.235`, `pct exec 100`) to `172.26.0.4:17894` — a different host and network namespace, 200 with token, 401 without.
- **Health:** local server `v1.19.1`; live stack `v1.19.1`, 24 total / 3 active / 2 stale, registry 24 keys.

Unavailable on this host, stated rather than skipped: no `docker` daemon and no `docker compose` / `docker-compose` binary here, so the P11-03 compose file was not re-parsed by me; its parse evidence remains CT100-side and E4 by my standard. The live stack's behaviour (health, registry, version) I did re-measure directly (E1).

---

## 5. Findings (none blocking)

**F1 — `cmd/probe-server` reports an empty version on `/api/v1/health`.** Verified live: same tree, `./probe-server --addr …` → `{"ok":true,"data":{"active_agents":0,"server_version":"","stale_agents":0,…}}`. Cause: `cmd/probe/serve.go:145` calls `srv.SetVersion(appVersion)`; `cmd/probe-server/main.go` never does (the value is used only for `--version`). Consequence: anyone reproducing P11-01's acceptance with `probe-server` (its §8 reproduce block says exactly that) sees a health surface that cannot be compared to the artifact version. Pre-existing, not introduced by P11-01/02/03; does not affect the shipped image (`Dockerfile:44-45` runs `probe serve`). Fix: one `srv.SetVersion(appVersion)` call in `cmd/probe-server/main.go`.

**F2 — matrix semantics stated more strongly than the tree implements.** P11-01 §7.3 says "A failed target fails the whole matrix … no artifacts published", and `build_matrix.go:371-375` says the same for the all-or-nothing case. That holds for a *compile failure*; for a target outside the platform matrix, `resolvedTargets` drops it and the request completes `201` with the remaining artifacts **and** `refusals[]` naming it — observed: 2 targets requested → 1 artifact + 1 refusal. Disclosure is present, so the safety property ("no *silently* shorter list") survives; the sentence should be scoped: unsupported targets are refused individually and published; build failures abort the matrix.

**F3 — the P11-01 test that names the property cannot detect its violation.** `p1101_matrix_test.go:134` compares the parsed version to `version.Wire()`, which is the value stamped in — a reader returning the constant passes. The property is nevertheless true (§3 mutation probe). Replace/extend with a non-default stamp fixture so the test bites.

**F4 — P11-01's "acceptance artifacts were not retained" is inaccurate.** They are retained under `/opt/data/profiles/coder/cache/scratch/p1101/live/out/` and match the published digests 3/3 (§2 B). Correct the record; the claim is verified either way.

---

## 6. Evidence index

```
/opt/data/profiles/reviewer/cache/scratch/p1104/
  matrix_response.json      # this session's 201, 3 artifacts
  refusal.json              # darwin-only 409 typed refusal
  refusal_mixed.json        # 1 artifact + 1 published refusal
  dl_linux.bin              # download, sha256 = b587aa2d…
  out/                      # the three produced artifacts
  suite.json                # go test ./... -json, 0 failures
  p1104_probe_test.go, overlay.json, artifact_v999{,.exe,_android}
/opt/data/profiles/coder/cache/scratch/p1101/live/{matrix_response.json,out/}   # P11-01 original run
```

## 7. Reproduce

```
cd /opt/data/projects/PROBE_UPGRADE/v119wt && git rev-parse HEAD   # cda0a0a
go build -o /tmp/p1104/probe ./cmd/probe
(cd . && /tmp/p1104/probe serve --addr 0.0.0.0:17894 --builder-db … \
   --builder-output-dir … --builder-go /tmp/go/bin/go \
   --builder-source "$PWD/cmd/probe-client/" --admin-password … --allowed-cidr 0.0.0.0/0)
TOK=$(curl -s -X POST -d '{"username":"admin","password":"…"}' localhost:17894/api/v1/login | jq -r .data.token)
curl -s -X POST -H "Authorization: Bearer $TOK" -d '{"name":"p1104","targets":[…],"wait":true}' \
     localhost:17894/api/v1/builds > matrix_response.json
sha256sum out/*                     # compare with artifacts[].sha256
out/…_linux-amd64 --version         # compare with artifacts[].version
curl -s -o dl.bin -H "Authorization: Bearer $TOK" localhost:17894/api/v1/builds/$ID/download
```
