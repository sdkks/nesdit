# nesdit

A CLI for deterministic, format-preserving edits to JSON, YAML, and TOML
documents.

Upstream: <https://github.com/sdkks/nesdit>.

## Commit trailer convention

Every commit and PR that lands for a tracked ticket MUST carry a
`Ticket: <ID>` git trailer **in the commit message**, for example:

```
Add --check flag

Ticket: STORY-0042
```

The trailer lives on the **commit**, not only in the PR body. Automated
status sync machine-greps merged commits (`git log --grep='Ticket: '`);
a PR-body trailer alone is not enough.

CI enforces a `Ticket:` line in the PR body as a lightweight guardrail
(see `.github/pull_request_template.md`). Commit-level enforcement is
carried by reviewer discipline for v1; a commit-range CI check is tracked
as a v1.1 hardening item.

PR titles additionally prefix the ticket ID: `[STORY-0042] Add --check flag`.

## Quickstart

Build, test, and lint via `make`:

```
make build       # go build -o bin/nesdit ./cmd/nesdit
make test        # go test -race -count=1 ./...
make test-e2e    # go test -tags=e2e -race -count=1 ./test/e2e/...  (builds first)
make test-all    # test + test-e2e
make lint        # golangci-lint run   (install hint printed if not on PATH)
make docs        # go run ./cmd/gendocs --out docs/reference && mkdocs build --strict
                 # (mkdocs step is skipped with a message if mkdocs is not installed)
make site        # mkdocs build --strict  (skipped with a message if mkdocs is not installed)
make clean       # rm -rf bin/ site/
```

Notes:

- `cmd/gendocs` is a placeholder until the cobra tree lands. `make docs`
  runs it today; the `--out` flag is accepted as part of the `docs`-target
  contract so the real gendocs implementation plugs in without Makefile
  churn.
- `make lint` prints an install hint pointing at
  `go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.59.1`
  when `golangci-lint` is absent. CI pins `v1.59.1`; a newer v1.x locally
  is fine (same schema).
- Zero unit tests and zero e2e fixtures ship at this stage — both
  commands exit green. Real fixtures arrive with the feature stories.

## Toolchain version split (TASK-0005)

Go CI runs on two parallel toolchain tracks — the **primary** gate stays
conservative; a **canary** gate watches upstream drift without blocking
merges.

- **Primary CI** (`.github/workflows/ci.yml`, the merge gate) stays
  pinned to the Go version declared in `go.mod` (currently Go 1.22) and
  runs an `['1.22', '1.23']` matrix. `gojq` is pinned to v0.12.17 and
  `pelletier/go-toml/v2` to v2.3.0 because newer releases of either
  raise the required Go toolchain beyond what the primary gate runs.
- **Canary CI** (`.github/workflows/canary.yml`, **non-blocking**) runs
  on Go 1.24 and uses `go get -u` to bump both `gojq` and
  `pelletier/go-toml/v2` to `@latest` before running
  `make test-all lint`. It is triggered on a weekly schedule, on any PR
  that touches `go.mod` / `go.sum`, and on `workflow_dispatch`. Failure
  surfaces as a visible red job but does not block merges
  (`continue-on-error: true`).

When the canary flips red, that is the signal to schedule a joint bump:
move the primary matrix to `['1.23', '1.24']`, refresh the pinned
dependencies to `@latest`, and cite the triggering upstream release(s)
in the commit body. Until the canary flips, the main branch stays on
the older toolchain so the merge gate remains stable. See `EPIC-0001`
risks ("TASK-0005 canary bump introduces upstream breakage — off
critical path; defer bump to post-epic if canary fails") for the
accepted-risk framing.

## golangci-lint version split (TASK-0007)

`golangci-lint` also runs on two tracks, for the same reason the Go
toolchain does: CI wants a reproducible, fast binary; local dev wants
a version that actually builds under whatever Go the contributor has
installed.

- **CI** pins **v1.59.1** via
  `golangci/golangci-lint-action@v6.5.2`, which downloads the prebuilt
  release binary — no `go install` from source, so the CI Go version
  is irrelevant to the lint binary itself.
- **Local install** (the install hint in `make lint` and the
  `Makefile`) points at **v1.62.2** via `go install`. v1.59.1's
  transitive dependencies do not build from source under Go 1.23+, so
  contributors on a modern Go toolchain need a newer v1.x. v1.62.2 is
  the current floor that builds cleanly.
- Both versions honour the **same v1 config schema**, so a single
  `.golangci.yml` works for CI and local runs without branching.

### Pre-staged v2 migration

v1.59 deprecated `run.skip-dirs` in favour of `issues.exclude-dirs`;
v2 removes the old key entirely. This commit makes the key hop early
— `.golangci.yml` now declares exclusions under a top-level
`issues.exclude-dirs`. The v1 schema still accepts it, so nothing
changes behaviourally today, but the eventual v2 bump will no longer
need a config edit in the same commit as the tool upgrade.
