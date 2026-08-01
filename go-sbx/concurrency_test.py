#!/usr/bin/env python3
"""Exercise the concurrent paths in StartSingBox/StopSingBox.

The three-state rewrite exists so StopSingBox can interrupt a StartSingBox
that is blocked downloading remote rule_sets. Every case here needs that
mid-flight window, which the sequential smoke test never produces.

A rule_set URL pointing at a local tarpit (accepts, never responds) makes
box.Start() stall, giving a deterministic window to call StopSingBox from
another thread.
"""
import ctypes
import json
import os
import platform
import socket
import sys
import tempfile
import threading
import time

# Same resolution rule as smoke_test.py: .so next to this script, SBX_SO wins.
_ARCH = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine(), platform.machine())
SO_PATH = os.environ.get(
    "SBX_SO",
    os.path.join(os.path.dirname(os.path.abspath(__file__)), f"sbx-linux-{_ARCH}.so"),
)
UUID = "9c8e1a2b-3d4f-5061-7283-94a5b6c7d8e9"
PORT = 18021


def start_tarpit():
    """Listen on localhost, accept, then never write a byte.

    An unroutable address (TEST-NET-3) was used here before, but how long a
    SYN to a blackhole takes before failing is environment-specific: WSL
    retransmits well past the probe window while a GitHub runner surfaces
    "i/o timeout" in 5s, which made the premise fail and aborted the run.

    Connecting to localhost always succeeds instantly, so no dial timeout is
    involved; the fetch then blocks reading the TLS ServerHello that never
    comes. That stalls for as long as the test needs, on any host.
    """
    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", 0))
    srv.listen(8)
    held = []

    def accept_loop():
        while True:
            try:
                conn, _ = srv.accept()
            except OSError:
                return
            held.append(conn)  # keep it open; never respond

    threading.Thread(target=accept_loop, daemon=True).start()
    return srv, srv.getsockname()[1], held


def stalling_config(stall_url):
    return {
        "log": {"level": "info"},
        "inbounds": [{
            "type": "vmess", "tag": "in",
            "listen": "127.0.0.1", "listen_port": PORT,
            "users": [{"uuid": UUID}],
            "transport": {"type": "ws", "path": "/vmess-argo"},
        }],
        "outbounds": [{"type": "direct", "tag": "direct"}],
        "route": {
            "rule_set": [{
                "tag": "tarpit", "type": "remote",
                "format": "binary", "url": stall_url,
            }],
            "final": "direct",
        },
    }


def fast_config():
    return {
        "log": {"level": "info"},
        "inbounds": [{
            "type": "vmess", "tag": "in",
            "listen": "127.0.0.1", "listen_port": PORT,
            "users": [{"uuid": UUID}],
            "transport": {"type": "ws", "path": "/vmess-argo"},
        }],
        "outbounds": [{"type": "direct", "tag": "direct"}],
    }


def port_open(port):
    s = socket.socket()
    s.settimeout(2)
    try:
        return s.connect_ex(("127.0.0.1", port)) == 0
    finally:
        s.close()


def main():
    # Closed in finally so the listener never outlives the run — including the
    # caseA abort path, where _report exits the process.
    tarpit, tarpit_port, _held = start_tarpit()
    try:
        _run(tarpit_port)
    finally:
        tarpit.close()


