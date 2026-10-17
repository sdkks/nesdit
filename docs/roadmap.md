# Data I/O and event inspection roadmap

This roadmap is for users and contributors planning data I/O and event-debugging workflows in nesdit.
It records shipped capabilities and the direction of future work, not delivery dates
or approved detailed specifications for later milestones.
V1–V4 name milestones, not semantic-major releases.

Native Core NATS JSON observation **shipped in [0.5.0](https://github.com/sdkks/nesdit/releases/tag/v0.5.0)**.
Start with the [NATS observation guide](modes/nats.md) for the supported command,
security configuration, limits, and failure behavior. Protobuf and Kafka remain
planned; JetStream is **WIP — design; not shipped**.

The direction combines bounded event inspection with local data inspection and conversion:
read data from files, stdin or brokers, and inspect payloads with source metadata where applicable.
It is not broker administration or full kcat parity. Broker transport and payload codecs are
separate concerns; carrying a format does not imply built-in decoding support.
Current JSON/YAML/TOML file and pipe workflows remain supported without changes to their
behavior; see the [overview](index.md) and [STDIN guide](modes/stdin.md) for today's capabilities.
The additional local formats and binary conversion described below are future work, not shipped features.

## V1 — Native Core NATS with JSON

**Status: shipped in 0.5.0.**

**Delivered scope**

- Temporary, native live observation of Core NATS messages with JSON payloads.
- Subject and header metadata alongside the payload view.
- Filtering that suppresses nonmatching events, projection of selected fields, and readable output.
- Bounded message count and duration, clear failures, and reliable cancellation.
- Preserve all existing file-editing and pipe behavior, including current filtering semantics.

**Non-goals:** retained-history reads, Protobuf, Kafka, publishing, broker administration,
and new capture/archive or validation subsystems.

**Delivered workflow:** observe JSON publications on an authorized subject with a plain,
temporary subscription; inspect subject and repeated header values separately from the
payload; suppress nonmatches and project a payload view. Queries see the body root,
not the displayed envelope: filters return exactly one boolean and projections exactly
one JSON value. Projection does not remove or sanitize headers.

Native output is JSON only. `--nats-output auto` uses pretty JSON on Unix stdout
terminals and JSONL when redirected; explicit `pretty` produces consecutive multiline
JSON documents, not JSONL. Redirected output is plain; readiness, stop reasons and
diagnostics go to stderr. Existing file and pipe JSON/YAML/TOML workflows are unchanged,
including conditional filtering that passes nonmatches through rather than suppressing them.

A positive `--duration` is required and starts before connection setup, after local
preparation. Optional `--count` counts messages admitted before decoding/filtering,
not displayed matches. Quiet or all-filtered ready sessions can finish normally at
the duration. Malformed payloads, query errors and detected delivery failures stop
the session with a nonzero exit. One interrupt stops observation on Linux/macOS;
the forced-stop fallback can truncate the final record and omit final diagnostics.

Core NATS observation is live and at-most-once, with **no reconnect**. A detected
disconnect ends the session; publications before readiness or during a gap cannot
be recovered. Successful completion does not prove complete delivery.

Connections require an explicit server. Supported security inputs include verified
TLS, JWT/NKey credentials, custom CA trust and mutual TLS; security files require
`tls://`. There is no insecure TLS mode or URL-embedded credential support. Use only
authorized subjects and a trusted, correctly configured broker. See the
[connection guide](modes/nats.md#connection-and-readiness) for supported mechanisms
and limitations; do not weaken broker security to accommodate inspection.

## V2 — Explicit Protobuf decoding

**Status: planned; not shipped.**

**Scope**

- Decode one raw Protobuf message per payload using a local descriptor set and an
  explicitly selected exact message type.
- Support read-only file/stdin inspection and live Core NATS payloads, with reuse for
  retained-history inspection when V3 is available.
- Use the standard ProtoJSON mapping for decoded views, filtering, projection, and output.
- Add Protobuf fixtures and integration cases at this milestone.

**Non-goals:** runtime schema compilation, schema registries, automatic type or codec
detection, Protobuf encoding, and binary write-back.

**Demo outcome:** inspect a known Protobuf message from a file or stdin, or a Core NATS
publication, using its descriptor set and type. Show a ProtoJSON payload view with
NATS metadata where applicable, and report descriptor or decoding failures clearly.

A decoded view is not a lossless binary representation. Successful decoding alone does
not establish that the selected type was correct; descriptors and type must match the payload.

## V3 — Bounded JetStream history

**Status: WIP — design; not shipped.** Native retained-history reads, consumers,
replay and ACK handling are not available in nesdit. The scope below is a design
direction, not a supported API or a guarantee that every retention policy or
permission configuration will allow consumer-free inspection.

**Scope**

- Inspect retained JetStream messages within explicit bounds.
- Select a starting position or time where the supported APIs permit it.
- Use standard consumer-free stream INFO and message GET requests, subject to retention
  and permissions; do not fall back to consumers.
- Add JetStream retention, permissions, and history cases at this milestone.

**Non-goals:** creating consumers, consumer fallback, ACKs, changing stream configuration,
administering streams, or replaying messages by publishing them. Inspection requires
broker API request traffic; those requests are not application-message publishing.

**Demo outcome:** inspect a bounded range of retained messages from a selected position
or supported time, with metadata and the existing JSON or Protobuf views, without
acknowledging an application consumer.

Retained history is not an immutable snapshot or a guarantee of every past publication.
Retention can remove messages during inspection. Unsupported read paths or insufficient
permissions must produce clear failures, not trigger consumer fallback or configuration changes.

## V4 — Native Kafka inspection

**Status: planned; not shipped.**

**Scope**

- Native, bounded Kafka inspection using the established JSON and Protobuf views.
- Key, header, partition, offset, and time metadata alongside payloads.
- Kafka-specific adapter and real-broker tests, introduced only in this milestone.

**Non-goals:** application-group side effects, broker administration, publishing,
and full kcat feature parity.

**Demo outcome:** inspect a bounded set of Kafka records, locate them by partition and
offset, and filter or project their payload views without changing application-group state.

## Local formats and conversion — future work

**Status: future direction; not shipped.** Finish the existing Protobuf and JetStream
work before this expansion. V1–V4 retain their order; these additions have no promised
dates or new release commitments.

**Direction**

- UTF-8 string and raw-byte inspection views, with explicit output selection.
- MessagePack inspection and local encoding.
- Avro inspection and local encoding using local schemas, including Avro container files.
- Protobuf encoding as separate future work, not part of V2's read-only decoding scope.
- Local JSON Schema validation over JSON, not a separate wire codec.

FlatBuffers and Cap'n Proto are conditional, later specialist additions, not prerequisites
for these formats. Live Schema Registry integration is optional, separate work; local
schemas must not require a network registry.

Local encoding does not authorize broker publishing or binary in-place editing. Each
format needs its own design for mappings, framing, fidelity, resource limits and safe
output. Decoding to an inspection view, validating data and encoding under a target schema
are distinct capabilities; this roadmap does not promise universal lossless conversion,
byte-identical round trips, zero-copy processing or performance gains.

Reuse existing capabilities where appropriate. A generic codec framework or source CLI
redesign is not a prerequisite for the current milestones.

## Subject discovery — future work

nesdit does not provide a complete active-subject catalog for Core NATS. Native
subject discovery is future work; observing a wildcard subject is not enumeration
of every active subject, and silence does not establish that a subject is unused.

Existing file queries can extract declared subject arrays from a YAML declaration.
For a local `stream.yaml` whose declaration stores the array at `.spec.subjects`:

```sh
nesdit stream.yaml --query '.spec.subjects'
```

This reads a local document, not the broker. A declaration catalog describes
configured subjects or patterns; it is not a complete inventory of live Core NATS
traffic, publishers or subscriptions. It does not add native JetStream support.

## Shared architecture principles

- Deliver each milestone as a usable inspection workflow. No milestone requires
  scaffolding or infrastructure for later milestones.
- Keep broker connection and message-delivery concerns separate from payload codecs,
  validation, queries, and presentation. Reuse existing capabilities where their semantics fit.
- Do not repurpose existing editing flags to silently change file or pipe behavior.
  Current STDIN filtering is conditional transformation, not event suppression.
- Keep metadata and payload views distinct. Decoded or formatted output is not a promise
  of exact original-byte fidelity.
- Preserve deterministic serialization, key order, value types, atomic file writes,
  existing resource safeguards, and the separation of terminal styling from serialized bytes.
- Preserve no-CGO builds on `linux/amd64`, `linux/arm64`, and `darwin/arm64`.
  The current release-binary ceiling is strictly below 40 MiB (41,943,040 bytes),
  an interim budget that maintainers may explicitly revisit as milestones progress;
  it is neither a permanent final budget nor permission for automatic increases.
  Historically, all three 0.5.0 release binaries were verified with CGO disabled
  and strictly below 16 MiB (16,777,216 bytes). Recheck native-client dependencies
  and binary sizes for each future milestone; that result is not a future size guarantee.
- Match authentication and TLS to the deployment's requirements.
  Never weaken broker security to make inspection work. Future milestones still need
  their own API, permissions and security design; milestone order does not approve them.

## Testing strategy

Use several layers of evidence; fixtures alone cannot prove network lifecycle behavior.

1. **Individual pieces:** synthetic or sanitized fixtures with exact payload bytes,
   metadata, and expected results for decoding, filtering, projection, and output.
   Include failures and boundary cases. Keep production data and secrets out of fixtures.
2. **Adapter behavior:** small fakes at nesdit's own adapter seam for controlled delivery,
   errors, limits, and cancellation. Do not simulate an entire broker.
3. **Protocol and CLI integration:** a disposable real NATS server and a controlled test
   publisher. Exercise delivery, metadata, connection lifecycle, stopping, and diagnostics.
   A separately installed broker or disposable Podman container can provide the test
   environment; no container runtime is a product dependency.
4. **Authorized passive checks:** after local verification, observe only approved
   subjects for a bounded window with the deployment's required security configuration.
   Do not publish test messages, acknowledge application work or change broker settings
   during passive checks. Keep endpoints, credentials and captured data out of public docs.

Fixture capture is test support, not a commitment to a shipped capture/archive subsystem.
Future local validation and conversion need their own format-specific fixtures, failure
cases and fidelity checks; they are not delivered by the event-inspection milestones.
Add Protobuf and JetStream cases in their respective milestones; introduce no Kafka
infrastructure before V4. Preserve regression coverage for current file and pipe
workflows throughout.

## Using the shipped milestone and planning the next

The [NATS observation guide](modes/nats.md) documents the shipped CLI, security
inputs, component budgets, exit statuses and partial-output limitations. The
[command reference](reference/nesdit.md) lists current flags. These are the sources
for today's behavior, not the future milestone descriptions above.

Complete the existing Protobuf and JetStream designs and milestones before expanding
local formats and conversion. Unshipped work still needs its applicable detailed design
and verification before implementation or release. This roadmap does not ratify exact
CLI shapes, dependencies, permission needs or operational policies. Keep each milestone
bounded and usable without introducing infrastructure for later work.
