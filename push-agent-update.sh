#!/bin/bash
# PROBE Agent Update — API-only procedure (P9-11)
#
# Usage: PROBE_TOKEN=<token> ./push-agent-update.sh <version_label> [binary_path]
#
# Artifact hygiene rule: the binary goes into the server's managed artifacts
# directory through the API. There is no scp, no docker cp and no shell on the
# server — upload (POST /api/v1/files) → update (POST .../update) → observe.

set -e

SERVER="${PROBE_SERVER:-http://187.124.31.229:80}"
TOKEN="${PROBE_TOKEN:?set PROBE_TOKEN (authorization bearer token)}"
AGENT_ID="${PROBE_AGENT_ID:-a0-falke}"
REPO_DIR="${PROBE_REPO_DIR:-/opt/data/workspace-operative/probe}"

VERSION_LABEL="${1:-unknown}"
BINARY_PATH="${2:-}"

if [ -z "$BINARY_PATH" ]; then
    BINARY_NAME="PROBE_${VERSION_LABEL}.exe"
    BINARY_PATH="${REPO_DIR}/${BINARY_NAME}"
    echo "Building ${BINARY_NAME}..."
    ( cd "${REPO_DIR}" && GOOS=windows GOARCH=amd64 go build -o "${BINARY_PATH}" ./cmd/probe-client/ )
fi

if [ ! -f "$BINARY_PATH" ]; then
    echo "binary not found: $BINARY_PATH" >&2
    exit 1
fi

BINARY_NAME=$(basename "$BINARY_PATH")

echo "Uploading ${BINARY_NAME} to the managed artifacts directory..."
# Streamed multipart upload — no base64 in the path. The server chooses the
# destination and returns {name, sha256, size, path}.
UPLOAD_RESPONSE=$(curl -sS -X POST "${SERVER}/api/v1/files" \
    -H "Authorization: Bearer ${TOKEN}" \
    -F "agent=${AGENT_ID}" \
    -F "file=@${BINARY_PATH}")

MANAGED_PATH=$(printf '%s' "$UPLOAD_RESPONSE" | python3 -c '
import json, sys
d = json.load(sys.stdin)
if not d.get("ok"):
    sys.exit((d.get("error") or {}).get("message", "upload failed"))
print(d["data"]["path"])
')

echo "Uploaded -> ${MANAGED_PATH}"
echo "Triggering agent update..."

# The HTTP response may time out because the agent's connection drops during
# the process swap — that is expected; the reconnect check is the proof.
curl -sS --max-time 180 -X POST "${SERVER}/api/agent/${AGENT_ID}/update" \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -d "$(printf '{"binary_path":"%s","version":"%s"}' "$MANAGED_PATH" "$VERSION_LABEL")" \
    || true

echo ""
echo "Waiting for the new agent to reconnect..."
sleep 5

echo "Checking agent version..."
curl -sS "${SERVER}/api/agents" -H "Authorization: Bearer ${TOKEN}" \
    | python3 -c 'import sys,json; d=json.load(sys.stdin); print(d[0]["version"] if d else "no-agent")'
