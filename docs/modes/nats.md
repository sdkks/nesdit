# Live Core NATS observation

Observe JSON messages without publishing application messages, creating consumers,
joining queue groups, acknowledging work, or changing broker configuration:

```sh
nesdit --nats 'orders.>' --server nats://127.0.0.1:4222 --duration 30s --count 3
```

Use an endpoint and subject you are authorized to observe. This command is a
plain, temporary Core NATS subscription. It reads neither stdin nor input documents; a file
named `nats` still works as a positional file. Quote wildcard expressions:
`orders.*` matches one token after `orders`, while `orders.>` matches one or more,
not the bare `orders` subject. Empty tokens, embedded wildcards, whitespace and
control characters are rejected locally.

## Connection and readiness

`--server` and a positive `--duration` are required, including with `--count`.
Supply one explicit `nats://host:port` or `tls://host:port`; there is no default
server or discovered-server failover. URLs containing credentials, query strings,
fragments, server lists or non-root paths are rejected. TLS always verifies the
server certificate and hostname. There is no insecure mode and no credentials in
URLs. Never weaken your broker's security to accommodate the inspector.

### Credentials and TLS files

| Configuration | Supported inputs |
| --- | --- |
| TLS with system trust | `--server tls://host:port`; omit `--tls-ca` |
| Custom trust | `--tls-ca path/to/ca.pem` replaces system roots for this connection |
| JWT/NKey authentication | `--creds path/to/operator.creds` |
| Mutual TLS | Paired `--tls-cert path/to/client.pem --tls-key path/to/client.key` |
| JWT plus mutual TLS | Combine the credentials and certificate/key flags |

Every security file requires an explicit `tls://` endpoint, even if a `nats://`
server would offer TLS. Each file must be readable, regular, and at most **1 MiB**;
empty paths, missing pairs, malformed credentials, empty/invalid CA bundles and
invalid/mismatched certificate/key pairs fail before connecting. Directories and
FIFOs are rejected. Use PEM certificates/keys and a standard combined user JWT/NKey
`.creds` file. Protect private keys and credentials with restrictive permissions.
The client checks credential-container and user-key syntax locally; the broker
remains the authority for signatures, issuer trust, expiry and subject permissions.

```sh
nesdit --nats 'orders.>' --server tls://broker.example.com:4222 --duration 30s \
  --creds path/to/operator.creds --tls-ca path/to/ca.pem \
  --tls-cert path/to/client.pem --tls-key path/to/client.key
```

Supply only the files required by your deployment. Omit the certificate/key pair
when mutual TLS is not required; omit the CA flag when system roots suffice.
Files are bounded and loaded once during local preparation, before the duration
clock starts. Authentication/TLS callbacks use memory, not later file reads;
changing a file does not rotate an active session. First-interrupt handling covers
preparation as well as connection and observation. Diagnostics name the flag or
failure stage, not paths, file contents, keys, JWTs or raw TLS errors.

Token/user-password-only authentication, TLS-first-only servers, proxies,
WebSockets and NATS context registries are not supported. Local supported-mode
tests do not establish compatibility with your actual cluster; confirm its
mechanism and permissions before an authorized bounded smoke test.

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
Payload key order and existing supported value types are preserved. Without
projection the full decoded payload bypasses the query engine, even when a filter
is supplied. This is not an arbitrary-precision promise: numbers must remain
representable by the existing JSON encoder. Queries normalize decimal/exponent
numbers to floating point and integers to machine or big integers; arithmetic and
reconciliation do not promise universal precision. Array key-order reconciliation
is positional, and new object keys sort lexicographically.

## Filter and project the payload

```sh
nesdit --nats 'orders.>' --server nats://127.0.0.1:4222 \
  --duration 30s --count 3 --filter '.show == true' --project '{id: .id}'
```

Queries see the message body directly, not the displayed envelope: use `.id`,
not `.payload.id`, for a body field named `id`. Transport subject and headers
cannot be queried and are preserved unchanged by projection, including repeated
headers and collisions with payload fields named `subject` or `headers`.

- No filter means every valid decoded message matches. A supplied filter must
  yield **exactly one boolean**. False suppresses normally without warnings or
  projection. Missing/nonboolean, zero/multiple results and runtime errors fail.
  Prefer `.show == true`; bare `.show` fails if the field is absent. `select(...)`
  is not a boolean predicate and cannot be used to implement this contract.
