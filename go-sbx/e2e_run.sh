#!/bin/bash
# E2E launcher for the Python entrypoint.
#
# Secrets come from the environment only — this file must stay free of any
# token so it can live in the repo. ARGO_AUTH is read from the caller's env.
#
# Usage:
#   ARGO_AUTH=... ARGO_DOMAIN=... bash e2e_run.sh [--no-argo]

set -u
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

WORKDIR="$HOME/sbx-e2e"
# Derived from this script's location so the same file runs from a WSL clone,
# a CI checkout, or a copy rsynced onto a remote host.
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "$(uname -m)" in
    x86_64)  ARCH=amd64 ;;
    aarch64) ARCH=arm64 ;;
    *)       ARCH="$(uname -m)" ;;
esac

mkdir -p "$WORKDIR/evidence"
cd "$WORKDIR" || exit 1

# R2: load the locally built library, not the one on 31888.xyz. This also
# exercises resolve_singbox_library's local-path branch.
export SBX_SOURCE="${SBX_SO:-$REPO/go-sbx/sbx-linux-$ARCH.so}"

# Ports chosen so the generated config covers every build tag:
#   reality -> with_utls, hysteria2 -> with_quic, wireguard -> with_gvisor.
export REALITY_PORT=18002
export HY2_PORT=18003
export ARGO_PORT=8001
export PORT=3000
export SUB_PATH=sub
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
# All output to a file: app.py calls clear_console() at t+45s, which would
# otherwise wipe the evidence out of the terminal.
#
# -u is required: Python block-buffers stdout when it is not a tty, so every
# print() would sit unflushed in the buffer while the Go side's direct stderr
# writes land immediately — leaving a log that looks empty.
nohup python3 -u "$REPO/python/app.py" > "$LOG" 2>&1 &
echo $! > "$WORKDIR/app.pid"
echo "pid $(cat "$WORKDIR/app.pid"), log $LOG"
