GO                    ?= go
BIN                   ?= bin/contemper
PKG                   := github.com/contemper-project/contemper/internal/buildinfo
VERSION               ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT                ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE                  ?= $(shell git log -1 --format=%cI 2>/dev/null)
LDFLAGS               := -X $(PKG).version=$(VERSION) -X $(PKG).commit=$(COMMIT) -X $(PKG).date=$(DATE)

# The golangci-lint version CI's lint job runs (.golangci-lint-version,
# read by both this Makefile and the golangci-lint-action `version-file`
# input, so the two can't drift) and where `make lint` caches its
# release binary once downloaded. golangci-lint's install docs
# discourage building it with `go install`/`go run`/`go tool` (the
# result depends on the local Go toolchain and isn't the tested release
# artifact), so hack/install-golangci-lint.sh downloads the release
# binary and verifies its checksum instead. `make clean` removes it
# along with the rest of bin/.
GOLANGCI_LINT_VERSION := $(shell cat .golangci-lint-version)
GOLANGCI_LINT_DIR     := bin/golangci-lint-$(GOLANGCI_LINT_VERSION)
GOLANGCI_LINT         := $(GOLANGCI_LINT_DIR)/golangci-lint

.PHONY: build test vet lint actionlint stubs example e2e e2e-variants e2e-volumes e2e-bootloader clean

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/contemper

test:
	$(GO) vet ./...
	$(GO) test ./...

vet:
	$(GO) vet ./...

$(GOLANGCI_LINT):
	./hack/install-golangci-lint.sh $(GOLANGCI_LINT_VERSION) $(GOLANGCI_LINT_DIR)

lint: $(GOLANGCI_LINT)
	$(GOLANGCI_LINT) run ./...
	$(GO) tool actionlint

actionlint:
	$(GO) tool actionlint

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

e2e-bootloader:
	./hack/e2e-bootloader.sh

clean:
	rm -rf bin _out