- No projection preserves the decoded payload. A supplied projection must yield
  **exactly one JSON-representable value**: objects (including new/renamed fields),
  arrays, strings, finite numbers, booleans and null are valid. `.items[]` is not
  fan-out: zero/multiple values fail. Use `[.items[]]` to produce one array.
- Expressions compile once before connection. Blank, oversized or invalid
  expressions fail locally. There are no query-file or argument-binding flags.
  Existing file/pipe `--query` and `--where` are rejected in this mode; their
  conditional passthrough and missing-path safeguards remain unchanged.
- JSON is validated before filtering. A false predicate cannot hide a malformed
  body. A failing filter, projection or encoder emits none of that event; earlier
  complete events remain. Diagnostics identify the stage without echoing query
  text, payloads, headers or evaluator error values.

For three publications `{"id":1,"show":false}`, `{"id":2,"show":true}` and
`{"id":3,"show":true}`, the example emits only ids 2 and 3 with their metadata.
Three false predicates still stop on count with admitted=3, emitted=0 and
unfinished_event=false; count does not wait for matches.

## Output layout

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
security-file preparation and query compilation,
and includes connect, readiness, receive, decode, filtering, projection, encoding
and output. It is not
an idle timeout and never restarts with traffic. Connect and readiness each have
at most five seconds, further capped by the remaining duration.

Optional `--count N` counts **admitted messages**: messages taken into processing,
not everything the broker publishes or the client buffers. Admission happens
before decoding/filtering; a false predicate or malformed message consumes a slot.
**Emitted** means a fully written event, not one merely selected. After the last admitted event completes, queued successors are
not drained. A competing stop can prevent the final event from completing.

Stderr completion includes the reason, admitted/emitted counts, and
`unfinished_event`. `--log-format json` carries these in structured fields as well
as the human-readable message; stdout contains events only.

| Exit | Meaning |
| --- | --- |
| 0 | Count completed, or a ready session reached its duration cooperatively. A quiet session succeeds with no stdout. |
| 1 | Configuration/startup, payload, query, resource, delivery, output or forced-stop failure, even after useful output. |
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
| Credential / CA / certificate / private-key file | 1 MiB (1,048,576 bytes) each, before connection |
| Inline filter / projection | 1 MiB (1,048,576 bytes) each, before compilation |
| Raw projection result | Depth 1000, 1,000,000 value nodes, 16 MiB aggregate key/string bytes |
| Serialized event | 16 MiB (16,777,216 bytes), including layout and final LF |

At-limit values are allowed; one over fails without truncation. Pretty indentation
can exceed the event limit where compact output fits. The event is fully validated
and serialized before emission; decode/query/encode/limit errors emit none of that
event. Raw projection results are checked iteratively before recursive conversion:
containers and scalar values each count as one node; object keys count toward the
aggregate key/string byte limit, not the node count. Root scalar depth is zero.
Other file-mode resource flags cannot override these limits.

Expressions are **trusted code for resource purposes**. gojq can allocate heavily
before returning a result; result/output budgets are not a strict query-heap cap.
Context cancellation is checked between VM instructions and during raw-result
preflight, but normalization, builtins and reconciliation are not uniformly
preemptible. The CLI watchdog bounds stopping, not memory allocation or OOM.
There is no AST whitelist, evaluator sandbox or universal allocation quota.

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
generate temporary synthetic CA/server/client keys and signed operator/account/user
JWT credentials at runtime, and terminate owned processes. Credential generation
uses the existing NKeys and standard-library packages, with the fixture format
verified against official `nats-io/jwt` v2.8.2 sources; no server or JWT module is
linked into the product. Tests exercise real custom-CA, JWT, mTLS and combined
connections, rejected trust/hostname/issuer/signatures/permissions, file bounds,
and interrupted preparation/handshakes. No production endpoint or committed
PEM/private key is needed. Missing
`NATS_SERVER_BIN` skips the broker layer; a skip is not verification. Run subprocess
cases on both Linux and macOS: cross-compilation does not prove runtime stopping.
The existing `make test-all` remains the final regression gate. Local results do
not establish compatibility with any actual deployment's authentication, trust,
server version or subject permissions.
