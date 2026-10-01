.PHONY: build mihomo run dist clean-dist dist-windows dist-linux dist-darwin fmt-check vet test test-race check-boundary vulncheck vulncheck-mihomo ci clean

# Version = nearest tag (or short sha); injected with the commit via ldflags.
# Release: make dist VERSION=v0.1.0
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo unknown)
PKG      = github.com/akari-projectX/akari-client
LDFLAGS  = -s -w -buildid= -X $(PKG)/internal/version.Version=$(VERSION) -X $(PKG)/internal/version.Commit=$(COMMIT)
BUILD    = go build -trimpath -buildvcs=false
MAIN     = ./cmd/akari-client
EXE     := $(shell go env GOEXE)

# The kernel ships NEXT TO akari-client as a separate executable: mihomo is
# GPL-3.0 and must never be linked into the proprietary client (see
# docs/DECISIONS.md D1). Unmodified upstream source; dependency pins in
# third_party/mihomo/go.mod.
MIHOMO_VERSION = v1.19.32
MIHOMO_LDFLAGS = -s -w -buildid= -X "github.com/metacubex/mihomo/constant.Version=$(MIHOMO_VERSION)"
# $(call mihomo_build,<goos>,<goarch>,<output path>)
mihomo_build = cd third_party/mihomo && GOOS=$(1) GOARCH=$(2) CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags '$(MIHOMO_LDFLAGS)' -o $(3) github.com/metacubex/mihomo

# Host build: bin/akari-client + bin/mihomo.
build: mihomo
	$(BUILD) -ldflags "$(LDFLAGS)" -o bin/akari-client$(EXE) $(MAIN)

mihomo:
	$(call mihomo_build,$(shell go env GOOS),$(shell go env GOARCH),$(CURDIR)/bin/mihomo$(EXE))

# Release layout: dist/<os>-<arch>/{akari-client,mihomo}[.exe] + SHA256SUMS.
#   windows/amd64  pure Go, GUI subsystem (no console window)
#   linux/amd64    pure Go (tray via D-Bus StatusNotifierItem)
#   darwin/*       client needs cgo (systray uses Cocoa) -> macOS host only;
#                  CI builds it on macos-latest. The kernel is pure Go.
dist: clean-dist dist-windows dist-linux
	cd dist && find . -type f ! -name SHA256SUMS | sort | xargs sha256sum > SHA256SUMS

clean-dist:
	rm -rf dist && mkdir -p dist

dist-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(BUILD) -ldflags "$(LDFLAGS) -H windowsgui" -o dist/windows-amd64/akari-client.exe $(MAIN)
	$(call mihomo_build,windows,amd64,$(CURDIR)/dist/windows-amd64/mihomo.exe)
	scripts/licenses.sh dist/windows-amd64 windows

dist-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(BUILD) -ldflags "$(LDFLAGS)" -o dist/linux-amd64/akari-client $(MAIN)
	$(call mihomo_build,linux,amd64,$(CURDIR)/dist/linux-amd64/mihomo)
	scripts/licenses.sh dist/linux-amd64 linux

dist-darwin:
	@[ "$$(uname -s)" = Darwin ] || { echo "dist-darwin needs a macOS host (cgo + Cocoa)"; exit 1; }
	for a in arm64 amd64; do \
	  GOOS=darwin GOARCH=$$a CGO_ENABLED=1 $(BUILD) -ldflags "$(LDFLAGS)" -o dist/darwin-$$a/akari-client $(MAIN) || exit 1; \
	  ($(call mihomo_build,darwin,$$a,$(CURDIR)/dist/darwin-$$a/mihomo)) || exit 1; \
	  scripts/licenses.sh dist/darwin-$$a darwin || exit 1; \
	done

fmt-check:
	@out=$$(gofmt -l .); [ -z "$$out" ] || { echo "gofmt needed:"; echo "$$out"; exit 1; }

# darwin: everything except the cgo-only tray (and main, which imports it)
# is type-checked from Linux; the full darwin build runs on macOS in CI.
vet:
	go vet ./...
	GOOS=windows go vet ./...
	GOOS=darwin CGO_ENABLED=0 go vet $$(go list ./... | grep -v -e /internal/ui/tray -e /cmd/)

# The integration tests drive the real kernel (bin/mihomo).
test: mihomo
	AKARI_REQUIRE_MIHOMO=1 go test -race -count=1 ./...

# License boundary: the client module must not depend on mihomo (GPL-3.0)
# in any way — only third_party/mihomo (a separate module) does.
check-boundary:
	@if go list -deps ./... | grep -q '^github.com/metacubex/'; then \
	  echo "akari-client links mihomo code:"; go list -deps ./... | grep '^github.com/metacubex/'; exit 1; fi
	@if grep -q 'metacubex' go.mod; then echo "go.mod requires a metacubex module"; exit 1; fi

# govulncheck: client source must be clean; the kernel binary is checked
# separately. Allow-listed for the kernel:
#   GO-2026-5932: x/crypto/openpgp (deprecated, no fix) is compiled in via
#   mihomo's self-upgrade endpoint, which the client never calls (the
#   controller is loopback-only with a per-run random secret).
MIHOMO_VULN_ALLOW ?= GO-2026-5932
vulncheck:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

vulncheck-mihomo: mihomo
	@out=$$(go run golang.org/x/vuln/cmd/govulncheck@latest -mode=binary bin/mihomo$(EXE) 2>&1); rc=$$?; echo "$$out"; \
	[ $$rc -eq 0 ] && exit 0; \
	bad=$$(echo "$$out" | grep -oE '^Vulnerability #[0-9]+: GO-[0-9]+-[0-9]+' | grep -oE 'GO-[0-9]+-[0-9]+' | sort -u | grep -vxF "$$(echo $(MIHOMO_VULN_ALLOW) | tr ' ' '\n')"); \
	if [ -n "$$bad" ]; then echo "NEW kernel vulnerabilities: $$bad"; exit 1; fi; \
	echo "kernel: only allow-listed vulnerabilities: $(MIHOMO_VULN_ALLOW)"

ci: fmt-check vet check-boundary test build vulncheck vulncheck-mihomo

run: build
	./bin/akari-client -v

clean:
	rm -rf bin dist
