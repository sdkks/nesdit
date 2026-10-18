# Changelog

## [Unreleased]

## [0.7.0] - 2026-10-18

### Added

- Whole-body UTF-8 inspection from an explicit file, explicit stdin, live Core NATS and retained JetStream history (`--input-codec text`, `--nats-codec text`)
- Local MessagePack inspection as MessagePack JSON v1 (`--input-codec msgpack`): ordered duplicate and compound map entries, exact integers, binary versus invalid-UTF-8 strings, opaque extensions and timestamps
- MessagePack encoding of a selected value to binary stdout (`--to msgpack`) from ordinary JSON (`--input-codec json`), MessagePack JSON v1 (`--input-codec msgpack-json`) or a MessagePack source, with physical-terminal refusal independent of color settings
- Read-only FlatBuffers inspection of one local payload against a separate compiled BFBS schema (`--input-codec flatbuffers --bfbs`)

### Fixed

- Correct the stdout mode guide to the supported single-file invocation

## [0.6.0] - 2026-10-18

### Added

- Inspect local Protobuf payloads as JSON with `--protobuf-file` or `--protobuf-stdin`, a required `--descriptor-set` FileDescriptorSet and an exact `--message-type`
- Decode Protobuf payloads from live Core NATS subjects with `--nats-codec protobuf` plus the same schema flags
- Inspect retained JetStream history without creating consumers via `--nats-history <stream>`, with a required `--subject` pattern, a `--start-seq` or `--start-time` starting bound and an optional `--end-seq` end bound (NATS 2.15.x)

## [0.5.0] - 2026-10-17

### Added

- Inspect live Core NATS JSON messages with `--nats <subject>`, an explicit `--server nats://host:port` or `tls://host:port` address and a required `--duration` observation window
- Stop a live observation after N admitted messages with `--count`, applied before filtering
- Filter live payloads with a jq-style `--filter` boolean predicate and reshape them with a `--project` projection returning one JSON value
- Render live output with `--nats-output auto|jsonl|pretty`: terminal-friendly by default, JSONL when redirected
- Authenticate observation with `--creds` credentials and mutual TLS via `--tls-ca`, `--tls-cert` and `--tls-key`

## [0.4.1] - 2026-10-09

### Added

- Add `make install` for native-host installation, preferring an explicit `GOBIN`, then a configured Go bin directory on `PATH`, then `~/.local/bin`

## [0.4.0] - 2026-10-09

### Added

- Highlight JSON, TOML and YAML output on terminal destinations only, keeping serialized bytes unchanged for pipes, redirection and files; a nonempty `NO_COLOR` or `TERM=dumb` disables styling
- Bundle third-party license notices with release archives

### Changed

- Prepare and publish releases through a guarded local workflow instead of CI automation, keeping release archives, checksums and platforms unchanged

### Removed

- Stop updating the Homebrew tap automatically on release

## [0.3.2] - 2026-04-29

### Fixed

- Apply YAML merge keys (`<<:`) at decode time: anchor entries merge into the parent map with explicit keys taking precedence, instead of appearing as a literal `<<` key

## [0.3.1] - 2026-04-28

### Added

- Publish a Homebrew formula to the `sdkks/homebrew-tap` tap automatically on each release

## [0.3.0] - 2026-04-28

### Added

- Add `--pretty` for human-friendly TOML output: multi-line arrays, expanded tables and blank lines between top-level entries; silently ignored for non-TOML output
- Separate multi-document TOML output streams with a `+++` delimiter (Hugo-style convention, not part of the TOML specification)

### Fixed

- Decode single-document JSON on stdin as one buffered value, so multi-line objects parse correctly instead of being split as JSONL lines

## [0.2.0] - 2026-04-28

### Added

- Add `--version`, printing the build-time version and commit hash (`dev` for local builds)

## [0.1.2] - 2026-04-28

### Fixed

