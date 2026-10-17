# nesdit

A CLI for deterministic, format-preserving edits to JSON, YAML, and TOML documents using jq-style queries. Key order and value types are preserved; comments and YAML anchors are not.

- Reads a file (or stdin), applies a query, and writes the result — without reformatting untouched content.
- Supports in-place editing (`-i`), dry-run diffs (`-n`), drift checking (`--check`), and editor-assisted query building (`--edit`).
- Enforces file/pipe resource caps by default; native NATS observation uses separate component limits and assumes a trusted broker.

**Full documentation:** https://sdkks.github.io/nesdit/

## Installation

**Go (1.27.2 or later):**

```sh
go install github.com/sdkks/nesdit/cmd/nesdit@latest
```

**From a local checkout:**

```sh
make install
# Optional destination override:
make install GOBIN="$HOME/.local/bin"
```

This builds the current source for your machine. It installs into configured `GOBIN`
(including Go's saved setting), otherwise the first configured `GOPATH/bin` listed
on `PATH`, otherwise `$HOME/.local/bin`. Missing destination directories are
created. Add the destination to `PATH` yourself if needed; no shell settings are changed.
`GO` can select an alternate Go executable.

**Pre-built binaries** (linux/amd64, linux/arm64, darwin/arm64) are available on the [releases page](https://github.com/sdkks/nesdit/releases).

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

## Read-only Protobuf inspection

```sh
nesdit --protobuf-file event.bin --descriptor-set schemas/events.pb --message-type example.Order
producer | nesdit --protobuf-stdin --descriptor-set schemas/events.pb --message-type example.Order \
  --filter '.active == true' --project '{id: .id}'
```

Choose one source explicitly. The separate, self-contained local descriptor set
and exact message name are required; no registry or runtime compiler is used.
One raw message is read through EOF, capped at 10 MiB, without prefix stripping
or guessed framing. An open under-limit stdin can wait indefinitely: there is no
standalone timer. File mode never reads stdin or modifies the input.

Output is only the decoded/projected JSON value, including scalar and array roots:
pretty on Unix terminals, compact when redirected, with one LF. A false filter
writes nothing and skips projection. Standard ProtoJSON mappings include local
expanded `Any`, extensions and well-known types; unknown wire fields are omitted
and decoded object keys sort recursively. Legacy query/edit/format/presentation,
broker and resource-override flags are rejected, even explicit defaults.

See the [Protobuf guide](docs/modes/protobuf.md) for preparation, mapping, independent
structure budgets, the conservative 16 MiB pre-Marshal certificate (which can
reject actual-fitting output), the separate final-output limit and cooperative
versus process-watchdog interruption. This is read-only inspection, not encoding
or a lossless binary capture.

## Live NATS observation

```sh
nesdit --nats 'orders.>' --server nats://127.0.0.1:4222 --duration 30s --count 3 \
  --filter '.show == true' --project '{id: .id}'
```

JSON is the default (`--nats-codec json`). To inspect Protobuf instead:

```sh
nesdit --nats 'orders.>' --server nats://127.0.0.1:4222 --duration 30s --count 3 \
  --nats-codec protobuf --descriptor-set schemas/events.pb --message-type example.Order \
  --filter '.active == true' --project '{id: .id}'
```

Both schema options are required for native Protobuf and rejected with JSON.
The self-contained local descriptor and exact type are prepared before connecting
and starting the total duration. The same Protobuf mapping and component limits
as standalone apply, but native output is an envelope, not a bare value; the
whole event must fit 16 MiB including headers, whitespace and final LF. Ordinary
empty Protobuf can succeed; a bodyless `Status: 503` remains a failure.

This explicit mode reads neither input documents nor stdin. It displays each decoded
message with its delivered subject and repeated headers, using pretty JSON on a
Unix terminal and JSONL when redirected. Server and total duration are required;
count is an optional cap on messages admitted **before filtering**, not matches or
all publications. Readiness and completion diagnostics go to stderr. There is no
reconnect, history or application publishing.

Queries see the body root: use `.id`, not the displayed `.payload.id`. A filter
must return exactly one boolean; false suppresses without running projection.
Use comparisons, not `select()`. Projection must return exactly one JSON value
(object, array, scalar or null); zero/multiple results are errors. Transport
subject and repeated headers are unchanged and cannot be queried. Three false
predicates with count three stop with admitted=3 and emitted=0, not three matches.
Without projection the original decoded payload bypasses the query engine.
Queried decimal/exponent numbers use floating-point arithmetic; this is not a
lossless numeric view. Expressions are trusted code, not heap-sandboxed.

For secure clusters, use `tls://` with optional `--creds path/to/operator.creds`,
`--tls-ca path/to/ca.pem`, and paired `--tls-cert path/to/client.pem` /
`--tls-key path/to/client.key`. All four files are regular and at most 1 MiB each,
validated and preloaded before the duration starts. Certificate and hostname
verification stay enabled; omitting `--tls-ca` uses system trust. Never put
credentials in URLs. Token/user-password-only, TLS-first-only, proxy and WebSocket
connections are outside this supported subset; actual-cluster compatibility still
needs an authorized smoke test.

Observation is live and at-most-once, not a complete or byte-exact capture.
Interrupt/output failure can leave a partial final record; check exit status.
See the [NATS guide](docs/modes/nats.md) for limits, verified TLS, cancellation,
output modes, and disposable-broker testing.

## Retained JetStream history

```sh
nesdit --nats-history ORDERS --subject 'orders.>' --start-seq 1 \
  --server nats://127.0.0.1:4222 --duration 30s --count 20
```

Read retained JSON by default, without creating/binding/pulling/acknowledging
consumers or changing stream configuration. For Protobuf, add `--nats-codec protobuf`,
`--descriptor-set schemas/events.pb` and `--message-type example.Order`; the same
local decoder and payload-root queries apply. History reads neither stdin nor files.

Choose exactly one inclusive positive sequence or timezone-qualified `--start-time`
(with at most nine fractional digits and within the signed Unix-nanosecond range).
Optional `--end-seq` narrows the initial stream end; later publications never extend
that bound. Count is admitted-before-filter. Output adds trustworthy `stream`,
decimal-string `sequence` and UTC `stored_time` before `subject`, `headers`, `payload`.
Stored Status headers are data, unlike Core's bodyless-503 sentinel.

History requires stable NATS **2.15.x from 2.15.0**, the default account API route,
and a standalone server or externally assured homogeneous same-series cluster.
Authority is INFO/GET request publication plus reply-inbox subscription; GET is
stream-level, not subject-isolated. There is no direct/consumer/reconnect fallback.
Normal exhaustion is not proof of complete history: retention creates gaps, and
stream replacement is only detected at checks. See the [history guide](docs/modes/nats-history.md)
for exact time bounds, permissions, limits, metadata and count/duration/exhaustion
semantics. Core observation retains its existing server support policy.

## Supported formats

| Format | Extensions      | Notes                       |
| ------ | --------------- | --------------------------- |
| JSON   | `.json`         | Preserves key order; file/pipe output is compact (single-line) |
| YAML   | `.yaml`, `.yml` | Single-document per file; anchors/aliases and merge keys (`<<:`) resolved on decode, not re-emitted |
| TOML   | `.toml`         | Tables and arrays of tables; nested tables emitted as inline syntax |

> **Known behaviors:** Comments are stripped from all formats on output. YAML anchors/aliases are resolved and not re-emitted; merge keys (`<<: *anchor`) are applied at decode time. YAML quoted strings are normalized to bare scalars. TOML `[section]` headers are rewritten to inline-table syntax (`{key = value}`). JSON file/pipe output is compact (single-line). None of these affect the parsed value — only the serialized form.

Format is auto-detected from the file extension. Use `--format json|jsonl|yaml|toml` to override input format, and `--output-format json|yaml|toml` to transcode to a different output format.

JSON, JSONL, YAML, and TOML data output, including serialized editor previews, is automatically syntax-highlighted when stdout is a terminal on Unix. Keys are bold; strings, numbers, and booleans use distinct basic terminal colors. Nulls use the terminal's default color. Native TOML date/time literals and YAML timestamps/custom-tagged values remain uncolored. Set `NO_COLOR` to a nonempty value or `TERM=dumb` to disable highlighting. Pipes, redirected files, in-place edits, backups, diffs, help, diagnostics, and editor query text remain plain. Windows output remains uncolored.

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
make test-release # sandboxed local release checks (no GitHub writes)
make test-install # sandboxed native install checks
make test-all   # lint + unit + integration + release + install checks
make lint       # golangci-lint run
make docs       # generate reference docs + build site
```

Development requires Go 1.27.2 or later and golangci-lint v2.14.0 (built with Go 1.27 or later). CI selects the baseline from `go.mod` across Ubuntu and macOS. A weekly, non-blocking canary tests the latest gojq and go-toml/v2 dependencies starting from that baseline; automatic toolchain selection may advance Go if those dependencies require it.

Install the matching linter with:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0
```

Go 1.27-built binaries require macOS 13 Ventura or later, or Linux kernel 3.2 or later on the supported Linux targets. Prebuilt binary users do not need Go installed.

Releases are prepared and published locally with `make bump-version`, `make release`,
and `make release-cli`; CI does not publish binaries or Homebrew formulas.
See [the maintainer release guide](docs/releasing.md) for prerequisites, safeguards,
asset names, and partial-upload recovery.

## License

See [LICENSE](LICENSE). YAML highlighting, native NATS and Protobuf dependency attributions and supplied licenses are in [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md), also included in release archives.
