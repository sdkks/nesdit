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

Whole-body UTF-8 inspection (JSON output only, no editing):
  nesdit --input-file notes.txt --input-codec text
  producer | nesdit --input-stdin --input-codec text --project 'split("\n")'
Choose exactly one source: a single regular file (commas literal, - not stdin)
or explicitly true stdin. Repeated sources, positionals and unrelated options
fail. --input-codec text is required; --to json is optional. Read through EOF,
at most 10 MiB. Empty input, BOM, NUL, CR/LF and whitespace remain string content;
invalid UTF-8 fails before filtering. Queries compile before reading and see the
original string root. No projection means no identity normalization. A false
filter succeeds with no output; projection must yield one JSON value.
Output is one JSON value plus LF, pretty on terminals and compact otherwise,
with exact 16 MiB size admission before encoding. Never redirect over the input:
the shell can truncate it first. No local overall timer; open stdin may wait.
One interrupt requests stopping on Linux/macOS with the existing one-second
process fallback; embedded Execute requires responsive borrowed I/O.
For native whole-body strings, use --nats-codec text with Core or history.
The unchanged envelope, including metadata and layout, must fit 16 MiB. Ordinary
empty bodies become strings; Core bodyless status remains an error, while stored
history Status headers are data. No descriptor flags with text. See the text
guide for independent budgets, output failures and lifecycle qualifications.

Local MessagePack inspection (JSON output only, no encoding):
  nesdit --input-file event.msgpack --input-codec msgpack > event.view.json
  producer | nesdit --input-stdin --input-codec msgpack
Uses the same explicit sources, JSON target, query/output and stop rules as text.
Exactly one complete value and EOF are required; empty input and any trailing
byte, even LF, fail before filtering. Maps become ordered $map entry arrays,
retaining duplicate and compound keys. $int/$float strings preserve numeric
values; $bin/$str64 distinguish binary from invalid-UTF-8 strings. $ext is opaque
except timestamp -1, which becomes $time with exact seconds/nanoseconds.
Float32 widens exactly; wire width, opcode choices and NaN bits are not retained.
Queries see this actual MessagePack JSON v1 data. Wrappers/base64 independently
count toward depth 100, one million values and 16 MiB key/string bytes; decoded
semantics have separate budgets. See the MessagePack guide for the full grammar.
Encoding, JSON/rich-JSON import, binary targets and broker MessagePack are not
implemented. Never redirect over the source file.

Standalone Protobuf inspection (JSON output only, no editing):
  nesdit --protobuf-file event.bin --descriptor-set schemas/events.pb --message-type example.Order
  producer | nesdit --protobuf-stdin --descriptor-set schemas/events.pb --message-type example.Order \
    --filter '.active == true' --project '{id: .id}'
Choose exactly one source explicitly. One raw body is read through EOF, at most
10 MiB; no prefixes or framing are inferred. Open stdin can wait indefinitely:
there is no standalone timer. Schema and queries are prepared before payload
input. Output is the JSON value only, pretty on terminals, compact otherwise,
with one LF; a false filter writes nothing. Use --filter/--project, not legacy
query, format, presentation, editing, timeout or resource flags. All descriptor
imports and Any targets must be local. See the Protobuf inspection guide for
mapping, conservative limits and cooperative versus process-stop behavior.

Retained JetStream history (consumer-free; JSON default):
  nesdit --nats-history ORDERS --subject 'orders.>' --start-seq 1 \
    --server nats://127.0.0.1:4222 --duration 30s --count 20
Choose exactly one inclusive --start-seq or timezone-qualified --start-time;
optional --end-seq narrows the initial stream end. Time precision is at most
nanoseconds, within 1677-09-21T00:12:43.145224192Z through
2262-04-11T23:47:16.854775807Z inclusive. Later publications do not extend the range.
Add the same --nats-codec protobuf and schema flags for raw Protobuf history.
Output adds stream, decimal-string sequence and UTC stored_time before subject,
headers, payload. Queries still see only the decoded payload root. Stored Status
headers are data, unlike Core's bodyless-503 sentinel.
History requires stable NATS 2.15.x (from 2.15.0), default account-local API routing,
and a standalone server or homogeneous same-series cluster. Runtime version checks
do not certify every future leader; cluster homogeneity is an external precondition.
INFO/GET request publication plus reply-inbox subscription authority is required;
GET is stream-level, not a subject ACL. No consumers, ACKs, application publication,
stream changes, direct reads, reconnect or fallback. Reads neither files nor stdin.
Initial INFO establishes readiness. Empty/no-match/range exhaustion succeeds with
reason=exhaustion, not a completeness guarantee: retention creates gaps and stream
replacement is only detected at checks. Count/duration stops may leave unread data.
History-only flags are invalid in other modes. See the history guide for protocol
limits, retention caveats, permissions and stopping behavior.

Live Core NATS observation (JSON default; reads neither input documents nor stdin):
  nesdit --nats 'orders.>' --server nats://127.0.0.1:4222 --duration 30s --count 3 \
    --filter '.show == true' --project '{id: .id}'
