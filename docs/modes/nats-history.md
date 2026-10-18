# Consumer-free JetStream history

Read currently retained JSON messages from an explicit stream and subject pattern:

```sh
nesdit --nats-history ORDERS --subject 'orders.>' --start-seq 1 \
  --server nats://127.0.0.1:4222 --duration 30s --count 20
```

Unlike [live Core observation](nats.md), this reads retained messages through the
standard stream-leader INFO/GET API. It creates no consumer, binds to none, pulls
no consumer messages, sends no acknowledgements, changes no stream configuration
and republishes no application messages. Existing consumers' delivered/read and
acknowledgement-floor positions remain unchanged. API requests are publications
and replies require inbox subscriptions; broker accounting, caches and audit
traffic can change.

## Scope and bounds

History requires all of:

- `--nats-history STREAM`: one stream name, without dots, wildcards, path
  separators, whitespace or control characters;
- `--subject PATTERN`: one explicit pattern, with the same wildcard grammar as
  Core. Quote wildcards; `orders.*` matches one following token and `orders.>`
  matches one or more. Deliberately use `--subject '>'` to select all subjects;
- an explicit `--server` and positive `--duration`, even when count is supplied;
- exactly one inclusive start: positive `--start-seq N` or `--start-time TIME`.

An optional positive `--end-seq N` narrows the inclusive upper bound and cannot
precede a sequence start. A positive `--count N` counts validated, in-range records
admitted **before decoding and filtering**, not emitted matches. A false predicate
still consumes a count slot; malformed admitted payloads fail rather than being
silently filtered away.

```sh
nesdit --nats-history ORDERS --subject 'orders.created' \
  --start-time '2026-10-17T08:00:00+02:00' --end-seq 2000 \
  --server tls://broker.example.com:4222 --duration 30s \
  --filter '.active == true' --project '{id: .id}'
```

Time means **broker storage time**, not an application timestamp. Supply
timezone-qualified RFC3339 with at most nine fractional-second digits. The exact
inclusive UTC range is `1677-09-21T00:12:43.145224192Z` through
`2262-04-11T23:47:16.854775807Z`, the signed 64-bit Unix-nanosecond interval.
Equivalent offset instants use the same bounds. Excess precision, absent timezone
or an out-of-range instant fails locally before connection, without rounding,
clamping or overflowing a nanosecond conversion.

The first validated INFO fixes H, the stream's initial last sequence. The upper
bound is H or a smaller explicit end sequence. Later publications do not extend
it. The inspector requests the next matching retained message at or after its
cursor; deletion, expiry and nonmatching subjects can cause sequence jumps.
Returned sequences strictly increase. A response beyond the upper bound may cross
the network, but is never admitted or emitted.

**This is not a snapshot, completeness guarantee, loss count or replay.** Gaps do
not reveal how many publications were lost. Retention or stream replacement can
prevent complete inspection. Before terminal exhaustion, an active session
rechecks stream identity `(name, created)` without replacing H. Detected
replacement/disappearance fails, but changes between checks can remain undetected.
Rerunning starts a new observation with a potentially different retained range;
a displayed sequence is not a guaranteed resume token.

## UTF-8 bodies

`--nats-codec text` is supported since 0.7.0 with the same history
selector, bounds and permissions:

```sh
bin/nesdit --nats-history LOGS --subject 'logs.>' --start-seq 1 \
  --server tls://broker.example.com:4222 --duration 20s --nats-codec text \
  --filter 'contains("error")'
```

Each whole valid UTF-8 body becomes one string; empty data becomes `""`, and stored
`Status` headers remain ordinary data. BOM, NUL, CR/LF and whitespace are retained.
Invalid UTF-8 fails before filtering. Queries see the original string root, not
provenance; count remains before decoding/filtering. No descriptor flags are
allowed. The exact JSON-output preflight counts the full history envelope,
metadata escaping, layout and LF against 16 MiB. See the [text guide](text.md).
This does not announce a public release or broaden history's server/security or
INFO/GET-only authority.

## JSON or explicit Protobuf

JSON is the default and needs no schema. To inspect Protobuf:

```sh
nesdit --nats-history ORDERS --subject 'orders.>' --start-seq 1 \
  --server tls://broker.example.com:4222 --duration 30s \
  --nats-codec protobuf --descriptor-set schemas/events.pb --message-type example.Order \
  --filter '.active == true' --project '{id: .id}'
```

The same [local descriptor, ProtoJSON mappings and component limits](protobuf.md)
as standalone and Core apply. Both schema options are required for Protobuf and
forbidden for JSON or text. Each stored body is one raw message, without prefix stripping,
framing, decompression or schema guessing. Omitted GET data is a legitimate
zero-byte body: it may decode as Protobuf when the type permits it, but is not
valid JSON. Stored `Status: 503` headers are **data**, not Core's bodyless-503
sentinel or an API error. API no-responders still fails.

Queries see only the decoded payload root: `.id`, not `.payload.id`. `--filter`
returns exactly one boolean; false suppresses output and skips projection.
`--project` returns exactly one value evaluated against the decoded root. Query
errors fail. Neither query can rewrite or inspect broker provenance.

## Output and stopping

```json
{"stream":"ORDERS","sequence":"9007199254740993","stored_time":"2026-10-17T08:00:00Z","subject":"orders.created","headers":{"Trace":["a","b"]},"payload":{"id":2}}
```

