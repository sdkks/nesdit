# Read-only Protobuf inspection

Inspect one raw binary message using a local schema, without a broker:

```sh
nesdit --protobuf-file event.bin \
  --descriptor-set schemas/events.pb --message-type example.Order

producer | nesdit --protobuf-stdin \
  --descriptor-set schemas/events.pb --message-type example.Order \
  --filter '.active == true' --project '{id: .id}'
```

The **descriptor set** describes the types; the **payload** contains one raw
message's bytes. Both schema options are mandatory. `--message-type` is an exact
fully qualified name, without a leading dot or URL. Obtain a binary
`google.protobuf.FileDescriptorSet` from your schema build, including every
ordinary and option-only import, well-known type and possible `Any` target.
nesdit does not compile `.proto` files, use an ambient registry or fetch schemas.

## Choose exactly one input

- `--protobuf-file FILE` selects one readable regular file. Commas in its name
  are literal. Repeated selections, positional inputs and glob expansion by
  nesdit are not supported. Shell expansion yielding several arguments fails.
- `--protobuf-stdin` explicitly selects stdin. `--protobuf-stdin=false` is an
  error, not a fallback to another mode. Redirecting stdin or supplying schema
  options alone does not select this mode. File mode never reads stdin.
- `--protobuf-file -` is rejected; use `--protobuf-stdin`, or `./-` for a literal
  file named `-`. The descriptor source is always a separate regular file,
  never stdin. Symlinks resolving to regular files are allowed.

Input is read **through EOF**, with an inclusive 10 MiB cap. There is no prefix
removal, decompression, registry-header stripping or guessed message framing.
Exactly 10 MiB still needs EOF or another byte to distinguish complete input
from oversize. Byte 10 MiB+1 fails without waiting for EOF. A zero-byte body at EOF
is legitimate: it succeeds if the selected type permits it, and fails if required
fields are missing. It does not necessarily produce `{}`.

**An open producer below the cap can wait indefinitely.** There is no standalone
overall timer; `--duration` and `--timeout` are not accepted. Use a producer that
finishes and closes its output, or interrupt an unwanted wait. Successful wire
decoding proves neither schema identity nor that wire messages were not concatenated.

## JSON views and queries

Stdout contains only the decoded or projected JSON value: object, array, scalar
or null. There is no subject, headers, filename, source envelope, readiness banner
or successful-stop footer. Output is two-space pretty JSON on a Unix terminal,
compact when redirected, with exactly one final LF. Existing `NO_COLOR` and
`TERM=dumb` controls disable color, not terminal layout. Windows output is plain.

Decoding uses standard ProtoJSON:

- JSON field names; known enum names and numeric unknown enum values;
- decimal strings for 64-bit integers, base64 for bytes, and strings such as
  `"NaN"`, `"Infinity"` and `"-Infinity"` for special floating-point values;
- omitted absent fields and implicit defaults, with explicitly present optional
  and oneof defaults retained;
- standard wrapper, Timestamp, Duration, Struct, ListValue, Value and FieldMask
  mappings, including scalar and array roots;
- locally declared extensions under bracketed names, such as `[example.extra]`;
- expanded `Any`, resolved by the final type-name component of its URL, solely
  within the supplied descriptor set. The hostname is neither fetched nor
  authenticated. Missing targets, invalid embedded data and missing required
  fields inside nested expansions fail rather than yielding opaque bytes.

Unknown wire fields are omitted, including inside `Any`; this is not lossless
binary inspection. Decoded object keys sort lexicographically at every level,
including map keys. Arrays keep their order. This is deterministic product
ordering, not a universal canonical ProtoJSON representation. Invalid schemas,
ambiguous JSON names, duplicate extension numbers and incompatible well-known
type shapes fail during local preparation.

Queries see the decoded root: `.id`, not `.payload.id`. `--filter` must yield
exactly one boolean. False succeeds with **zero stdout bytes** and skips
projection. A retained `--project` must yield exactly one JSON value, evaluated
against the original decoded root. `empty`, multiple results, nonboolean filters
and runtime errors fail. An absent filter retains the value; an absent projection
leaves it unchanged. Projection follows existing inspection ordering and can
construct new keys without editing the binary source:

```sh
nesdit --protobuf-file event.bin \
  --descriptor-set schemas/events.pb --message-type example.Order \
  --project '{order_id: .id, inspected: true}'
```

A projected result is ordinary JSON, not a claim that it can be encoded back into
the original message. Expressions are trusted code: query evaluation may allocate
before result checks and is not a heap sandbox. Decimal/exponent query arithmetic
uses floating point; Protobuf 64-bit integer strings remain strings unless the
query explicitly converts them.

## Compatible options

