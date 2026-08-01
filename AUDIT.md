# PROBE — Comprehensive Code Audit

**Date:** 2026-07-29  
**Auditor:** Reviewer (subagent)  
**Repo:** `/opt/data/workspace-operative/probe` @ `415881f` (v1.9.4)  
**Scope:** Security, code quality, architecture, protocol, platform, binaries, build, frontend, tests, docs

---

## Executive Summary

PROBE is a well-structured remote agent tool with 93 Go files, 26 TS/TSX frontend files, 87 API routes, 68 agent command dispatch cases, and 108 protocol message types. The codebase builds cleanly, has no hardcoded secrets, uses proper crypto for token generation, and implements TLS/mTLS/rate-limiting/IP-filtering. However, there are **critical security gaps** (no request body limits, CORS wide-open, `InsecureSkipVerify` fallback, timing-unsafe token comparison), **1 vet error**, **1 failing test**, **13 tracked/present binary artifacts bloatening the repo**, and **several stale/contradictory docs**.

---

## 1. Security Audit

### 1.1 Authentication (Token-Based) — MOSTLY GOOD, TWO ISSUES

**✅ Good:**
- `server_token.go:119-141`: `isValidToken` checks primary, extra, and rotated tokens
- `server_ws.go:17-24`: WebSocket endpoint rejects unauthorized connections with 401
- `operator.go:44`: Token field has `json:"-"` tag — never serialized in API responses
- `operator.go:52-68`: Passwords are bcrypt-hashed (`bcrypt.GenerateFromPassword`, `bcrypt.CompareHashAndPassword`)
- `server_token.go:20-26`: Token generation uses `crypto/rand` with fallback
- `enrollment.go:157-163`: Enrollment tokens use `crypto/rand`

**🔴 CRITICAL — Timing-unsafe token comparison:**
- `server_token.go:124`: `authHeader == "Bearer "+s.token` — uses Go string `==` which short-circuits on first byte mismatch. Vulnerable to timing side-channel attacks.
- `server_token.go:129`: `authHeader == "Bearer "+t` in loop — same issue
- `server_token.go:137`: `authHeader == "Bearer "+rt` — same issue
- `server.go:617`: `token == authHeader || token == ""` — same issue
- `agent.go:275`: `token != "Bearer "+a.cfg.Token` — same issue (agent-side)
- **Fix:** Use `crypto/subtle.ConstantTimeCompare` or `hmac.Equal` for all token comparisons.

**🟡 MODERATE — Auth optional by default:**
- `server_token.go:174-181`: When `requireAPIAuth` is false (default), unauthenticated API requests are allowed through with only a log warning. This means the HTTP API (exec, fs-read, fs-write, proc-kill, etc.) is **open by default** unless the operator explicitly passes `--require-api-auth`.
- **Recommendation:** Default to `requireAPIAuth = true`; require explicit opt-out.

### 1.2 TLS — GOOD WITH ONE GAP

**✅ Good:**
- `server.go:428`: `MinVersion: tls.VersionTLS13` — enforces TLS 1.3 minimum
- `server.go:431-442`: mTLS with `RequireAndVerifyClientCert` when clientCA configured
- `websocket.go:43`: Client dialer also enforces TLS 1.3
- `websocket.go:60-66`: Client certificate support for mTLS

**🔴 MODERATE — `InsecureSkipVerify` fallback:**
- `websocket.go:57`: `dialer.TLSClientConfig.InsecureSkipVerify = true` — when no CA cert is provided, the agent skips server certificate verification entirely. This makes the agent vulnerable to MITM attacks.
- **Recommendation:** Require a CA cert for wss:// connections, or at minimum log a prominent warning.

### 1.3 Rate Limiting — PRESENT BUT LIMITED

**✅ Good:**
- `server.go:22-26`: `RateLimitConfig` struct with rate, burst, max concurrent
- `proxy.go`: LLM proxy has rate limiter
- `relay.go:39-41,52-56`: Relay has per-IP connection limits (`MaxPerIP`, `MaxAgents`)

**🟡 GAP — No rate limiting on HTTP API endpoints:**
- Rate limiting is only applied to the LLM proxy, not to the HTTP API routes (`/api/v1/agents/{id}/exec`, `/api/v1/agents/{id}/fs-write`, etc.). An authenticated user can hammer these endpoints without throttling.
- **Recommendation:** Apply rate limiting middleware to all `/api/v1/` routes.

