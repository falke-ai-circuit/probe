# P11-05 — ROLL OUT THE PINNED COMPOSE TO CT100

unit: P11-05
owner: operative=rollout
host: KS-3 139.99.209.235 (Proxmox) → CT100 falke-stack 10.10.10.100 (via `pct exec 100`)
access path: ssh root@139.99.209.235 -i /opt/data/.ssh/hostinger_key → `pct exec 100` (hermes uid 10000 has no docker.sock)
compose source: /opt/data/projects/PROBE_UPGRADE/v119wt@cda0a0a (branch r119-build)
deployed file: /docker/operator-op01/docker-compose.yml sha256 191e30de64a8b242c8082b89f2c7a8635e280d39bc17534af2acb90034245bc1
window: 2026-11-24T20:41Z → 2026-11-24T20:46Z

## 0. What the live stack actually was (measured before touching it)

| Claim in the brief | Measured pre-swap | Evidence |
|---|---|---|
| "tag-only image, no digest pin" | TRUE | `/docker/operator-op01/docker-compose.yml` (pre) line `image: ghcr.io/falke-ai-circuit/probe:70de7fa…` — no `@sha256` |
| "no healthcheck" | TRUE | `docker inspect operator-op01-probe-1 --format '{{json .Config.Healthcheck}}'` → `null` |
| "no builder flags" | **FALSE** | pre-swap compose already carried `--builder-go /usr/local/go/bin/go` and `--builder-source /src/cmd/probe-client/`; the running container's `Config.Cmd` carried both; and the stack had already completed a build at 2026-11-24T15:56Z (`build-a1d2a4ce…`, `status":"complete"`) on that same image |

So the swap's real delta is: digest pin, healthcheck, `/data` named volume, network block in-file, env block — not the builder flags. The builder flags were already live before this unit; §5 still proves them end-to-end because acceptance asks for it.

Pre-swap container: `operator-op01-probe-1`, image `sha256:466b2fc2…d5504`, `Up (no health state)`, nets `falke-net 172.25.0.15 (alias probe)` + `operator-op01_operator-net 172.24.0.2`.

## 1. Rollback artefacts (captured BEFORE any change, read back)

All under `/opt/data/tmp/probe119/rollback/`:

| artefact | path | size | sha256 |
|---|---|---|---|
| compose project dir (file + all prior .bak) | `operator-op01-compose-dir-pre-P11-05.tar.gz` | 1,155 B | `dd602b124b40409bed5cb31acf2a52ae2f92a9ceeb9543e14c86bd4c55f6e431` |
| full `docker inspect` | `container-inspect-pre-P11-05.json` | 14,653 B | `7f16ef3450d0aed8255339a9d0f98945c38cd34f7138f5b50fcfff6fa06827c2` |
| state: `runtime/` (65 MB incl. 7 build binaries) + `logs/falke/probe/` (69 MB) | `probe-state-pre-P11-05.tar.gz` | 93,294,128 B | `141a98e1637a28b4fe71d9ed7c0978a82cbc1909842bfc3855a0e8f91da2d1e8` |

Read-back (not a checksum of the archive — a checksum of the bytes inside it vs the live bytes):

```
tar xzOf operator-op01-compose-dir-pre-P11-05.tar.gz ./docker-compose.yml | sha256sum
  70f701b14f7ef2ee025857e46b76639e9a565d237a8fbfc3f4c9ff0c716543ab
sha256sum /docker/operator-op01/docker-compose.yml   (on CT100, pre-swap)
  70f701b14f7ef2ee025857e46b76639e9a565d237a8fbfc3f4c9ff0c716543ab   ← identical

tar xzf probe-state… -C rb runtime/builds.json runtime/ca/ca.crt ; sha256sum
  e8b6a09f7a55d13909075ce32a7c9a184ae5711aadef6aa17aba6ffd80151644  rb/runtime/builds.json
  bd3d3108b839951aa35f17c8a7b966066d2b6fe21f76fc33b79b6b2e958db9fa  rb/runtime/ca/ca.crt
CT100 live:
  e8b6a09f…1644  /docker/…/runtime/builds.json      ← identical
  bd3d3108…b9fa  /docker/…/runtime/ca/ca.crt        ← identical

`runtime/registry.json` inside the archive: cd2aa33c…bc70 vs live 7f3f97df…f90d — the registry is rewritten on every agent heartbeat (mtime 20:40Z, agents re-registering during the tar). Content check on the archived copy: valid JSON, 24 keys, same 24 agent names as live.
```

`SHA256SUMS` for the three archives is in the same directory.

Rollback procedure if needed (one command, image named by digest, nothing pulled):

```
cd /docker/operator-op01 && \
  cp -a docker-compose.yml.bak-pre-P11-05-20261124T2044Z docker-compose.yml && \
  docker compose up -d --remove-orphans
```

The `.bak-pre-P11-05-20261124T2044Z` file is the exact pre-swap compose (`70f701b1…43ab`) and is still on the host — rollback does not depend on the tarball. The stack runs on `sha256:466b2fc2…d5504` in both states, so a rollback involves no image change and no data move.

