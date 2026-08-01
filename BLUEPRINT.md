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
