# Changelog

## 1.0.0 (2026-04-27)


### Features

* add release pipeline (release-please + GoReleaser) ([afe1244](https://github.com/sdkks/nesdit/commit/afe1244f4e541993d8705f21ccbb1a3f18a64034))
* **ci:** add SHA↔tag consistency guard for action pins (TASK-0020) ([20341cb](https://github.com/sdkks/nesdit/commit/20341cbf155da0e00510daab42836f04da0004a6))
* implement --where predicate filter (FR-10 / STORY-0009) ([9d7cbfd](https://github.com/sdkks/nesdit/commit/9d7cbfd0ee10aa169d92c1224b48311bf8c34fdd))
* migrate to golangci-lint v2 + action v8.0.0 (TASK-0021) ([3e616a4](https://github.com/sdkks/nesdit/commit/3e616a43c5b2f21a3f822f5b73ae58ac86a158ec))
* **STORY-0004:** atomic writer, two-pass in-place orchestrator, mixed-format rejection, batch summary ([bee09a4](https://github.com/sdkks/nesdit/commit/bee09a42113403c52ee90fa70e2b077c276fe265))
* STORY-0005 STDIN stream mode + multi-doc framing + TOML multi-doc rejection ([d5ff928](https://github.com/sdkks/nesdit/commit/d5ff928039cd9ec046ed3ab25ee5d5aaaa8a8459))
* **STORY-0006:** add -n/--dry-run, --check, and ExitCode enum ([0d36c54](https://github.com/sdkks/nesdit/commit/0d36c54a226d9cb7d2f73af9ecbf86aff0cc3d09))
* **STORY-0007:** add --edit expression builder, diff engine, and editor integration ([9e6b617](https://github.com/sdkks/nesdit/commit/9e6b617fd3d85aeb870b52035d889c7501a78fb0))
* **STORY-0010:** add --backup[=.bak] pre-write backup for -i mode (FR-14) ([5bcc053](https://github.com/sdkks/nesdit/commit/5bcc05378dde252e67d1e7f14bf28505019f7fd4))
* **STORY-0011:** implement --log-format=json (FR-15) NDJSON stderr emitter ([f066f44](https://github.com/sdkks/nesdit/commit/f066f4419b76797ad3e48dd8ecd19badd2976a10))
* **STORY-0012:** add --create-missing (FR-16) strict-by-default path creation ([35fbd81](https://github.com/sdkks/nesdit/commit/35fbd81e4d2eade0b943f12068f8b0082931d367))
* **STORY-0013:** add --keep-going / --strict for per-doc error policy (FR-17) ([d3e9bdc](https://github.com/sdkks/nesdit/commit/d3e9bdcb2f26949d12bf4f63096e045df7d32f81))
* **yaml:** reject trailing documents in DecodeValue (TASK-0017) ([476a2c4](https://github.com/sdkks/nesdit/commit/476a2c4c911ee25647aba206bd9684c4c21135e8))


### Bug Fixes

* address code review findings from Phase 1 stories ([688e1eb](https://github.com/sdkks/nesdit/commit/688e1ebc6b4e3f6c02044eae76d8580b52ab600b))
* **ci:** add version: "2" to .golangci.yml required by golangci-lint v2 ([10085b3](https://github.com/sdkks/nesdit/commit/10085b33c1b3f338e8dbc034a95dbe3dda4e22cd))
* **ci:** correct golangci-lint v2 config schema (v1 keys were used) ([bfcf6a5](https://github.com/sdkks/nesdit/commit/bfcf6a5b18c695ebcf5f8e1fd299dc3159467179))
* **ci:** fix all remaining staticcheck QF/S/ST violations for golangci-lint v2 ([6866f57](https://github.com/sdkks/nesdit/commit/6866f57e3cbcd998467cbbfc2d3454c40585124e))
* **ci:** fix gocritic/gofmt/revive violations introduced by Phase 1 stories ([5483599](https://github.com/sdkks/nesdit/commit/5483599d6fa2d61c197035a079829c24881b7a9b))
* **ci:** handle errcheck violations flagged by golangci-lint v2 ([a1a7629](https://github.com/sdkks/nesdit/commit/a1a76296345d4aeb72f365f5761d7ef94190d9f3))
* **ci:** resolve remaining revive and errcheck violations for golangci-lint v2 ([fd92d60](https://github.com/sdkks/nesdit/commit/fd92d606570b42cd9e37293cb1599ee6fd0ac0ab))
* **e2e:** add --create-missing to fr10_where_file_mode fixture ([2a8f7d3](https://github.com/sdkks/nesdit/commit/2a8f7d3b10ed5e69e8d20e6e9507b2f58c96490f))
* **edit:** use isatty(stdin) for TTY check instead of opening /dev/tty ([1c0e61d](https://github.com/sdkks/nesdit/commit/1c0e61d5a693a76208718f1e3dc3ead7366453d1))
* remove inverted --backup conflict rule that broke -i --backup ([3019b29](https://github.com/sdkks/nesdit/commit/3019b297be08c122b6814dcad95d6862f3d85b18))
* **STORY-0006:** add E2E fixtures for FR-21 dryrun/check conflict and enforce single-file for --dry-run/--check ([84d74a9](https://github.com/sdkks/nesdit/commit/84d74a99251b1fff5737a9431d4972bb9c24230b))
* **STORY-0007:** resolve all rework-pass MUST-FIX items ([215978e](https://github.com/sdkks/nesdit/commit/215978e1b7fb7928935f2e3275bc92a69fa4cb87))
* **stream:** STORY-0005 rework — fix TOML `---` false-positive and json/jsonl alias ([01154f5](https://github.com/sdkks/nesdit/commit/01154f536c28120dd822a70ab2175033123a9ed4))
* **TASK-0025:** sanitise newlines in --dry-run diff header paths ([3f69a89](https://github.com/sdkks/nesdit/commit/3f69a89826d6a643d733639a14796814dbb687d2))
