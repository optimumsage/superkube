# superkube — Makefile
#
# Targets:
#   make build      Build the superkube binary into ./bin/
#   make install    Install superkube and the `sk` symlink into $GOBIN (or $HOME/go/bin)
#   make test       Run unit tests
#   make e2e        Run e2e tests (require a running kind cluster, build tag `e2e`)
#   make lint       Run golangci-lint
#   make fmt        gofmt + goimports
#   make clean      Remove ./bin
#   make tidy       go mod tidy

BIN_DIR    := bin
BIN_NAME   := superkube
PKG        := github.com/optimumsage/superkube
CMD_PKG    := $(PKG)/cmd/superkube

GOBIN      ?= $(shell go env GOBIN)
ifeq ($(GOBIN),)
GOBIN      := $(shell go env GOPATH)/bin
endif

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE       ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS    := -s -w \
              -X $(PKG)/internal/version.Version=$(VERSION) \
              -X $(PKG)/internal/version.Commit=$(COMMIT) \
              -X $(PKG)/internal/version.Date=$(DATE)

.PHONY: build install test e2e lint fmt clean tidy run help

help:
	@grep -E '^[a-zA-Z_-]+:.*?##' Makefile | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

build: ## Build the superkube binary into ./bin
	@mkdir -p $(BIN_DIR)
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN_DIR)/$(BIN_NAME) $(CMD_PKG)
	@echo "built $(BIN_DIR)/$(BIN_NAME) ($(VERSION))"

install: build ## Install superkube and create `sk` symlink in $GOBIN
	@mkdir -p $(GOBIN)
	@install -m 0755 $(BIN_DIR)/$(BIN_NAME) $(GOBIN)/$(BIN_NAME)
	@ln -sf $(GOBIN)/$(BIN_NAME) $(GOBIN)/sk
	@echo "installed $(GOBIN)/$(BIN_NAME) and $(GOBIN)/sk -> $(BIN_NAME)"

test: ## Run unit tests
	go test -race -count=1 ./...

e2e: ## Run e2e tests (requires a reachable kubernetes cluster)
	go test -race -count=1 -tags=e2e ./test/e2e/...

lint: ## Run golangci-lint
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed; see https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run ./...

fmt: ## Format Go sources
	gofmt -s -w .

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR)

tidy: ## go mod tidy
	go mod tidy

run: build ## Build and run with args, e.g. `make run ARGS="get pods"`
	./$(BIN_DIR)/$(BIN_NAME) $(ARGS)
