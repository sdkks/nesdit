## Summary

<!-- One paragraph: what does this PR do and why? -->

## Type

<!-- Check all that apply -->
- [ ] `fix` — bug fix (patch bump)
- [ ] `feat` — new feature (minor bump)
- [ ] `feat!` — breaking change (major bump)
- [ ] `refactor` — internal restructure, no behaviour change
- [ ] `test` — tests only
- [ ] `docs` — documentation only
- [ ] `ci` — CI/CD / release pipeline
- [ ] `chore` — tooling, deps, config

## Changes

<!-- Bullet list of notable changes. Link to functions or files where helpful. -->
-
-

## Input / output formats affected

<!-- Which formats does this touch? Uncheck if unaffected. -->
- [ ] JSON (single-doc stdin)
- [ ] JSONL (streaming stdin)
- [ ] YAML
- [ ] TOML
- [ ] Auto-detect logic (`internal/format/detect.go`)
- [ ] Output format / transcoding (`--output-format`)
- [ ] `--pretty` flag

## Execution paths affected

<!-- Uncheck if unaffected -->
- [ ] File mode (`runOnce` / `runFiles`)
- [ ] Stdin mode (`runStdin` / `runStdinJSON`)
- [ ] In-place writes (`--in-place`)
- [ ] Dry-run / diff (`--dry-run`)
- [ ] Check mode (`--check`)
- [ ] Stream layer (`internal/stream/`)
- [ ] Query engine (`internal/query/`)

## Verification

<!-- How was this verified? Check all that apply, add manual steps if relevant. -->
- [ ] `go test ./...`
- [ ] `go test -tags=e2e -race -count=1 ./test/e2e/...`
- [ ] `make lint` (golangci-lint)
- [ ] Manual: <!-- describe the command(s) you ran -->
- [ ] New unit tests added
- [ ] New e2e txtar fixture added

## Reproduction command (for fixes)

```sh
# Paste the exact command that reproduces the bug before this fix
```

## Breaking changes

<!-- If this is a breaking change, describe what changes for users and what they need to update -->

## Linked issues

<!-- Closes #, Fixes # -->
