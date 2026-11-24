#!/bin/sh
# PROBE bring-up that reports what it is.
#
# Purpose: recreate/start the server from the pinned image and then print the
# facts an operator actually needs — the running server version, the number of
# agents the server knows about, and whether the running image is the pinned one.
# "It started" is not a bring-up result; the server's own /api/v1/health surface
# is the authority, so this script reads that instead of inventing a second one.
#
# Usage:
#   ./scripts/bringup.sh              # docker compose up -d, wait for health, report
#   ./scripts/bringup.sh --no-up      # report on an already-running server
#   PROBE_COMPOSE_DIR=/path ./scripts/bringup.sh   # run against another stack dir
#
# Requires only docker (with the compose plugin) and curl — both already required
# by the compose healthcheck. Nothing is installed by this script.
#
# Exit codes: 0 = healthy and the running image matches the pinned digest,
#             1 = unhealthy / version not reported / image digest mismatch,
#             2 = the compose file does not carry a pinned (non-latest) image.
set -eu

COMPOSE_DIR=${PROBE_COMPOSE_DIR:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}
PROBE_URL=${PROBE_URL:-http://127.0.0.1:7701}
WAIT_SECONDS=${WAIT_SECONDS:-90}
DO_UP=1

for arg in "$@"; do
	case "$arg" in
	--no-up) DO_UP=0 ;;
	-h | --help)
		sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "unknown argument: $arg" >&2
		exit 2
		;;
	esac
done

cd "$COMPOSE_DIR"

# The pin as written in the compose file: explicit tag and/or digest, never latest.
PINNED=$(docker compose config 2>/dev/null | sed -n 's/^ *image: *//p' | head -n 1)
case "$PINNED" in
"" | *:latest)
	echo "FAIL: compose image is not pinned: '${PINNED}'" >&2
	exit 2
	;;
esac

if [ "$DO_UP" = 1 ]; then
	docker compose up -d --remove-orphans
fi

# Poll the server's own health surface until it answers or the budget runs out.
BODY=""
elapsed=0
while [ "$elapsed" -lt "$WAIT_SECONDS" ]; do
	if BODY=$(curl -sf "$PROBE_URL/api/v1/health" 2>/dev/null) && [ -n "$BODY" ]; then
		break
	fi
	sleep 2
	elapsed=$((elapsed + 2))
done

if [ -z "$BODY" ]; then
	echo "FAIL: no healthy response from $PROBE_URL/api/v1/health after ${WAIT_SECONDS}s" >&2
	exit 1
fi

field() { printf '%s' "$BODY" | sed -n "s/.*\"$1\":\"\{0,1\}\([^\",}]*\)\"\{0,1\}.*/\1/p"; }

VERSION=$(field server_version)
TOTAL=$(field total_agents)
ACTIVE=$(field active_agents)
STALE=$(field stale_agents)

if [ -z "$VERSION" ] || [ -z "$TOTAL" ]; then
	echo "FAIL: health answered without server_version/total_agents: $BODY" >&2
	exit 1
fi

# Prove the running container is the pinned image, not a look-alike tag.
CID=$(docker compose ps -q probe || true)
IMAGE_MATCH="unverified"
if [ -z "$CID" ]; then
	echo "note: no running container for this compose project — checking the pinned image only" >&2
	RUNNING_IMAGE="$PINNED"
else
	RUNNING_IMAGE=$(docker inspect -f '{{.Config.Image}}' "$CID")
fi
# RepoDigests belongs to the image, not the container: ask the image by the
# reference the container was created from.
RUNNING_DIGEST=$(docker inspect -f '{{json .RepoDigests}}' "$RUNNING_IMAGE" 2>/dev/null || echo '[]')
if [ -n "$RUNNING_DIGEST" ]; then
	case "$PINNED" in
	*@sha256:*)
		WANT=$(printf '%s' "$PINNED" | sed -n 's/.*\(@sha256:[0-9a-f]*\).*/\1/p')
		case "$RUNNING_DIGEST" in
		*"$WANT"*) IMAGE_MATCH="pinned-digest-ok" ;;
		*) IMAGE_MATCH="DIGEST-MISMATCH want=$WANT running=${RUNNING_DIGEST:-none}" ;;
		esac
		;;
	*)
		[ "$RUNNING_IMAGE" = "$PINNED" ] && IMAGE_MATCH="pinned-tag-ok" || IMAGE_MATCH="IMAGE-MISMATCH want=$PINNED running=$RUNNING_IMAGE"
		;;
	esac
fi

echo "probe bring-up"
echo "  image     $PINNED"
echo "  running   ${RUNNING_IMAGE:-unknown}"
echo "  image pin $IMAGE_MATCH"
echo "  version   $VERSION"
echo "  agents    total=$TOTAL active=$ACTIVE stale=$STALE"
echo "  uptime    $(field uptime_seconds)s"

case "$IMAGE_MATCH" in
DIGEST-MISMATCH* | IMAGE-MISMATCH*)
	exit 1
	;;
esac
