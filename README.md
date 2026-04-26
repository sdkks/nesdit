# nesdit

A CLI for deterministic, format-preserving edits to JSON, YAML, and TOML documents.
This repository holds the implementation; specifications and tickets are tracked
separately in the md-doc-analyst repo.

## SDLC

- Parent epic: `EPIC-0001` (in md-doc-analyst)
- Spec: `SPEC-0001` (`generated/sdlc/specs/SPEC-0001.md` in md-doc-analyst)
- Scaffolding task: `TASK-0002`

Tickets and specs live in the companion repo
[`md-doc-analyst`](https://github.com/sakkas-zendesk/md-doc-analyst). Code lives
here; no symlinks or submodules bridge the two.

## Cross-repo commit trailer convention

Every commit and PR that lands for an md-doc-analyst ticket MUST carry a
`Ticket: <ID>` git trailer **in the commit message**, for example:

```
Add --check flag

Ticket: STORY-0042
```

The trailer lives on the **commit**, not only in the PR body. `/deliver` in
md-doc-analyst machine-greps merged commits (`git log --grep='Ticket: '`) to
sync ticket Status Logs; a PR-body trailer alone is not enough.

CI enforces a `Ticket:` line in the PR body as a lightweight guardrail (see
`.github/pull_request_template.md`). Commit-level enforcement is documented
here and carried by reviewer discipline for v1; a commit-range CI check is
tracked as a v1.1 hardening item.

PR titles additionally prefix the ticket ID: `[STORY-0042] Add --check flag`.

## Quickstart

Build, test, and lint via `make`. Targets follow SPEC-0001 §5:

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

- `cmd/gendocs` is a placeholder until STORY-0003 populates the cobra tree.
  `make docs` runs it today; the `--out` flag is accepted as part of the
  `docs`-target contract so STORY-0003 plugs in without Makefile churn.
- `make lint` prints an install hint pointing at
  `go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.59.1`
  when `golangci-lint` is absent. CI pins `v1.59.1`; a newer v1.x locally is
  fine (same schema).
- Zero unit tests and zero e2e fixtures ship in this task — both commands
  exit green. Real fixtures arrive with the feature stories.
