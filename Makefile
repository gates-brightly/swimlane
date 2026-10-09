BINARY  := swim
PKG     := ./cmd/swim
BIN_DIR := bin
LINK_DIR ?= $(HOME)/.local/bin
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X swim/internal/cli.Version=$(VERSION)

.DEFAULT_GOAL := help

.PHONY: help build install link unlink run test test-unit test-e2e cover fmt vet tidy check clean

help: ## Show available targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Build the swim binary into ./bin
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(PKG)

install: ## Install swim into $GOBIN (or $GOPATH/bin)
	go install -ldflags "$(LDFLAGS)" $(PKG)

link: build ## Symlink bin/swim into LINK_DIR (default ~/.local/bin) so it's on your PATH
	@mkdir -p $(LINK_DIR)
	ln -sf $(CURDIR)/$(BIN_DIR)/$(BINARY) $(LINK_DIR)/$(BINARY)
	@case ":$$PATH:" in *":$(LINK_DIR):"*) ;; *) echo "note: $(LINK_DIR) is not on your PATH; add: export PATH=\"$(LINK_DIR):\$$PATH\"";; esac

unlink: ## Remove the symlink created by 'make link'
	@if [ -L $(LINK_DIR)/$(BINARY) ]; then rm $(LINK_DIR)/$(BINARY) && echo "removed $(LINK_DIR)/$(BINARY)"; else echo "no symlink at $(LINK_DIR)/$(BINARY)"; fi

run: build ## Build and run swim (pass args with ARGS="...")
	./$(BIN_DIR)/$(BINARY) $(ARGS)

test: ## Run all tests, including e2e
	go test ./...

test-unit: ## Run unit tests only (skip e2e)
	go test $$(go list ./... | grep -v /internal/e2e)

test-e2e: ## Run end-to-end tests against the built binary
	go test -count=1 ./internal/e2e/...

cover: ## Run tests with a coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

fmt: ## Format Go sources
	gofmt -s -w .

vet: ## Run go vet
	go vet ./...

tidy: ## Tidy go.mod / go.sum
	go mod tidy

check: ## Verify formatting, vet, and tests (CI-style)
	@test -z "$$(gofmt -s -l .)" || { echo "gofmt needed on:"; gofmt -s -l .; exit 1; }
	go vet ./...
	go test ./...

clean: ## Remove build and coverage artifacts
	rm -rf $(BIN_DIR) coverage.out
