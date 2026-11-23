.PHONY: build vet test cross clean windows check-build-matrix check-version-artifact

GOCMD=/opt/data/go/bin/go1.23.12
GO123=/opt/data/go/bin/go1.23.12
GOBUILD=$(GOCMD) build
GOVET=$(GOCMD) vet

build:
	$(GOBUILD) -o ./cmd/probe-client/probe-client ./cmd/probe-client/
	$(GOBUILD) -o ./cmd/probe-server/probe-server ./cmd/probe-server/

vet:
	$(GOVET) ./...

test:
	$(GOCMD) test ./... -v -count=1

cross: check-version-artifact
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o ./build/probe-client-linux-amd64 ./cmd/probe-client/
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o ./build/probe-client-linux-arm64 ./cmd/probe-client/
	GOOS=windows GOARCH=amd64 $(GOBUILD) -ldflags "-X main.configB64=" -o ./build/probe-client-windows-amd64.exe ./cmd/probe-client/
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o ./build/probe-client-darwin-amd64 ./cmd/probe-client/
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -o ./build/probe-client-darwin-arm64 ./cmd/probe-client/

clean:
	rm -rf ./build/
	rm -f ./cmd/probe-client/probe-client
	rm -f ./cmd/probe-server/probe-server

windows: check-version-artifact
	GOTOOLCHAIN=local GOOS=windows GOARCH=amd64 $(GO123) build -o ./build/ProbeClient.exe ./cmd/probe-client/

# check-version-artifact is P8-12 F6: the version gate in internal/upgrade is
# only meaningful if the binary this branch builds can actually answer
# `--version` with the intended semver. Build one with -X and assert the exact
# output, so a release that skips the linkage fails here instead of passing a
# manifest it never proved.
VERSION ?= 1.19.1
check-version-artifact:
	@mkdir -p ./build/matrix
	@GOFLAGS=-mod=vendor $(GOBUILD) -trimpath \
	  -ldflags "-X github.com/falke-ai-circuit/probe/internal/version.Semver=$(VERSION)" \
	  -o ./build/matrix/version-check ./cmd/probe-client/
	@got=$$(./build/matrix/version-check --version); rc=$$?; \
	if [ $$rc -ne 0 ]; then echo "FAIL: --version exited $$rc"; exit 1; fi; \
	if [ "$$got" != "$(VERSION)" ]; then \
	  echo "FAIL: --version printed '$$got', want '$(VERSION)'"; exit 1; fi; \
	echo "version artifact OK: --version printed $(VERSION), exit 0"

# check-build-matrix compiles every client entrypoint for every target the fleet
# ships to. P8-04 shipped a guardian that only built on linux (undefined itoa in
# jobs_windows.go), so "compiles for windows" is a check here, not a hope.
check-build-matrix:
	@set -e; mkdir -p ./build/matrix; \
	for os in windows linux darwin; do \
	  for pkg in ./cmd/probe-client/ ./cmd/probe/; do \
	    name=$$(basename $$pkg); \
	    echo "GOOS=$$os GOARCH=amd64 $$pkg"; \
	    GOFLAGS=-mod=vendor GOOS=$$os GOARCH=amd64 $(GOBUILD) -trimpath \
	      -o ./build/matrix/$$name-$$os-amd64 $$pkg; \
	  done; \
	done; \
	echo "build matrix OK (windows/linux/darwin amd64 x probe-client/probe)"
