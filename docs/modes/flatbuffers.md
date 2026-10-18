# Read-only FlatBuffers inspection

Available in the current development source (`make build`), not a new release
announcement. Inspect one local ordinary FlatBuffers payload against one
separate ordinary BFBS compiled schema and receive a known-field JSON view:

```sh
bin/nesdit --input-file event.bin --input-codec flatbuffers --bfbs schemas/event.bfbs
producer | bin/nesdit --input-stdin --input-codec flatbuffers --bfbs schemas/event.bfbs \
  --filter '.active' --project '{id: .id, label: .label}'
```

JSON is the only target (`--to json` is optional). Neither file is modified,
no network or schema lookup occurs, and success certifies only the known
fields it shows — not schema provenance, unknown payload fields or every
byte belonging to one message.

## Source, schema and framing

Choose exactly one `--input-file FILE` or explicitly true `--input-stdin` for
the payload. `--bfbs FILE` is required, must be exactly one nonempty regular
file, must be separate from the payload file and is never stdin. File mode
never reads stdin. Only source, codec, permitted target, `--filter`,
`--project`, `--log-format` and normal help/version options are accepted;
positional inputs, legacy editing/format/query options, Protobuf/native
settings, timers and limit overrides fail even at their defaults.

Both inputs are ordinary, not size-prefixed: a size prefix is not stripped
and there is no alternate-root selection, runtime compiler, `.fbs` parsing,
registry or broker path. The whole BFBS (at most 4 MiB) is validated and
compiled to a finite root-reachable type plan **before any payload byte is
read**; query compilation comes before both. The payload is one complete
body through EOF, at most 10 MiB, with the same cap-plus-one semantics as
the other local codecs; an unfinished under-cap producer can wait
indefinitely. EOF is the supplied boundary: unreferenced padding, trailing
or appended bytes are not detected or rejected. If the schema declares a
nonempty file identifier it must be exactly four bytes and match the
payload's `[4:8]`; success does not authenticate the schema.

## Known-field view

Known declared fields appear in field-ID order, **including deprecated
fields**. Unknown payload fields are omitted, not verified. A short vtable
means trailing fields are absent.

| Construct | JSON view |
| --- | --- |
| Bool | `false` for byte 0, `true` for any nonzero byte |
| 8/16/32-bit integers | JSON numbers, no float intermediary |
| All 64-bit integers (including enums and union tags) | exact decimal strings, preserving unsigned high-bit values |
| Float | float32 promotes exactly to float64; finite values use shortest round-trip numbers; payload-stored or metadata-stored negative zero is the string `-0`; specials are `NaN`, `Infinity`, `-Infinity` |
| Strings | valid UTF-8 with a checked terminator; embedded NUL stays content |
| Vectors | order-preserving arrays; present-empty stays `[]`; `[ubyte]` is a numeric array |
| Enums | underlying numeric value, including values unknown to the schema |
| Single unions | schema-named discriminator sibling plus the active value |
| Tables/structs | objects; struct fields are always present |

Defaults and presence: absent ordinary scalars emit the BFBS-represented
default, including metadata-default positive zero when the compiler omitted
an explicit equal default; explicitly stored equal defaults are
indistinguishable from omission. Absent optional scalars and absent
references (strings, vectors, tables, structs) are omitted; explicitly
present zero, `false` and empty values are retained. Required
references must be present. Unions: a zero discriminator with no value emits
the tag and omits the value; a zero tag **with** a populated value slot
fails, as does a nonzero tag without a valid present value, an unknown
active tag, an invalid pairing or width, and a required union value that is
absent.

## Default-fidelity caveat (important)

BFBS is authoritative for absent scalar defaults — not a guarantee of
fidelity to the original `.fbs` application accessors. The pinned compiler
compares defaults numerically, so it can **omit** an original `.fbs`
negative-zero default (it equals `+0`) and `uint64` defaults above
`int64` max. For such an absent field this view therefore shows `0`, while
the original-schema generated accessor yields `-0` (or the large value).
The lost sign/value cannot be recovered or selectively detected from BFBS:
an omitted original `+0` looks identical. Negative zero actually supplied
by payload bytes or by stored metadata bits (including defaults narrowed
through a declared `float` width) is preserved. This is a documented
inspection-fidelity boundary, not a corruption of your data.

## Compatibility profile

Supported: the BFBS-declared root table; ordinary scalar/string/table/struct
vectors; force-aligned structs; shared objects and vtables, including
negative shared-vtable displacement; recursive **table** types; signed and
unsigned 8/16/32/64-bit single-union discriminators, sparse tags, aliases,
and table, struct and string union members; optional scalars; deprecated
fields.

Rejected during preparation, even when the payload field would be absent:
root-reachable fixed-length arrays, union/discriminator vectors, `Vector64`
and any field with `offset64`; deprecated fields participate in
reachability. Rejected globally: the `DefaultVectorsAndStrings` feature bit,
any unknown feature bit, unknown base types and populated unknown metadata
slots. Unrelated known-unsupported declarations elsewhere in the schema are
structurally checked but do not prevent reading the declared root.

Valid FlatBuffers outside this profile fail with an actionable diagnostic;
that is a compatibility boundary, not a malformed input. Size-prefixed
buffers, alternate roots and encoding/production of FlatBuffers are out of
scope.

## Budgets, output and lifecycle

Schema and payload are treated as untrusted: every offset, vtable, length,
alignment and layout is validated by an owned checked reader before use,
with checked arithmetic before any length-driven allocation. Component
budgets: BFBS 4 MiB; metadata 10,000 unique declarations, depth 100, one
million work units and 16 MiB processed strings; payload 10 MiB; decoded
view depth 100, one million nodes and 16 MiB aggregate text (absent
optional/reference fields still charge a conceptual node, bounding repeated
tables with many absent fields; repeated references charge every
occurrence); JSON output 16 MiB including layout and final LF, admitted by
an exact size preflight before serialization. ANSI terminal presentation is
separate from authoritative JSON bytes.

Diagnostics name stages/categories and numeric structural positions, never
payload or schema values, field names or query text. Success and
false-filter suppression exit 0, failure exits 1, cancellation 130. One
interrupt requests stopping on Linux/macOS with the existing one-second
process fallback; embedded execution requires responsive borrowed I/O.
Never redirect over the input or schema: the shell can truncate either
before nesdit starts, although nesdit itself never opens them for writing.