## 2. Network reconciliation — decision and why

The repo compose has no network block at all; the live container sits on `falke-net` at `172.25.0.15` with the DNS alias `probe`, plus the project bridge. Dropping `falke-net` would have detached it from every peer that resolves `probe`/`172.25.0.15` (the Proxmox DNAT for 80/443/7701 and the probe relay path both land there — 172.25.0.15 is the documented probe POV address).

Choice: **corrected host-side file**, not an overlay `docker-compose.override.yml`. Reasons: (a) one authoritative file means `docker compose config` prints exactly what runs, with no merge layer to hide a delta; (b) the project has no override file today, so adding one creates a second source of truth for a single-service stack; (c) it makes the deployed file diffable against `cda0a0a` in one place (§3).

Networks in the deployed file:

```
networks:
  operator-net: {driver: bridge}
  falke-net: {external: true, name: falke-net}
service probe networks:
  operator-net
  falke-net: {aliases: [probe]}
```

Post-swap measured: `falke-net 172.25.0.15` (same IP as before), `operator-net 172.24.0.2` (same), aliases `["operator-op01-probe-1","probe","probe"]` — identical to pre-swap.

## 3. Deltas from `cda0a0a` (every one stated, because a rollout report is not a file copy)

| # | repo `cda0a0a` | deployed | why |
|---|---|---|---|
| 1 | no `name:` | `name: operator-op01` | keeps project identity; without it the project would be `operator-op01` only by directory luck |
| 2 | `container_name: falke-probe` | `container_name: operator-op01-probe-1` | renaming the unit would break host-side scripts/history keyed on the live name |
| 3 | `test: ["CMD","sh","-c","curl -sf …"]` | `test: ["CMD-SHELL","bash -c '… /dev/tcp …'"]` | **measured**: the pinned image has no `curl`, no `wget`, no `nc`, no `busybox` (`command -v` empty for all four). The repo's test would have marked a healthy server unhealthy on first check. `bash` is present; `/dev/tcp` performs the same HTTP probe (`HTTP/1.0 200 OK` verified inside the container before the swap) |
| 4 | no network block / no service `networks` | `operator-net` + external `falke-net` with alias `probe` | §2 |
| 5 | `probe-data:/data` (fresh named volume) | same declaration, **pre-seeded** from the live anonymous volume `6c38619b…91561` (24 MB: `/data/files/{.artifacts.json, build-a1d2a4ce…exe, hostname, pc_win_1191.exe, pc_win_3aee718.exe}`) | declaring the named volume without seeding would have silently emptied `/data/files`; no data dir was moved, the bytes were copied and verified (`du -sh /to` = 24M, `ls -la /to/files` 7 entries) |
| 6 | `--blacklist-cidr ${PROBE_BLACKLIST_CIDR:-}` | present (value `""`) | repo command line; empty value = no blacklist, same behaviour as the pre-swap absence |

Nothing else differs: image line, all other flags, bind mounts (`runtime`, `logs` host paths identical), admin password, token, allowed CIDRs, token TTL are byte-for-byte the repo file.

## 4. Post-swap verification (deployed stack's own numbers)

```
docker compose config  → CONFIG_RC=0
  image: ghcr.io/falke-ai-circuit/probe:70de7fa03f879e0cdc48dc9048a5d25c69121211@sha256:466b2fc23e3349ba136a0a34867b44b48d24d9bd08ea7121fe324e24765d5504

docker ps → operator-op01-probe-1 | ghcr.io/.../probe:70de7fa… | Up 2 minutes (healthy) | 0.0.0.0:7701->7701/tcp
docker inspect .Image                 → sha256:466b2fc23e3349ba136a0a34867b44b48d24d9bd08ea7121fe324e24765d5504
docker inspect .State.Health          → {"Status":"healthy","FailingStreak":0,"Log":[{"ExitCode":0,…}]}

GET /api/v1/health → {"ok":true,"data":{"active_agents":3,"stale_agents":2,"server_version":"v1.19.1","status":"ok","total_agents":24,"uptime_seconds":130}}
registry.json keys → 24            (24 == total_agents ✓)
```

The pinned digest was already present on the host before the swap (`docker images --digests` shows `70de7fa… / sha256:466b2fc2…d5504`, 25 h old); nothing was pulled.

Healthcheck evidence: the container reached `healthy` with `FailingStreak: 0` under the `/dev/tcp` test — i.e. delta #3 works as written, not just as reasoned.

## 5. Builder proof — a real build through the DEPLOYED server

Request (from CT100 root shell, against `127.0.0.1:7701`, bearer = operator token from `operators.json`):

```
POST /api/v1/builds
{"name":"P11-05-live","os":"linux","arch":"amd64","server_url":"ws://139.99.209.235:7701/ws",
 "token":"…","capabilities":["exec","filesystem","process"],"permissions":"full","wait":true}
→ HTTP=201 t=0.0015s  {"id":"build-2ba500fe-d95c-4992-a486-8936195af958","status":"pending",…}
```

Settled by polling `GET /api/v1/builds/build-2ba500fe-…`:

