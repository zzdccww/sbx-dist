#!/bin/bash
# End-to-end check of the Argo tunnel path against a real Cloudflare tunnel.
#
# This cannot be part of smoke_test.py: the failure class it targets only
# appears once the tunnel delivers real traffic. A nil ConnectionDialer, for
# instance, lets NewService() and Start() both succeed and only panics on the
# first inbound request — taking the whole host process down. No offline test
# reaches that code path.
#
# Secrets come from the environment; this file must stay token-free.
#
# Usage:
#   BOT_TOKEN=<token> CHAT_ID=<id> ARGO_AUTH=<token> ARGO_DOMAIN=<host> bash e2e_tunnel.sh
#
# Requires the Cloudflare dashboard ingress for that tunnel to point at
# http://localhost:8001 (ARGO_PORT).

set -u
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

WORKDIR="$HOME/sbx-e2e"
DOMAIN="${ARGO_DOMAIN:?ARGO_DOMAIN not set}"
: "${ARGO_AUTH:?ARGO_AUTH not set}"

HERE="$(cd "$(dirname "$0")" && pwd)"
bash "$HERE/e2e_run.sh" || exit 1
APP_PID=$(cat "$WORKDIR/app.pid")
LOG="$WORKDIR/run-tunnel.log"

echo "waiting for tunnel registration..."
sleep 25

failures=0
note() { echo "  $*"; }
fail() { echo "  FAIL: $*"; failures=$((failures + 1)); }
port_bound() {
    local wanted table slot local_addr remote_addr state rest
    wanted=$(printf '%04X' "$1")
    for table in /proc/net/tcp /proc/net/tcp6 /proc/net/udp /proc/net/udp6; do
        [ -r "$table" ] || continue
        while read -r slot local_addr remote_addr state rest; do
            case "$state" in
                0A|07) [ "${local_addr##*:}" = "$wanted" ] && return 0 ;;
            esac
        done < "$table"
    done
    return 1
}
log_is_secret_free() {
    local secret
    for secret in "${BOT_TOKEN:-}" "${CHAT_ID:-}" "${ARGO_AUTH:-}" "${ARGO_DOMAIN:-}" "${UUID:-}" "${SBX_SOURCE:-}" \
        0a6568ff-ea3c-4271-9020-450560e10d61 0a6568ff-ea3c-4271-9020-450560e10d63; do
        [ -n "$secret" ] || continue
        grep -Fq -- "$secret" "$LOG" && return 1
    done
    ! grep -Eq 'vmess://|hysteria2://|vless://|PrivateKey:|PublicKey:|Private key:|Public key:' "$LOG"
}

ws=(-H "Connection: Upgrade" -H "Upgrade: websocket"
    -H "Sec-WebSocket-Version: 13"
    -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==")

echo ""
echo "=== 1. process survived startup ==="
if ps -p "$APP_PID" >/dev/null 2>&1; then
    note "alive"
else
    fail "app died during startup; inspect the protected remote log directly"
    exit 1
fi

echo "=== 2. control: local inbound answers the WS handshake ==="
code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 10 "${ws[@]}" \
    "http://127.0.0.1:8001/vmess-argo?ed=2560")
[ "$code" = "101" ] && note "localhost -> 101" || fail "localhost -> $code (want 101)"

echo "=== 3. tunnel round trip (HTTP/1.1) ==="
# Must be HTTP/1.1. Over HTTP/2 the Upgrade header is meaningless (RFC 8441
# extended CONNECT would be required), so an h2 request returns 400 even when
# the tunnel is perfectly healthy — an easy false negative.
#
# Retried: the edge can hold stale registrations for a while after a previous
# instance of the same tunnel disconnects, returning 530 for a few seconds even
# though the new instance is healthy. Repeated start/stop cycles make this
# likely, so a single 530 is not yet evidence of a defect.
code=""
for attempt in 1 2 3 4 5 6; do
    code=$(curl -s -o /dev/null -w "%{http_code}" --http1.1 --max-time 25 "${ws[@]}" \
        "https://$DOMAIN/vmess-argo?ed=2560")
    [ "$code" = "101" ] && break
    note "attempt $attempt -> $code, retrying in 10s"
    sleep 10
done
case "$code" in
    101) note "edge -> tunnel -> inbound -> 101 (end to end OK)" ;;
    502) fail "502 — edge reached us but the origin failed (did it panic?)" ;;
    530) fail "530 — edge has no healthy route; tunnel never registered" ;;
    1033) fail "1033 — tunnel not registered with the edge" ;;
    *)   fail "$code — unexpected" ;;
esac

echo "=== 4. path routing is done by our process ==="
# 404 on an unknown path proves sing-box saw the request, rather than
# Cloudflare answering on its behalf.
code=$(curl -s -o /dev/null -w "%{http_code}" --http1.1 --max-time 25 "${ws[@]}" \
    "https://$DOMAIN/definitely-not-a-real-path")
[ "$code" = "404" ] && note "unknown path -> 404 (matched by sing-box)" \
                    || fail "unknown path -> $code (want 404)"

echo "=== 5. no panic or secret residue after serving traffic ==="
if grep -q "panic:" "$LOG"; then
    fail "panic in protected log"
else
    note "no panic"
fi
if log_is_secret_free; then
    note "log is secret-free"
else
    fail "secret or node residue detected in protected log"
fi
ps -p "$APP_PID" >/dev/null 2>&1 && note "still alive" || fail "process died while serving"

echo "=== 6. shutdown ==="
kill -TERM "$APP_PID" 2>/dev/null
# Poll rather than sleeping a fixed interval. The native contract caps
# StopSingBox at 8s, so the launcher must exit inside the 15s host grace window.
for _ in $(seq 1 15); do
    ps -p "$APP_PID" >/dev/null 2>&1 || break
    sleep 1
done
if ps -p "$APP_PID" >/dev/null 2>&1; then
    fail "process still running 15s after SIGTERM"
else
    note "exited"
fi
for p in 8001 18002 18003; do
    bound=1
    for _ in $(seq 1 10); do
        port_bound "$p" || { bound=0; break; }
        sleep 1
    done
    [ "$bound" = 0 ] && note "port $p released" || fail "port $p still bound"
done

echo ""
if [ "$failures" -gt 0 ]; then
    echo "=== E2E TUNNEL TEST FAILED ($failures issue(s)) ==="
    exit 1
fi
echo "=== E2E TUNNEL TEST PASSED ==="