Field order is exactly `stream`, `sequence`, `stored_time`, `subject`, `headers`,
`payload`. Sequence is a decimal **string**, without floating-point conversion;
stored time is UTC RFC3339Nano. Provenance comes from validated API responses,
never payload/header names. Repeated header values and exact header-name case
are retained; names sort lexicographically. Core keeps its existing three-field
envelope and standalone Protobuf keeps value-only output.

`--nats-output auto` is pretty on Unix terminals and compact JSONL when redirected.
Explicit `jsonl` and `pretty` retain their native meanings; redirected pretty is
multiple JSON documents, not JSONL. Color is terminal-only; `NO_COLOR` and
`TERM=dumb` disable it. Output is a decoded view, not original-byte capture.

Local query, descriptor and security preparation precede the total clock and
observes cancellation. The duration then covers connection, INFO, GET, decoding,
queries and output. Each API request has a five-second deadline capped by the
remaining total duration. There is one outstanding request, no prefetch.

`nats.ready` means initial INFO and startup validation succeeded, not that a Core
subscription exists or a record has arrived. Completion diagnostics stay on stderr:

| Stop / exit | Meaning |
| --- | --- |
| `exhaustion`, 0 | Empty retained range, inclusive upper boundary, or server-reported no matching message; not proof of complete history. |
| `count`, 0 | The admitted-record cap completed; not a count of matches. |
| `duration`, 0 | A ready session ended cooperatively; the range may remain unread and an unfinished pre-output event may be discarded. |
| Failure, 1 | Local validation, startup, permission, request, protocol, payload, query, resource or output failure. |
| Interrupt/cancellation, 130 | Stopping requested; earlier complete output remains. |

Only API `404 / 10037` means server-reported no message. Missing stream `10059`,
no responders and other failures are not empty success. The server maps store
lookup errors to `10037`, so that response cannot prove absence of an internal
storage fault. Detected fatal errors override normal count, duration or exhaustion.
Count/duration stops need no extra INFO read. Before-readiness expiry is failure.

Diagnostics report `admitted`, `emitted` and `unfinished_event`; `--log-format json`
also carries structured fields. Once external writing starts, partial output is
a failure and cannot be recalled. On Linux/macOS, the native one-second process
fallback after any stop request bounds stalled work/cleanup and blocked stdout or
stderr, but can truncate output or omit diagnostics. Embedded `Execute` remains
cooperative and requires responsive borrowed I/O; it never exits its host.

## Supported servers, security and permissions

History supports stable **NATS 2.15.x, from 2.15.0**, using the default account-local
`$JS.API` route, on standalone servers or **homogeneous same-series clusters**.
Both the connected server version and INFO's server-generated `_nats.ver` are
checked. Missing/malformed/prerelease/other-series versions and leaderless cluster
INFO fail closed. Core has no new server-version restriction.

Homogeneity is an **external deployment precondition**: a responding INFO does
not certify all future leaders. There is no topology audit, automatic upgrade,
custom API-domain/import-prefix inference or compatibility claim for other series.
Disposable tests exercise 2.15.0 memory/file stores, standalone and three-node
clusters, sparse time selection and leader changes; they do not certify your
actual deployment.

The observer needs request-PUB authority on exactly:

- `$JS.API.STREAM.INFO.ORDERS`
- `$JS.API.STREAM.MSG.GET.ORDERS`

and SUB authority for its reply inboxes (`_INBOX.>`). It needs no application-subject
subscription or consumer authority. **GET permission is stream-level**:
`--subject` selects records; it is not a broker-enforced subject ACL. Keep broker
permissions appropriate to the whole stream. There is no direct-read, consumer,
reconnect, permission-widening or scan-from-start fallback.

[Verified TLS and credential-file rules](nats.md#credentials-and-tls-files) are
unchanged: `tls://`, optional JWT/NKey `--creds`, optional `--tls-ca`, paired
`--tls-cert`/`--tls-key`, regular files at most 1 MiB each, no insecure bypass or
credentials in URLs. Correct permissions or deployment inputs explicitly; do not
weaken broker security to make a failed read work.

## Limits and option isolation

INFO data is capped at 1 MiB and GET data at 16 MiB before JSON/base64 conversion.
Decoded payload is capped at 10 MiB, stored and outer wire headers at 64 KiB, and
routing strings at 4 KiB each. Encoded lengths are checked before base64 allocation;
limits never truncate data. Protocol validation rejects duplicate keys, trailing
JSON, wrong/missing fields, inconsistent metadata and simultaneous success/error;
unknown additive fields may be ignored. All [native query/event limits](nats.md#fixed-component-budgets)
and, when selected, [Protobuf limits](protobuf.md#fixed-limits) still apply. The
final 16 MiB envelope cap includes provenance, headers, layout and LF. Client/library
allocations may precede checks; this is not a process-heap or hostile-broker sandbox.

History never reads stdin or positional files. Its selector is mutually exclusive
with Core and standalone selectors. History-only flags are invalid in other modes;
legacy file/edit/format/resource options are invalid here even at explicit defaults.
A bare filename such as `nats-history` remains a filename.

Run the disposable checks with an independently verified test server, not a
production endpoint:

```sh
NATS_SERVER_BIN=/path/to/nats-server go test -race -count=1 -v \
  ./internal/natsinspect ./internal/run -run History
```

A missing server skips broker cases, not passes them. Run process-stop checks on
both Linux and macOS; cross-compilation alone is not runtime cancellation proof.
The older-version gate fixture substitutes a handshake version in front of a real
2.15.0 server; it is not an older-server interoperability claim.
