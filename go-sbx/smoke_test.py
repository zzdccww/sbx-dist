#!/usr/bin/env python3
"""Runtime smoke test for sbx.so — loads via ctypes and exercises the C ABI."""
import base64
import ctypes
import json
import os
import platform
import socket
import subprocess
import sys
import tempfile
import time

# Resolve the .so next to this script so the same test runs unchanged in CI
# (including the native arm64 runner) and in local WSL. SBX_SO overrides it.
_ARCH = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine(), platform.machine())
SO_PATH = os.environ.get(
    "SBX_SO",
    os.path.join(os.path.dirname(os.path.abspath(__file__)), f"sbx-linux-{_ARCH}.so"),
)

UUID = "9c8e1a2b-3d4f-5061-7283-94a5b6c7d8e9"

# Minimal sing-box config: VMess+WS inbound + direct outbound.
CONFIG = {
    "log": {"level": "info"},
    "inbounds": [
        {
            "type": "vmess",
            "tag": "vmess-ws-in",
            "listen": "127.0.0.1",
            "listen_port": 18001,
            "users": [{"uuid": UUID, "alterId": 0}],
            "transport": {"type": "ws", "path": "/vmess-argo"},
        }
    ],
    "outbounds": [{"type": "direct", "tag": "direct"}],
}


def _x25519_private_key():
    key = bytearray(os.urandom(32))
    key[0] &= 248
    key[31] &= 127
    key[31] |= 64
    return base64.urlsafe_b64encode(bytes(key)).decode().rstrip("=")


def full_config(cert_path, key_path):
    """Mirror generateSingBoxConfig at its widest: every inbound the launchers
    can emit plus the WARP WireGuard endpoint.

    This is the case that matters for build tags. The minimal CONFIG above
    exercises none of them, so a missing -tags value (with_utls for reality,
    with_quic for hysteria2, with_gvisor for the wireguard endpoint) compiles,
    passes `go vet`, and still fails only once the .so is loaded.
    """
    return {
        "log": {"level": "info"},
        "http_clients": [{"tag": "http-client-direct"}],
        "inbounds": [
            {
                "type": "vmess",
                "tag": "vmess-ws-in",
                "listen": "127.0.0.1",
                "listen_port": 18001,
                "users": [{"uuid": UUID}],
                "transport": {
                    "type": "ws",
                    "path": "/vmess-argo",
                    "early_data_header_name": "Sec-WebSocket-Protocol",
                },
            },
            {
                "type": "vless",
                "tag": "vless-reality",
                "listen": "127.0.0.1",
                "listen_port": 18002,
                "users": [{"uuid": UUID, "flow": "xtls-rprx-vision"}],
                "tls": {
                    "enabled": True,
                    "server_name": "www.iij.ad.jp",
                    "reality": {
                        "enabled": True,
                        "handshake": {"server": "www.iij.ad.jp", "server_port": 443},
                        "private_key": _x25519_private_key(),
                        "short_id": [""],
                    },
                },
            },
            {
                "type": "hysteria2",
                "tag": "hysteria-in",
                "listen": "127.0.0.1",
                "listen_port": 18003,
                "users": [{"password": UUID}],
                "masquerade": "https://bing.com",
                "tls": {
                    "enabled": True,
                    "alpn": ["h3"],
                    "certificate_path": cert_path,
                    "key_path": key_path,
                },
            },
        ],
        "endpoints": [
            {
                "type": "wireguard",
                "tag": "wireguard-out",
                "mtu": 1280,
                "address": [
                    "172.16.0.2/32",
                    "2606:4700:110:8dfe:d141:69bb:6b80:925/128",
                ],
                "private_key": "YFYOAdbw1bKTHlNNi+aEjBM3BO7unuFC5rOkMRAz9XY=",
                "peers": [
                    {
                        "address": "engage.cloudflareclient.com",
                        "port": 2408,
                        "public_key": "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=",
                        "allowed_ips": ["0.0.0.0/0", "::/0"],
                        "reserved": [78, 135, 76],
                    }
                ],
            }
        ],
        "outbounds": [{"type": "direct", "tag": "direct"}],
        "route": {"default_http_client": "http-client-direct", "final": "direct"},
    }


def make_cert(workdir):
    cert_path = os.path.join(workdir, "cert.pem")
    key_path = os.path.join(workdir, "private.key")
    subprocess.run(
        [
            "openssl", "req", "-x509", "-nodes", "-newkey", "ec",
            "-pkeyopt", "ec_paramgen_curve:prime256v1",
            "-keyout", key_path, "-out", cert_path,
            "-subj", "/CN=bing.com", "-days", "1",
        ],
        check=True, capture_output=True,
    )
    return cert_path, key_path


