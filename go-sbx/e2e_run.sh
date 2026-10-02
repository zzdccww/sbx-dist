#!/bin/bash
# E2E launcher for the Python entrypoint.
#
# Secrets come from the environment only — this file must stay free of any
# token so it can live in the repo. ARGO_AUTH is read from the caller's env.
#
# Usage:
#   BOT_TOKEN=... CHAT_ID=... ARGO_AUTH=... ARGO_DOMAIN=... bash e2e_run.sh [--no-argo]

set -u
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

WORKDIR="$HOME/sbx-e2e"
# Derived from this script's location so the same file runs from a WSL clone,
# a CI checkout, or a copy rsynced onto a remote host.
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

mkdir -p "$WORKDIR/evidence"
cd "$WORKDIR" || exit 1

BOT_TOKEN_VALUE="${BOT_TOKEN:-}"
CHAT_ID_VALUE="${CHAT_ID:-}"
if [ -z "${BOT_TOKEN_VALUE//[[:space:]]/}" ] || [ -z "${CHAT_ID_VALUE//[[:space:]]/}" ]; then
    echo "BOT_TOKEN / CHAT_ID not set in environment" >&2
    exit 1
fi
unset BOT_TOKEN_VALUE CHAT_ID_VALUE

# Default to the launcher's published release download + sidecar checksum path.
# Set SBX_SO explicitly only when the local-path branch is the intended test.
if [ -n "${SBX_SO:-}" ]; then
    export SBX_SOURCE="$SBX_SO"
else
    unset SBX_SOURCE
fi

# Ports chosen so the generated config covers every build tag:
#   reality -> with_utls, hysteria2 -> with_quic, wireguard -> with_gvisor.
export REALITY_PORT=18002
export HY2_PORT=18003
export ARGO_PORT=8001
export FILE_PATH=.cache
# WARP_MODE deliberately unset: the empty default is the real deployment
# shape and the one that exposed the missing with_gvisor tag.

if [ "${1:-}" = "--no-argo" ]; then
    export DISABLE_ARGO=true
    LOG="$WORKDIR/run-baseline.log"
    echo "=== baseline run (no argo) ==="
else
    export DISABLE_ARGO=false
    LOG="$WORKDIR/run-tunnel.log"
    echo "=== tunnel run ==="
    if [ -z "${ARGO_AUTH:-}" ] || [ -z "${ARGO_DOMAIN:-}" ]; then
        echo "ARGO_AUTH / ARGO_DOMAIN not set in environment" >&2
        exit 1
    fi
fi

rm -f "$LOG"
# Launcher logs contain status only; node URIs and Telegram credentials must
# never enter evidence. -u is required: Python block-buffers stdout when it is
# not a tty, so every
# print() would sit unflushed in the buffer while the Go side's direct stderr
# writes land immediately — leaving a log that looks empty.
nohup python3 -u "$REPO/python/app.py" > "$LOG" 2>&1 &
echo $! > "$WORKDIR/app.pid"
echo "pid $(cat "$WORKDIR/app.pid"), log $LOG"
