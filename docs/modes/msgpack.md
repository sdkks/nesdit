# Local MessagePack inspection and conversion

Available in the current development source (`make build`), not a new release
announcement. Inspect one local MessagePack value as **MessagePack JSON v1**:

```sh
bin/nesdit --input-file event.msgpack --input-codec msgpack > event.view.json
bin/nesdit --input-file event.view.json --input-codec msgpack-json --to msgpack > rebuilt.msgpack
bin/nesdit --input-file ordinary.json --input-codec json --to msgpack > event.msgpack
producer | bin/nesdit --input-stdin --input-codec msgpack
```

Sources are never modified. The local source/target matrix is explicit:

| Input codec | Target | Interpretation |
| --- | --- | --- |
| `text` | `json` only (default) | Whole UTF-8 string |
| `msgpack` | `json` (default) or explicit `msgpack` | MessagePack JSON v1 query root |
| `json` | Explicit `msgpack` only | Ordinary JSON; objects remain application maps |
| `msgpack-json` | Explicit `msgpack` only | Strict MessagePack JSON v1 |

No broker MessagePack, FlatBuffers, direct YAML/TOML conversion, JSONL streams,
managed output path, binary in-place edit, backup or force-terminal option.

## Source, framing and failure

Choose exactly one `--input-file FILE` or explicitly true `--input-stdin`.
A file must resolve to a regular file; commas are literal, repeated sources and
`-` as a stdin alias fail (use `./-` for a file named `-`). File mode never reads
stdin. No extension/content detection, prefix stripping or schema lookup occurs.
Only source, codec, permitted target, `--filter`, `--project`, `--log-format` and normal
help/version options are accepted. Positional inputs, legacy editing/format/query
options, Protobuf/native settings, timers and limit overrides fail even when
explicitly set to defaults. Orphaned codec/target options fail.

One complete body is acquired through EOF, at most 10 MiB including headers.
Exactly at the cap, EOF or a next byte must be observed; cap-plus-one fails
without waiting for more. An unfinished under-cap producer can wait indefinitely:
there is no local overall timer. The body must contain exactly one value.
Empty input, reserved opcode `0xc1`, truncation, a second value and **any trailing
byte, including LF**, fail. An empty MessagePack string, array, map or binary
value is valid; a zero-byte file is not.

For ordinary or rich JSON, one complete JSON value is required, followed only
by JSON whitespace. Whole-body UTF-8, escapes and paired UTF-16 surrogate escapes
are validated; duplicate decoded object keys fail rather than overwrite. A bounded
lexical pass admits source depth/nodes/decoded strings before building the tree.

Options and physical binary-destination safety are checked before query preparation
and input acquisition. The entire source must pass decoding and source-profile
admission before filtering. A false filter cannot hide malformed rich tags or
profile-over-limit input. Valid ordinary JSON numbers outside the binary target's
range remain valid sources: filtering can suppress them or projection can remove
them. Target validation occurs only after selection, before encoding and writing.
Diagnostics name stages/categories and
numeric positions, never payload values, map keys, query text or evaluator data.
Success and false-filter suppression exit 0, failure exits 1, cancellation 130.

## MessagePack JSON v1

The JSON data itself carries the types; there are no invisible tags.

| MessagePack category | JSON view |
| --- | --- |
| nil, boolean, valid UTF-8 string | `null`, boolean, string |
| array | Array of rich values in original order |
| integer | `{"$int":"18446744073709551615"}` |
| float | `{"$float":"0.1"}` |
| binary | `{"$bin":"AP8="}` |
| invalid-UTF-8 str | `{"$str64":"/w=="}` |
| opaque extension | `{"$ext":[7,"AP8="]}` |
| timestamp | `{"$time":["-1",999999999]}` |
| map | `{"$map":[[key,value],...]}` |

Each wrapper has exactly one known key and the exact illustrated arity/types.
Unknown/extra keys and bare rich numbers fail except in the designated extension-ID
and timestamp-nanosecond positions. Arrays and map entries contain rich values
recursively. Object key names are profile syntax, not application map keys.

