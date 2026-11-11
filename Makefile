# Every target here is also what CI runs, so a green `make verify` locally means
# a green pipeline.

BINARY   := skupper-mcp-server
PKG      := ./cmd/$(BINARY)
OUT      := bin/$(BINARY)
IMAGE    ?= skupper-mcp-server:dev
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

GO       ?= go
GOFLAGS  := -trimpath

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the server binary into bin/
	$(GO) build $(GOFLAGS) -ldflags="-s -w" -o $(OUT) $(PKG)

.PHONY: run
run: ## Run the server locally against your current kubeconfig context
	SKUPPER_MCP_ALLOWED_NAMESPACES=$${NAMESPACES:-default} \
	SKUPPER_MCP_LOG_FORMAT=text \
	SKUPPER_MCP_LOG_LEVEL=debug \
	$(GO) run $(PKG)

.PHONY: stdio
stdio: ## Run the server on stdio, for MCP Inspector and desktop clients
	SKUPPER_MCP_TRANSPORT=stdio \
	SKUPPER_MCP_ALLOWED_NAMESPACES=$${NAMESPACES:-default} \
	$(GO) run $(PKG)

.PHONY: fmt
fmt: ## Format the code
	gofmt -w .

.PHONY: fmt-check
fmt-check: ## Fail if anything is not gofmt-clean
	@unformatted=$$(gofmt -l .) || exit $$?; \
	if [ -n "$$unformatted" ]; then echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; fi

.PHONY: vet
vet: ## Run go vet
	$(GO) vet ./...

.PHONY: test
test: ## Run the tests with the race detector
	$(GO) test -race ./...

.PHONY: cover
cover: ## Run the tests and print per-function coverage
	$(GO) test -race -coverprofile=coverage.out -covermode=atomic ./...
	$(GO) tool cover -func=coverage.out

.PHONY: cover-html
cover-html: cover ## Open the coverage report in a browser
	$(GO) tool cover -html=coverage.out

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: vulncheck
vulncheck: ## Scan for known vulnerabilities
	govulncheck ./...

.PHONY: verify
verify: fmt-check vet test lint vulncheck manifests ## Everything CI runs (except the Inspector check, which needs node)

.PHONY: manifests
manifests: ## Render every Kustomize overlay
	@find deploy/kustomize/overlays -name kustomization.yaml -print | while IFS= read -r manifest; do \
		overlay=$${manifest%/*}; \
		echo "==> $$overlay"; \
		kubectl kustomize "$$overlay" > /dev/null || exit 1; \
	done

.PHONY: image
image: ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

.PHONY: inspector-e2e
inspector-e2e: ## Drive the real server with MCP Inspector against fixture data (no cluster needed)
	./scripts/inspector-e2e.sh

.PHONY: inspector
inspector: ## Drive the server with MCP Inspector over stdio
	SKUPPER_MCP_TRANSPORT=stdio \
	SKUPPER_MCP_ALLOWED_NAMESPACES=$${NAMESPACES:-default} \
	npx @modelcontextprotocol/inspector $(GO) run $(PKG)

.PHONY: clean
clean: ## Remove build output
	rm -rf bin coverage.out
