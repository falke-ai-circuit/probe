# PROBE Phase 4 — Dynamic Mode Switching & Aware Relay

**Status:** Design  
**Date:** 2026-07-24  
**Supersedes:** Phase 4 (Deferred) section in DESIGN.md  

---

## 1. Architecture Overview

### 1.1 Basic topology

```
┌──────────────────────────────────┐
│         PROBE Server (Y)         │
│  serve + WebUI + API + topology  │
└──────────┬───────────────────────┘
           │ WebSocket (direct)
    ┌──────┴──────┐
    │ Direct agents │  (supervisor + connect)
    └─────────────┘
           │
    ┌──────┴──────────────────────┐
    │     PROBE Relay (A)         │  (supervisor + connect + relay)
    │  forwards agents upstream   │
    └──────┬──────────────────────┘
           │ Channel-ID framing
    ┌──────┴──────┐
    │ Relayed agents│  (relay/A/agent-name)
    └─────────────┘
```

### 1.2 Relay chaining (multi-hop)

```
Client Z ──→ Relay A ──→ Relay B ──→ Server Y
                              │
                    Relay A's upstream = Relay B
                    Relay B's upstream = Server Y
                    Server Y sees: relay/B/Z
                    Relay B sees: relay/A/Z (its own relayed agent)
```

Relays accept connections from both agents AND other relays. Detection:
- First message is **binary** with channel-ID framing and `relay_register` → it's a relay
- First message is **text** (JSON protocol.Envelope) → it's a direct agent

This means `handleDownstream` in relay.go must check: if the incoming connection is a relay, treat it as a nested relay session (multiplex its channels through our own upstream). The relay's channel map gains a "nested relay" entry whose channels are sub-channels of the parent relay.

### 1.3 Server-as-relay (Agent X = serve + relay simultaneously)

```
Client Z ──→ Agent X (serve + relay) ──→ Server Y
                │
        X serves Z locally:
        - Z appears in X's local WebUI
        - Z can be operated from X
        
        X also relays Z upstream:
        - Z appears in Server Y as relay/X/Z
        - Server Y can also operate Z

        Selective forwarding:
        - X can keep some clients local-only (not relayed)
        - X can forward some clients upstream
        - Configurable per-client or default: relay all
```

Agent X runs `serve` mode (listening for direct client connections) AND `relay` mode (forwarding to Server Y) simultaneously. X is a local server for downstream clients, AND a relay for Server Y upstream.

**Selective forwarding rules:**
- Default: all connected clients are relayed upstream
- Per-client override: server can tell X via `mode_control` which clients to relay or keep local
- A client relayed upstream appears in BOTH X's local WebUI and Server Y's WebUI (with relay prefix on Y)

### 1.4 Full topology example

```
Server Y (187.124.31.229)
├── vegas-c2022 (direct)
├── relay/relay-B/agent-01 (relayed through B)
├── relay/relay-B/relay/relay-A/client-Z (relayed through B→A chain)
└── relay/relay-X/gorizia-gorl19031 (relayed through X, which is also a server)
    └── Agent X is also a server for local clients
```

**Key principle:** Every PROBE binary starts as a supervisor. The supervisor auto-starts modes based on config. Modes can be started/stopped at runtime via local management API (:9700) or remotely via server WebSocket commands. Any node can run any combination of modes simultaneously — serve + connect + relay on the same binary.

---

## 2. Unified Config Format

Single JSON config file replaces separate serve/connect/relay configs:

```json
{
  "name": "vegas-c2022",
  "token": "shared-secret",

  "server": {
    "addr": ":7700",
    "admin_password": "admin",
    "allowed_cidr": "0.0.0.0/0",
    "rate_limit": 10,
    "rate_burst": 20,
    "max_concurrent": 5,
    "registry_path": "/tmp/probe-registry.json"
  },

  "client": {
    "server": "ws://187.124.31.229:80/ws",
    "token": "agent-token",
    "mode": "silent",
    "permissions": "full"
  },

  "relay": {
    "listen": ":7701",
    "upstream": "ws://187.124.31.229:80/ws",
    "token": "relay-token",
    "agent_tokens": "agent-token-1,agent-token-2"
  }
}
```

**Auto-detection rules (no subcommand):**
- `client.server` present → start supervisor + connect mode
- `client.server` absent, `server` section present → start supervisor + serve mode  
- `client.server` absent, no `server` section → start supervisor + serve mode (default: localhost:7700)
- `relay` section present → also start relay mode on boot
- `relay.upstream` absent but `client.server` present → relay upstream = client.server

**Backward compat:** existing flat config (`{"server":"ws://...","token":"...","name":"..."}`) still works — mapped to `client` section automatically.

---

## 3. Protocol Changes

### 3.1 New message: `mode_control` (server → agent)