Standalone accepts only its chosen source, `--descriptor-set`, `--message-type`,
`--filter`, `--project`, `--log-format`, and ordinary help/version behavior.
All other explicitly supplied options fail, even when set to defaults. In
particular, do not add:

- native NATS selectors, server/security options, count, duration or `--nats-output`;
- `--format`, `--output-format` (even `json`), `--pretty`, or `--yaml-version`;
- legacy `--query`, `--where`, `--from-file`, `--arg`, or `--argjson`;
- in-place, backup, check, dry-run, editor, create-missing, strict or keep-going flags;
- timeout or resource-limit overrides.

The full option combination, inline queries and descriptor/type selection are
validated before opening a payload file or reading payload stdin. This path
performs no network access, input mutation, editor launch or backup creation.
Legacy JSON/YAML/TOML file/pipe and native JSON observation retain their meanings.
Protobuf over NATS is not exposed by this standalone workflow.

## Fixed limits

All limits are inclusive; rejection never truncates an admitted value.

| Component | Limit |
| --- | --- |
| Descriptor bytes / files | 4 MiB / 256 |
| Descriptor declarations | 10,000 |
| Descriptor message-tree and import-chain depth | 100 |
| Raw input | 10 MiB |
| Decoded/expanded structure | Depth 100, 1,000,000 value nodes, 16 MiB key/string bytes |
| Actual-shaped JSON view, independently | Depth 100, 1,000,000 value nodes, 16 MiB key/string bytes |
| Aggregate embedded `Any` wire bytes | 10 MiB per input |
| Conservative intermediate ProtoJSON certificate | 16 MiB |
| Each inline query | 1 MiB |
| Projection result | Depth 1000, 1,000,000 value nodes, 16 MiB key/string bytes |
| Final JSON output, including layout and LF | 16 MiB |

Declarations count files, messages, fields, extension fields, oneofs, enums, enum
values, services and methods once each, including synthetic map-entry messages,
fields and oneofs. Options/source locations are not declarations, but their
message nesting and bytes still count. Every descriptor message descent adds a
level from the set root at zero; every import edge adds a level.

For both structure budgets, the root is depth zero; containers and scalars count
as nodes, keys charge bytes but not nodes. Decoded message/list/map containers
count even when a WKT collapses them in JSON. Reflection string values charge
text; integer, enum and special-float string spellings charge text in the JSON
view instead. Byte fields charge base64 length in both shapes.
Each expanded `Any` target descends from its enclosing message. Nested expansions
share the same aggregate wire and structural allowances; they never reset them.
The view budget follows the actual WKT shape, not a descriptor-shaped approximation.

Before full ProtoJSON marshaling, nesdit assesses both shapes and computes a
conservative serialized-size certificate covering escaped text, base64, bounded
numeric spellings, syntax and the pinned serializer's optional comma spaces.
**This certificate can reject a value whose actual serialized bytes would fit.**
Accepted values still undergo actual-size checks against the certificate and
16 MiB, plus actual-view validation. The final JSON buffer is a separate limit:
pretty whitespace, escaping in final presentation and the LF can make it larger.
Redirection chooses compact presentation, but does not bypass any other limit.

These are component safeguards, not a process-memory sandbox or OOM guarantee.
Library allocations can precede checks. Descriptor construction, wire decoding
and marshaling remain synchronous and are not uniformly preemptible.

## Interrupts and failures

On Linux/macOS, one interrupt requests cancellation. Process-owned stdin uses
read deadlines when its handle supports them. Ordinary inherited blocking pipes
may not: a one-second process fallback after a stop request covers stalled reads,
writes, library work, diagnostics and cleanup. Forced stopping can truncate output,
skip cleanup or omit diagnostics. No new Windows bounded-interrupt guarantee is
claimed.

Embedded `run.Execute` is cooperative: all supplied readers/writers are borrowed,
including `*os.File`. They are not automatically closed or given deadlines, and
no blocked reader is abandoned in a detached worker. Callers must provide
responsive I/O or arrange unblocking; `Execute` never exits its host process.

Exit 0 means successful inspection (including a false filter), 1 means failure,
and 130 means interruption/cancellation. EOF finishes acquisition only: decoding,
queries and output must still succeed. Read failures, partial reads and cancelled
acquisition never become empty success. Output is completely buffered before the
external write, so decode/query/encoding failures emit none of the value. A short
write or output error fails, but already-written bytes cannot be recalled.

Diagnostics stay on stderr, identify preparation/acquisition/decoding/query/output
stages and do not quote payload, query or descriptor contents. A certificate
rejection is not a measured-output-size report. A final-output rejection includes
presentation overhead. Correct schema/query errors locally; raising legacy limits
is not a supported recovery action for this mode.