```
t+0s    pending
t+5..20 building
t+22s   {"status":"complete","binary_path":"/data/runtime/builds/build-2ba500fe-d95c-4992-a486-8936195af958_P11-05-live",
         "created_at":"2026-11-24T20:45:18Z","completed_at":"2026-11-24T20:45:40Z"}
```

Artifact metadata read off the produced bytes (host path of the bind mount):

```
/docker/agent-core/data/workspace-operative/probe/runtime/builds/build-2ba500fe-…_P11-05-live
  size 10,980,786   mode -rwxr-xr-x   mtime 2026-11-24 20:45
  sha256 b7d67a6085cbe304e9076a1dbe2a4d9f946ba6731866c65718faad7a5e2fde87
  magic  7f 45 4c 46                      (ELF)
  ./artifact --version → 1.19.1           (matches /api/v1/health server_version v1.19.1)
```

So the deployed server compiled a client from `/src/cmd/probe-client/` with `/usr/local/go/bin/go` — the builder flags are live and functional on the rolled-out stack.

### 5a. What this build could NOT produce, and why (blocking finding)

The acceptance item asks for "artifacts and their metadata". On this pin there is no `artifacts[]`:

- `wait:true` was **ignored** (201 returned in 1.5 ms with `status:"pending"`);
- `targets:[{"os":"linux","arch":"amd64"}]` was **rejected** — `HTTP=400 {"code":"INVALID_PARAMS","message":"invalid os \"\": must be windows, linux, or darwin"}` (the field is unknown to the handler; only the v1.18.x `os`/`arch` pair is accepted);
- the completed record carries `binary_path` but no `artifacts[]`, no `sha256`, no `size`, no `matrix_version`.

Cause, proven against the repo rather than inferred: the pin is `70de7fa` ("release: bump Semver 1.19.0 -> 1.19.1"). `git ls-tree 70de7fa internal/server/` has **no `build_matrix.go`**, and `git show 70de7fa:internal/server/builder.go` contains **no `Targets []BuildTarget`, `Artifacts []BuildArtifact`, `MatrixVersion`**. The v2 build surface (P11-01) is not in the pinned commit — it exists only in HEAD, which is exactly what P11-04 proved against. The same holds for every published image in the host's cache (`8a44426`, `54ed531`, `9d2be63`: `build_matrix.go` absent in all three), so no currently-available pin satisfies that acceptance clause.

Interpretation: the compose at `cda0a0a` is internally consistent and deploys cleanly, but its image pin predates the API the acceptance item describes. Rolling out the compose did not and could not change the server's API surface — the image is the API. This is not a rollout failure; it is a gap in what `cda0a0a` pins, and it needs a decision the unit cannot make for itself (publish an image from ≥ P11-01 and bump the pin, which is a different unit).

## 6. Acceptance ledger

| item | state | evidence |
|---|---|---|
| rollback artefacts named with paths + read-back proving usable | MET | §1 (three artefacts, inner-bytes hashes match live; one documented registry delta from live rewrites) |
| `docker compose config` exit 0 on host, pinned image line quoted | MET | §4 `CONFIG_RC=0`, image line with tag+digest |
| post-swap `/api/v1/health` version + agent counts + registry key count | MET | §4 `v1.19.1`, `total_agents 24`, registry keys `24` |
| one build through the deployed server with artifacts + their metadata | **PARTIAL** | §5: real build, real binary, sha256/size/magic/version measured on disk — but the response carries no `artifacts[]` metadata because the pinned image predates that API (§5a). Provenance-proven, not a harness error |
| container healthy, on its networks, same digest | MET | §4 (`healthy`, `FailingStreak 0`), §2 (172.25.0.15 / 172.24.0.2 unchanged) |
| no agent-facing behaviour changed beyond the compose | MET with disclosure | §3 deltas: identity, healthcheck mechanism, network block, `/data` volume seeded, `--blacklist-cidr ""`. Token scheme, auth, CIDRs, data dirs untouched |

## 7. Open item (for reviewer, not for this unit)

Pin decision: `cda0a0a` pins `70de7fa` — an image whose builder has no v2 API (`targets[]`, `wait`, `artifacts[]`). Either (a) accept the v1.18.x build surface on CT100 and scope the P11 acceptance to "a compile happens", or (b) publish an image from a commit ≥ P11-01 and bump the pin in a follow-up unit. Recommend (b); (a) makes the P11 contract unverifiable on the deployed stack.

## 8. Evidence files

```
/opt/data/tmp/probe119/rollback/            pre-swap archives + SHA256SUMS
/opt/data/tmp/probe119/rollback/rb/         read-back extraction (builds.json, ca.crt, registry.json)
/opt/data/tmp/probe119/deployed-compose-20261124T2043Z.yml   sha256 191e30de…45bc1  (what is on CT100)
/opt/data/tmp/probe119/deployed-config-resolved.yml          `docker compose config` output
/opt/data/tmp/probe119/build-create-response.json            the 201 from the deployed server
/opt/data/tmp/probe119/container-inspect-post-P11-05.json    post-swap inspect
```

---
reviewed_by: reviewer
verdict: NEEDS-REVIEW
verdict_sha_of_head: cda0a0a
