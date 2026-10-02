# sbx-native dependency patch

Source: <https://github.com/SagerNet/sing-cloudflared>

Pinned revision: `d9787e794aa394c49449087b185eda8708070a62`
(`v0.1.3-0.20260706062323-d9787e794aa3`). The complete Go module snapshot and
its GPL-3.0 license are retained here. The license text only gains a final
newline compared with the module-cache copy.

`go-sbx/go.mod` uses a local `replace` so builds do not depend on modified
global module caches, network patching, or an unpublished remote fork.

## EDUPCONN registration recovery

Production diagnostics observed QUIC/HTTP2 reconnects receiving `EDUPCONN`
with `ShouldRetry=false`. Upstream wrapped that response as permanent, so
`Service.superviseConnection` returned and never replenished that connection.
Two of four connection tasks exited this way while every TCP edge probe passed.

`internal/control/control.go` now treats the exact `EDUPCONN` cause as
`protocol.RetryableError`. All other registration responses retain their
retry flag and delay semantics. Existing supervision handles backoff, edge
rotation and cancellation; no extra supervisor or log parsing is introduced.

Official behavior used as reference (read 2026-09-24):

- `cloudflare/cloudflared/connection/control.go`, blob
  `a9f4f2a200defd5396fad411622733dce2b811f4`: duplicate registration maps to
  `ErrDuplicateConnection` before permanent-error classification.
- `cloudflare/cloudflared/supervisor/tunnel.go`, blob
  `01cca579321338194dae1ae9ba98d49c3116d12c`: duplicate connections cause an
  edge-address change.
- `cloudflare/cloudflared/supervisor/supervisor.go`, blob
  `b2c19e79f28f507e63f73ecd20e6ac4126a52279`: duplicate registration is retryable
  during startup, including wrapped errors.

Regression test: `internal/control/registration_error_test.go`. From `go-sbx`:

```sh
go test -race -tags "$(make -s print-tags)" github.com/sagernet/sing-cloudflared/internal/control
```

The two duplicate/permanent cases failed before the behavior change. Other
permanent authentication/configuration failures and exact-match boundaries
are covered. The unrelated upstream root integration tests still reference
older ICMP APIs and are not claimed as passing under this project's graph.

When upstream includes equivalent behavior, replace this directory override
with a tested upstream version and retain the regression coverage. No FFI,
launcher, QUIC timeout or outer watchdog behavior changes are part of this patch.
