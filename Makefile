BINARY  = git-remote-arweave
CLI     = arweave-git
VERSION = $(shell git describe --tags --always --dirty)
LDFLAGS = -X main.version=$(VERSION)

.PHONY: build install test clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/git-remote-arweave/
	go build -o $(CLI) ./cmd/arweave-git/

install: build
	install -m 755 $(BINARY) $(shell go env GOPATH)/bin/
	install -m 755 $(CLI) $(shell go env GOPATH)/bin/

test:
	go test ./...

clean:
	rm -f $(BINARY) $(CLI)
