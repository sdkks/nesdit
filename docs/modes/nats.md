# Live Core NATS observation

Observe JSON messages without publishing application messages, creating consumers,
joining queue groups, acknowledging work, or changing broker configuration:

```sh
nesdit --nats 'orders.>' --server nats://127.0.0.1:4222 --duration 30s --count 3
```

Use an endpoint and subject you are authorized to observe. This command is a
plain, temporary Core NATS subscription. It reads neither stdin nor files; a file
named `nats` still works as a positional file. Quote wildcard expressions:
`orders.*` matches one token after `orders`, while `orders.>` matches one or more,
not the bare `orders` subject. Empty tokens, embedded wildcards, whitespace and
control characters are rejected locally.

## Connection and readiness

`--server` and a positive `--duration` are required, including with `--count`.
Supply one explicit `nats://host:port` or `tls://host:port`; there is no default
server or discovered-server failover. URLs containing credentials, query strings,
fragments, server lists or non-root paths are rejected. TLS uses certificate and
hostname verification against system trust. There is no insecure mode. JWT
credentials, custom CA and client-certificate CLI flags are not available in this
observation increment; do not put credentials in URLs or weaken your broker's
security to accommodate it.

`nats.ready` on stderr means the server processed the subscription and a PING/PONG
barrier, and the client has no detected startup failure. It does not mean that an
event arrived or that every publication will be received. Subscribe permission
denial, connection, authentication and TLS failures return nonzero without a false
ready line. There is **no reconnect**: a detected disconnect or pending overflow
ends the observation rather than implying continuity across a gap.

Core NATS is live and **at-most-once**. Publications before readiness or during a
gap cannot be recovered. Undetected loss remains possible. A successful or quiet
observation never proves that nothing was published or that delivery was complete.

## Output and fidelity

One complete JSON value in each NATS message becomes one event. Multiline objects,
arrays, strings, numbers, booleans and null are accepted. Empty/malformed bodies,
trailing content and concatenated values fail immediately, retaining earlier
complete output. A bodyless `Status: 503` message is an empty-JSON error, not a
claim that an application request failed: the observer makes no requests.

```json
{"subject":"orders.created","headers":{"Trace":["a","b"]},"payload":{"id":2}}
```

The envelope order is `subject`, `headers`, `payload`. Header names sort
lexicographically with exact client-provided case; repeated values keep their
order, empty values remain strings, and absent headers are `{}`. Header arrays
are not collapsed. Payload keys named `subject` or `headers` stay inside `payload`
and cannot impersonate transport metadata. The reply subject is checked for size
but is not displayed. No timestamp or sequence/completeness claim is added.

This is a decoded view, **not byte-exact capture**: whitespace, escape spelling,
header wire formatting and duplicate JSON keys are not retained as original bytes.
Payload key order and existing supported value types are preserved. The full
payload bypasses the query engine; this is not an arbitrary-precision promise and
numbers must be representable by the existing JSON encoder. File/pipe queries
retain their existing numeric limitations. Filtering/projection are not provided
by this command yet; existing `--query` and `--where` are rejected, not ignored.

- `--nats-output auto` (default): two-space pretty JSON on Unix stdout terminals,
  compact JSONL with exactly one LF per event when redirected.
- `--nats-output jsonl`: compact layout explicitly.
- `--nats-output pretty`: multiline JSON documents even when redirected. This is
  **not JSONL**; use a parser that accepts consecutive JSON documents.

Layout and color are independent. `NO_COLOR` and `TERM=dumb` disable highlighting,
not terminal pretty layout. Redirected output is always plain, including explicit
pretty output. Windows stays plain and auto uses compact output. There is no
color flag. Status/diagnostics, help and file serializers remain plain.

## Stopping and exit status

The total duration starts just before connection setup, after local validation,
and includes connect, readiness, receive, decode, encoding and output. It is not
an idle timeout and never restarts with traffic. Connect and readiness each have
at most five seconds, further capped by the remaining duration.

