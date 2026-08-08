# AGENTS.md — PROBE (Agent Delegation Rules)

> This file is referenced by agent profiles when working with this repo.

## Repo Conventions

### Build
```bash
make build          # Build all binaries (PROBE + server)
make test           # Run tests (if available)
make vet            # Run go vet
make cross          # Cross-compile for all platforms (linux/amd64, linux/arm64, windows/amd64, darwin/amd64, darwin/arm64)
```

### Commit Style
- Prefix: `feat:`, `fix:`, `refactor:`, `docs:`, `test:`, `chore:`
- Tag: annotated with release notes (`git tag -a v0.1.0-a0 -m "..."`)
- Push: `git push origin main --tags`

### Forbidden
- Force-push to main without explicit approval
- Breaking protocol changes without version bump
- Hardcoded secrets in source (use env vars)
- Direct edits to `go.mod`/`go.sum` (use `go get`/`go mod tidy`)
- New dependencies without justification in PR description

### Review Gates
1. `go build ./...` exits 0
2. `go vet ./...` passes
3. `go test ./...` passes (if tests exist)
4. Binary runs with `--help` cleanly
5. Integration test: server starts, agent connects, tool roundtrip

### R-LIVE (mandatory for server/agent changes)
- Start server binary on test port
- Start agent binary, verify connection
- Test all 5 remote tools via plugin
- Auto-re-loop on FAIL with exact failure evidence

### Creative Integration Testing
- For any deliverable talking to an external system, build a misbehaving mock and test with real I/O
- Mock server: wrong TLS cert, slow responses, connection drops
- Mock agent: invalid tokens, wrong protocol version, malformed messages

### Agent Briefs
See `.github/agents/` for per-agent task templates:
- `ANALYST.md` — Codebase analysis
- `ARCHITECT.md` — System design
- `CODER.md` — Implementation
- `REVIEWER.md` — Quality gate
- `OPERATIVE.md` — Deployment + troubleshooting


## Flows (v1.13.0+)

PROBE has a server-side flow runtime. When working with flow code:

- **Where it lives**: `internal/server/flows*.go`. NOT `internal/sensors/` (that was an early design name that was rejected).
- **The dispatcher uses TaskManager** for `command` steps. Don't write a parallel scheduler.
- **NDJSON event store** is a single writer goroutine. Don't bypass with direct file writes.
- **Templates are loaded at startup** from `internal/server/flowtemplates/`. To add a template: drop a JSON file and restart the server.
- **Server-flow integration test**: a flow's `command` step calls `forwardToAgent`. Test by assigning the flow to a connected agent and verifying the event lands in the survey log.

### Common mistakes (do NOT do)

- Creating `internal/sensors/` package — use the manager-on-Agent pattern, NOT a new package
- Using `strings.Contains` to check CIDR — use `net.ParseCIDR`
- Returning a `*Channel` from a handler — the `mux` expects `func(http.ResponseWriter, *http.Request)`
- Forgetting to call `s.audit.Log` on flow CRUD operations — every change is auditable
- Hardcoding the version in flow steps — use the JSON schema, never strings


## Sensors (v1.14.0+)

PROBE ships 16 generic sensors. When working with sensor code:

- **Where it lives**: `internal/agent/sensors_*.go` + `internal/agent/agent_sensor.go`.
- **No `internal/sensors/` package** — files are flat in `internal/agent/` matching the existing pattern.
- **Adding a sensor**: implement the `Sensor` interface (Name/Category/Description/Read), then add the type to the slice in `sensors_register.go`'s init().
- **Stdlib only**. No `golang.org/x/sys`, no `github.com/shirou/gopsutil`, no `github.com/shirou/gopsutil/v3`. Use `runtime`, `os`, `net`, `time`, `encoding/binary`, `crypto/sha256`.
- **OS-independent**: same code runs on Windows, Linux, macOS, Android. The only exception is `disk_usage` and `file_stat` which use build tags to pick `syscall.Statfs` (Unix) vs `os.DiskUsage` (Windows). Do NOT add platform-specific sensors.
- **Read-only**. Never mutate the host. Writes go through the `fs-write` command.
- **Stateless**. No state between calls.

### Common mistakes (do NOT do)

- Importing `github.com/shirou/gopsutil` — that violates the zero-deps rule
- Using `/proc/...` directly — Linux-only, violates OS-independence
- Caching results in a struct field — sensors are stateless
- Using `os/exec` — sensor implementations should never shell out
- Adding a `Init()` method to the `Sensor` interface — registration is via package init() in sensors_register.go

### Verification

Run `go test -race ./internal/agent/ -run TestSensors` to confirm all 16 sensors still register and metadata is intact.
