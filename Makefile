# Toise — Makefile
# Run `make help` (the default target) to list available targets.

BINARY      := toise
BIN_DIR     := bin
CMD_PKG     := ./cmd/toise
COVERAGE    := coverage.out
COVERAGE_HTML := coverage.html

.DEFAULT_GOAL := help

.PHONY: help build test test-coverage lint fmt tidy proto clean

help: ## Show this help
	@echo "Toise — available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build the toise binary into bin/
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY) $(CMD_PKG)

test: ## Run all tests
	go test ./...

test-coverage: ## Run tests and produce an HTML coverage report
	go test -coverprofile=$(COVERAGE) ./...
	go tool cover -html=$(COVERAGE) -o $(COVERAGE_HTML)
	@echo "Coverage report written to $(COVERAGE_HTML)"

lint: ## Run golangci-lint
	golangci-lint run ./...

fmt: ## Format the code (gofmt -s + goimports)
	gofmt -s -w .
	goimports -w -local github.com/toise-dev/toise .

tidy: ## Tidy go.mod / go.sum
	go mod tidy

proto: ## (placeholder) Generate code from .proto definitions
	@echo "proto generation is not wired up yet."
	@echo "It will be added once the .proto definitions land in proto/toise/v1/."

clean: ## Remove build artifacts and coverage files
	rm -rf $(BIN_DIR) dist $(COVERAGE) $(COVERAGE_HTML)
