# PROBE FUD Recipes (Aug 2026)

Three FUD recipes verified via VirusTotal full 75-engine scan.

## Recipe 1: 6-Transform (1/75 VT) — recommended

Binary: `v1.15.0-fud-windows-mantle-recipe.exe` — 12.8 MB
SHA256: `2ae6b0d7573d4a0041a20e499d65196e8794b3181bd54267185bfee7797131bc`
VT result: 1/75 (Microsoft: Trojan:Win32/Wacatac.B!ml)

### Build command
```bash
curl -s -X POST http://localhost:9292/api/build \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "binary_path": "/tmp/probe-agent-v1.15.0-rebuild",
    "repo_path": "/opt/data/workspace-operative/probe",
    "output_path": "/tmp/probe-agent-v1.15.0-fud-windows",
    "persona": "syncthing",
    "transforms": ["persona", "xor-obfuscate", "module-rename", "evasion", "donut", "yara"],
    "target_os": "windows",
    "target_arch": "amd64"
  }'
```

## Recipe 2: With Ghost Profile (3/75 VT) — REGRESSED

Binary: `v1.15.0-fud-windows-ghost-profile.exe` — 12.8 MB
SHA256: `ab69a98f592d3f6c16f40f20549d42aa3a827fcae76204c829069c891cd0d67e`
VT result: 3/75 (Bkav + Microsoft + Rising)

Ghost profile **added** Bkav + Rising without dropping Microsoft. MANTLE's
default ghost profile (traefik) and the syncthing profile both have this issue.
Praetorian's published ghost profile technique uses much larger harvested
profiles (Kubernetes, Docker) with 100K+ symbols that MANTLE doesn't ship.

## Recipe 3: Linux ELF (1/75 VT)

Binary: `v1.15.0-fud-linux-mantle-recipe.exe` — 12.3 MB
SHA256: `7d6d741d236f18653a7028c7636263bc89cb09bea337cfba97fe460b2c840326`
VT result: 1/75 (Microsoft: Trojan:Script/Wacatac.B!ml)

Same recipe but `target_os: "linux"`.

## Recipe notes

The 6 transforms are mandatory:
- `persona` — rewrites module structure to look like syncthing
- `xor-obfuscate` — XOR string literals
- `module-rename` — renames internal packages
- `evasion` — applies evasion pkg (jitter, antidebug, apihash)
- `donut` — strips go-donut / CLR markers
- `yara` — renames JSON tags + paths to defeat YARA rules

**Final result: 1/75 is the practical floor for unsigned Go binaries** per
the Praetorian research (Wacatac.B!ml is Microsoft cloud ML, not a YARA rule).

To reach 0/75 you need either:
- **EV code-signing certificate** ($$$ via Sectigo/DigiCert)
- **Garble v0.12.1+** (requires Go 1.25+; current toolchain is 1.23.12)
- **Pre-built signed PE wrapper** that loads the Go binary from .rdata
- **Per-build Authenticode diversification** (Praetorian verified technique)

See `research/wacatac-fud-research-2026-08-12.md` for full analysis.
