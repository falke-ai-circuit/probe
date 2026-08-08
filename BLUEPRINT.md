# BLUEPRINT — PROBE v0.1.0-a0

> **⚠ SUPERSEDED (2026-07-28):** This blueprint is a design-time artifact from v0.1.0-a0. The codebase is now at v1.9.4. File structure, command count, protocol types, and server architecture have all diverged significantly. See **README.md** and **KNOWLEDGE.md** (in project workspace at `/opt/data/projects/probe/`) for the current source of truth. This file is retained for historical reference only.

**Author:** Architect (via Orchestrator)
**Date:** 2026-06-13
**Last Updated:** 2026-06-16
**Status:** ACTIVE — Phase A-D complete, Phase E COMPLETE (Commit 7/7: reconnect + Windows + macOS + rate limiting + health monitoring + token rotation + TLS mutual auth). Phase F pending.
**Repo:** `github.com/falke-ai-circuit/probe`
**Branch:** `main`
**Tag:** `v0.1.0-a0`

---

## 1. Problem

The operator agent needs to control remote machines — desktops, laptops, servers, phones. The existing falke-remote relay (CT101:7700) is a Windows-only, central-relay, HTTP REST system with no shell access. We need a **single binary** that runs Hermes natively on any remote machine, using the main server's LLM infrastructure.

## 2. Architecture Decision

**PROBE binary = Hermes agent running on remote machine, with LLM calls routed through the main server.** No API keys on the remote. No SSH tunnels. No raw shell relay. Just Hermes, running wherever you put it.

```
┌─────────────────────────────────────────┐
│  MAIN SERVER                            │
│  ┌───────────────────────────────┐      │
│  │ PROBE server :7700    │      │
│  │ (WebSocket relay + LLM proxy  │      │
│  │  + session manager)           │      │
│  └───────────────────────────────┘      │
└──────────────────┬──────────────────────┘
        │              │              │
   ┌────▼─────┐  ┌───▼──────┐  ┌───▼──────┐
   │ Silent    │  │ Silent   │  │ Interactive│
   │ daemon    │  │ daemon   │  │ CLI prompt │
   └───────────┘  └──────────┘  └───────────┘
```

## 3. Two Modes

| Mode | Command | Behavior |
|------|---------|----------|
| **Silent** | `--mode silent` | Daemon in background. Visible as local instance to server. Controlled via operative profile. |
| **Interactive** | `--mode interactive` | Full Hermes CLI session. Real prompt, tools, memory. LLM runs on server. Tools on remote. |
| **Dual** | `--listen :7700` | Both modes simultaneously — daemon + can accept inbound connections. |

## 4. Protocol

- **Transport:** WebSocket (RFC 6455) over TLS 1.3
- **Auth:** Token in header `Authorization: Bearer ***`
- **Messages:** JSON envelope with `{id, type, params, result, error}`
- **Commands:** 25 commands across 5 categories (shell, filesystem, screen, input, system)
- **Heartbeat:** 15s ping/pong, 3-miss disconnect threshold

## 5. Server Components

| Component | Purpose |
|-----------|---------|
| **LLM Proxy** | Routes LLM calls to providers (DeepSeek, MiniMax, Ollama) using server's API keys |
| **Session Manager** | Creates one Hermes session per connected agent — memory, skills, context |
| **Agent Registry** | Persisted JSON registry of all connected agents with health |
| **WS Relay** | WebSocket server on :7700, handles connect/auth/message routing |

## 6. Agent Components

| Component | Purpose |
|-----------|---------|
| **Agent Loop** | Full Hermes agent loop (system prompt → LLM call → tool dispatch → response) |
| **Platform Adapters** | Linux (native), Windows (PowerShell), macOS (osascript/screencapture) — for platform-specific tools |
| **Protocol Client** | WebSocket dial, ping/pong, message serialization |

## 7. Plugin Integration

Operative profile gets new tools via `kind: standalone` Hermes plugin:
- `remote_agent_list` — list all connected agents
- `remote_shell` — execute command on agent
- `remote_fs_read` — read file from agent
- `remote_fs_write` — write file to agent
- `remote_screenshot` — capture screen from agent

## 8. File Structure

