GO         ?= go
BIN        ?= bin/contemper
PKG        := github.com/contemper-project/contemper/internal/buildinfo
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE       ?= $(shell git log -1 --format=%cI 2>/dev/null)
LDFLAGS    := -X $(PKG).version=$(VERSION) -X $(PKG).commit=$(COMMIT) -X $(PKG).date=$(DATE)

.PHONY: build test vet lint stubs example e2e e2e-variants e2e-volumes clean

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/contemper

test:
	$(GO) vet ./...
	$(GO) test ./...

vet:
	$(GO) vet ./...

lint:
	golangci-lint run ./...

stubs:
	./hack/fetch-stubs.sh

example:
	podman build -t contemper-example:dev examples/alpine

e2e:
	./hack/e2e.sh $(if $(EXAMPLE),--example $(EXAMPLE))

e2e-variants:
	./hack/e2e-variants.sh

e2e-volumes:
	./hack/e2e-volumes.sh $(if $(EXAMPLE),--example $(EXAMPLE))

clean:
	rm -rf bin _out