### 1.4 Input Validation — PARTIAL

**✅ Good:**
- `server_download.go:54`: Path traversal check: `strings.Contains(filename, "..")` and `strings.Contains(filename, "/")`
- `server_download.go:74`: Same check for body-based download
- `agent.go:563-568`: Exec timeout clamped to `maxTimeout` (300s)
- `api_v1.go:196-211`: v1Forward reads body with error handling

**🔴 CRITICAL — No request body size limits:**
- 20+ instances of `io.ReadAll(r.Body)` with no `MaxBytesReader` or `io.LimitReader`:
  - `server_api.go:171,235,261,297,323,348,397,422,620` (9 instances)
  - `tunnel.go:204,253,335,396` (4 instances)
  - `mitm.go:16,41,66` (3 instances)
  - `debug.go:16` (1 instance)
  - `api_v1.go:203` (1 instance)
- 25 instances of `json.NewDecoder(r.Body).Decode()` with no body size limit
- **Impact:** An attacker can send a multi-GB request body to exhaust server memory (DoS).
- **Fix:** Wrap `r.Body` with `http.MaxBytesReader(w, r.Body, <limit>)` in a middleware or per-handler.

**🟡 MINOR — Exec command injection is by design:**
- `agent_proc.go:93`: `exec.Command("sh", "-c", params.Command)` — the agent executes arbitrary shell commands. This is the intended behavior (remote shell), but the server-side API has no input validation on the command string. Any authenticated user can run any command.

### 1.5 CORS — WIDE OPEN

**🔴 MODERATE:**
- `websocket.go:21`: `CheckOrigin: func(r *http.Request) bool { return true }`
- `server_ws.go:29`: Same
- `relay.go:46`: Same
- All three WebSocket upgraders accept connections from any origin. This enables CSRF-style attacks where a malicious website initiates a WebSocket connection to a local PROBE server.
- **Fix:** Configure `CheckOrigin` to validate against a whitelist of allowed origins.

### 1.6 Hardcoded Secrets — CLEAN

**✅ Good:**
- Zero hardcoded tokens, passwords, or API keys found in source code
- All secrets are passed via CLI flags, environment variables, or config files
- `virustotal.go:32-37`: VT API key is passed at runtime, not hardcoded

### 1.7 File Permissions on Persisted State — WEAK

**🟡 MODERATE:**
- `operator.go:318`: `os.WriteFile(om.savePath, data, 0644)` — operator file (contains **bcrypt hashes** and **plaintext API tokens**) is world-readable
- `enrollment.go:136`: `os.WriteFile(em.savePath, data, 0644)` — enrollment tokens (plaintext) world-readable
- `registry.go:334`: `os.WriteFile(r.savePath, data, 0644)` — agent registry world-readable
- `tasks.go:365`: Same pattern
- `builder.go:521`: Same pattern
- `profiles.go:177`: Same pattern
- `filetransfer.go:527`: Same pattern
- `audit.go:74`: Audit log `0644`
- **Only `enrollment.go:345`** uses `0600` for the CA private key (correct)
- **Fix:** Use `0600` for all files containing tokens, hashes, or sensitive data.

---

## 2. Code Quality

### 2.1 Go Vet — 1 ERROR

```
internal/relay/relay.go:60:2: self-assignment of cfg.RelayID to cfg.RelayID
```
- `relay.go:60`: `cfg.RelayID = cfg.RelayID` — no-op self-assignment. The intent was likely to keep the value if already set, but this line does nothing. The `New()` function receives `cfg` by value, so this assignment has no effect on the caller's copy.
- **Fix:** Remove the line or implement actual logic (e.g., generate a random ID if empty).

### 2.2 Go Test — 1 FAILING TEST

```
FAIL: TestCLI_NoArgs_PrintsUsage (cmd/probe/main_test.go:78)
  expected 'connect' in usage, got: "PROBE v1.9.4 — no config file found, starting as server (default)..."
  Management API error: listen tcp :9700: bind: address already in use
```
- The test expects the CLI to print usage when run with no args, but the binary starts in server mode by default (new behavior). The test is stale — it tests the old behavior.
- Secondary issue: `:9700` port conflict — another process (or previous test instance) is holding the management API port.
- **Fix:** Update the test to expect the new default-server-mode behavior, or use a random port.

