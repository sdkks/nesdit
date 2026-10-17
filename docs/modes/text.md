# Whole-body UTF-8 inspection

Available in the current development source; these examples do not announce a
new published release. Build the checkout with `make build` and use `bin/nesdit`,
or use a binary built from this source.

```sh
bin/nesdit --input-file notes.txt --input-codec text
producer | bin/nesdit --input-stdin --input-codec text --project 'split("\n")'
```

Select exactly one source. `--input-file` takes one nonempty regular-file path;
commas are literal, repeated selections fail, and `-` is not a stdin alias.
Symlinks resolving to regular files are allowed. `--input-stdin` must be explicitly
true and supplied once; `--input-stdin=false` fails. File mode never reads stdin
and neither source is opened for writing. Positional filenames retain their
existing editing behavior: a file named `text` does not select this mode.

`--input-codec text` is mandatory. The only local target is JSON, also selectable
as `--to json`. No charset, file-extension or content guessing is performed.
MessagePack, JSON conversion and FlatBuffers inputs are not available through
these flags. Existing Protobuf selectors remain separate.

## One body, one string

The whole source is read through EOF, up to **10 MiB inclusive**, then validated
as UTF-8. Empty input is the empty string `""`. A UTF-8 BOM, NUL, tabs, CR/LF and
surrounding whitespace are content: nothing is trimmed, split or normalized.
Invalid UTF-8 fails, without replacement characters or arbitrary-byte fallback.
There is no raw-text export; output is a JSON representation of the string.

At exactly 10 MiB, the reader must see EOF or the next byte; byte 10 MiB+1 fails
without waiting for the producer to close. An unfinished smaller producer may
wait indefinitely. Read failures never become empty success. There is no local
overall timer.

## Filtering and projection

```sh
bin/nesdit --input-file notes.txt --input-codec text \
  --filter 'contains("error")' --project '{body: ., characters: length}'
```

Queries compile before payload reads. Both queries see the **original string
root**, not a source envelope or the filter's evaluator copy. Use `contains`,
`length` or `split` rather than `.payload` to inspect text.

- `--filter` must yield exactly one boolean. False succeeds with zero output and
  skips projection. Malformed or oversized input still fails before the filter.
- `--project` must yield exactly one JSON value: string, number, boolean, null,
  array or object. Empty/multiple results and evaluation failures are errors.
- Without projection, the decoded string bypasses identity-query normalization,
  including when a filter is present.

Only the source, codec, JSON target, `--filter`, `--project`, `--log-format` and
normal help/version options are accepted. No positional input, Protobuf/native
settings, legacy `--query`/`--where`, format/edit/presentation flags, query files,
argument bindings, resource overrides or timers are allowed, even at explicitly
supplied default values. Orphaned `--input-codec` or `--to` flags fail.

## JSON output and limits

Local output is one JSON value plus LF, with no provenance envelope or banner.
Unix terminals use two-space pretty layout; redirected output is compact and
plain. `NO_COLOR` or `TERM=dumb` disables highlighting, not terminal indentation.
Windows output is plain and compact. Payload control characters are JSON-escaped,
not executed as terminal controls.

Raw, decoded, view, projection and final-output limits are independent. A text
body is one depth-zero string node; its 10 MiB raw cap also fits the decoded and
view budgets (depth 100, one million value nodes, 16 MiB aggregate key/string
bytes). Each inline query is at most 1 MiB. Existing projection limits remain
depth 1000, one million value nodes and 16 MiB aggregate key/string bytes.

The selected output is preflighted against **16 MiB inclusive**, counting exact
JSON escaping, punctuation, indentation and final LF. For native text this
includes the complete envelope and metadata. A valid body can expand past the
output cap; a false filter avoids output expansion, but cannot hide invalid
input. These output checks are not retrofitted into released JSON or Protobuf
admission rules.

The entire selected value is staged before external writing. A preparation,
read, decode, query or encoding failure writes none of that value. A write or
short-write error may leave partial output; stdout is **not transactional**.
Never redirect output over the input file: the shell can truncate it before
nesdit starts, regardless of validation. These are component bounds, not hard
RSS/CPU bounds, an OOM guarantee or a sandbox for untrusted queries.

## Live and retained text messages

```sh
bin/nesdit --nats 'logs.>' --server tls://broker.example.com:4222 \
  --duration 20s --count 10 --nats-codec text --filter 'contains("error")'

bin/nesdit --nats-history LOGS --subject 'logs.>' --start-seq 1 \
  --server tls://broker.example.com:4222 --duration 20s --nats-codec text
```

Native text needs no schema and forbids descriptor options. Queries are prepared
before connection; native sources never read stdin. Each complete UTF-8 body
becomes the string `payload` in the unchanged native envelope. Projection cannot
alter provenance. Count remains before decoding/filtering, not a match count.

An ordinary empty Core publication becomes `""`; Core's existing bodyless-status
failure remains a failure. In retained history an empty body is also `""`, and
stored `Status` headers remain data, not a transport sentinel. Invalid UTF-8 and
raw-limit violations fail even with a false filter. Prior complete native events
remain if a later event fails.

The [Core guide](nats.md) and [history guide](nats-history.md) remain authoritative
for TLS/authentication, permissions, readiness, duration, layouts and protocol
limits. Text adds no reconnect, application publishing, consumer or ACK authority.
History still requires stable NATS 2.15.x, default account-local API routing and
a homogeneous deployment, uses only INFO/GET plus reply inboxes, and makes no
snapshot or completeness guarantee.

## Stopping and diagnostics

Local success or false filter exits 0; failure exits 1; interruption/cancellation
exits 130. Exit 2 remains the legacy drift-check result. Diagnostics identify
stages/categories without echoing body/query values or evaluator error contents.

On Linux/macOS one interrupt requests stopping. The existing one-second process
fallback covers stalled input, output and diagnostics, including a parsed source
selector followed by an invalid option. Forced exit may truncate output or omit
cleanup/diagnostics. Errors before a recognizable selector retain ordinary parser
behavior; there is no argv-wide selector scan. No Windows bounded-stop guarantee
is added.

Embedded `Execute` is cooperative: it never exits its host, abandons a blocked
I/O worker, closes borrowed handles or changes their deadlines. Callers must
supply responsive I/O or unblock it themselves. Only process-owned actual stdin
can use the existing read-deadline cancellation helper.
