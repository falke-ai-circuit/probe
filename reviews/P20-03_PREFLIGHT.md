# P20-03 — ROLLOUT PREFLIGHT (READ-ONLY): can a Radxa deploy take EAS1 down?

Unit: P20-03 · operative · 2026-11-25 · window 10:33–11:00 UTC
Scope: read-only. No container was pulled, created, recreated, restarted or stopped. No file was written on
any probe host. EAS1 was not touched beyond reading central registry/log state (its client file is unreadable —
see Q2). Mutations performed by me on any host: NONE.

Vantage points actually used (all read-only):
- Radxa host `radxa-e54c` 100.71.4.28 (tailnet; LAN 192.168.18.245), uid 1000 radxa, docker group — ssh.
- Agent netns of `probe-relay` (172.17.0.3) via central `/exec` (TCP probes into the docker bridge).
- Central server `operator-op01-probe-1` on KS-3 CT100 (`pct exec 100 -- docker …`) — logs + registry + builds.
- Radxa container inspect/logs via `docker inspect`/`docker logs` as the radxa user.

---

## Q1 — RELAY LOCATION: it is a CONTAINER, and NOT the probe-radxa container

`:7800` is **not** a host process and **not** a process inside `probe-radxa`. It is its own container,
`probe-relayd`, on the default docker bridge.

Host `ps` (Radxa, as radxa):

```
1384512 1384492 root  /app/probe relay -listen 0.0.0.0:7800 \
                      -upstream ws://139.99.209.235:7701/ws -token <redacted> -relay-id radxa-relay
1384469    1478 root  /usr/sbin/docker-proxy -proto tcp -host-ip 0.0.0.0 -host-port 7800 \
                      -container-ip 172.17.0.4 -container-port 7800
1384477    1478 root  /usr/sbin/docker-proxy -proto tcp -host-ip :: -host-port 7800 \
                      -container-ip 172.17.0.4 -container-port 7800
```

