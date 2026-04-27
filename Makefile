SHELL      := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

GO        ?= go
BIN       := bin/nesdit
PKGS      := ./...

.PHONY: all build test test-e2e test-all lint check-pins docs site clean

all: test-all

build:
	$(GO) build -ldflags='-s -w' -o $(BIN) ./cmd/nesdit

test:
	$(GO) test -race -count=1 $(PKGS)

test-e2e: build
	$(GO) test -tags=e2e -race -count=1 ./test/e2e/...

test-all: lint test test-e2e

lint:
	@LINT=golangci-lint; \
	if ! command -v $$LINT >/dev/null; then \
		if [ -x "$$(go env GOPATH)/bin/golangci-lint" ]; then \
			LINT="$$(go env GOPATH)/bin/golangci-lint"; \
		else \
			echo "golangci-lint not found on PATH or in \$$(go env GOPATH)/bin."; \
			echo "Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@v2.11.4"; \
			echo "(CI pins v2.11.4 via golangci/golangci-lint-action@v8.0.0 — same binary as local)"; \
			exit 1; \
		fi; \
	fi; \
	$$LINT run

check-pins:
	@bash scripts/check-action-pins.sh

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
