# nesdit

A CLI for deterministic, format-preserving edits to JSON, YAML, and TOML documents using jq-style queries. Key order and value types are preserved; comments and YAML anchors are not.

- Reads a file (or stdin), applies a query, and writes the result — without reformatting untouched content.
- Supports in-place editing (`-i`), dry-run diffs (`-n`), drift checking (`--check`), and editor-assisted query building (`--edit`).
- Enforces per-run resource caps by default to handle adversarial inputs safely.

**Full documentation:** https://sdkks.github.io/nesdit/

## Installation

**Go:**

```sh
go install github.com/sdkks/nesdit/cmd/nesdit@latest
```

**Pre-built binaries** (linux/amd64, linux/arm64, darwin/arm64) are available on the [releases page](https://github.com/sdkks/nesdit/releases).

**Homebrew**:

```sh
brew tap sdkks/tap && brew install sdkks/tap/nesdit
```

## Quick start

```sh
# Print a YAML file after editing .replicas
nesdit deployment.yaml --query '.replicas = 3'

# Edit in-place (atomic write)
nesdit -i config.json --query '.env = "production"'

# Preview changes without writing (unified diff to stdout)
nesdit -n config.yaml --query '.timeout = "30s"'

# Check if a file is up-to-date (exit 2 on drift, 0 if identical)
nesdit --check config.yaml --query '.'

# Filter: only apply query to docs where .kind == "Deployment"
nesdit -i manifests.yaml --where '.kind == "Deployment"' --query '.spec.replicas = 2'

# Open $EDITOR on a temp copy and get a suggested query from your edits
nesdit --edit config.yaml

# Read from stdin
echo '{"x": 1}' | nesdit --query '.x = 2'

# Transcode: convert a JSON file to YAML output
nesdit config.json --output-format yaml --query '.'

# Inject a shell variable as a typed JSON value
nesdit -i deploy.yaml --argjson replicas "$REPLICAS" --query '.replicas = $replicas'
```

## Supported formats

| Format | Extensions      | Notes                       |
| ------ | --------------- | --------------------------- |
| JSON   | `.json`         | Preserves key order; output is always compact (single-line) |
| YAML   | `.yaml`, `.yml` | Single-document per file; anchors/aliases resolved on decode, not re-emitted |
| TOML   | `.toml`         | Tables and arrays of tables; nested tables emitted as inline syntax |

> **Known behaviors:** Comments are stripped from all formats on output. YAML anchors/aliases are resolved and not re-emitted. YAML quoted strings are normalized to bare scalars. TOML `[section]` headers are rewritten to inline-table syntax (`{key = value}`). JSON output is always compact (single-line). None of these affect the parsed value — only the serialized form.

Format is auto-detected from the file extension. Use `--format json|jsonl|yaml|toml` to override input format, and `--output-format json|yaml|toml` to transcode to a different output format.

## Key flags

| Flag                        | Description                                                                         |
| --------------------------- | ----------------------------------------------------------------------------------- |
| `--query <jq>`              | jq-style query to apply                                                             |
| `-f, --from-file <path>`    | Load query from a file                                                              |
| `--arg K=V`                 | Bind `$K` as a string value in the query (repeatable)                               |
| `--argjson K=V`             | Bind `$K` as a JSON-decoded value in the query (repeatable)                         |
| `--where <jq>`              | Filter: only apply query to matching documents                                      |
| `--format <fmt>`            | Force input format (`json\|jsonl\|yaml\|toml`); default is extension-based detection  |
| `--output-format <fmt>`     | Output format (`json\|yaml\|toml`); defaults to same as input                        |
| `--yaml-version <1.1\|1.2>` | YAML boolean dialect: `1.1` coerces `yes/no/on/off`; `1.2` (default) requires `true/false` |
| `-i, --in-place`            | Edit file(s) atomically in place                                                    |
| `-n, --dry-run`             | Emit a unified diff; do not write                                                   |
| `--check`                   | Exit 2 if query would change input; exit 0 if identical                             |
| `--edit`                    | Open `$EDITOR`, emit a suggested query from the diff                                |
| `--backup[=.ext]`           | Write a sibling backup before each in-place edit (requires `-i`)                    |
| `--create-missing`          | Allow queries to create keys/paths not present in the input                         |
| `--strict`                  | Halt on first document error (default behaviour; explicit alias)                    |
| `--keep-going`              | Continue after per-document errors; exit 1 at end if any failed                     |
| `--log-format json`         | Emit NDJSON on stderr instead of text                                               |
| `--timeout <dur>`           | Cancel query after duration (e.g. `500ms`, `30s`)                                   |
| `--max-bytes <n>`           | Reject inputs larger than `n` bytes (default 10 MiB; `0` disables)                  |
| `--max-depth <n>`           | Reject documents nested deeper than `n` levels (default 1000; `0` disables)         |
| `--max-yaml-nodes <n>`      | YAML alias-expansion cap, billion-laughs mitigation (default 100 000; `0` disables) |
| `--max-query-bytes <n>`     | Reject `--from-file` queries larger than `n` bytes (default 1 MiB; `0` disables)    |
| `--pretty`                  | Emit human-readable TOML output (multi-line arrays, expanded tables, blank lines); silently ignored for non-TOML output |

See [`nesdit --help`](https://sdkks.github.io/nesdit/reference/nesdit/) for the full reference.

## Development

```sh
make build      # go build -o bin/nesdit ./cmd/nesdit
make test       # go test -race -count=1 ./...
make test-e2e   # integration tests (builds first)
make test-all   # unit + integration
make lint       # golangci-lint run
make docs       # generate reference docs + build site
```

CI runs on Go 1.22 and 1.23 across ubuntu and macOS. A weekly canary job tests against the latest Go toolchain and dependency versions (non-blocking).

## License

See [LICENSE](LICENSE).