Server sends to agent via WebSocket to dynamically start/stop modes:

```json
{
  "type": "mode_control",
  "id": "uuid-12345",
  "action": "start|stop",
  "mode": "serve|connect|relay",
  "config": {
    "listen": ":7701",
    "upstream": "ws://...",
    "token": "...",
    "agent_tokens": "..."
  }
}
```

Agent responds with `mode_control_result`:

```json
{
  "type": "mode_control_result",
  "id": "uuid-12345",
  "result": {
    "mode": "relay",
    "action": "start",
    "status": "running|stopped|error",
    "error": ""
  }
}
```

### 3.2 New message: `mode_status` (agent → server, periodic)

Agent reports its current mode status to server on connect and on change:

```json
{
  "type": "mode_status",
  "id": "uuid-67890",
  "result": {
    "modes": {
      "connect": {"running": true, "uptime": 3600},
      "relay": {"running": true, "uptime": 120, "channels": 3},
      "serve": {"running": false}
    }
  }
}
```

### 3.3 Relay registration metadata (relay → server)

Extended `relay_register` control message with topology info:

```json
{
  "type": "relay_register",
  "relay_id": "relay-abc123",
  "token": "relay-token",
  "metadata": {
    "listen_addr": ":7701",
    "max_agents": 100,
    "agent_count": 3,
    "upstream": "ws://187.124.31.229:80/ws"
  }
}
```

### 3.4 Relay prefix naming

Relayed agents registered as `relay/{relay_id}/{agent_name}`:
- Direct agent: `vegas-c2022`
- Relayed agent: `relay/relay-abc123/vegas-c2022`

The `agent_name` field in AgentInfo stays unchanged. The prefix is applied server-side in `relay_ws.go` when registering the agent in the registry.

### 3.5 Backward compatibility

- Existing flat config files work unchanged
- `probe serve`, `probe connect`, `probe relay` subcommands still work
- Phase 2 relay protocol (channel-ID framing) unchanged
- Old agents without mode_status — server treats as "connect only, no relay capability"

---

## 4. API Changes

### 4.1 Server REST API

| Method | Path | Body | Purpose |
|--------|------|------|---------|
| `POST` | `/api/v1/agents/{id}/mode` | `{"action":"start","mode":"relay","config":{...}}` | Tell agent to start/stop a mode |
| `GET` | `/api/v1/agents/{id}/mode` | — | Get agent's current mode status |
| `GET` | `/api/v1/topology` | — | Get full relay topology tree |
| `POST` | `/api/v1/relays/{id}/drop` | `{"agent":"agent-name"}` | Tell relay to drop specific agent |

### 4.2 Management API (local, unchanged)

| Method | Path | Body | Purpose |
|--------|------|------|---------|
| `GET` | `/api/mgmt/status` | — | List mode statuses |
| `POST` | `/api/mgmt/start` | `{"mode":"relay","config":{...}}` | Start a mode locally |
| `POST` | `/api/mgmt/stop` | `{"mode":"relay"}` | Stop a mode locally |

The management API stays for local control. The new server API endpoints forward mode commands to agents via WebSocket.

---

## 5. Frontend Changes (WebUI)

### 5.1 Agent List — relay prefix display

Agents table in Dashboard.tsx:
- Direct agents: show name as-is (`vegas-c2022`)
- Relayed agents: show `relay/relay-abc/vegas-c2022` with relay name in muted color
- Add a "Type" column: `Direct` / `Relay` / `Server`
- Sort: direct agents first, then relayed (grouped by relay)

### 5.2 Agent Detail — new "Modes" tab

New tab showing mode status with toggle controls:
- **Serve** — toggle on/off (if agent has serve capability)
- **Connect** — toggle on/off, edit server URL
- **Relay** — toggle on/off, edit listen address, upstream, agent tokens
- Status indicators: green (running), gray (stopped), red (error)
- Uptime display for each running mode
- Channel count for relay mode

### 5.3 Topology graph (Dashboard — live network visualization)

Interactive graph on the Dashboard showing real-time connections between servers, relays, and agents. NOT a text tree — a visual node-and-edge graph.