For Protobuf, add --nats-codec protobuf --descriptor-set schemas/events.pb
--message-type example.Order. The local schema is prepared before connection and
the duration starts; no codec or framing is guessed. Schema flags are invalid
with JSON or text. Output remains subject, headers, payload, unlike standalone values.
The shared Protobuf component limits apply, plus the 16 MiB final event limit.
Server and positive total duration are required; count is optional and counts
messages admitted before filtering, not matches or all publications. Queries see
the body root: use .id, not the displayed .payload.id. --filter requires exactly
one boolean (false suppresses without projection); --project exactly one JSON
value. Use comparisons, not select(); zero/multiple results are errors.
Observation is live and at-most-once; readiness does not guarantee complete
delivery. No reconnect.
Security files require tls://: --creds for a user JWT/NKey file, --tls-ca for
custom trust, and paired --tls-cert/--tls-key for mutual TLS. Files must be regular,
at most 1 MiB each, and are validated/loaded before the duration starts. Omitted
CA uses system trust; certificate/hostname verification is never disabled.
Do not put credentials in URLs. Token/user-password-only, TLS-first-only, proxy
and WebSocket connections are not supported. First interrupt covers preparation.
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
      --arg stringArray             bind $K=V in the query as a literal string (repeatable)
      --argjson stringArray         bind $K=V in the query as a JSON-decoded value (repeatable)
      --backup string[=".bak"]      write a sibling backup before each in-place edit (default suffix .bak; custom: --backup=.orig); requires -i
      --check                       exit 2 if the query would change the input; exit 0 if identical; exit 1 on error
      --count uint                  NATS: stop after N messages admitted for processing, before filtering; may show fewer events
      --create-missing              allow queries to create keys/paths that do not exist in the input document; by default, absent paths are rejected
      --creds string                NATS: JWT/NKey credentials file (regular, at most 1 MiB); requires tls://
      --descriptor-set string       Protobuf: required self-contained local binary FileDescriptorSet, at most 4 MiB
  -n, --dry-run                     emit a unified diff of before/after to stdout; do not write any file
      --duration duration           NATS: required total observation limit including connection setup; not an idle timeout
      --edit                        open $EDITOR on a temp copy of the file; diff before/after and emit suggested query
      --end-seq uint                History: optional positive inclusive end sequence; initial stream end always bounds reads
      --filter string               Inspection: decoded-root predicate returning exactly one boolean; false suppresses
      --format string               force input format (json|jsonl|yaml|toml); default is extension-based detection
  -f, --from-file string            load query from file (mutually exclusive with --query)
  -h, --help                        help for nesdit
  -i, --in-place                    edit file(s) in-place using atomic temp+rename writes
      --input-codec string          Inspection: required input codec: text (whole UTF-8 body) or msgpack (MessagePack JSON v1 view)
      --input-file stringArray      Inspection: one regular file; commas are literal, - is not stdin
      --input-stdin                 Inspection: explicitly read one complete body through EOF from stdin; no overall timer
      --keep-going                  continue processing after per-document errors; errored docs are skipped; exit 1 at end if any errors occurred
      --log-format string           stderr rendering mode: text (default, canonical lines) or json (NDJSON) (default "text")
      --max-bytes int               reject inputs larger than this many bytes; 0 disables the cap (default 10485760)
      --max-depth int               reject documents nested deeper than this; 0 disables the cap (default 1000)
      --max-query-bytes int         reject query files (--from-file) larger than this many bytes; 0 disables the cap (default 1048576)
      --max-yaml-nodes int          YAML node-materialisation cap (billion-laughs mitigation); 0 disables the cap (default 100000)
      --message-type string         Protobuf: required exact fully qualified message name (no leading dot or URL)
      --nats string                 NATS: observe one live subject expression; reads neither input documents nor stdin
      --nats-codec string           NATS: payload codec json, protobuf or text (whole UTF-8); protobuf requires --descriptor-set and --message-type (default "json")
      --nats-history string         NATS: inspect bounded retained history from an explicit stream without consumers (2.15.x only)
      --nats-output string          NATS: auto (terminal pretty, redirected JSONL), jsonl, or pretty (multiline documents) (default "auto")
      --output-format string        output format (json|yaml|toml); defaults to same as input format when not specified. jsonl is not a valid output format: JSONL output happens automatically when the input format is jsonl (one output line per input line). When outputting toml with multiple documents, documents are separated by +++ (Hugo-style convention; not TOML spec)
      --pretty                      emit human-friendly TOML output: multi-line arrays, expanded tables, blank lines between entries (currently only affects TOML output)
      --project string              Inspection: decoded-root projection returning exactly one JSON value
      --protobuf-file stringArray   Protobuf: inspect one raw regular file as JSON; no positional input, framing, or editing
      --protobuf-stdin              Protobuf: explicitly read one raw message through EOF from stdin; no overall timer
      --query string                jq-style query (default '.' identity when neither --query nor -f is given)
      --server string               NATS: required explicit nats://host:port or tls://host:port
      --start-seq uint              History: inclusive positive starting sequence; exactly one of --start-seq/--start-time
      --start-time string           History: inclusive stored time, timezone-qualified RFC3339, at most nanoseconds, within signed Unix-nanosecond range
      --strict                      halt on the first document error (default behaviour; explicit alias for documentation)
      --subject string              History: required subject pattern; explicit > selects all
      --timeout duration            cancel the query after this duration (e.g. 500ms, 30s); 0 disables the cap
      --tls-ca string               NATS: PEM CA file (regular, at most 1 MiB); requires tls://; omitted uses system trust
      --tls-cert string             NATS: PEM client certificate file (regular, at most 1 MiB); requires --tls-key and tls://
      --tls-key string              NATS: PEM client private key file (regular, at most 1 MiB); requires --tls-cert and tls://
      --to string                   Inspection: output target: json (default "json")
      --where string                filter docs by jq predicate: only matching docs have --query applied (stream: others pass through; in-place batch mode (-i): non-matching files are skipped; not supported in single-file stdout mode)
      --yaml-version string         YAML dialect for boolean coercion: 1.1 (yes/no/on/off as bools) or 1.2 (true/false only; default) (default "1.2")
```

###### Auto generated by spf13/cobra on 18-Oct-2026
