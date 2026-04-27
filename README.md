# nesdit

A CLI for deterministic, format-preserving edits to JSON, YAML, and TOML
documents.

Upstream: <https://github.com/sdkks/nesdit>.

## Limit flags and escape hatches

`nesdit` enforces per-run resource caps by default. Every cap can be
raised or disabled on a per-invocation basis:

- `--max-bytes <n>` — reject inputs larger than `n` bytes (default 10 MiB).
  Set to `0` to disable the cap entirely — useful for legitimately large
  inputs such as Terraform state files or generated Kustomize overlays.
- `--max-depth <n>` — reject documents nested more than `n` levels deep
  (default 1000). Set to `0` to disable.
- `--max-yaml-nodes <n>` — YAML alias-expansion cap; rejects inputs that
  would materialise more than `n` nodes (default 100 000, billion-laughs
  mitigation). Set to `0` to disable.
- `--timeout <dur>` — cancel a runaway query after `dur` (e.g. `500ms`,
  `30s`). Disabled by default (0).

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
  `go install github.com/golangci/golangci-lint/cmd/golangci-lint@v2.11.4`
  when `golangci-lint` is absent. CI and canary both pin `v2.11.4` via
  `golangci/golangci-lint-action@v8.0.0` (SHA `4afd733a`), so the local
  install hint and CI resolve to the same binary version.
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
  surfaces as visible red steps but does not block merges
  (`continue-on-error: true` on individual steps, not the job).
  **Important:** the `canary / toolchain-bump` job MUST NOT be added as a
  required branch-protection check — `continue-on-error` on steps makes
  the job always exit 0, which would silently make branch protection
  vacuous.

When the canary flips red, that is the signal to schedule a joint bump:
move the primary matrix to `['1.23', '1.24']`, refresh the pinned
dependencies to `@latest`, and cite the triggering upstream release(s)
in the commit body. Until the canary flips, the main branch stays on
the older toolchain so the merge gate remains stable. See `EPIC-0001`
risks ("TASK-0005 canary bump introduces upstream breakage — off
critical path; defer bump to post-epic if canary fails") for the
accepted-risk framing.

## golangci-lint pin (TASK-0007 / BUG-0002)

`golangci-lint` is pinned to a single version across CI, canary, and
the local install hint. This used to be a two-track split (CI on
v1.59.1, local on v1.62.2); BUG-0002 collapsed it back to one track
after v1.59.1 proved incompatible with Go 1.23 — its typechecker
cannot load the Go 1.23 stdlib (`regexp`, `math/big`, `slices`) or
`pelletier/go-toml/v2@v2.3.0/unstable`, producing spurious
`(typecheck)` errors in CI while the same codebase lints clean
locally on v1.62.2.

- **Everywhere** — CI (`.github/workflows/ci.yml`), canary
  (`.github/workflows/canary.yml`), and the `make lint` install hint —
  pins **v2.11.4** via `golangci/golangci-lint-action@v8.0.0`
  (SHA `4afd733a84b1f43292c63897423277bb7f4313a9`, verified on 2026-04-27).
  All environments resolve to the same binary; local and CI lint results
  are directly comparable.
- The `.golangci.yml` v2 schema is in use (`run:` block removed; timeout
  passed via CLI `--timeout=5m`). Migrated as part of TASK-0021.
