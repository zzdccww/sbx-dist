# sbx-dist

Public distribution repository for the **sbx** native library — a single CGO
c-shared (`.so`) build that bundles [sing-box](https://github.com/SagerNet/sing-box)
together with an embedded [Cloudflare Tunnel](https://github.com/cloudflare/cloudflared)
provided by [sing-cloudflared](https://github.com/Sagernet/sing-cloudflared).

This repo exists for two reasons:

1. **Binary distribution.** Prebuilt `sbx-linux-amd64.so` / `sbx-linux-arm64.so`
   (plus `.sha256` checksums) are published as GitHub Releases under the
   `sbx-<sing-box-version>-<run-number>` tag scheme, and fetched via
   `https://github.com/zzdccww/sbx-dist/releases/latest/download/sbx-linux-{amd64|arm64}.so`.
2. **GPL-3.0 source availability.** The complete corresponding source of the
   `.so` lives under [`go-sbx/`](./go-sbx). This directory builds, byte-for-byte
   (modulo toolchain), the binaries shipped in Releases, so anyone who receives
   a `.so` from this repo can rebuild it and exercise their GPL-3.0 rights.

## What's here

```
go-sbx/        CGO c-shared wrapper source — builds sbx.so
                   main.go / payload.go / registry.go   the wrapper
                   Makefile                              build targets
                   go.mod / go.sum                       exact dependency versions
                   .gitignore                            keeps *.so / *.h out of source
                   setup.sh                              host-arch build + smoke test
                   smoke_test.py / concurrency_test.py   ctypes FFI tests
                   e2e_run.sh / e2e_tunnel.sh            real-tunnel end-to-end scripts
LICENSE         GNU GPL v3, Version 3, 29 June 2007
```

Prebuilt artifacts (`.so`, `.h`, `.sha256`) are **not** committed; they are
produced by the build and released as GitHub Release assets. The `.h` header
is discarded by `.gitignore` because the three launchers declare the ABI
themselves and only ever load `sbx.so`.

## Upstream versions (pinned in `go-sbx/go.mod`)

| Dependency | Version |
| --- | --- |
| `github.com/sagernet/sing-box` | `v1.14.0-beta.2` |
| `github.com/sagernet/sing` | `v0.8.12-0.20260721063414-596db5dd6ef4` |
| `github.com/sagernet/sing-cloudflared` | `v0.1.3-0.20260706062323-d9787e794aa3` |
| Go toolchain | `1.24.7` (see `go.mod`) |

Each Release tag records the exact `sing-box` upstream version it was built from.

## Reproducing a build

The wrapper is plain CGO; `sbx.so` is a normal Go c-shared library, **no
forked** upstream is required — `go.mod` pins stock `sing-box`, `sing`, and
`sing-cloudflared` releases.

### Prerequisites

- Linux `amd64` or `arm64` host (the `.so` targets Linux only).
- Go matching the toolchain line in `go-sbx/go.mod` (1.24.x).
- A C compiler matching the target (`gcc`/`g++`, or a cross toolchain when host
  and target differ — see the `Makefile` CC-detection comments).
- `make`.

### Build (host architecture)

```bash
cd go-sbx
make print-tags            # show the build tags, optional sanity check
make linux-amd64           # or linux-arm64 on an arm64 host
# -> sbx-linux-amd64.so  (and sbx-linux-amd64.h, ignored by launchers)
```

`TAGS = with_gvisor,with_quic,with_wireguard,with_utls` — `with_gvisor` is
**required** for the WireGuard endpoint (Cloudflare WARP); without it
`NewDevice` falls through to a stub that fails `box.New` at runtime.

### Cross-build (optional, not used by CI)

CI builds each arch on a **native** runner, which is the supported path. The
`Makefile` CC-detection block explains why forcing a cross `CC` on a native
runner breaks the build. For cross-compilation you must point `CC` at a
cross toolchain yourself:

```bash
make linux-arm64 CC=aarch64-linux-gnu-gcc GOARCH=arm64 GOAMD64=
```

### Verify the FFI (host arch)

After building, the ctypes smoke test loads the freshly built `.so` and runs
the same ABI path the Java/Node/Python launchers use:

```bash
python3 smoke_test.py        # 9 cases
python3 concurrency_test.py  # StartSingBox / StopSingBox under concurrency
```

### Real Cloudflare Tunnel end-to-end (optional)

`e2e_tunnel.sh` provisions an actual fixed Cloudflare Tunnel via
`sing-cloudflared` and validates that sing-box + tunnel come up together. It
needs `ARGO_AUTH` / `ARGO_DOMAIN` env vars set and is **not** run in CI.

## ABI summary

`sbx.so` exports two symbols, both `//export`-ed in `go-sbx/main.go`:

| Symbol | Purpose |
| --- | --- |
| `StartSingBox` | Start sing-box with a JSON payload (`config`, `workingDir`, `disableColor`, optional `tunnel{ token, hostname, backendPort }`). Blocking until stop. |
| `StopSingBox` | Signal the running instance to stop and release the lock. |

The launchers (Java via JNA, Node via koffi, Python via ctypes) declare this
ABI themselves; only the `.so` is consumed. The `.h` header is not used.

## License

Copyright (C) 2026 zzdccww.

Everything in this repository is licensed under the **GNU General Public
License v3.0** — see [`LICENSE`](./LICENSE). The prebuilt `.so` artifacts in
Releases are distributed under the same license; the corresponding complete
source is the contents of `go-sbx/` at the matching Release tag.

`sing-box`, `sing`, and `sing-cloudflared` are © their respective upstream
authors and licensed under their own terms (GPL-3.0); pinning `go.mod` to
their published releases reproduces exactly what gets linked into `sbx.so`.
