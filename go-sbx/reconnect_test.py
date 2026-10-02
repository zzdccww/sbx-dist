#!/usr/bin/env python3
"""
Tunnel reconnection test for go-sbx watchdog mechanism.

Test flow:
1. Start with valid ARGO_AUTH token
2. Verify tunnel service is running
3. Manually simulate disconnection (requires direct .so access)
4. Wait for watchdog to detect and reconnect
5. Verify tunnel service restored

Requirements:
- ARGO_AUTH environment variable with valid token
- Manual observation of stderr logs

Expected logs:
[sbx-watchdog] health check failed (1/3)
[sbx-watchdog] health check failed (2/3)
[sbx-watchdog] health check failed (3/3)
[sbx-watchdog] tunnel unhealthy, reconnecting...
[sbx-watchdog] tunnel reconnected successfully
"""

import os
import sys
import ctypes
import json
import time

# Load the shared library
so_path = os.environ.get('SBX_SO', './sbx.so')
lib = ctypes.CDLL(so_path)

lib.StartSingBox.argtypes = [ctypes.c_char_p]
lib.StartSingBox.restype = ctypes.c_int
lib.StopSingBox.argtypes = []
lib.StopSingBox.restype = ctypes.c_int

def test_reconnect():
    """Test watchdog reconnection mechanism."""

    # Check for ARGO_AUTH
    token = os.environ.get('ARGO_AUTH')
    if not token:
        print("[reconnect-test] SKIP: ARGO_AUTH not set (manual test only)")
        return True

    print("[reconnect-test] Starting with valid tunnel config...")

    payload = {
        "config": "D:\\sbx-native\\go-sbx\\fixtures\\valid.json",
        "working_dir": "D:\\sbx-native\\go-sbx\\fixtures",
        "tunnel": {
            "token": token,
            "hostname": "test.example.com"
        }
    }

    rc = lib.StartSingBox(json.dumps(payload).encode('utf-8'))
    if rc != 0:
        print(f"[reconnect-test] FAIL: StartSingBox returned {rc}")
        return False

    print("[reconnect-test] Service started successfully")
    print("[reconnect-test] Watchdog should be running in background")
    print("[reconnect-test]")
    print("[reconnect-test] MANUAL TEST PROCEDURE:")
    print("[reconnect-test] 1. Observe logs for normal health checks (silent)")
    print("[reconnect-test] 2. Simulate disconnection by blocking edge access")
    print("[reconnect-test] 3. Wait for 3 consecutive failures (~6 minutes)")
    print("[reconnect-test] 4. Observe reconnection attempt logs")
    print("[reconnect-test] 5. Verify 'tunnel reconnected successfully' appears")
    print("[reconnect-test]")
    print("[reconnect-test] Press Enter to stop service...")

    input()

    print("[reconnect-test] Stopping service...")
    rc = lib.StopSingBox()
    if rc != 0:
        print(f"[reconnect-test] WARN: StopSingBox returned {rc}")

    print("[reconnect-test] Service stopped")
    print("[reconnect-test] PASS (manual verification required)")
    return True

if __name__ == '__main__':
    try:
        success = test_reconnect()
        sys.exit(0 if success else 1)
    except Exception as e:
        print(f"[reconnect-test] FAIL: {e}")
        sys.exit(1)