### 2.3 TODO/FIXME — CLEAN

**✅ Good:** Zero `TODO`, `FIXME`, `HACK`, `XXX`, or `BUG` comments in non-test source code.

### 2.4 panic() in non-test — CLEAN

**✅ Good:** Zero `panic()` calls in non-test source code.

### 2.5 os.Exit() in non-main — CLEAN

**✅ Good:** Zero `os.Exit()` calls outside `cmd/` packages.

### 2.6 Swallowed Errors — 1 INSTANCE

- `platform_windows.go:602`: `ret, _, _ = procProcess32Next.Call(...)` — error from Windows API call is discarded. This is a common pattern for Windows syscall wrappers, but the error should at least be logged.
- `server_ws.go:238`: `_ = json.Unmarshal(env.Result, &result)` — token rotation result unmarshal error silently ignored. Acceptable since the result is informational.

### 2.7 Structural Issue in server_token.go

- `server_token.go:82-84`: Extra closing braces — the `runTokenRotation` function has mismatched indentation suggesting a brace was misplaced or a block was left empty. The code compiles (Go is forgiving with braces), but the structure is confusing:
```go
            }      // line 80 — closes if
            }      // line 81 — closes for
            }      // line 82 — closes select case
                       // line 83 — empty
                       // line 84 — empty
```
- Lines 83-84 have trailing whitespace/empty lines that suggest a missing block. The function works but is hard to read.

---

## 3. Architecture

### 3.1 Package Map

| Package | Files | Purpose |
|---------|-------|---------|
| `.` (embed.go) | 1 | Embedded web UI assets |
| `cmd/probe` | 8 (5 src + 3 test) | Unified binary (serve/connect/relay) |
| `cmd/probe-client` | 1 | Legacy standalone client |
| `cmd/probe-server` | 1 | Legacy standalone server |
| `internal/agent` | 19 (17 src + 2 test) | Agent runtime: command handlers, platform dispatch |
| `internal/crypto` | 2 (1 src + 1 test) | E2E encryption (AES-GCM) |
| `internal/modes` | 4 | Runtime mode manager (serve/connect/relay) |
| `internal/platform` | 4 | Platform abstraction (Linux/Darwin/Windows) |
| `internal/protocol` | 5 (4 src + 1 test) | Message types, binary framing, WebSocket dial/listen |
| `internal/relay` | 3 (2 src + 1 test) | Relay bridge (agent ↔ upstream server) |
| `internal/server` | 42 (31 src + 11 test) | HTTP API, WebSocket server, registry, builder, tasks, transfers |
| `internal/testutil` | 3 | Test helpers (mock server, mock agent, mock platform) |

### 3.2 Circular Dependencies — NONE

**✅ Good:** `go build ./...` succeeds with no import cycle errors. The dependency graph is clean and unidirectional: `cmd/` → `internal/modes` → `internal/server` / `internal/agent` → `internal/protocol` / `internal/platform` / `internal/crypto`.

### 3.3 68-Case Command Dispatch (agent.go:432-551)

- `internal/agent/agent.go:432-551`: Single `switch env.Type` with 68 cases dispatching to handler methods.
- **Assessment:** The switch is large but well-organized — each case is a one-liner calling a dedicated handler method (`handleExec`, `handleFSList`, etc.). This is the standard Go pattern for message dispatch. The alternative (a map of type → handler func) would add indirection without benefit.
- **Minor issue:** The permission check at lines 397-430 extracts `execCmd` and `path` by re-parsing params, which is then re-parsed in each handler. Double parsing is wasteful but not a bug.

### 3.4 87 API Routes (api_v1.go:241-399 + server_api.go + server.go:361-383)

- **Legacy routes** (`server.go:361-383`): 6 routes on `/api/agents`, `/api/agent/`, `/download/`, `/api/download/`, `/logreport/`, `/health`
- **v1 routes** (`api_v1.go:241-399`): 81 routes under `/api/v1/` using Go 1.22 `ServeMux` method+path patterns
- **Assessment:** Good separation — v1 routes use proper RESTful patterns with method constraints. Legacy routes kept for backward compatibility. The `handleAgentRoute` function in `server_api.go:16-140` is a manual switch with 30+ cases — could be refactored to use Go 1.22 patterns like v1 does, but it works.

