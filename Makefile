SHELL      := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

GO        ?= go
BIN       := bin/nesdit
PKGS      := ./...

.PHONY: all build install test test-e2e test-release test-install test-all lint check-pins docs site clean bump-version release release-cli

# Pass overrides as data, not interpolated shell source in recipes.
export BUMP GO

all: test-all

build:
	$(GO) build -ldflags='-s -w' -o $(BIN) ./cmd/nesdit

install: export GOBIN := $(GOBIN)
install: export GOPATH := $(GOPATH)
install:
	bash scripts/install.sh

test:
	$(GO) test -race -count=1 $(PKGS)

test-e2e: build
	$(GO) test -tags=e2e -race -count=1 ./test/e2e/...

test-release:
	bash test/release/release_test.sh

test-install:
	bash test/install/install_test.sh

test-all: lint test test-e2e test-release test-install

lint:
	@LINT=golangci-lint; \
	if ! command -v $$LINT >/dev/null; then \
		if [ -x "$$(go env GOPATH)/bin/golangci-lint" ]; then \
			LINT="$$(go env GOPATH)/bin/golangci-lint"; \
		else \
			echo "golangci-lint not found on PATH or in \$$(go env GOPATH)/bin."; \
			echo "Install with: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0"; \
			echo "(CI pins v2.14.0 via golangci/golangci-lint-action@v8.0.0)"; \
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

# Local-only versioning and publication. Each release is sequential even with -j.
bump-version:
	bash scripts/bump-version.sh

release release-cli:
	bash scripts/release.sh

clean:
	rm -rf bin/ site/