- **Maps:** preserve entry order, duplicate keys and compound/non-string keys.
  An application map with a key named `$int` is not confused with an integer:
  `{"$int":"7"}` as a MessagePack application map displays as
  `{"$map":[["$int","7"]]}`. The JSON source `{"$int":"7"}` encodes an
  application map under `--input-codec json` but integer 7 under `msgpack-json`.
- **Integers:** exact mathematical domain −2^63 through 2^64−1. Decimal strings
  have no plus sign, leading zero, exponent or integer `-0`. A positive signed
  opcode and a positive fixint of the same value have the same view. Producer
  signedness, opcode and width are not retained.
- **Floats:** finite values use shortest binary64-round-tripping spelling.
  Float32 is first decoded at its original width, then widened exactly: float32
  near 0.1 displays `{"$float":"0.10000000149011612"}`, while float64 0.1 displays
  `{"$float":"0.1"}`. Special strings are `-0`, `NaN`, `Infinity`, `-Infinity`;
  positive zero is `0`. Integer and Float remain distinct. Decimal text identifies
  an IEEE value, not an exact rational decimal; original width and NaN bits,
  sign or signaling status are not retained. Typed finite strings accept decimal
  and exponent syntax (including `+1`, `.5`, `1.` and leading zeros), not hex,
  separators or whitespace. Parsing rounds nearest-even to binary64; overflow
  and nonzero coefficients rounding to zero fail. Representable subnormals work.
  Every encoded Float uses float64 opcode `0xcb`, even integral values; NaN uses
  canonical quiet bits `0x7ff8000000000000`.
- **Bytes:** binary and invalid-UTF-8 str remain distinct. Base64 is canonical,
  padded standard RFC 4648 spelling. Valid UTF-8 str stays a plain JSON string;
  invalid bytes do not become replacement characters or binary values. Rich import
  rejects whitespace, missing padding and nonzero pad bits in base64. `$str64`
  requires invalid-UTF-8 bytes; valid UTF-8 (including empty) must be a plain string.
- **Extensions:** signed IDs −128 through 127 other than −1 remain opaque,
  including reserved negative IDs; bytes are retained with no application decoder.
- **Timestamp −1:** accept valid 4-, 8- and 12-byte layouts and equivalent
  extension headers. Seconds are exact signed-int64 decimal strings, nanoseconds
  are integers 0–999999999. Invalid lengths/nanoseconds fail, never opaque fallback.
  No `time.Time`, RFC3339 range or timezone normalization is imposed. Encoding
  chooses the shortest legal 4-, then 8-, otherwise 12-byte layout.

Encoding is deterministic for the same selected semantic value and interpretation.
Integers use minimal valid encodings. Unmodified rich export/reimport preserves
semantic categories and values, not wire bytes, header choices, float width or
producer identity, **only when both representations fit their limits**:
a 10–16 MiB JSON export exceeds the 10 MiB input cap.

## Ordinary JSON numbers

Integer lexemes without fraction/exponent remain exact in −2^63…2^64−1.
Fraction/exponent lexemes select nearest-even binary64: `0.1` is deliberately
rounded, `1e3` is Float 1000, and `1000` is Integer 1000. Lexical negative zero,
including `-0`, `-0.0` and `-0e3`, becomes Float −0. Overflow such as `1e309` and
nonzero-to-zero underflow such as `1e-4000` fail selected-target validation;
representable subnormals such as `5e-324` work. Exponent magnitude never drives
arbitrary powers or rational allocations.

For example, ordinary `{"bad":1e309,"ok":7}` is a valid source. `--filter false`
succeeds without output, and `--project .ok` encodes 7. Without selection removing
the invalid target, encoding fails with no value emission. Rich
`{"$float":"1e309"}` is invalid source grammar and fails even with a false filter.

## Query the actual view

Maps are entry arrays, so `.name` is not a shortcut. Collect duplicate matches
into one result:

```sh
bin/nesdit --input-file event.msgpack --input-codec msgpack \
  --filter 'any(."$map"[]; .[0] == "name")' \
  --project '[."$map"[] | select(.[0] == "name") | .[1]]'
```