- relay process pid **1384512**, ppid **1384492** (containerd-shim; i.e. the container's init), NOT the host's pid 1.
- host-side listener = docker-proxy pids **1384469/1384477**, ppid **1478** = dockerd. That is the published
  port, not a host daemon.
- `docker inspect probe-relayd`: ip **172.17.0.4**, restart **unless-stopped**, image `probe:arm64-local`,
  started **2026-11-24T22:39:50Z**, Cmd `relay -listen 0.0.0.0:7800 -upstream ws://139.99.209.235:7701/ws -relay-id radxa-relay`.
- `docker inspect probe-radxa`: ip **172.17.0.2**, publishes only **7701**, started **2026-11-25T08:58:27Z**.

Cross-check from a different netns (probe-relay container, 172.17.0.3, via `/exec`):
`172.17.0.4:7800 OPEN`, `172.17.0.2:7701 OPEN`, host gateway `172.17.0.1:7800 OPEN` (published mapping).
The listener exists in 172.17.0.4's netns → it is container-resident, published to the host.

**Plainly:** recreating `probe-radxa` does NOT take EAS1's path down. Recreating **`probe-relayd` would**.
This is already proven empirically on this host: `probe-radxa` was recreated today at **08:58:27Z** and
`probe-relayd` has been up since **22:39:50Z the day before** — the relay survived a server recreate untouched.

Relay log (`docker logs probe-relayd`), unbroken across that window:
```
2026/11/24 22:39:51 [relay] connected to upstream ws://139.99.209.235:7701/ws (id=radxa-relay)
2026/11/24 22:39:51 [relay] listening on 0.0.0.0:7800, magic=0xC2, max-agents=100
2026/11/25 02:30:17 [relay] channel 3 opened
2026/11/25 10:09:14 [relay] channel 3 closed (agent from 172.17.0.1)
```

---

## Q2 — EAS1 RECONNECT: live config confirmed, and the spec's reading of maxRetries:0 was WRONG

**Live file**, Radxa agent `probe-relay` container, `/config/probe-client.json` (read-only bind from the host):

```json
{"server":"ws://139.99.209.235:7701/ws","token":"<redacted>","name":"radxa-e54c",
 "mode":"interactive","maxRetries":0,"backoffMin":"1s","backoffMax":"60s"}
```

CONFIRMED verbatim against the running container: `maxRetries 0`, `backoffMin 1s`, `backoffMax 60s`,
`mode interactive` — spec §4 was accurate about the values.

**CORRECTION — the meaning was wrong.** In this codebase `maxRetries: 0` means **INFINITE retries**, not
"no retry / no self-heal":

```
internal/agent/agent.go:142   MaxRetries int // 0 = infinite retries
internal/agent/agent.go:368   if a.cfg.MaxRetries > 0 && a.backoffAttempt > a.cfg.MaxRetries { return err }
```

The reconnect loop retries forever at 0, with exponential backoff + jitter between `BackoffMin` and
`BackoffMax` (1s→60s), and resets `backoffAttempt` to 0 on every successful connect. A broken **path**
therefore self-heals on its own; what does not heal is a dead **process** (the loop lives inside it).

**EAS1's own client file: NOT readable, and that is a live finding, not a tooling gap.**
- `V6049A-EAS1` `/exec` → `AGENT_UNAVAILABLE … not connected` on every one of ~10 attempts 10:42–10:47Z.
- Central transport log: last `agent connected: V6049A-EAS1` **10:36:19**, `agent disconnected` **10:36:35**
  (`websocket: close 1006`), **no later connect line** in a 3000-line tail — while the registry still reports
  `status: active`, `health_score 1`, `last_heartbeat` advancing to 10:47:49. Registry is stale; `/exec` wins.
- `eas1-via-radxa` (the client that arrives **through the Radxa relay**) is `inactive`, last heartbeat
  **10:09:14Z**, matching the relay's `channel 3 closed` at 10:09:14. So **the EAS1 relay path is currently
  carrying nothing.**

What can still be pinned about EAS1's client, from live server state:
- mode `outbound`, version 1.18.5, windows/amd64 (registry).
- its build records (`V6049A-EAS1` ×3, latest 2026-11-24T15:55:48Z) carry **no `max_retries` field**; the
  builder only writes that key when non-zero (`internal/server/builder.go:399`,
  `builder_test.go:122` "maxRetries should be absent when 0") → the embedded config is the default **0 =
  infinite retry**. `autostart: false` — there is no evidence the client is installed as a service.

Access still needed to close this datum from the live file: a shell on EAS1 (10.10.10.1 is the Proxmox host,
not EAS1) **or** its agent back online, then read the client's `--config` JSON.

---

## Q3 — if maxRetries is 0: no change is needed; bounding it would make things worse

`0` already gives unbounded, backoff-paced retry — the strongest self-healing setting available. The
smallest change that would give *bounded* retry, **if the owner wanted one**, is a config value (`maxRetries`
> 0) on disk: the reconnect loop reads the value into memory at start and never re-reads the file, so such an
edit takes effect only at the next process start and does not disturb the running client.

**Recommendation: do not set it.** Bounding retries converts a recoverable path break into a permanent one,
which is exactly the failure mode the owner is protecting against.

The deploy consequence is therefore a sequencing rule, not a config rule:
**the path never breaks because the relay container is never recreated in the demo window.**

---

## Q4 — BLAST RADIUS: one host, and EAS1 is untouched by construction

Deploy target is the Radxa only. Image `probe:arm64-local` = `sha256:db51a9b33d54508c0cc945d0e8dbec0ce9ae25f9f7d0c6d9b39a2a7412643717`,
arm64/linux, built 2026-11-24T22:30:26Z. Containers using it:

| Container | Net | Ports | Carries today |
|---|---|---|---|
| `probe-radxa` | 172.17.0.2 | 0.0.0.0:7701 | central server for the plant; registry, builder, operator DB, profiles |
| `probe-relayd` | 172.17.0.4 | 0.0.0.0:7800 | **relay `radxa-relay` → the EAS1 path** (upstream ws://139.99.209.235:7701/ws) |
| `probe-relay` | 172.17.0.3 | — | agent **radxa-e54c** → central (my `/exec` vantage) |
| `probe-relay443` | 172.17.0.6 | 0.0.0.0:7801 | relay `radxa-relay-443` → central :443/ws |
| `probe-selfagent` | host net | — | agent `radxa-self-probe` → ws://127.0.0.1:7701 |
| `probe-dl` | 172.17.0.5 | 0.0.0.0:8099 | nginx file drop — **not** the probe image |

Not touched by any Radxa deploy: **EAS1** (no exec, no file write, no restart — its client keeps its own
socket), KS-3/CT100 `operator-op01-probe-1` (only sees the relay's upstream socket re-dial), Gorizia/Gotham,
Hetzner, and every other fleet gateway. The Radxa's own `nop-*`, Dashy, guacd, kali stacks are separate
images and are not recreated.

---

## VERDICT: GO — scoped, with the relay container excluded from the recreate set

Gates that must hold at deploy time (any one failing = stop, do not continue):

1. **Never recreate/restart `probe-relayd` in the same window as the rest.** It is the EAS1 path. If the new
   image must land on it, that is a separate owner-approved window (and note: any `eas1-via-radxa` client
   attached will drop and must re-dial itself; it will, only while its process is alive).
2. `probe-radxa` may be recreated — proven safe today at 08:58:27Z while the relay stayed up.
3. Take a rollback tag before the first recreate: `docker tag probe:arm64-local probe:arm64-prev`
   (sha256:db51a9b3…). Rollback = recreate the container from `probe:arm64-prev` with the exact Cmd recorded
   above per container.

Required sequence:
1. Baseline: `docker inspect` all five, `curl localhost:7701/api/v1/health`, central `/api/v1/agents` row list
   (expect `radxa-e54c` active, `radxa-self-probe` active, both advancing heartbeats).
2. Recreate **`probe-radxa`** only. Verify: health 200 `{status:ok}`, `/data/runtime/registry.json` still
   present and non-empty, agents `radxa-self-probe` + `radxa-e54c` re-registered within 60s.
3. Recreate **`probe-relay`** and **`probe-relay443`**. Verify from central: `agent connected: radxa-e54c`,
   `relay connected: id=radxa-relay-443`, then one `/exec` round-trip (stdout + exit_code 0) on `radxa-e54c`
   and `docker logs probe-relay443 | grep "connected to upstream"`.
4. **Stop here.** `probe-relayd` keeps running the old image and EAS1's path is untouched.
5. Rollback (any step): stop+rm the recreated container, re-run from `probe:arm64-prev` with the recorded Cmd,
   re-verify the same points.

Open datum (does not gate the plan above): EAS1's own `maxRetries`/`mode` from its **live file** — needs an
EAS1 shell or its agent online. It does not gate because the approved sequence never breaks that path, and the
build records already show the embedded value is the 0 = infinite default.

reviewed_by: orchestrator (parent) · verdict: GO (scoped; probe-relayd out of scope)
