# Elevated execution — `exec{as:"SYSTEM"}` (P8-03)

Ratified contract: P8-02 §2, §10 C6, Amendment A-1, OD-1(a).

## What this is, and what it is not

`as:"SYSTEM"` is a **declaration that is verified against the process principal**.
It is not an escalation primitive. A process running as `flk` with a UAC-filtered
token (`S-1-16-8192`) has no privilege source: a request parameter cannot
manufacture SYSTEM. The delivery route is the Windows service install — the
service principal is LocalSystem, so the call runs elevated because the process
already is.

Wire field: `internal/protocol/messages.go:242`

```
POST /api/v1/agents/{id}/exec
{ "command": "...", "as": "SYSTEM" }
```

`as` absent or `"current"` → today's code path, unchanged.

## Agent behaviour (`internal/agent/agent.go:658`)

| condition | result |
|---|---|
| `as` is not `current`/`SYSTEM` | `INVALID_PARAMS` (400) |
| `as:"SYSTEM"`, principal **is** SYSTEM (`S-1-5-18`) | command runs; `data.principal` reports the token identity |
| `as:"SYSTEM"`, principal is not SYSTEM | `ELEVATION_UNAVAILABLE` **before anything is spawned**, with `detail.principal`, `is_system`, `elevated`, `integrity`, `token_filtered`, `remediation` |

Principal detection: `internal/agent/principal_windows.go` (token + LocalSystem SID),
`internal/agent/principal_other.go` (Unix — SYSTEM does not exist there, so the
call always refuses rather than claiming root is SYSTEM).

## Server behaviour (`internal/server/server_api.go:191`)

1. Unknown `as` values are refused 400.
2. `as:"SYSTEM"` is gated on the **explicit** capability `exec.system`, not the
   permissive "empty list means all" rule. A pre-1.19 agent advertises nothing,
   would ignore the unknown field and run the command unelevated, and report
   success — that silent lie is what the gate closes: `UNSUPPORTED_AGENT` (409),
   before dispatch.
3. Refusal from the agent → 409 `ELEVATION_UNAVAILABLE`, preserving `detail`.
4. An old agent's success shape is untouched; the new fields are additive.

## Amendment A-1 — a 200 never means "we did not run it"

| state | HTTP |
|---|---|
| agent returned a result (any exit code, including non-zero) | 200 |
| call exceeded its deadline | 504 `DEADLINE_EXCEEDED` |
| agent not connected / WS write failed | 503 `AGENT_UNAVAILABLE` |
| agent lacked the capability | 409 `CAPABILITY_MISSING` / `UNSUPPORTED_AGENT` |
| agent refused on policy/principal | 403 / 409 `ELEVATION_UNAVAILABLE` |

Error envelopes carry the time fields (`started_at_utc`, `tz`, `tz_offset_min`)
and `ran:false` with `exit_code:null`: no process ran, so no exit code is
invented.

## Envelope on every exec response

`exit_code`, `stdout`, `stderr`, `duration_ms`, `started_at_utc`,
`started_at_local`, `tz`, `tz_offset_min`, `principal`.
`duration_ms` is measured monotonically; `started_at_*` come from the agent's own
clock and the zone is declared, never inferred by the caller.
Fields live in `internal/protocol/messages.go` (`ExecResult`).

## The askpass workaround is deleted

The runtime-compiled `askpass.exe` + local sshd chain has no implementation in
this tree — it was operator-side, compiled per machine. It is retired, not
maintained: the replacement is this parameter plus the LocalSystem service
install. `internal/agent/elevation_test.go` (`TestNoAskpassPathInAgentOrClientSource`)
fails if the string reappears in `internal/` or `cmd/`.

## Open item

The service install itself (`--install-service`, `x/sys/windows/svc`, §8.5) is
its own unit. Until it lands, the refusal's remediation names the documented
one-time admin step; `as:"SYSTEM"` cannot succeed on a non-service agent by
design.