For a known unique key, `."$map"[] | select(.[0] == "name") | .[1]` selects its
value. Zero or multiple results fail the exactly-one projection rule; collecting
produces an array, not deduplication. Filters must produce exactly one boolean.
False succeeds with no output and skips projection and output-size admission.

Queries see the original rich root, not a source envelope or the filter's
normalized copy. Without projection, no identity query normalizes the value.
Integer/float strings protect their precision from incidental query-number
conversion; explicit arithmetic/conversion queries can intentionally lose it.
Arbitrary projected JSON, such as `{names: [."$map"[][1]]}`, can be displayed
but is not automatically a valid rich interchange value. With `--to msgpack`,
the selected result must satisfy the source's interpretation (`json` stays ordinary;
`msgpack` and `msgpack-json` stay rich). For example, projecting bare `7` from a
rich source fails binary-target validation; projecting `{"$int":"7"}` succeeds.
Ordinary projections use existing gojq normalization and can change numeric types,
lexemes or precision. No numeric lexical-fidelity promise survives projection.
A filter alone does not normalize the original selected value.

## Output, resources and stopping

JSON inspection output is one value plus LF, no envelope/banner. Supported Unix terminals
use two-space pretty layout and normal terminal styling; redirection is compact
and plain. `NO_COLOR` or `TERM=dumb` disables styling, not indentation. Windows
output stays plain/compact. Payload controls are JSON-escaped, never raw terminal
instructions.

Binary stdout has no LF, delimiter, ANSI or presentation markup. Actual Linux/macOS
terminals and Windows consoles are refused before payload access, independently
of `NO_COLOR` and `TERM=dumb`, even with a false filter. Other platforms without
an implemented physical probe fail unsupported. There is no force override.
An opaque embedded writer cannot be inspected: its caller must supply a safe
binary sink. Nesdit does not unwrap it, close it or change its deadlines.

Independent inclusive budgets apply before length-driven allocations:

- Raw body: 10 MiB.
- Decoded semantics: root depth 0, depth 100, 1,000,000 values, 16 MiB aggregate
  str/bin/ext payload bytes. Map keys and values each count as values.
- Actual view: depth 100, 1,000,000 JSON values, 16 MiB key/string bytes.
  Every wrapper, entries array, pair array and decimal/base64 string counts.
  Object key names count as text, not value nodes. Base64 expansion is admitted
  before allocation. Counters do not reset for subtrees.
- Queries: existing 1 MiB per inline query; result depth 1000, 1,000,000 values
  and 16 MiB key/string bytes.
- Output: 16 MiB of authoritative bytes. JSON uses exact preflight including
  escaping, punctuation, indentation and final LF; binary uses guarded complete
  staging. Styling is separate from authoritative JSON bytes.

Rich JSON import independently admits the actual JSON tree and semantic value.
Ordinary import first admits its source tree; after selection, semantics and the
hypothetical rich-view expansion are independently admitted without materializing
wrappers. Rich binary targets recheck actual view and semantic limits after
projection; the query result's deeper budget is not binary-profile admission.

Rich wrappers can fail the view budget before the decoded budget. A source and
view that fit can still exceed the output budget. A false filter skips only that
final output check. These are component/work bounds, not hard RSS/CPU guarantees,
an OOM guarantee or a sandbox for trusted queries.

Nesdit never writes the input file. **Never redirect over the input:** the shell
may truncate it before nesdit starts. Use distinct paths as above. Complete
staging prevents output on pre-write failure; an external write or short-write
failure can still leave partial stdout.

On Linux/macOS one interrupt requests stopping, with the existing one-second
process fallback for stalled work, I/O or diagnostics, including option errors
after a recognized source selector. Forced stop can truncate output and omit
cleanup. Embedded `Execute` remains cooperative: no host exit, detached blocked
worker, borrowed-handle close or deadline change. Callers must provide responsive
or explicitly unblocked I/O. No Windows bounded-stop promise is added.
