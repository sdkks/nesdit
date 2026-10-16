## nesdit

Edit structured config (JSON/YAML/TOML) with jq-style queries

### Synopsis

nesdit reads a single JSON, YAML, or TOML document, applies a jq-style
query, and writes the result to stdout.

Supported modes:
  - File input with --query '<jq>' or --from-file / -f <path>.
  - STDIN stream mode: omit the file argument (or pass -) to read from stdin.
  - --edit to open $EDITOR on the file, then emit a suggested query from the diff.
  - --arg K=V (string) and --argjson K=V (JSON-decoded) bindings.
  - --format <json|jsonl|yaml|toml> to override extension-based detection.
  - $${VAR} literal escape in a query (nesdit never expands shell env).
  - -i / --in-place to edit files atomically in place.
  - -n / --dry-run to preview changes as a unified diff (no writes).
  - --check to gate on drift: exit 2 if the query changes the input.
  - --pretty to emit human-friendly TOML output (multi-line arrays, expanded
    inline tables, blank lines between entries); silently ignored for non-TOML
    output formats.
  - --timeout <dur> to cancel a runaway query on a deadline.
  - --max-bytes, --max-depth, --max-yaml-nodes, and --max-query-bytes
    resource caps for decode-phase hardening.

Live Core NATS JSON observation (reads neither files nor stdin):
  nesdit --nats 'orders.>' --server nats://127.0.0.1:4222 --duration 30s --count 3
Server and positive total duration are required; count is optional and counts
messages admitted for processing, not all publications. Observation is live and
at-most-once; readiness does not guarantee complete delivery. No reconnect.
--nats-output auto uses pretty JSON on a terminal, JSONL when redirected;
explicit pretty is a sequence of multiline JSON documents, not JSONL.
One interrupt stops observation on Linux/macOS. A one-second forced-stop fallback
may truncate the final record and omit final diagnostics. Check exit status and
validate redirected output. Exit 0 means normal count/ready-duration completion,
1 means failure, and 130 means interruption. Earlier complete output is retained.
See the NATS observation guide for component limits and trust assumptions.

```
nesdit [flags] <file> [<file>...]
```

### Options

```
      --arg stringArray          bind $K=V in the query as a literal string (repeatable)
      --argjson stringArray      bind $K=V in the query as a JSON-decoded value (repeatable)
      --backup string[=".bak"]   write a sibling backup before each in-place edit (default suffix .bak; custom: --backup=.orig); requires -i
      --check                    exit 2 if the query would change the input; exit 0 if identical; exit 1 on error
      --count uint               NATS: stop after N messages admitted for processing; may show fewer events
      --create-missing           allow queries to create keys/paths that do not exist in the input document; by default, absent paths are rejected
  -n, --dry-run                  emit a unified diff of before/after to stdout; do not write any file
      --duration duration        NATS: required total observation limit including connection setup; not an idle timeout
      --edit                     open $EDITOR on a temp copy of the file; diff before/after and emit suggested query
      --format string            force input format (json|jsonl|yaml|toml); default is extension-based detection
  -f, --from-file string         load query from file (mutually exclusive with --query)
  -h, --help                     help for nesdit
  -i, --in-place                 edit file(s) in-place using atomic temp+rename writes
      --keep-going               continue processing after per-document errors; errored docs are skipped; exit 1 at end if any errors occurred
      --log-format string        stderr rendering mode: text (default, NFR-10 canonical lines) or json (NDJSON per FR-15) (default "text")
      --max-bytes int            reject inputs larger than this many bytes; 0 disables the cap (default 10485760)
      --max-depth int            reject documents nested deeper than this; 0 disables the cap (default 1000)
      --max-query-bytes int      reject query files (--from-file) larger than this many bytes; 0 disables the cap (default 1048576)
      --max-yaml-nodes int       YAML node-materialisation cap (billion-laughs mitigation); 0 disables the cap (default 100000)
      --nats string              NATS: observe one live JSON subject expression; reads neither files nor stdin
      --nats-output string       NATS: auto (terminal pretty, redirected JSONL), jsonl, or pretty (multiline documents) (default "auto")
      --output-format string     output format (json|yaml|toml); defaults to same as input format when not specified. jsonl is not a valid output format: JSONL output happens automatically when the input format is jsonl (one output line per input line). When outputting toml with multiple documents, documents are separated by +++ (Hugo-style convention; not TOML spec)
      --pretty                   emit human-friendly TOML output: multi-line arrays, expanded tables, blank lines between entries (currently only affects TOML output)
      --query string             jq-style query (default '.' identity when neither --query nor -f is given)
      --server string            NATS: required explicit nats://host:port or tls://host:port
      --strict                   halt on the first document error (default behaviour; explicit alias for documentation)
      --timeout duration         cancel the query after this duration (e.g. 500ms, 30s); 0 disables the cap
      --where string             filter docs by jq predicate: only matching docs have --query applied (stream: others pass through; in-place batch mode (-i): non-matching files are skipped; not supported in single-file stdout mode)
      --yaml-version string      YAML dialect for boolean coercion: 1.1 (yes/no/on/off as bools) or 1.2 (true/false only; default) (default "1.2")
```

###### Auto generated by spf13/cobra on 17-Oct-2026
