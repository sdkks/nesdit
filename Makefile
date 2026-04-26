SHELL      := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

GO        ?= go
BIN       := bin/nesdit
PKGS      := ./...

.PHONY: all build test test-e2e test-all lint docs site clean

all: test-all

build:
	$(GO) build -o $(BIN) ./cmd/nesdit

test:
	$(GO) test -race -count=1 $(PKGS)

test-e2e: build
	$(GO) test -tags=e2e -race -count=1 ./test/e2e/...

test-all: test test-e2e

lint:
	@if ! command -v golangci-lint >/dev/null; then \
		echo "golangci-lint not found on PATH."; \
		echo "Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.59.1"; \
		echo "(CI pins v1.59.1 via golangci/golangci-lint-action; local dev can use a newer v1.x)"; \
		exit 1; \
	fi; \
	golangci-lint run

docs:
	$(GO) run ./cmd/gendocs --out docs/reference
	@if ! command -v mkdocs >/dev/null; then \
		echo "mkdocs not installed — skipping 'mkdocs build --strict'."; \
		echo "Install with: pip install mkdocs (or 'pip install mkdocs-material')."; \
		exit 0; \
	fi; \
	mkdocs build --strict

site:
	@if ! command -v mkdocs >/dev/null; then \
		echo "mkdocs not installed — skipping 'mkdocs build --strict'."; \
		echo "Install with: pip install mkdocs (or 'pip install mkdocs-material')."; \
		exit 0; \
	fi; \
	mkdocs build --strict

clean:
	rm -rf bin/ site/