### 3.5 Separation of Concerns — GOOD

- Server struct (`server.go:29-134`) has 30+ fields spanning registry, sessions, proxy, tunnels, tokens, operators, audit, enrollment, CA, builder, profiles, VT, tasks, transfers, relays, E2E. This is a **God Object** — it manages everything. However, each subsystem has its own manager type (`Registry`, `SessionManager`, `OperatorManager`, `AuditLogger`, `EnrollmentManager`, `CAManager`, `BuilderManager`, `ProfileManager`, `TaskManager`, `TransferManager`), so the Server struct is more of a coordinator than a monolith.
- **Recommendation:** Consider grouping related fields into sub-structs (e.g., `Server.auth`, `Server.tunnels`) to reduce the field count.

---

## 4. Protocol

### 4.1 Message Types (messages.go — 108 types)

- `internal/protocol/messages.go`: 108 message type constants (lines 10-157)
- Well-organized by category: Agent→Server, Server→Agent commands, results, Phase 4/7 additions
- **Deprecated aliases** documented: `TypeAuthRefresh` (line 41) = `TypeTokenRotate`, `TypeAuthRequest` (line 43) = `TypeTokenRefresh`

### 4.2 Versioning — PARTIAL

**✅ Good:**
- `messages.go:192`: `ProtocolVersion` field in `AgentInfo` — `"1"` = original, `"2"` = post-refactor, missing = `"1"` (backward compat)
- `server_ws.go:129-133`: Server detects protocol version and logs it

**🟡 GAP:**
- No version negotiation — the server simply logs the version but doesn't use it to select behavior. All commands are handled the same regardless of protocol version.
- No protocol version in the `Envelope` itself — versioning is only at connection level.
- **Recommendation:** If protocol diverges in the future, add version-gated behavior in `handleMessages`.

### 4.3 Binary Framing (binary.go)

- `binary.go:9-37`: Binary frame format = 4-byte big-endian header length + JSON header. Simple and correct.
- `MaxSmallPayloadSize = 1 << 20` (1MB) — but this constant is **never used** anywhere in the codebase. Dead code.

### 4.4 WebSocket (websocket.go)

- `websocket.go:18-22`: Upgrader with 64KB read/write buffers
- `websocket.go:28-76`: `Dial` function with TLS, mTLS, and token auth
- `websocket.go:83-118`: `Listen` function with TLS, mTLS
- `websocket.go:120-134`: Certificate fingerprint utility
- **Issue:** `CheckOrigin: func(r *http.Request) bool { return true }` (line 21) — see §1.5

---

## 5. Platform

### 5.1 Interface (platform.go:15-48) — 23 Methods

The `Platform` interface defines 23 methods:
- **Filesystem (7):** ListDir, FileStat, ReadFile, WriteFile, DeleteFile, MoveFile, Mkdir
- **Shell (1):** Exec
- **Screen (4):** CaptureDisplay, ScreenInfo, ScreenStreamStart, ScreenStreamStop
- **Input (4):** Click, TypeText, KeyPress, KeyCombo
- **System (7):** Health, ProcessList, ProcessKill, OpenURL, Notify, ClipboardGet, ClipboardSet

### 5.2 Implementations — ALL 23 METHODS IMPLEMENTED ON ALL 3 PLATFORMS

| Platform | File | Methods | All Implemented? |
|----------|------|---------|-------------------|
| Linux | `platform_linux.go` (468 lines) | 23 | ✅ Yes |
| Darwin | `platform_darwin.go` (468 lines) | 23 | ✅ Yes |
| Windows | `platform_windows.go` (639 lines) | 23 | ✅ Yes |
| Generic (fallback) | `platform.go` (genericPlatform) | 23 | ✅ Yes (stubs) |

**✅ Good:** All 23 interface methods are implemented on all 3 OS-specific platforms plus the generic fallback. No missing methods.

### 5.3 Platform Factory

- `platform_linux.go:17`, `platform_darwin.go:19`, `platform_windows.go:21`: Each has `func New(name string) Platform`
- Build-tag constrained (`_linux.go`, `_darwin.go`, `_windows.go`) — correct Go pattern for platform-specific files.

---

## 6. Binary Artifacts

### 6.1 Present in Repo Root — 13 FILES, ~100MB