- Fix the man page path inside release archives to `share/man/man1/nesdit.1`

## [0.1.1] - 2026-04-28

### Added

- Add an `nesdit(1)` man page and bundle it in release archives

## [0.1.0] - 2026-04-28

### Added

- Add `--output-format` to choose the output format (`json`, `yaml`, `toml`) independently of the input format, enabling cross-format transcoding such as YAML to JSON or JSON to TOML; combining it with `-i` is rejected, and TOML output requires a table at the document root
- Add `--yaml-version` to select the YAML boolean dialect: `1.1` treats unquoted `yes`/`no`/`on`/`off` as booleans, while `1.2` (the default) treats only `true`/`false` as booleans; ignored for non-YAML input
- Publish the project under the MIT license with a rewritten user-facing README

### Fixed

- Reject `--where` in single-file stdout mode without `-i` or stdin before any input is read

## [0.0.3](https://github.com/sdkks/nesdit/compare/v0.0.2...v0.0.3) (2026-04-27)


### Bug Fixes

* **release:** use PAT for release-please so tag push triggers goreleaser ([2c3ff71](https://github.com/sdkks/nesdit/commit/2c3ff71e57ae513cc94dbd7bb50fcb685f67aa03))

## [0.0.2](https://github.com/sdkks/nesdit/compare/v0.0.1...v0.0.2) (2026-04-27)


### Bug Fixes

* **release:** split goreleaser to tag trigger; optimize release binaries ([952f88f](https://github.com/sdkks/nesdit/commit/952f88f949c963a54e5de43565fef0ba9d6093e6))

## [0.0.1](https://github.com/sdkks/nesdit/compare/v0.0.0...v0.0.1) (2026-04-27)


### Features

* add release pipeline (release-please + GoReleaser) ([afe1244](https://github.com/sdkks/nesdit/commit/afe1244f4e541993d8705f21ccbb1a3f18a64034))
* **ci:** add SHA↔tag consistency guard for action pins ([20341cb](https://github.com/sdkks/nesdit/commit/20341cbf155da0e00510daab42836f04da0004a6))
* implement --where predicate filter ([9d7cbfd](https://github.com/sdkks/nesdit/commit/9d7cbfd0ee10aa169d92c1224b48311bf8c34fdd))
* migrate to golangci-lint v2 + action v8.0.0 ([3e616a4](https://github.com/sdkks/nesdit/commit/3e616a43c5b2f21a3f822f5b73ae58ac86a158ec))
* atomic writer, two-pass in-place orchestrator, mixed-format rejection, batch summary ([bee09a4](https://github.com/sdkks/nesdit/commit/bee09a42113403c52ee90fa70e2b077c276fe265))
* STDIN stream mode + multi-doc framing + TOML multi-doc rejection ([d5ff928](https://github.com/sdkks/nesdit/commit/d5ff928039cd9ec046ed3ab25ee5d5aaaa8a8459))
* add -n/--dry-run, --check, and ExitCode enum ([0d36c54](https://github.com/sdkks/nesdit/commit/0d36c54a226d9cb7d2f73af9ecbf86aff0cc3d09))
* add --edit expression builder, diff engine, and editor integration ([9e6b617](https://github.com/sdkks/nesdit/commit/9e6b617fd3d85aeb870b52035d889c7501a78fb0))
* add --backup[=.bak] pre-write backup for -i mode ([5bcc053](https://github.com/sdkks/nesdit/commit/5bcc05378dde252e67d1e7f14bf28505019f7fd4))
* implement --log-format=json NDJSON stderr emitter ([f066f44](https://github.com/sdkks/nesdit/commit/f066f4419b76797ad3e48dd8ecd19badd2976a10))
* add --create-missing strict-by-default path creation ([35fbd81](https://github.com/sdkks/nesdit/commit/35fbd81e4d2eade0b943f12068f8b0082931d367))
* add --keep-going / --strict for per-doc error policy ([d3e9bdc](https://github.com/sdkks/nesdit/commit/d3e9bdcb2f26949d12bf4f63096e045df7d32f81))
* **yaml:** reject trailing documents in DecodeValue ([476a2c4](https://github.com/sdkks/nesdit/commit/476a2c4c911ee25647aba206bd9684c4c21135e8))


### Bug Fixes

* address code review findings ([688e1eb](https://github.com/sdkks/nesdit/commit/688e1ebc6b4e3f6c02044eae76d8580b52ab600b))
* **ci:** add version: "2" to .golangci.yml required by golangci-lint v2 ([10085b3](https://github.com/sdkks/nesdit/commit/10085b33c1b3f338e8dbc034a95dbe3dda4e22cd))
* **ci:** correct golangci-lint v2 config schema (v1 keys were used) ([bfcf6a5](https://github.com/sdkks/nesdit/commit/bfcf6a5b18c695ebcf5f8e1fd299dc3159467179))
* **ci:** fix all remaining staticcheck QF/S/ST violations for golangci-lint v2 ([6866f57](https://github.com/sdkks/nesdit/commit/6866f57e3cbcd998467cbbfc2d3454c40585124e))
* **ci:** fix gocritic/gofmt/revive violations ([5483599](https://github.com/sdkks/nesdit/commit/5483599d6fa2d61c197035a079829c24881b7a9b))
* **ci:** handle errcheck violations flagged by golangci-lint v2 ([a1a7629](https://github.com/sdkks/nesdit/commit/a1a76296345d4aeb72f365f5761d7ef94190d9f3))
* **ci:** resolve remaining revive and errcheck violations for golangci-lint v2 ([fd92d60](https://github.com/sdkks/nesdit/commit/fd92d606570b42cd9e37293cb1599ee6fd0ac0ab))
* **e2e:** add --create-missing to the in-place --where fixture ([2a8f7d3](https://github.com/sdkks/nesdit/commit/2a8f7d3b10ed5e69e8d20e6e9507b2f58c96490f))
* **edit:** use isatty(stdin) for TTY check instead of opening /dev/tty ([1c0e61d](https://github.com/sdkks/nesdit/commit/1c0e61d5a693a76208718f1e3dc3ead7366453d1))
* **release:** gate on ci success and fix auto-merge shell injection ([b43ee99](https://github.com/sdkks/nesdit/commit/b43ee99cf224e0ca1c76088174bac275d36ef603))
* **release:** parse PR number with jq instead of fromJSON in env block ([d81d86e](https://github.com/sdkks/nesdit/commit/d81d86e9ddab908bbad747e4c8b05687080ee6c9))
* **release:** pass --repo to gh pr merge to avoid missing .git error ([0af7625](https://github.com/sdkks/nesdit/commit/0af76253dfac2d463c1b10a1c3f5344c8f2f2c91))
* remove inverted --backup conflict rule that broke -i --backup ([3019b29](https://github.com/sdkks/nesdit/commit/3019b297be08c122b6814dcad95d6862f3d85b18))
* add E2E fixtures for dryrun/check conflict and enforce single-file for --dry-run/--check ([84d74a9](https://github.com/sdkks/nesdit/commit/84d74a99251b1fff5737a9431d4972bb9c24230b))
* fix editor/query flag conflicts, escape quotes in suggested commands, and report test-only TTY bypass ([215978e](https://github.com/sdkks/nesdit/commit/215978e1b7fb7928935f2e3275bc92a69fa4cb87))
* **stream:** fix TOML `---` false-positive and json/jsonl alias ([01154f5](https://github.com/sdkks/nesdit/commit/01154f536c28120dd822a70ab2175033123a9ed4))
* sanitise newlines in --dry-run diff header paths ([3f69a89](https://github.com/sdkks/nesdit/commit/3f69a89826d6a643d733639a14796814dbb687d2))
