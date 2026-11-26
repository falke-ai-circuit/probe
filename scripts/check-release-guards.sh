#!/usr/bin/env bash
# check-release-guards.sh — P24-01 §8 release guards as a local/CI-checkable script.
#
# Guard A (version label vs version source):
#   image label org.opencontainers.image.version must equal the Semver read
#   from internal/version/version.go at the built sha (string equality modulo
#   a leading "v").
# Guard B (revision label vs tag existence):
#   image label org.opencontainers.image.revision must be a commit that carries
#   a v<semver> tag resolving to exactly that sha
#   (git rev-parse v<semver>^{commit} ref check), with fetch-tags=on in CI.
#   Refuses: no tag for the sha; tag resolves to a different commit; build was
#   not a v* tag push while claiming release semantics.
#
# Usage: scripts/check-release-guards.sh <image-ref> [sha]
#   <image-ref>  image to inspect (label source)
#   [sha]        revision to check against (default: image's revision label,
#                fallback: git rev-parse HEAD)
# Exit 0 = both guards pass. Exit 1 = named guard failure (CI red, pre-push).
# Design source: /opt/data/tmp/probe119/P24-01_RELEASEPROTOCOL.md §8.
set -uo pipefail

die() { echo "GUARD-FAIL: $*" >&2; exit 1; }

IMAGE="${1:?usage: check-release-guards.sh <image-ref> [sha]}"
REVISION="${2:-}"
VFILE="internal/version/version.go"

# ---- read image labels ---------------------------------------------------
VL=$(docker image inspect "$IMAGE" --format '{{index .Config.Labels "org.opencontainers.image.version"}}' 2>/dev/null) \
  || die "Guard A+B: cannot inspect image $IMAGE (docker daemon unreachable or image absent)"
case "$VL" in
  ""|"None") die "Guard A: image $IMAGE carries NO org.opencontainers.image.version label (pre-OCI-labelling build?)" ;;
esac

if [ -z "$REVISION" ]; then
  REVISION=$(docker image inspect "$IMAGE" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' 2>/dev/null)
  [ -n "$REVISION" ] && [ "$REVISION" != "None" ] || REVISION=$(git rev-parse HEAD)
fi

# ---- Guard A: label == source-literal at the built sha -------------------
if [ -f "$VFILE" ]; then
  SRC=$(sed -n 's/^var Semver = "\([^"]*\)"$/\1/p' "$VFILE" | tail -1)
elif git cat-file -e "$REVISION:$VFILE" 2>/dev/null; then
  SRC=$(git show "$REVISION:$VFILE" | sed -n 's/^var Semver = "\([^"]*\)"$/\1/p' | tail -1)
else
  die "Guard A: $VFILE not found in worktree nor at revision $REVISION"
fi
[ -n "$SRC" ] || die "Guard A: no Semver literal parsed from $VFILE"

SRC_N="${SRC#v}"; VL_N="${VL#v}"   # display-form label == canonical form allowed
if [ "$SRC_N" != "$VL_N" ]; then
  die "Guard A: image version label '$VL' != internal/version Semver '$SRC' at $REVISION"
fi
echo "GUARD-A-OK: image version label '$VL' == internal/version Semver '$SRC' at $REVISION"

# ---- Guard B: revision label has a matching v* tag at that sha -----------
TAG="v${SRC_N}"
# On a tag push the pushed ref must BE the release tag the source implies:
# a v1.99.0 tag whose version.go says 1.20.0 is exactly the label-vs-tag
# disagreement P24-01 §8 bars.
if [ -n "${GITHUB_REF:-}" ] && [[ "${GITHUB_REF}" == refs/tags/* ]]; then
  [ "${GITHUB_REF}" = "refs/tags/${TAG}" ] \
    || die "Guard B: pushed ref ${GITHUB_REF} != release tag refs/tags/${TAG} (version source and pushed tag disagree)"
fi
if ! git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null 2>&1; then
  die "Guard B: no tag '$TAG' exists; image $IMAGE (revision $REVISION, version $SRC_N) cannot claim release identity"
fi
TAGGED=$(git rev-parse "${TAG}^{commit}")
if [ "$TAGGED" != "$REVISION" ]; then
  die "Guard B: tag '$TAG' resolves to $TAGGED, but image revision label is $REVISION"
fi
echo "GUARD-B-OK: tag '$TAG' -> $TAGGED == image revision $REVISION"
