# Working on nesdit

This guide is for contributors working in this repository.
See `README.md` for product usage and `docs/` for user guides.
The commands below use the repository's Bash/Make developer workflow.

## Repository layout

- `cmd/nesdit/`: executable entry point.
- `internal/run/`: Cobra command metadata and CLI execution paths.
- `internal/format/`, `internal/omap/`, `internal/query/`: encoding, ordered values, and queries.
- `internal/io/`, `internal/stream/`: file writes and streaming input/output.
- `internal/display/`: terminal-only styling of serialized output.
- `test/e2e/`, `test/release/`, `test/install/`: integration and sandboxed script checks.
- `scripts/`: installation, version preparation, packaging, and local publication.

## Build, install, and check

Use the Go baseline in `go.mod`; match the linter version in `Makefile` and `.github/workflows/ci.yml`.
The linter installation command is documented in `README.md` and the Makefile.

```sh
make build         # build bin/nesdit
make install       # install current source for the native host
make install GOBIN="$HOME/.local/bin" # choose the destination
make test          # all Go packages, with race detection and no cached results
make test-e2e      # build, then run the tagged integration tests with race detection
make test-release  # isolated release-script checks; no real publication
make test-install  # sandboxed install-script checks
make lint          # golangci-lint
make test-all      # lint, unit, integration, release, and install checks
```

`make` defaults to `make test-all`. For focused Go checks, use `go test ./internal/run/...`
or the relevant package; tagged integration checks use `make test-e2e`.
Configured pre-commit hooks are listed in `.pre-commit-config.yaml`, including
secret scanning, Go checks, and conventional commit-message validation.
Configuration alone does not mean those hooks are installed.

`make install` prefers explicit `GOBIN`, then Go's saved `GOBIN`, then the first
configured `GOPATH` root whose `bin` directory is listed on `PATH`, then `$HOME/.local/bin`.
It creates missing destination directories and embeds the current `VERSION`.
`GO` selects another Go executable; the installer uses its `GOHOSTOS`/`GOHOSTARCH`
even when cross-compilation settings are present. It does not edit shell configuration;
add the destination to `PATH` yourself if needed.

## Preserve existing behavior

Keep authoritative serialized bytes separate from terminal presentation.
Styling belongs at display sinks, not in file serializers: pipes, redirected output,
in-place files, backups, diffs, help, diagnostics, and editor query text stay plain.
Highlighting is automatic for supported terminal output on Unix; a nonempty `NO_COLOR`
or `TERM=dumb` disables it. Windows output stays plain; there are no color flags.
Preserve deterministic serialization, key order, value types, and atomic in-place writes.
Keep input, nesting, YAML expansion, query-file, and timeout safeguards intact;
`internal/format/format.go` and CLI flag definitions are the default-limit sources.
Release packaging targets `linux/amd64`, `linux/arm64`, and `darwin/arm64`, with
each binary smaller than 40 MiB (41,943,040 bytes), an interim roadmap budget that
may be explicitly revisited as milestones progress. See `scripts/package-release.sh`
and CI for the checks.

## Documentation and site

A user-facing change and its `CHANGELOG.md` bullet land in the same commit;
see `.agents/skills/changelog/SKILL.md` for the format, backfill and release
rules. Releases follow `docs/releasing.md`; `make release-cli` owns the
version bump commit, tag and publication — never hand-edit `VERSION` history
or tags.

`cmd/gendocs/main.go` constructs `run.NewRootCmd()` and generates Cobra Markdown
into `docs/reference/`. Command metadata, flag names, and descriptions come from
`internal/run/run.go`; update those sources rather than hand-editing generated reference text.
The generator does not infer runtime behavior or environment controls, and does not
write `README.md`, tutorials, other manual guides, or `docs/nesdit.1`.
Update those manually when relevant. Site navigation is maintained in `mkdocs.yml`.

```sh
make docs          # regenerate CLI reference, then build the site strictly
make site          # build the existing Markdown strictly, without regenerating
```

The configured site uses MkDocs Material; see `.github/workflows/docs.yml` for setup.
If `mkdocs` is missing, `make docs` still regenerates the reference before skipping
its site-build step; `make site` prints a skip message and succeeds without building.
A skipped build is not successful site validation. With MkDocs installed, both
run `mkdocs build --strict`. Neither `make test-all` nor the configured pre-commit
hooks generates documentation.

The Pages workflow regenerates reference docs, builds, and deploys on selected `main`
pushes affecting `docs/**`, `mkdocs.yml`, `cmd/gendocs/**`, or `internal/run/run.go`,
and on manual dispatch. It has no pull-request trigger and does not commit generated
files back. A release tag alone does not update the site. This root guide is not a site page.

## Local releases

`make bump-version` prepares `VERSION` locally; `make release` and `make release-cli`
verify and publish. Follow `docs/releasing.md` for prerequisites, permissions, assets,
and partial-upload recovery. Publication requires explicit maintainer authorization
and the documented GitHub release-write and Git push access. Use normal hooks;
do not force tags, overwrite existing release assets, or bypass verification.

## Public-facing text

Use ordinary product language in new code comments, documentation, CLI messages,
commit messages, and pull-request descriptions. Do not embed private ticket names or IDs,
planning/tracking-system metadata, private machine or worktree paths, or tool/session
transcripts. Examples should use repository-relative paths or portable variables,
not actual workstation paths. Keep private coordination details out of user-facing text;
this is not a reason to alter user-supplied serialized data or normal operation/error paths.
