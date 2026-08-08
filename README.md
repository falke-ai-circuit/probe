# PROBE

Remote agent for the Hermes ecosystem. Run Hermes natively on any remote machine using the main server's LLM infrastructure.

**Version:** v1.13.0
**Repo:** `github.com/falke-ai-circuit/probe`
**Go:** 1.22 (Go 1.23/1.25 crash on Valmet VM — pinned to 1.22)
**Dependencies:** `gorilla/websocket` only (zero other external deps)

## Flow Runtime (v1.13.0+)

PROBE includes a server-side **flow runtime** for orchestrating scheduled or triggered surveys. A flow is a DAG of steps that call existing agent commands, branch, transform data, and emit survey events.

### What flows are for

- **Surveys** — periodic snapshots of host state (processes, network connections, file inventory)
- **Sensors** — continuous monitoring of specific conditions (sensitive file access, logins, config changes)
- **Audit enrichment** — pre-process command output before the existing audit log records it
- **Cross-host correlation** — same flow definition can run on multiple agents; events are centralized

### Quick example

```bash
# List built-in templates
curl -H "Authorization: Bearer $TOKEN" http://server:7701/api/v1/flow-templates

# Instantiate one
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"template_name":"network_summary"}' \
  http://server:7701/api/v1/flows/from-template

# Assign to an agent
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"agent_id":"vegas-c2022"}' \
  http://server:7701/api/v1/flows/{flow_id}/assign
```

### Built-in flow templates

| Template | Trigger | What it does |
|---|---|---|
| `network_summary` | recurring 5min | Snapshots `NetConnections`, emits event with connection list |
| `sensitive_file_access` | recurring 5min | Scans user profile dirs for SSH keys, AWS creds, wallet files |
| `file_watch_summary` | recurring 1h | File inventory of user profile dirs with size totals |

Templates are JSON files in `internal/server/flowtemplates/`. Add a new template by dropping a `.json` file in that directory; the server picks it up on restart.

### Flow step types

- `command` — forward an existing agent command (NetConnections, FileSearch, Sysinfo, …) and store the result in flow state
- `wait` — pause for N seconds (cancellable via flow context)
- `branch` — conditional next-step routing on a state value
- `compute_diff` — diff two prior step results (added/removed/changed)
- `classify` — apply glob rules to label a state value
- `emit` — write a survey event to the NDJSON event log

### Storage layout

```
/data/runtime/flows.json           # flow definitions (persisted)
/data/runtime/flowtemplates/      # template .json files
/data/logs/flows.json.events.ndjson  # survey events (append-only)
```

All three paths are configurable via `--flows-path` (single arg; the other two are derived).

### WebUI

The `Flows` sidebar entry shows a CRUD page with templates, runs, and enable/disable. The per-agent `Survey` tab shows a timeline of survey events for that host with filtering and auto-refresh.

See [CHANGELOG.md](CHANGELOG.md) for the full v1.13.0 release notes.

## Quick Start

```bash
# Build
make build

# Start server (on main Hermes host)
./cmd/server/server --addr :7700 --token "hermes.circuit.remote.2026"

# Create a config file on the remote machine (probe-client.json):
cat > probe-client.json << 'EOF'
{
  "server": "ws://your-server:7700",
  "token": "your-auth-token",
  "name": "my-computer",
  "mode": "silent",
  "maxRetries": 0,
  "backoffMin": "1s",
  "backoffMax": "60s"
}
EOF

# Run the agent with the config file
./cmd/probe-client/probe-client --config probe-client.json

# Or use the default config path (probe-client.json in the current directory)
./cmd/probe-client/probe-client
```

## Usage

```
PROBE Client v1.9.4
A remote assistant tool for the Hermes AI ecosystem

Usage:
  ProbeClient.exe [--config probe-client.json]
```

All connection settings are read from a JSON config file. Run with `--help` to see
all available config fields and an example config.

### Config File Fields

- **server** — WebSocket server URL (`ws://` or `wss://`)
- **token** — Authentication token
- **name** — Display name for this agent (default: `probe-client`)
- **mode** — `silent` (daemon) or `interactive` (CLI prompt) (default: `silent`)
- **listen** — Address for inbound connections (e.g. `:7700`)
- **maxRetries** — Max reconnect attempts; `0` = infinite (default: `0`)
- **backoffMin** — Min reconnect backoff (default: `1s`)
- **backoffMax** — Max reconnect backoff (default: `60s`)
- **tokenFile** — Path to persist rotated token (default: `.probe-token`)
- **cert** — CA certificate (PEM) for verifying server TLS on `wss://`
- **clientCert** — Client certificate (PEM) for mTLS
- **clientKey** — Client key (PEM) for mTLS
- **certFile** — TLS certificate (PEM) for inbound server mode
- **keyFile** — TLS key (PEM) for inbound server mode

## WebUI