Optional `--count N` counts **admitted messages**: messages taken into processing,
not everything the broker publishes or the client buffers. A malformed message
consumes a slot before failing. **Emitted** means a fully written event, not one
merely decoded. After the last admitted event completes, queued successors are
not drained. A competing stop can prevent the final event from completing.

Stderr completion includes the reason, admitted/emitted counts, and
`unfinished_event`. `--log-format json` carries these in structured fields as well
as the human-readable message; stdout contains events only.

| Exit | Meaning |
| --- | --- |
| 0 | Count completed, or a ready session reached its duration cooperatively. A quiet session succeeds with no stdout. |
| 1 | Configuration/startup, payload, resource, delivery, output or forced-stop failure, even after useful output. |
| 130 | Cancellation/first interrupt in native observation on Linux/macOS. |
| 2 | Remains exclusive to file-mode `--check` drift. |

If duration expires before event emission, an unfinished event can be discarded
with exit 0 and `unfinished_event=true`. Once emission starts, an incomplete record
is a failure. Already detected fatal errors are not erased by duration success.
Expiration before readiness is a startup failure, not a successful empty session.

One interrupt requests cancellation on Linux/macOS. A **one-second cleanup
allowance** starts with the first stop request (count, duration, interrupt or
failure). If owned work or stdout/stderr blocks, the process-only watchdog exits
with 1, or 130 for interruption. It may skip cleanup, truncate the final record,
and omit final stderr. Check exit status and validate redirected output; stdout
writes are not transactional. Earlier complete events remain available. Windows
has no new bounded-interrupt guarantee. In-process `Execute` never exits its host
or installs signal handlers: its caller must supply responsive writers and a
context, and receives cooperative cancellation only.

## Fixed component budgets

| Component | Maximum accepted value |
| --- | --- |
| Admitted JSON payload | 10 MiB (10,485,760 bytes) |
| Payload container depth | 1000; root scalar has depth zero |
| Client pending queue | 8 messages and 64 MiB **payload-only** bytes |
| Admitted wire header block | 64 KiB (65,536 bytes) |
| Subject / reply routing strings | 4 KiB (4096 bytes) each |
| Serialized event | 16 MiB (16,777,216 bytes), including layout and final LF |

At-limit values are allowed; one over fails without truncation. Pretty indentation
can exceed the event limit where compact output fits. The event is fully validated
and serialized before emission; decode/encode/limit errors emit none of that
event. Other file-mode resource flags cannot override these limits.

These are **not a whole-process heap ceiling or a hostile-broker sandbox**. The
native client allocates/parses an incoming frame and headers before application
checks. Pending-byte accounting excludes headers, routing strings and overhead.
Admitted limits cannot undo prior allocations. Use a trusted, correctly configured
broker with appropriate publisher controls. Output intentionally contains your
application data and is not sanitized for sharing; diagnostics do not dump raw
payloads, headers, URL credentials or untrusted server errors.

## Disposable local verification

Integration tests require a separately installed `nats-server` **v2.15.0**. Obtain
the platform artifact from the official NATS server release and verify its
published checksum before use; keep it outside the repository. It is test-only,
not linked into nesdit, and Docker is not required for native tests.

```sh
NATS_SERVER_BIN=/path/to/nats-server go test -race -count=1 -v ./internal/natsinspect ./internal/run -run 'Test(NATS|RealAdapter|Socket|ClientFirst)'
```

Tests start disposable loopback servers, synchronize publication to readiness,
generate temporary synthetic TLS keys/trust at runtime, and terminate owned
processes. No production endpoint or committed PEM/private key is needed. Missing
`NATS_SERVER_BIN` skips the broker layer; a skip is not verification. Run subprocess
cases on both Linux and macOS: cross-compilation does not prove runtime stopping.
The existing `make test-all` remains the final regression gate. Local results do
not establish compatibility with any actual deployment's authentication, trust,
server version or subject permissions.
