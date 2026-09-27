GO       ?= go
BIN      ?= bin/contemper
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)

.PHONY: build test vet stubs example e2e e2e-variants clean

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/contemper

test:
	$(GO) vet ./...
	$(GO) test ./...

vet:
	$(GO) vet ./...

stubs:
	./hack/fetch-stubs.sh

example:
	podman build -t contemper-example:dev examples/alpine

e2e:
	./hack/e2e.sh

e2e-variants:
	./hack/e2e-variants.sh

clean:
	rm -rf bin _out
