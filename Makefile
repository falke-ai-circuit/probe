.PHONY: build vet test cross clean windows

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

cross:
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o ./build/probe-client-linux-amd64 ./cmd/probe-client/
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o ./build/probe-client-linux-arm64 ./cmd/probe-client/
	GOOS=windows GOARCH=amd64 $(GOBUILD) -ldflags "-X main.configB64=" -o ./build/probe-client-windows-amd64.exe ./cmd/probe-client/
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o ./build/probe-client-darwin-amd64 ./cmd/probe-client/
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -o ./build/probe-client-darwin-arm64 ./cmd/probe-client/

clean:
	rm -rf ./build/
	rm -f ./cmd/probe-client/probe-client
	rm -f ./cmd/probe-server/probe-server

windows:
	GOTOOLCHAIN=local GOOS=windows GOARCH=amd64 $(GO123) build -o ./build/ProbeClient.exe ./cmd/probe-client/