def main():
    workdir = tempfile.mkdtemp(prefix="sbx-smoke-")
    config_path = os.path.join(workdir, "config.json")
    with open(config_path, "w") as f:
        json.dump(CONFIG, f)

    print(f"[test] workdir     = {workdir}")
    print(f"[test] loading     = {SO_PATH}")
    lib = ctypes.CDLL(SO_PATH)

    lib.StartSingBox.argtypes = [ctypes.c_char_p]
    lib.StartSingBox.restype = ctypes.c_int
    lib.StopSingBox.argtypes = []
    # c_int, matching how all three launchers declare it (JNA invokeInt,
    # koffi `int StopSingBox()`, ctypes restype=c_int). Exporting void here
    # would leave them printing a garbage register as the status code.
    lib.StopSingBox.restype = ctypes.c_int
    print("[test] symbols resolved: StartSingBox, StopSingBox")

    failures = []

    # --- Case 1: invalid payload (missing required fields) -> expect 2
    rc = lib.StartSingBox(json.dumps({"disableColor": True}).encode())
    print(f"[test] case1 invalid-payload      rc={rc} (expect 2)")
    if rc != 2:
        failures.append(f"case1: expected rc=2, got {rc}")

    # --- Case 2: nonexistent config file -> expect 3
    payload = {"config": "no-such-file.json", "workingDir": workdir, "disableColor": True}
    rc = lib.StartSingBox(json.dumps(payload).encode())
    print(f"[test] case2 missing-config       rc={rc} (expect 3)")
    if rc != 3:
        failures.append(f"case2: expected rc=3, got {rc}")

    # --- Case 3: valid config, no tunnel -> expect 0
    payload = {"config": "config.json", "workingDir": workdir, "disableColor": True}
    rc = lib.StartSingBox(json.dumps(payload).encode())
    print(f"[test] case3 valid-start          rc={rc} (expect 0)")
    if rc != 0:
        failures.append(f"case3: expected rc=0, got {rc}")
        print("[test] start failed; skipping remaining cases")
        _report(failures)
        return

    time.sleep(2)

    # --- Case 4: verify the inbound is actually listening
    s = socket.socket()
    s.settimeout(3)
    listening = s.connect_ex(("127.0.0.1", 18001)) == 0
    s.close()
    print(f"[test] case4 port-18001-listening  {listening} (expect True)")
    if not listening:
        failures.append("case4: port 18001 not listening after start")

    # --- Case 5: double-start must be rejected -> expect 1
    rc = lib.StartSingBox(json.dumps(payload).encode())
    print(f"[test] case5 double-start         rc={rc} (expect 1)")
    if rc != 1:
        failures.append(f"case5: expected rc=1, got {rc}")

    # --- Case 6: stop, then verify port released
    # The return value is asserted because all three launchers print it as a
    # status code; a void export would make them report a garbage register.
    stop_rc = lib.StopSingBox()
    print(f"[test] case6 stop-returns         rc={stop_rc} (expect 0)")
    if stop_rc != 0:
        failures.append(f"case6: expected StopSingBox rc=0, got {stop_rc}")
    time.sleep(2)
    s = socket.socket()
    s.settimeout(3)
    still_up = s.connect_ex(("127.0.0.1", 18001)) == 0
    s.close()
    print(f"[test] case6 port-released         {not still_up} (expect True)")
    if still_up:
        failures.append("case6: port 18001 still listening after stop")

    # --- Case 7: restart after stop -> expect 0
    rc = lib.StartSingBox(json.dumps(payload).encode())
    print(f"[test] case7 restart-after-stop   rc={rc} (expect 0)")
    if rc != 0:
        failures.append(f"case7: expected rc=0, got {rc}")
    else:
        lib.StopSingBox()

    # --- Case 8: a bad tunnel token must roll sing-box back, not leak the port.
    # Regression guard: an early `return 5` here would leave sing-box running
    # while running==false, making StopSingBox a no-op and leaking the port.
    bad = dict(payload, tunnel={"token": "not-a-valid-token", "backendPort": 18001})
    rc = lib.StartSingBox(json.dumps(bad).encode())
    print(f"[test] case8 bad-tunnel-token     rc={rc} (expect 5)")
    if rc != 5:
        failures.append(f"case8: expected rc=5, got {rc}")

    time.sleep(1)
    s = socket.socket()
    s.settimeout(3)
    leaked = s.connect_ex(("127.0.0.1", 18001)) == 0
    s.close()
    print(f"[test] case8 port-rolled-back      {not leaked} (expect True)")
    if leaked:
        failures.append("case8: port 18001 leaked after tunnel failure")

    # A clean rollback means the next start still succeeds.
    rc = lib.StartSingBox(json.dumps(payload).encode())
    print(f"[test] case8 start-after-rollback rc={rc} (expect 0)")
    if rc != 0:
        failures.append(f"case8: start after rollback expected rc=0, got {rc}")
    else:
        lib.StopSingBox()

    # --- Case 9: build-tag coverage. The widest config the launchers can emit —
    # reality (with_utls), hysteria2 (with_quic) and the WARP wireguard endpoint
    # (with_gvisor). Dropping any of those tags still compiles and still passes
    # `go vet`; only loading the .so catches it. WARP_MODE defaults to empty,
    # not "off", so this is the *default* deployment shape, not an edge case.
    cert_path, key_path = make_cert(workdir)
    full_path = os.path.join(workdir, "config-full.json")
    with open(full_path, "w") as f:
        json.dump(full_config(cert_path, key_path), f)

    full_payload = {"config": "config-full.json", "workingDir": workdir, "disableColor": True}
    rc = lib.StartSingBox(json.dumps(full_payload).encode())
    print(f"[test] case9 full-config-start    rc={rc} (expect 0)")
    if rc != 0:
        failures.append(
            f"case9: expected rc=0, got {rc} "
            "(likely a missing build tag: with_utls / with_quic / with_gvisor)"
        )
    else:
        time.sleep(2)
        for port, label in ((18001, "vmess"), (18002, "reality"), (18003, "hysteria2")):
            s = socket.socket()
            s.settimeout(3)
            # hysteria2 is UDP-only; only probe the TCP listeners.
            up = s.connect_ex(("127.0.0.1", port)) == 0
            s.close()
            if label == "hysteria2":
                continue
            print(f"[test] case9 {label}-listening  {up} (expect True)")
            if not up:
                failures.append(f"case9: {label} port {port} not listening")
        lib.StopSingBox()

    _report(failures)


def _report(failures):
    print()
    if failures:
        print(f"=== SMOKE TEST FAILED ({len(failures)} issue(s)) ===")
        for f in failures:
            print(f"  - {f}")
        sys.exit(1)
    print("=== SMOKE TEST PASSED ===")


if __name__ == "__main__":
    main()
