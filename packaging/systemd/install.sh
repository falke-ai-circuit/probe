#!/usr/bin/env bash
# Install PROBE as a systemd service.
#
# One command, no hand-editing of paths: the defaults below are the paths the
# shipped unit file already contains, and anything you override here is
# substituted into the unit before it is installed. Run it as root on the host
# that should run the agent:
#
#   sudo packaging/systemd/install.sh
#
# Overridable (env or leading VAR=value):
#   PROBE_BIN       binary to install          (default /opt/probe/probe)
#   PROBE_CONFIG    agent config, must exist   (default /etc/probe/agent.json)
#   PROBE_USER      service account            (default probe)
#   UNIT_DIR        systemd unit directory     (default /etc/systemd/system)
set -euo pipefail

PROBE_BIN="${PROBE_BIN:-/opt/probe/probe}"
PROBE_CONFIG="${PROBE_CONFIG:-/etc/probe/agent.json}"
PROBE_USER="${PROBE_USER:-probe}"
UNIT_DIR="${UNIT_DIR:-/etc/systemd/system}"

UNIT_NAME="probe-agent.service"
UNIT_SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/${UNIT_NAME}"
STATE_DIR="/var/lib/${PROBE_USER}"

die() { echo "install.sh: $*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "must run as root (systemctl daemon-reload and unit install need it)"
[ -f "$UNIT_SRC" ] || die "unit file not found beside this script: $UNIT_SRC"
[ -x "$PROBE_BIN" ] || die "binary not executable: $PROBE_BIN (build it, then point PROBE_BIN at it)"
[ -f "$PROBE_CONFIG" ] || die "config not found: $PROBE_CONFIG (create it from probe-client.json before installing)"
command -v systemctl >/dev/null || die "systemctl not found: this host has no systemd"

# Service account: no shell, no home on purpose — the agent needs neither, and a
# shell-capable account would widen what a compromised agent can do.
if ! id "$PROBE_USER" >/dev/null 2>&1; then
    useradd --system --no-create-home --home-dir /nonexistent \
            --shell /usr/sbin/nologin "$PROBE_USER"
    echo "created service account: $PROBE_USER"
fi

install -d -m 0755 "$(dirname "$PROBE_BIN")"
install -d -m 0755 "$(dirname "$PROBE_CONFIG")"
install -d -m 0755 -o "$PROBE_USER" -g "$PROBE_USER" "$STATE_DIR"

install -m 0755 "$PROBE_BIN" "$PROBE_BIN"

# Substitute only when a default was overridden, so the installed unit still
# matches the shipped file byte for byte on the documented install.
tmp="$(mktemp)"
cp "$UNIT_SRC" "$tmp"
if [ "$PROBE_BIN" != "/opt/probe/probe" ] || [ "$PROBE_CONFIG" != "/etc/probe/agent.json" ] ||
   [ "$PROBE_USER" != "probe" ] || [ "$STATE_DIR" != "/var/lib/probe" ]; then
    sed -i \
        -e "s#/opt/probe/probe#${PROBE_BIN}#g" \
        -e "s#/etc/probe/agent.json#${PROBE_CONFIG}#g" \
        -e "s#^User=probe\$#User=${PROBE_USER}#" \
        -e "s#^Group=probe\$#Group=${PROBE_USER}#" \
        -e "s#/var/lib/probe/#${STATE_DIR}/#g" \
        "$tmp"
fi
install -m 0644 "$tmp" "${UNIT_DIR}/${UNIT_NAME}"
rm -f "$tmp"

systemctl daemon-reload
systemctl enable --now "$UNIT_NAME"

# Prove it is up from the live state, not from unit intent. Exit 3 means
# "installed but not running" — a failed install, so fail the script loudly.
sleep 2
set +e
"$PROBE_BIN" --service-status --status-file "${STATE_DIR}/status.json"
rc=$?
set -e
if [ "$rc" -ne 0 ]; then
    echo "install.sh: agent is not running (status exit $rc); see: journalctl -u ${UNIT_NAME} -n 50" >&2
    exit "$rc"
fi
echo "installed ${UNIT_NAME}: journalctl -u ${UNIT_NAME} -f"
