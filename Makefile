BINARY  = git-remote-arweave
VERSION = $(shell git describe --tags --always --dirty)
LDFLAGS = -X main.version=$(VERSION)

.PHONY: build install test clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/git-remote-arweave/

install: build
	install -m 755 $(BINARY) $(shell go env GOPATH)/bin/

test:
	go test ./...

clean:
	rm -f $(BINARY)