def _run(tarpit_port):
    workdir = tempfile.mkdtemp(prefix="sbx-conc-")
    stall_url = f"https://127.0.0.1:{tarpit_port}/tarpit.srs"
    with open(os.path.join(workdir, "stall.json"), "w") as f:
        json.dump(stalling_config(stall_url), f)
    with open(os.path.join(workdir, "fast.json"), "w") as f:
        json.dump(fast_config(), f)

    lib = ctypes.CDLL(SO_PATH)
    lib.StartSingBox.argtypes = [ctypes.c_char_p]
    lib.StartSingBox.restype = ctypes.c_int
    lib.StopSingBox.argtypes = []
    lib.StopSingBox.restype = None

    stall = json.dumps(
        {"config": "stall.json", "workingDir": workdir, "disableColor": True}
    ).encode()
    fast = json.dumps(
        {"config": "fast.json", "workingDir": workdir, "disableColor": True}
    ).encode()

    failures = []

    # --- Case A: does box.Start() actually stall on the tarpit rule_set?
    # If it returns immediately the rest of this file proves nothing, so
    # establish the premise before relying on it.
    print("[conc] caseA probing whether a tarpit rule_set stalls start...")
    result = {}
    t = threading.Thread(target=lambda: result.update(rc=lib.StartSingBox(stall)))
    t.start()
    t.join(timeout=6)
    stalled = t.is_alive()
    print(f"[conc] caseA start-stalls        {stalled} (expect True)")
    if not stalled:
        print(f"[conc]       start returned rc={result.get('rc')} in under 6s;")
        print("[conc]       cannot exercise the mid-flight window — aborting.")
        failures.append("caseA: premise failed, start did not stall")
        _report(failures)
        return

    # --- Case B: StopSingBox must interrupt the stalled start, not block on mu.
    # The whole point of releasing the lock for the slow phase.
    print("[conc] caseB calling StopSingBox against the stalled start...")
    stop_done = threading.Event()
    threading.Thread(
        target=lambda: (lib.StopSingBox(), stop_done.set())
    ).start()

    stopped_in_time = stop_done.wait(timeout=20)
    print(f"[conc] caseB stop-returns        {stopped_in_time} (expect True)")
    if not stopped_in_time:
        failures.append("caseB: StopSingBox blocked >20s on a stalled start")

    # The starter must also unwind rather than hang forever.
    t.join(timeout=20)
    starter_finished = not t.is_alive()
    print(f"[conc] caseB starter-unwinds     {starter_finished} (expect True)")
    if not starter_finished:
        failures.append("caseB: StartSingBox never returned after abort")
    else:
        print(f"[conc] caseB aborted start rc={result.get('rc')} (expect 4)")
        if result.get("rc") != 4:
            failures.append(f"caseB: expected rc=4 on abort, got {result.get('rc')}")

    # --- Case C: the aborted start must leave no listener behind.
    time.sleep(1)
    leaked = port_open(PORT)
    print(f"[conc] caseC no-port-leak        {not leaked} (expect True)")
    if leaked:
        failures.append(f"caseC: port {PORT} still listening after aborted start")

    # --- Case D: state must be back to idle, so a normal start still works.
    rc = lib.StartSingBox(fast)
    print(f"[conc] caseD start-after-abort   rc={rc} (expect 0)")
    if rc != 0:
        failures.append(f"caseD: expected rc=0 after abort, got {rc}")
    else:
        time.sleep(1)
        up = port_open(PORT)
        print(f"[conc] caseD port-listening      {up} (expect True)")
        if not up:
            failures.append("caseD: port not listening after recovery start")
        lib.StopSingBox()

    # --- Case E: concurrent StopSingBox calls must not double-close or panic.
    rc = lib.StartSingBox(fast)
    if rc != 0:
        failures.append(f"caseE: setup start failed rc={rc}")
    else:
        errs = []
        threads = [
            threading.Thread(target=lambda: _guard(lib.StopSingBox, errs))
            for _ in range(4)
        ]
        for th in threads:
            th.start()
        for th in threads:
            th.join(timeout=15)
        alive = [th for th in threads if th.is_alive()]
        print(f"[conc] caseE 4x-concurrent-stop  {not alive and not errs} (expect True)")
        if alive:
            failures.append(f"caseE: {len(alive)} StopSingBox calls hung")
        if errs:
            failures.append(f"caseE: StopSingBox raised {errs}")

    # --- Case F: still healthy after all that churn.
    rc = lib.StartSingBox(fast)
    print(f"[conc] caseF final-start         rc={rc} (expect 0)")
    if rc != 0:
        failures.append(f"caseF: expected rc=0, got {rc}")
    else:
        lib.StopSingBox()

    _report(failures)


def _guard(fn, errs):
    try:
        fn()
    except Exception as exc:  # noqa: BLE001
        errs.append(repr(exc))


def _report(failures):
    print()
    if failures:
        print(f"=== CONCURRENCY TEST FAILED ({len(failures)} issue(s)) ===")
        for f in failures:
            print(f"  - {f}")
        sys.exit(1)
    print("=== CONCURRENCY TEST PASSED ===")


if __name__ == "__main__":
    main()
