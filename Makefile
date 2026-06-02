# Toise — Makefile
# Run `make help` (the default target) to list available targets.

BINARY       := toise-server
DEMO_BINARY  := toise-demo
PROBE_BINARY := toise-probe
BIN_DIR      := bin
CMD_PKG      := ./cmd/toise-server
DEMO_PKG     := ./cmd/toise-demo
PROBE_PKG    := ./cmd/toise-probe
COVERAGE    := coverage.out
COVERAGE_HTML := coverage.html

# Version stamped into the binaries. Defaults derive from git: on a tagged
# commit, VERSION is the tag (e.g. 0.1.0, no "v" prefix); otherwise the short
# commit. Override with `make build VERSION=0.1.0`.
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
VERSION_PKG := github.com/toise-dev/toise/internal/version
LDFLAGS     := -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT)

.DEFAULT_GOAL := help

.PHONY: help build test test-coverage bench lint fmt tidy proto clean

help: ## Show this help
	@echo "Toise — available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build the toise-server, toise-demo and toise-probe binaries into bin/
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(CMD_PKG)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(DEMO_BINARY) $(DEMO_PKG)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(PROBE_BINARY) $(PROBE_PKG)

test: ## Run all tests
	go test ./...

test-coverage: ## Run tests and produce an HTML coverage report
	go test -coverprofile=$(COVERAGE) ./...
	go tool cover -html=$(COVERAGE) -o $(COVERAGE_HTML)
	@echo "Coverage report written to $(COVERAGE_HTML)"

bench: ## Run all benchmarks
	go test -run '^$$' -bench=. -benchmem ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Format the code (gofmt -s + goimports)
	gofmt -s -w .
	goimports -w -local github.com/toise-dev/toise .

tidy: ## Tidy go.mod / go.sum
	go mod tidy

proto: ## Generate Go code from proto/ definitions (requires buf)
	buf generate

clean: ## Remove build artifacts and coverage files
	rm -rf $(BIN_DIR) dist $(COVERAGE) $(COVERAGE_HTML)