| File | Size | Date | Git Tracked? |
|------|------|------|--------------|
| `HermesRemote_v5.exe` | 9.2M | Jul 8 | No (gitignored) |
| `HermesRemote_v7.exe` | 10M | Jun 29 | No |
| `HermesRemote_v8b.exe` | 7.0M | Jul 1 | No |
| `HermesRemote_v8c.exe` | 5.9M | Jul 1 | No |
| `HermesRemote_v8c_fixed.exe` | 5.9M | Jul 1 | No |
| `HermesRemote_v8d.exe` | 5.9M | Jul 1 | No |
| `HermesRemote_v9.exe` | 9.2M | Jul 2 | No |
| `HermesRemote_v9b.exe` | 9.2M | Jul 2 | No |
| `HermesRemote_v9c-v0.2.2.exe` | 9.2M | Jul 2 | No |
| `HermesRemote_vegas_latest.exe` | 6.3M | Jul 10 | No |
| `hermes-remote.exe` | 9.7M | Jun 26 | No |
| `probe-v1.9.3.exe` | 12M | Jul 24 | No |
| `HermesRemote_gorizia_v5.zip` | 5.2M | Jul 8 | **YES** ⚠️ |

**🔴 ISSUE:** `HermesRemote_gorizia_v5.zip` (5.2MB) is **tracked in git** despite `.gitignore` having `*.exe`. The `.zip` pattern is NOT in `.gitignore`.

### 6.2 .gitignore Coverage

```
*.exe          ← covers all .exe files ✅
*.test         ← covers test binaries ✅
*.out          ← covers coverage files ✅
build/         ← covers build output ✅
```

**Missing from .gitignore:**
- `*.zip` — the tracked .zip file proves this gap
- `*.dll`, `*.so`, `*.dylib` — no shared library patterns
- `probe` (root binary) — `probe` and `probe-server` show as modified in git status, meaning they ARE tracked

### 6.3 Also Tracked

- `git status` shows `probe` and `probe-server` as modified — these are compiled binaries that are tracked in git. They should be in `.gitignore`.