```
probe/
├── cmd/
│   ├── probe-client/
│   │   └── main.go              # CLI flags, mode selection
│   └── server/
│       └── main.go              # Server entry point
├── internal/
│   ├── protocol/
│   │   ├── messages.go          # All message types
│   │   ├── websocket.go         # Dial/Listen/Upgrade
│   │   ├── binary.go            # Binary frame encoding
│   │   └── server.go            # Server wrapper
│   ├── server/
│   │   ├── server.go            # Multi-session WS server
│   │   ├── registry.go          # Agent registry (persisted JSON)
│   │   ├── proxy.go             # LLM proxy to providers
│   │   └── session.go           # Per-agent Hermes session
│   ├── agent/
│   │   └── agent.go             # Agent loop + command dispatch
│   └── platform/
│       ├── platform.go          # Platform interface
│       ├── platform_linux.go    # Linux implementation (bash, xdotool, import/scrot, xclip)
│       ├── platform_windows.go  # Windows implementation (PowerShell: System.Drawing, SendKeys, user32.dll)
│       └── platform_darwin.go   # macOS implementation (screencapture, osascript, pbpaste/pbcopy, open, ps)
├── tool/
│   ├── plugin.py                # Hermes plugin registration
│   └── plugin.yaml              # Plugin manifest
├── .github/
│   ├── workflows/
│   │   ├── build.yml            # CI: go vet + build + test
│   │   └── release.yml          # goreleaser on tag
│   └── agents/
│       ├── ANALYST.md
│       ├── ARCHITECT.md
│       ├── CODER.md
│       ├── REVIEWER.md
│       └── OPERATIVE.md
├── AGENTS.md                    # Agent delegation rules
├── CLAUDE.md                    # Project overview + build/run instructions
├── project_knowledge.json       # Hot cache + architecture map + gotchas
├── BLUEPRINT.md                 # This document
├── ROADMAP.md                   # Phase overview + timeline
├── CHANGELOG.md                 # Release history
├── CONTRIBUTING.md              # PR process + conventions
├── README.md                    # Project overview
├── LICENSE                      # MIT
├── Makefile                     # Build, test, cross-compile
├── go.mod
├── go.sum
└── .gitignore
```

## 9. Phase Status

| Phase | Scope | Status |
|-------|-------|--------|
| **A** | Scaffold — protocol, server, agent, platform, CLI, plugin | ✅ Complete |
| **B** | Fixes — 8 bugs across 6 files, Go agent connects, all 4 endpoints verified | ✅ Complete |
| **C** | Plugin — 5 remote_* tools registered, tested | ✅ Complete |
| **D** | Integration test on remote host (Kali Linux) | ✅ Complete — 7/7 PASS |
| **E** | Production hardening — TLS mutual auth, token rotation, reconnect, rate limiting, health monitoring | ✅ Complete — Commit 7/7 (reconnect + Windows + macOS + rate limiting + health monitoring + token rotation + TLS mutual auth) |
| **F** | Final review + v1.0.0 release | ⏳ Pending |

## 10. Success Criteria

| # | Criterion | Evidence | Status |
|---|-----------|----------|--------|
| 1 | Binary compiles for linux/amd64 | `go build ./cmd/...` exits 0 | ✅ |
| 2 | Server starts on :7700 with TLS | `./server --addr :7700` accepts connections | ✅ |
| 3 | Agent connects in silent mode | `./probe-client --connect wss://localhost:7700 --mode silent` registers | ✅ |
| 4 | Agent connects in interactive mode | `./probe-client --connect wss://localhost:7700 --mode interactive` opens CLI | ✅ |
| 5 | Operative tools work | `remote_agent_list` shows connected agents | ✅ |
| 6 | Remote shell works | `remote_shell agent="a0-test" command="echo hello"` returns `hello` | ✅ |
| 7 | Kali Linux test | Binary compiled and deployed, connects from Kali container to server | ✅ — 7/7 endpoints PASS |
| 8 | Multi-agent | 3 agents connected simultaneously, all visible in registry | ⏳ |
| 9 | Cross-compile | `make cross` builds for all 5 targets | ✅ |
| 10 | CI passes | `go build ./... && go vet ./... && go test ./...` | ✅ |

## 11. Closure Criteria

```
ALL phases A-F complete
ALL 10 success criteria PASS
Git tag v1.0.0
GitHub release with binary artifacts for all platforms
Plugin deployed to operative profile
Evolution entry in orchestrator evolution.jsonl
CLOSURE_REQUEST sent to FalkeCondBot
```


