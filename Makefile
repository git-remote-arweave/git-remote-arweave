BINARY  = git-remote-arweave
CLI     = arweave-git
VERSION = $(shell git describe --tags --always --dirty)
LDFLAGS = -X main.version=$(VERSION)

.PHONY: build install test lint test-integration test-all clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/git-remote-arweave/
	go build -o $(CLI) ./cmd/arweave-git/

install: build
	install -m 755 $(BINARY) $(shell go env GOPATH)/bin/
	install -m 755 $(CLI) $(shell go env GOPATH)/bin/

test:
	go test ./...

lint:
	golangci-lint run ./...

test-integration:
	INTEGRATION=1 go test -v -timeout 120s ./internal/integration/

test-all: test test-integration

clean:
	rm -f $(BINARY) $(CLI)
