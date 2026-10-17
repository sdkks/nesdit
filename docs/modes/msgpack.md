# Local MessagePack inspection

Available in the current development source (`make build`), not a new release
announcement. Inspect one local MessagePack value as **MessagePack JSON v1**:

```sh
bin/nesdit --input-file event.msgpack --input-codec msgpack > event.view.json
producer | bin/nesdit --input-stdin --input-codec msgpack
```

This is read-only inspection with optional JSON queries. Encoding, rich-JSON
import, ordinary-JSON conversion, binary output and broker MessagePack are not
implemented. `--to json` is optional; `--to msgpack`, `--input-codec json`,
`msgpack-json` and `flatbuffers` are invalid. No YAML/TOML targets or JSONL streams.

## Source, framing and failure

Choose exactly one `--input-file FILE` or explicitly true `--input-stdin`.
A file must resolve to a regular file; commas are literal, repeated sources and
`-` as a stdin alias fail (use `./-` for a file named `-`). File mode never reads
stdin. No extension/content detection, prefix stripping or schema lookup occurs.
Only source, codec, JSON target, `--filter`, `--project`, `--log-format` and normal
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

Options and queries prepare before input acquisition. The entire source must
pass decoding and view admission before filtering. A false filter cannot hide
malformed or profile-over-limit input. Diagnostics name stages/categories and
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

Each wrapper has exactly one key. Arrays and map entries contain rich values
recursively. Object key names are profile syntax, not application map keys.
Bare JSON numbers occur only in extension-ID and timestamp-nanosecond positions.

- **Maps:** preserve entry order, duplicate keys and compound/non-string keys.
  An application map with a key named `$int` is not confused with an integer:
  `{"$int":"7"}` as a MessagePack application map displays as
  `{"$map":[["$int","7"]]}`.
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
  sign or signaling status are not retained.
- **Bytes:** binary and invalid-UTF-8 str remain distinct. Base64 is canonical,
  padded standard RFC 4648 spelling. Valid UTF-8 str stays a plain JSON string;
  invalid bytes do not become replacement characters or binary values.
- **Extensions:** signed IDs −128 through 127 other than −1 remain opaque,
  including reserved negative IDs; bytes are retained with no application decoder.
- **Timestamp −1:** accept valid 4-, 8- and 12-byte layouts and equivalent
  extension headers. Seconds are exact signed-int64 decimal strings, nanoseconds
  are integers 0–999999999. Invalid lengths/nanoseconds fail, never opaque fallback.
  No `time.Time`, RFC3339 range or timezone normalization is imposed.

This view preserves semantic categories and values, not wire bytes, header
choices or producer identity. A future encoder is separate work; the current CLI
cannot reimport this view or reconstruct a binary. Even with a future encoder,
reconstruction is conditional on both representations fitting their limits:
a 10–16 MiB JSON export exceeds the 10 MiB input cap.

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
but is not automatically a valid rich interchange value.

## Output, resources and stopping

Output is one JSON value plus LF, no envelope/banner. Supported Unix terminals
use two-space pretty layout and normal terminal styling; redirection is compact
and plain. `NO_COLOR` or `TERM=dumb` disables styling, not indentation. Windows
output stays plain/compact. Payload controls are JSON-escaped, never raw terminal
instructions.

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
- Output: exact 16 MiB including escaping, punctuation, indentation and final LF,
  checked before serialization. Styling is separate from authoritative JSON bytes.

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