---

## Flow Runtime Design (v1.13.0)

**Author:** Architect
**Date:** 2026-08-08
**Status:** IMPLEMENTED — Part A complete, deployed to OVH, verified end-to-end.

### Goals

1. **Server-side workflow orchestration** — flows are defined, stored, and executed by the server, not the agent. The agent remains a stateless command executor.
2. **Reuse existing commands** — flows call existing agent commands (NetConnections, FileSearch, Sysinfo, …) via the existing `forwardToAgent` path. No new agent-side code for the common case.
3. **Reuse existing scheduler** — the server's `TaskManager` already runs recurring commands. Flows piggyback on it for the `command` step type; the dispatcher just calls `TaskManager.Create` internally for any `command` step.
4. **Decoupled from agent lifecycle** — flows can target any connected agent, queued and dispatched when the agent comes online. No agent-side state for the framework.
5. **Operator-first** — every flow has an operator ID at creation time. Every action (create, update, enable, run-now, assign) is written to the audit log.

### Architecture

```
┌──────────────────┐
│  REST API        │  /api/v1/flows, /api/v1/flow-runs, /api/v1/flow-templates,
│  (api_v1.go)     │  /api/v1/agents/{id}/flows, /api/v1/agents/{id}/survey
└────────┬─────────┘
         │
┌────────▼─────────┐    ┌──────────────────┐
│  FlowManager     │◄──►│  TemplateManager │  loads flowtemplates/*.json
│  (flows.go)      │    │  (flow_templates.go)│
│  CRUD + persist  │    └──────────────────┘
└────────┬─────────┘
         │            ┌──────────────────┐
┌────────▼─────────┐   │  NDEventStore     │  append-only NDJSON
│  FlowDispatcher  │──►│  (flows_events.go)│  single writer goroutine
│  (flows_dispatcher.go)│└──────────────────┘  drop-on-overflow
└────────┬─────────┘
         │ uses
┌────────▼─────────┐
│  TaskManager     │  already in the codebase; dispatcher.Create + await
│  (existing)     │
└──────────────────┘
```

### Step types and their semantics

| Step | Code path | State mutation | Notes |
|---|---|---|---|
| `command` | `forwardToAgent(agentID, type, params)` | stores result under `as` / `store_as` | Reuses TaskManager. Returns the result of the command. If agent is offline, run stays `pending` until reconnect. |
| `wait` | `time.After(seconds)` with `ctx.Done()` select | none | Cancellable via flow context (5-min default timeout). |
| `branch` | `evalCondition("{{state.x}} == value")` | none | Supports `==`, `!=`, `contains`, `starts_with`. Returns next step ID. |
| `compute_diff` | `computeDiff(left, right)` via JSON unmarshal | stores diff under `diff_as` | Recursive for maps and slices; returns `{added, removed, changed}` for scalars. |
| `classify` | `globToRegex(rule.If).MatchString(input)` | stores `{input, label}` under `classify_as` | First matching rule wins. Empty label if no match. |
| `emit` | `flowEvents.Append(&FlowEvent{...})` | none | Writes to NDJSON. Non-blocking; drops on full buffer. |

### Flow definition schema (JSON)

```json
{
  "name": "network_summary",
  "description": "Periodic snapshot of network connections",
  "trigger": { "type": "recurring", "interval_seconds": 300 },
  "steps": [
    { "id": "s1", "type": "command", "command_type": "net_connections", "store_as": "snapshot" },
    { "id": "s2", "type": "compute_diff", "left": "baseline", "right": "snapshot", "diff_as": "delta" },
    { "id": "s3", "type": "emit", "signal": "network_summary", "payload": "{{state.snapshot}}" }
  ]
}
```

State is a `map[string]json.RawMessage`. Steps reference prior step results by the key they were stored under.

### Server integration

- `cmd/probe/serve.go` parses `--flows-path` and calls `srv.SetFlowsPath()`.
- `internal/server/server.go: SetFlowsPath` initializes FlowManager, FlowDispatcher, NDEventStore, TemplateManager in one shot.
- `api_v1.go: registerV1Routes` adds 15 new flow routes (CRUD + templates + survey).

### What was deliberately NOT done

