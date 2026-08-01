#!/bin/bash
# Build both .so artifacts and run the runtime smoke test.
#
# Works on an amd64 or arm64 host, locally (WSL) or in CI. The native target is
# always built; the other one only when its cross toolchain is present, because
# a c-shared CGO build cannot be produced without a matching C compiler.
set -e

# Read from go.mod so there is one source of truth. Pinning a second version
# here already drifted once: go.mod moved to 1.24.7 while this said 1.24.5, and
# nothing failed locally because GOTOOLCHAIN=auto silently fetched the newer
# toolchain. CI with GOTOOLCHAIN=local has no such fallback.
GO_VERSION=$(awk '/^go /{print $2; exit}' "$(dirname "${BASH_SOURCE[0]}")/go.mod")

case "$(uname -m)" in
    x86_64)  HOST_ARCH=amd64; CROSS_PKG=gcc-aarch64-linux-gnu; CROSS_CC=aarch64-linux-gnu-gcc ;;
    aarch64) HOST_ARCH=arm64; CROSS_PKG=gcc-x86-64-linux-gnu;  CROSS_CC=x86_64-linux-gnu-gcc ;;
    *)       echo "unsupported host arch: $(uname -m)" >&2; exit 1 ;;
esac
[ "$HOST_ARCH" = amd64 ] && CROSS_ARCH=arm64 || CROSS_ARCH=amd64

echo "=== Host: $(uname -m) -> $HOST_ARCH (cross target: $CROSS_ARCH) ==="

# WSL leaves apt half-configured often enough that this is worth doing up front.
# Harmless on a clean CI runner.
echo "=== Killing stale apt processes ==="
sudo killall -9 apt-get dpkg 2>/dev/null || true
sudo rm -f /var/lib/dpkg/lock-frontend /var/lib/dpkg/lock /var/cache/apt/archives/lock /var/lib/apt/lists/lock 2>/dev/null || true
sudo dpkg --configure -a 2>/dev/null || true

echo "=== Installing build tools ==="
sudo apt-get update -qq
sudo apt-get install -y -qq make gcc
# Not fatal: without it we simply skip the cross-arch artifact below.
sudo apt-get install -y -qq "$CROSS_PKG" || echo "WARN: $CROSS_PKG unavailable, skipping $CROSS_ARCH"

echo "=== Installing Go $GO_VERSION ==="
if [ ! -f /usr/local/go/bin/go ]; then
    cd /tmp
    # Tarball must match the *host* arch, not the build target.
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-${HOST_ARCH}.tar.gz"
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "go${GO_VERSION}.linux-${HOST_ARCH}.tar.gz"
    rm -f "go${GO_VERSION}.linux-${HOST_ARCH}.tar.gz"
fi

export PATH=$PATH:/usr/local/go/bin
echo "=== Go version ===" && go version
echo "=== GCC version ===" && gcc --version | head -1
if command -v "$CROSS_CC" >/dev/null 2>&1; then
    echo "=== $CROSS_CC version ===" && "$CROSS_CC" --version | head -1
fi

echo "=== Setting up Go project ==="
# Derived from this script's location: no hardcoded checkout path.
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
go mod tidy

# Build tags live in the Makefile only — duplicating them here has already
# caused a drift bug once (missing with_gvisor).
TAGS=$(make -s print-tags)
echo "=== Build tags: $TAGS ==="

echo "=== go vet ==="
go vet -tags "$TAGS" ./...

echo "=== Building $HOST_ARCH (native) ==="
make "linux-$HOST_ARCH"

if command -v "$CROSS_CC" >/dev/null 2>&1; then
    echo "=== Building $CROSS_ARCH (cross) ==="
    make "linux-$CROSS_ARCH"
else
    echo "=== Skipping $CROSS_ARCH: $CROSS_CC not found ==="
fi

echo "=== Build result ==="
ls -lh sbx-linux-*.so sbx-linux-*.h 2>/dev/null

# Compiling is not enough: a missing protocol registry only surfaces at runtime.
# smoke_test.py picks the .so matching this host, so on an arm64 runner this is
# the first time an arm64 artifact is actually loaded and started.
echo "=== Runtime smoke test ($HOST_ARCH) ==="
python3 smoke_test.py

echo "=== DONE ==="
