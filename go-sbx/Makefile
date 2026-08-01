# with_gvisor is required by the WireGuard endpoint: without it NewDevice falls
# through to newStackDevice, which is stubbed out to ErrGVisorNotIncluded and
# fails box.New at runtime ("gVisor is not included in this build"). WARP_MODE
# defaults to empty (not "off") in all three launchers, so every default
# deployment emits a wireguard endpoint and would fail to start.
TAGS = with_gvisor,with_quic,with_wireguard,with_utls
LDFLAGS = -s -w

.PHONY: linux-amd64 linux-arm64 clean print-tags

# CGO needs a C compiler matching the *target*. When host and target already
# agree (an arm64 runner building arm64), the default cc is correct and the
# cross toolchain is not installed — forcing CC there breaks the build.
HOST_ARCH := $(shell uname -m)

ifeq ($(HOST_ARCH),x86_64)
AMD64_CC =
else
AMD64_CC = CC=x86_64-linux-gnu-gcc
endif

ifeq ($(HOST_ARCH),aarch64)
ARM64_CC =
else
ARM64_CC = CC=aarch64-linux-gnu-gcc
endif

# Consumed by setup.sh so the tag list has a single source of truth.
print-tags:
	@echo $(TAGS)

linux-amd64:
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 $(AMD64_CC) \
		go build -tags $(TAGS) -buildmode=c-shared \
		-ldflags="$(LDFLAGS)" -o sbx-linux-amd64.so

linux-arm64:
	CGO_ENABLED=1 GOOS=linux GOARCH=arm64 $(ARM64_CC) \
		go build -tags $(TAGS) -buildmode=c-shared \
		-ldflags="$(LDFLAGS)" -o sbx-linux-arm64.so

clean:
	rm -f *.so *.h