**Recommendation:**
1. Add `*.zip`, `*.dll`, `*.so`, `*.dylib`, `probe`, `probe-server`, `probe-client` to `.gitignore`
2. `git rm --cached HermesRemote_gorizia_v5.zip probe probe-server` to untrack
3. Delete all 13 binary artifacts from the repo root (they're build artifacts, not source)

---

## 7. Build System

### 7.1 Makefile

- Uses `/opt/data/go/bin/go` (Go 1.24.4) for `build`, `vet`, `test` targets — **WRONG**. The codebase requires `go1.23.12` with `GOTOOLCHAIN=local`. The Makefile will use the wrong Go version and crash.
- `GO123` variable is defined but only used in the `windows` target.
- **Fix:** Use `GOTOOLCHAIN=local /opt/data/go/bin/go1.23.12` for all targets, or set `GOTOOLCHAIN=local` as an environment variable.

### 7.2 go.mod

- `go 1.22` — the codebase uses Go 1.22 `ServeMux` patterns (`GET /api/v1/...`), which requires Go 1.22+. Correct.
- Builds with `go1.23.12` — works because Go is backward compatible.
- Dependencies: `gorilla/websocket v1.5.3`, `golang.org/x/sys v0.20.0`, `golang.org/x/crypto v0.23.0` — all reasonable, no bloat.

### 7.3 GitHub Workflows

**`build.yml`:**
- Uses `go-version: "1.22"` — matches go.mod. ✅
- Runs `go vet`, `go build`, `go test` — standard. ✅
- **Issue:** CI uses Go 1.22 but the codebase is tested with go1.23.12 locally. Minor version skew but should be fine.

**`release.yml`:**
- Uses `goreleaser-action@v5` — but **no `.goreleaser.yml` config file exists** in the repo. The release workflow will fail.
- **Fix:** Add a `.goreleaser.yml` or remove the release workflow.

### 7.4 Cross-Compilation

- `Makefile` `cross` target: builds for linux/amd64, linux/arm64, windows/amd64, darwin/amd64, darwin/arm64. ✅
- `windows` target: uses `GOTOOLCHAIN=local` and `go1.23.12`. ✅
- **Gap:** No `arm64` for Windows (Windows ARM is increasingly common).

---

## 8. Frontend

### 8.1 Structure

- 10 pages: Dashboard, Agents, AgentDetail, Builder, Profiles, Tasks, Transfers, Credentials, Settings, Login
- 8 agent detail tabs: Screen, Terminal, Modes, Files, MITM, Debug, Processes, Tunnels
- 3 components: Sidebar, TopologyGraph, StatusBadge
- API client (`client.ts`): 321 lines, covers all v1 API endpoints
- Types (`types.ts`): 165 lines, matches Go backend structures

### 8.2 API Client — GOOD WITH MINOR ISSUES

**✅ Good:**
- Centralized `apiFetch<T>` wrapper with auth header injection
- 401 handling: clears token and redirects to login (`client.ts:35-39`)
- Consistent error handling: checks `body.ok` and throws on error
- Token stored in `localStorage` (standard SPA pattern)

**🟡 Issues:**
- `client.ts:122-132`: `streamStart`, `streamStop`, `streamFrame` use `/api/v1/agents/${id}/...` as full path, but `apiFetch` already prepends `/api/v1`. This results in **double-prefixed paths**: `/api/v1/api/v1/agents/${id}/stream-start`. **BUG.**
- `client.ts:136-153`: Same double-prefix issue for `pointerClick`, `keyPress`, `keyCombo`, `textInput`.
- **Fix:** Remove the `/api/v1` prefix from these endpoint paths.

### 8.3 Error Handling — ADEQUATE

- All pages use try/catch with `setError((e as Error).message)` pattern
- Loading states present: `loading` state in Agents, Transfers, Login
- Error display: `<div className="error-msg">{error}</div>` in most pages
- **Gap:** Some pages (Builder.tsx:58, 108, 138) silently swallow errors with `catch { /* ignore */ }` — should at least log.

### 8.4 State Management — SIMPLE BUT SUFFICIENT

- No global state management (no Redux, Zustand, or Context)
- Each page manages its own state with `useState` + `useEffect`
- Auth token in `localStorage` — simple but works for a single-user tool
- **Assessment:** Adequate for current scope. If the app grows, consider Context for shared state (agent list, current user).

### 8.5 Missing Frontend Tests

- Zero frontend test files. No `*.test.tsx`, `*.spec.ts`, no testing framework configured.
- **Recommendation:** Add Vitest + React Testing Library for component and integration tests.

---

## 9. Test Coverage

### 9.1 Test File Map

| Package | Source Files | Test Files | Coverage |
|---------|-------------|------------|----------|
| `.` (embed) | 1 | 0 | 0% |
| `cmd/probe` | 5 | 3 | ✅ CLI tests |
| `cmd/probe-client` | 1 | 0 | 0% |
| `cmd/probe-server` | 1 | 0 | 0% |
| `internal/agent` | 17 | 2 | 🟡 Partial (capabilities, permissions only) |
| `internal/crypto` | 1 | 1 | ✅ Full |
| `internal/modes` | 4 | 0 | 0% |
| `internal/platform` | 4 | 0 | 0% |
| `internal/protocol` | 4 | 1 | 🟡 Partial (messages only) |
| `internal/relay` | 2 | 1 | 🟡 Partial (mux only) |
| `internal/server` | 31 | 11 | ✅ Good (api_v1, audit, builder, capabilities, enrollment, filetransfer, ipfilter, operator, proxy, registry, tasks) |
| `internal/testutil` | 3 | 0 | N/A (helpers) |
| **Total** | **74 src** | **19 test** | **25% file coverage** |

### 9.2 Test Results

```
PASS  internal/agent         0.542s
PASS  internal/crypto         0.007s
PASS  internal/protocol       0.008s
PASS  internal/relay          0.006s
PASS  internal/server         3.379s
FAIL  cmd/probe               8.181s  (TestCLI_NoArgs_PrintsUsage)
```

### 9.3 Missing Tests

**Critical gaps:**
- `internal/platform` — **zero tests** for any of the 23 interface methods on 3 OS implementations
- `internal/modes` — **zero tests** for mode switching logic
- `internal/agent` — only 2 test files for 17 source files; no tests for `agent.go` (1228 lines), `agent_proc.go`, `agent_tunnel.go`, `agent_mitm.go`, `agent_update.go`, `agent_stream.go`
- `internal/relay/relay.go` (723 lines) — no test; only `mux_test.go` tests the channel multiplexer
- `cmd/probe-client` and `cmd/probe-server` — zero tests (legacy, but still compiled)

---

## 10. Documentation

### 10.1 Doc Status

| File | Lines | Last Modified | Status |
|------|-------|---------------|--------|
| `README.md` | 158 | Jul 28 | ✅ Current (v1.9.4) |
| `CHANGELOG.md` | 289 | Jul 28 | ✅ Current (v1.9.4) |
| `CLAUDE.md` | 99 | Jul 23 | 🟡 Likely stale (no version ref) |
| `AGENTS.md` | 51 | Jul 23 | 🟡 Likely stale |
| `CONTRIBUTING.md` | 112 | Jul 23 | 🟡 Likely stale |
| `BLUEPRINT.md` | 172 | Jul 28 | ⚠️ Self-marked SUPERSEDED |
| `ROADMAP.md` | 179 | Jul 23 | 🔴 Stale (v0.1.0-a0, phases A-F) |
| `DESIGN.md` | 367 | Jul 24 | 🟡 Phase 4 design — partially implemented |
| `DESIGN_PHASE4.md` | 472 | Jul 24 | 🟡 Phase 4 design — partially implemented |

### 10.2 Contradictions

**🔴 BLUEPRINT.md vs Reality:**
- BLUEPRINT.md header says "v0.1.0-a0" but repo is at v1.9.4. The file self-marked as SUPERSEDED (line 3), which is good, but it should be moved to a `docs/archive/` directory or deleted.

**🔴 ROADMAP.md vs Reality:**
- ROADMAP.md describes phases A-F for v0.1.0-a0. All phases marked as ✅ Complete, but the codebase is at v1.9.4 with significant features beyond the roadmap (Phase 7 capabilities, builder, VT scanning, tasks, transfers). The roadmap is stale and misleading.

**🟡 DESIGN.md + DESIGN_PHASE4.md:**
- These are design documents for Phase 4 (dynamic mode switching, aware relay). They're useful as design references but don't reflect the current implementation state. No "implemented" vs "designed" tracking.

**🟡 CLAUDE.md:**
- Last modified Jul 23 (6 days ago). Likely written for an earlier version. Should be reviewed for accuracy.

### 10.3 Missing Documentation

- No `KNOWLEDGE.md` in repo (BLUEPRINT.md references it at `/opt/data/projects/probe/KNOWLEDGE.md` — external path, not in repo)
- No API documentation beyond the auto-generated OpenAPI spec (`/openapi.json` endpoint)
- No deployment guide
- No security guide (given this is a remote access tool, security documentation is critical)

---

## Summary of Findings by Severity

### 🔴 Critical (4)
1. **No request body size limits** — 20+ `io.ReadAll(r.Body)` calls without `MaxBytesReader` → DoS vulnerability
2. **Timing-unsafe token comparison** — 5 instances of `==` for token comparison → side-channel attack
3. **Auth optional by default** — `requireAPIAuth` defaults to false → API open without explicit config
4. **`HermesRemote_gorizia_v5.zip` tracked in git** — 5.2MB binary in version control

### 🟡 Moderate (6)
5. **`InsecureSkipVerify = true`** fallback in WebSocket dialer → MITM risk
6. **CORS wide open** — all 3 WebSocket upgraders accept any origin → CSRF risk
7. **Weak file permissions** — operator/enrollment/registry files at `0644` (world-readable)
8. **No rate limiting on HTTP API** — only LLM proxy is rate-limited
9. **Frontend API path double-prefix bug** — 5 endpoints in `client.ts` get `/api/v1/api/v1/...`
10. **GoReleaser workflow has no config** — release.yml will fail

### 🟢 Minor (7)
11. `go vet` error: self-assignment in `relay.go:60`
12. Failing test: `TestCLI_NoArgs_PrintsUsage` — stale test expectation
13. 13 binary artifacts (~100MB) in repo root
14. `probe` and `probe-server` binaries tracked in git
15. Makefile uses wrong Go version (`go` instead of `go1.23.12`)
16. `MaxSmallPayloadSize` constant in `binary.go` is dead code
17. `server_token.go:82-84` has confusing brace structure
18. Stale docs: ROADMAP.md (v0.1.0-a0), BLUEPRINT.md (superseded)
19. Zero frontend tests
20. Zero platform tests