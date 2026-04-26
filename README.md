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
`Ticket: <ID>` git trailer, for example:

```
Add --check flag

Ticket: STORY-0042
```

PR titles additionally prefix the ticket ID: `[STORY-0042] Add --check flag`.
This lets `/deliver` in md-doc-analyst sync status back to the originating
ticket by grepping merged commits.

## Quickstart

No runnable targets yet — `Makefile`, CI, and the testscript harness arrive
with `TASK-0003`. For now the scaffolding is just a Go module with package
stubs; `go build ./...` and `golangci-lint run` are expected to pass clean.