The embedded React WebUI (Vite + TypeScript) provides a full management interface:

**Sidebar navigation:** Dashboard, Agents, Tasks, Transfers, Credentials, Builder, Profiles, Settings

**Pages:**
- **Dashboard** — agent overview, health status, quick actions
- **Agents** — agent list with search, capabilities toggle, redeploy; Agent Detail with breadcrumb navigation (Agents > [Name] > [Tab]) and tabs: Terminal, Files, Processes, Tunnels, MITM, Debug, Screen, Audit
- **Tasks** — scheduled task management (delayed, recurring, offline queue)
- **Transfers** — global file transfer view across all agents with progress bars, status badges, pause/resume, filter by status
- **Credentials** — scan agents for passwords, hashes, API keys, tokens, connection strings, AWS keys, private keys via regex; manual text paste scanner; OS-specific gather commands (Windows/Linux/macOS)
- **Builder** — 5-step agent build wizard with capability checkboxes (tooltips on all 9 capabilities), cross-compilation, VirusTotal scan integration
- **Profiles** — build profile management
- **Settings** — server configuration

**Builder capability tooltips:** all 9 capability checkboxes have descriptive `title` attributes explaining what each capability does.

## Two Modes

- **Silent** — `--mode silent` in config. Daemon controlled from the main server via operative profile tools.
- **Interactive** — `--mode interactive` in config. Full Hermes CLI session. LLM runs on server, tools run on remote.

## Operative Tools

Once the plugin is installed, the operative profile gets 5 new tools:

- **`remote_agent_list`** — List all connected agents with health
- **`remote_shell`** — Execute shell command on agent
- **`remote_fs_read`** — Read file from agent filesystem
- **`remote_fs_write`** — Write file to agent filesystem
- **`remote_screenshot`** — Capture screen from agent

## Architecture

```
Server (LLM proxy + session manager + relay + builder) ← WebSocket → Agent (runs on remote)
```

Remote machines never get API keys. LLM inference happens on the server. Tools (terminal, file, screen, input, tunnel, MITM, debug) execute locally on the remote machine.

### Packages

- `cmd/probe` — Unified binary (serve + connect + relay, always available, no build tags)
- `cmd/probe-client` — Legacy standalone client binary
- `cmd/probe-server` — Legacy standalone server binary
- `internal/agent` — Agent loop, 68-case command dispatch, MITM, tunnel, process control, debugger, failover, self-update, platform-specific disk/sysproc (19 files)
- `internal/crypto` — E2E encryption (ChaCha20-Poly1305)
- `internal/modes` — Connection mode manager (connect, relay, server, dual)
- `internal/platform` — 18-method platform interface, 3 OS implementations (Linux, Windows, macOS)
- `internal/protocol` — 108 message type constants, binary framing, WebSocket transport
- `internal/relay` — Relay multiplexer and relay handler (723+211 LOC)
- `internal/server` — REST API server (42 files, 87 routes), agent registry, LLM proxy, session manager, tunnel, MITM, builder, VirusTotal, enrollment, audit, tasks, file transfers, operator auth, profiles, capabilities
- `internal/testutil` — Test utilities (mock server, mock platform, mock agent)

### API Surface

- **v1 API:** 79 routes under `/api/v1/` — agents, operators, builds, tasks, transfers, profiles, topology, enrollment, health, audit
- **Legacy API:** 8 routes — `/ws`, `/health`, `/api/agents`, `/api/agent/`, `/download/`, `/logreport/`, `/openapi.json`
- **Agent commands:** 68 dispatch cases — shell exec, filesystem ops, screen capture, input simulation, tunnel, MITM, process control, debug, clipboard, health, tasks, streaming, auth refresh, port forwarding, port scan, SOCKS5, key press/combo, notifications
- **Protocol:** 108 message type constants, ProtocolVersion field (v1/v2 backward compatible)

## Build

```bash
make build          # Build both binaries
make cross          # All platforms (linux/amd64, linux/arm64, windows/amd64, darwin/amd64, darwin/arm64)
make windows        # Windows exe only (with version info, stripped symbols)
make vet            # Run go vet
make test           # Run tests
```

### Unified Binary (v1.6.0+)

The single `cmd/probe` source tree compiles into one binary with all three modes always available. No build tags are needed.

```bash
# Build (Linux/macOS)
go build -trimpath -o probe ./cmd/probe/

# Cross-compile for Windows
GOOS=windows GOARCH=amd64 go build -trimpath -o probe.exe ./cmd/probe/
```

All subcommands are always available: `probe serve`, `probe connect`, `probe relay`.

**Trade-off**: The unified binary includes server and relay code in every build, increasing the reverse-engineering surface compared to the v1.5.0 build-tag approach. This trade-off is accepted for operational simplicity — a single binary for all deployments. Obfuscation/evasion code was removed (commit 87fecdf) — MANTLE handles deployment-time obfuscation now.

## License

MIT