- **No agent-side state** for the flow framework. All state lives on the server.
- **No per-flow worker pool**. One goroutine per active run, cancelled when done or on 5-min timeout.
- **No FalkorDB graph queries** for flow state — that's deferred. NDJSON is enough for v1.13.0.
- **No real-time push** of survey events. Frontend polls every 5s. WebSocket push is on the roadmap.
- **No flow versioning** — flows are mutable. Use git for history.

### Open questions

- **Q1:** Should flow runs be retried on transient failure? (Currently: no. Run status is `failed` after one try.)
- **Q2:** Should `flow.from_template` audit-log the template name? (Yes, currently it does.)
- **Q3:** Survey event retention? (Currently: unbounded. Plan: logrotate config + `--flows-store-max-size` flag.)

### Compatibility

- v1.12.x agents (e.g., gorproxmox) connect to v1.13.0 server without changes.
- v1.13.0 server treats unknown message types from old agents as `ErrUnknownCommand` (existing behavior).
- Frontend can be rolled back to v1.12.0 builds; new routes return 404 gracefully.


---

## Sensor Subsystem Design (v1.14.0)

**Author:** Architect
**Date:** 2026-08-08
**Status:** IMPLEMENTED — Part B complete, deployed to OVH.

### Goals

1. **OS-independent** — same binary works on Windows, Linux, macOS, Android.
2. **Stdlib-only** — zero new Go deps, only `runtime`, `os`, `net`, `time`, `encoding/binary`, `crypto/sha256`.
3. **Stateless** — sensors don't keep state between calls. The `schedStart` map on the server tracks *which flows have fired*, not sensor state.
4. **Composable with flows** — sensors can be referenced from flow steps as `{type:"command", command_type:"sensor_read"}`. The dispatcher routes the call to the agent's `SensorRegistry`.
5. **Read-only** — sensors never mutate the host. (Writes go through the existing `fs-write` command.)

### Architecture

```
┌──────────────────┐
│  Agent           │
│  ┌────────────┐  │
│  │ Registry   │◄─┼─── init() registers all sensors
│  └────────────┘  │
│        ▲         │
│        │ Read()  │
│  ┌────────────┐  │   ┌──────────────────┐
│  │ handleSensor│  │   │  Built-in        │
│  │   Read     │  │   │  sensors (15)    │
│  └────────────┘  │   │  - process       │
└──────────────────┘   │  - filesystem    │
                       │  - network       │
┌──────────────────┐   │  - time          │
│  Server          │   │  - agent         │
│  GET  .../sensors │   └──────────────────┘
│  POST .../enable │
│  POST .../disable│
└──────────────────┘
```

### Sensor catalog (16 sensors)

| Name | Category | Stdlib package(s) |
|---|---|---|
| `process_detail` | process | `os`, `runtime` |
| `runtime_metrics` | process | `runtime` |
| `memory_stats` | process | `runtime` (ReadMemStats) |
| `disk_usage` | filesystem | `os`, `syscall` (Unix) / `os` (Windows) |
| `file_stat` | filesystem | `os` |
| `env_vars` | filesystem | `os` |
| `network_interfaces` | network | `net` |
| `dns_resolve` | network | `net` |
| `dns_resolve_mx` | network | `net` |
| `dns_resolve_txt` | network | `net` |
| `network_dial` | network | `net` |
| `system_time` | time | `time` |
| `uptime` | time | `time` |
| `ntp_drift` | time | `net`, `encoding/binary`, `time` |
| `agent_metrics` | agent | `sync/atomic`, `time` |
| `audit_chain` | agent | `crypto/sha256`, `time` |

### Compatibility

- v1.14.0 server + v1.12.x agent = works. Server doesn't send `TypeSensorRead` if no sensor is enabled for that agent.
- v1.13.0 server + v1.14.0 agent = works. Agent doesn't error if it sees no `TypeSensorRead` (server doesn't request).

### What was deliberately NOT done

- **No persistent sensors.** Each call is independent.
- **No subscription / push model.** Sensors are read-only, polled. WebSocket push is on the v1.15.0 roadmap.
- **No per-sensor auth.** The whole agent auth chain (Bearer token + RBAC) is the gate.
- **No sensor versioning.** Breaking a sensor's payload shape requires a server-version bump.
- **No platform-specific sensors.** All 16 use only stdlib that works on Windows/Linux/macOS/Android. Disk usage is the only OS-split (via build constraints in helpers, not in sensor impl).