**Visual design:**
- **Nodes**: circles for agents, squares for relays, hexagon for server
- **Colors**: matrix green (#00ff41) for connected/healthy, amber for warning, red for disconnected
- **Edges**: solid lines for direct connections, dashed lines for relayed connections
- **Edge labels**: show connection type (direct, relay, relay-chain)
- **Node labels**: agent name, version, mode icons (🔊=serve, 🔗=connect, 🔄=relay)
- **Animations**: pulsing edge = active data flow, static edge = idle
- **Interaction**: click node → opens AgentDetail, hover → tooltip with IP/uptime/mode info

**Layout:**
```
                    ┌─────────┐
                    │ Server  │  (hexagon, top center)
                    │  v1.9.0 │
                    └────┬────┘
              ┌──────────┼──────────┐
              │          │          │
         ┌────┴───┐ ┌───┴────┐ ┌───┴────┐
         │vegas   │ │gorizia │ │relay-X │  (circle, square)
         │c2022   │ │gorl190 │ │ serve+ │
         │        │ │  31    │ │ relay  │
         └────────┘ └────────┘ └───┬────┘
                                   │ (dashed)
                              ┌────┴────┐
                              │client-Z │  (circle, relayed)
                              └─────────┘
```

**Data source:** `GET /api/v1/topology` returns JSON graph structure:
```json
{
  "nodes": [
    {"id": "server", "type": "server", "name": "187.124.31.229", "version": "v1.9.0"},
    {"id": "vegas-c2022", "type": "agent", "name": "vegas-c2022", "version": "v1.8.5", "modes": ["connect"]},
    {"id": "relay-X", "type": "relay", "name": "relay-X", "modes": ["serve","connect","relay"], "agent_count": 1},
    {"id": "relay/X/client-Z", "type": "agent", "name": "client-Z", "relayed": true}
  ],
  "edges": [
    {"from": "vegas-c2022", "to": "server", "type": "direct"},
    {"from": "relay-X", "to": "server", "type": "relay"},
    {"from": "relay/X/client-Z", "to": "relay-X", "type": "relayed"}
  ]
}
```

**Implementation:**
- Use `react-flow` (or lightweight custom SVG renderer) for the graph
- Auto-refresh every 5s via polling `GET /api/v1/topology`
- Auto-layout: server at top, direct agents in second row, relays branch down
- Drag nodes to reposition, zoom/pan support
- Full screen toggle for large topologies

**Files:** `frontend/src/components/TopologyGraph.tsx`, `frontend/src/pages/Dashboard.tsx`

---

## 6. Implementation Order

### Step 1: Unified config parser (foundation)
- New `ConfigFile` struct in `cmd/probe/main.go` with `server`, `client`, `relay` sections
- Backward compat: flat config → map to `client` section
- Auto-detection logic: if no subcommand, read config, decide modes
- Files: `cmd/probe/main.go`, new `cmd/probe/config.go`

### Step 2: Default supervisor startup
- `probe` with no args and no subcommand → `probe supervisor --config probe.json`
- If config has `client.server` → supervisor starts connect mode
- If no `client.server` → supervisor starts serve mode
- If `relay` section → supervisor also starts relay mode
- Files: `cmd/probe/main.go`, `internal/modes/manager.go`

### Step 3: Relay prefix naming
- In `internal/server/relay_ws.go`: when registering relayed agent, prefix name with `relay/{relay_id}/`
- Update registry to handle prefixed names
- Files: `internal/server/relay_ws.go`

### Step 4: Relay metadata in registration
- Extend `relay_register` control message with `metadata` field
- Server stores relay metadata in a `relays` map
- Files: `internal/relay/relay.go`, `internal/server/relay_ws.go`

### Step 5: Agent mode_status reporting
- Agent sends `mode_status` on connect and on mode change
- Agent's mode manager emits status change events
- Server stores mode status per agent
- Files: `internal/modes/manager.go`, `internal/modes/connect_mode.go`, `internal/agent/agent.go`, `internal/server/server_ws.go`

### Step 6: Server → agent mode_control
- New protocol message type in `internal/protocol/protocol.go`
- Agent handles `mode_control` message, calls internal mode manager
- Agent sends `mode_control_result` back
- Files: `internal/protocol/protocol.go`, `internal/agent/agent.go`, `internal/modes/manager.go`

### Step 7: Server REST API for mode control
- `POST /api/v1/agents/{id}/mode` — sends `mode_control` to agent via WebSocket
- `GET /api/v1/agents/{id}/mode` — returns cached mode status
- `GET /api/v1/topology` — builds topology tree from relay sessions + agent registry
- `POST /api/v1/relays/{id}/drop` — sends `channel_close` to relay for specific agent
- Files: `internal/server/api_v1.go`

### Step 8: WebUI — Modes tab
- New "Modes" tab in AgentDetail.tsx
- Toggle buttons for serve/connect/relay
- Config editing for relay (listen, upstream, tokens)
- Real-time status updates (poll or WebSocket)
- Files: `frontend/src/pages/AgentDetail.tsx`, new `frontend/src/components/ModesTab.tsx`

### Step 9: WebUI — Topology view
- Tree visualization on Dashboard or new Topology page
- Expandable relay nodes showing relayed agents
- Files: `frontend/src/pages/Dashboard.tsx`, new `frontend/src/components/TopologyTree.tsx`

### Step 10: Client-as-relay
- ConnectMode + RelayMode running simultaneously in same supervisor
- Relay upstream = connect server URL (if not specified separately)
- Relay listens on :7701, agents connect to relay, relay forwards to upstream server
- Files: `internal/modes/manager.go`, `cmd/probe/main.go`

### Step 11: Server-as-relay (serve + relay simultaneously)
- Agent runs `serve` mode (local server) AND `relay` mode (forward upstream) at the same time
- Clients connecting to Agent X's server appear in X's local WebUI AND forwarded upstream
- Selective forwarding: default relay-all, per-client override via `mode_control` from upstream server
- New control message `forward_policy`: `{"type":"forward_policy","agent":"client-z","action":"relay|local"}`
- Files: `internal/modes/manager.go`, `internal/relay/relay.go`, `internal/server/relay_ws.go`, `cmd/probe/main.go`

### Step 12: Relay chaining (multi-hop relays)
- Relays accept connections from other relays (not just agents)
- Detection in `handleDownstream`: first message binary + `relay_register` → nested relay session
- Nested relay's channels are sub-multiplexed through parent relay's upstream channel
- Path accumulation: `relay/B/relay/A/agent-name` for 2-hop chain
- Relay's channel map gains `nestedRelays` map for sub-relay sessions
- Files: `internal/relay/relay.go`, `internal/relay/mux.go`, `internal/server/relay_ws.go`

### Step 13: E2E encryption (optional, stretch)
- AES-GCM encrypted payloads between agent and server
- Relay sees encrypted bytes, can't read
- Key exchange via token-derived shared secret
- Files: new `internal/crypto/e2e.go`, `internal/agent/agent.go`, `internal/server/server_ws.go`

### Step 14: Relay failover (optional, stretch)
- Agent config supports multiple relay addresses
- Agent tries relays in order, falls back on failure
- Files: `internal/agent/agent.go`, `cmd/probe/config.go`

---

## 7. Security Considerations

1. **Mode control authentication** — only authenticated server operators can send `mode_control`. The server API endpoint requires operator auth. The WebSocket message is sent by the server, not by other agents.

2. **Relay token separation** — relay uses its own token for upstream auth. Agent tokens at relay are validated locally. No token pass-through.

3. **E2E encryption (Step 11)** — without E2E, relay can see all traffic. Document risk explicitly. E2E eliminates relay MITM.

4. **Config file sensitivity** — unified config may contain server, client, and relay tokens. Protect with file permissions (0600). Consider env var overrides for tokens.

5. **Relay remote management** — server can tell relay to drop an agent. This is a management action, not a security control. Dropped agents can reconnect unless blocked at relay token level.

6. **Topology information leakage** — `GET /api/v1/topology` exposes full network topology. Restrict to authenticated operators only.

7. **Client-as-relay exposure** — a client running relay exposes a listening port (:7701). This may be blocked by host firewall. Document port requirements.

---

## 8. What Stays from Phase 2 (Backward Compat)

| Feature | Phase 2 | Phase 4 |
|---------|---------|---------|
| Channel-ID framing | `[magic][chanID BE][payload]` | Unchanged |
| Relay registration | `relay_register` control message | Extended with `metadata` field |
| Agent info on first frame | Agent sends AgentInfo as first data frame | Unchanged |
| Relay reconnection | Exponential backoff, channel re-registration | Unchanged |
| `serve`/`connect`/`relay` subcommands | Standalone mode | Still work, but default is supervisor |
| Flat config format | `{"server":"...","token":"...","name":"..."}` | Still works, mapped to `client` section |
| Management API (:9700) | Local mode start/stop | Unchanged, also available remotely |
| WebSocket race condition fix | `if s.conns[agentID] == conn` before delete | Unchanged |
| CREATE_BREAKAWAY_FROM_JOB | Update mechanism | Unchanged |
| Pure Go native Win32 | Zero PowerShell | Unchanged |

---

## 9. Dependencies

No new external dependencies. All features implemented with:
- `github.com/gorilla/websocket` — WebSocket transport
- `encoding/json` — protocol messages
- `encoding/binary` — channel-ID framing
- `sync` — mode manager concurrency
- `net/http` — management API + REST API

---

## 10. Testing Plan

1. **Unit tests** — config parser, mode manager, relay prefix naming
2. **Local integration** — supervisor starts serve + connect + relay in one process, agent connects through relay
3. **Remote mode switch** — send `mode_control` via API, verify relay starts on agent
4. **Relay prefix** — relayed agent shows as `relay/relay-id/agent-name` in API + WebUI
5. **Client-as-relay** — agent connects to server, then relay mode enabled, second agent connects through first agent's relay
6. **Backward compat** — old flat config works, old `probe connect` subcommand works
7. **WebUI** — Modes tab toggles work, topology tree renders correctly