VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
PREFIX  ?= $(HOME)/.local
BIN     := bin/gitident

.PHONY: all build test lint fmt install uninstall snapshot clean

all: lint test build

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/gitident
	ln -sf gitident bin/git-ident

test:
	go test ./...

lint:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)
	go vet ./...
	@command -v staticcheck >/dev/null && staticcheck ./... || echo "staticcheck not installed (go install honnef.co/go/tools/cmd/staticcheck@latest)"

fmt:
	gofmt -w .

# Installs gitident and the git-ident symlink into $(PREFIX)/bin.
install: build
	install -d $(PREFIX)/bin
	install -m 0755 $(BIN) $(PREFIX)/bin/gitident
	ln -sf gitident $(PREFIX)/bin/git-ident

uninstall:
	rm -f $(PREFIX)/bin/gitident $(PREFIX)/bin/git-ident

# Local release dry run (needs goreleaser).
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin dist
