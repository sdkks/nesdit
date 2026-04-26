// Package logx is the canonical stderr emitter for nesdit. Every
// error/warn/info line that reaches stderr in human mode MUST be
// produced by a *Logger method — no subsystem may fmt.Fprintln directly.
//
// The text formatter enforces the SPEC-0001 NFR-10 canonical shape:
//
//	nesdit: <severity>: <file>:<index>: <event>: <message>
//
// with `<file>` and `<index>` omitted (along with their colons) when
// absent, per DR-004's explicit omission rules. `<event>` is one of a
// closed enum (Event type below) shared with the future FR-15 NDJSON
// mode — one vocabulary, two renderings, one Go `const` block.
//
// STORY-0003 ships the text formatter + the initial event taxonomy.
// STORY-0008 extends the enum with decoder.limit.* and query.timeout;
// the shape and the IsKnownEvent registry are designed to grow without
// churn to callers.
//
// # Shape contract for <file>
//
// The NFR-10 canonical regex defines the `<file>` slot as one-or-more
// non-colon, non-C0-control bytes (optionally followed by `:<index>`).
// This means:
//
//   - A filename that contains ':' is NOT representable in the canonical
//     shape. nesdit's path-resolution layer rejects such paths before
//     they reach the emitter (see STORY-0004's atomic writer).
//   - Control bytes (\x00-\x1f, \x7f) in either the file or the message
//     are stripped by stripControls before rendering, replaced with '?'
//     to keep the line visible and preserve the record boundary. '?'
//     is chosen over silent drop so forged content becomes conspicuous
//     to a human reader rather than seamlessly merging into the line.
//     This hardens the emitter against CR-smuggling attacks in which an
//     attacker-supplied filename carries embedded \r\n sequences that
//     would otherwise forge a second apparent canonical line on
//     terminals and in naive log scrapers (OWASP A09).
package logx

import (
	"io"
	"strconv"
	"strings"
	"sync"
)

// Event is a closed-enum token identifying the class of a log line.
// See NFR-10 for the shape contract: lowercase, dot-delimited,
// matching [a-z][a-z0-9_.]*.
type Event string

// Event tokens emitted by STORY-0003. STORY-0008 will append
// decoder.limit.* and query.timeout. The registry is the single source
// of truth; IsKnownEvent consults it so no ad-hoc token can reach
// stderr.
const (
	// Format / parse / decode errors across all format packages.
	EventParseError         Event = "parse.error"
	EventFormatUnknown      Event = "format.unknown"
	EventFormatUnsupported  Event = "format.unsupported"
	EventFormatIncompatible Event = "format.incompatible"
	EventFormatMixed        Event = "format.mixed"

	// Encode errors not covered by cross-format incompatibility.
	EventEncodeError Event = "encode.error"

	// IO boundary.
	EventIORead  Event = "io.read"
	EventIOWrite Event = "io.write"

	// Query engine — classifier matches the Op field on *query.Error.
	EventQueryParse       Event = "query.parse"
	EventQueryCompile     Event = "query.compile"
	EventQueryRuntime     Event = "query.runtime"
	EventQueryResult      Event = "query.result"
	EventQueryUnsupported Event = "query.unsupported"

	// --arg / --argjson parse failures (FR-7, FR-8).
	EventArgDecode Event = "arg.decode"

	// -f/--from-file read errors (FR-13).
	EventFromFileRead Event = "from_file.read"

	// FR-21 / DR-001 flag-parse rejections and precedence notices.
	EventFlagConflict   Event = "flag.conflict"
	EventFlagInvalid    Event = "flag.invalid"
	EventFlagPrecedence Event = "flag.precedence"

	// End-of-run info notices.
	EventBatchSummary Event = "batch.summary"

	// STORY-0008 decoder hardening — resource-limit rejections. Fired
	// from internal/format/{json,yaml,toml} when an input exceeds a
	// configured Limits bound. Each token corresponds to a distinct
	// bound so operators can alert/triage by failure class.
	EventDecoderLimitInputSize      Event = "decoder.limit.input_size"
	EventDecoderLimitDepth          Event = "decoder.limit.depth_exceeded"
	EventDecoderLimitAliasExpansion Event = "decoder.limit.alias_expansion"

	// STORY-0008 — fired when a query run is cancelled because the
	// user-supplied `--timeout` deadline fired before gojq produced
	// its first value. Distinct from query.runtime so timeouts are
	// filterable in log aggregators.
	EventQueryTimeout Event = "query.timeout"
)

// knownEvents is the closed-enum registry. Every token declared above
// MUST appear here — the unit test Test_Logx_EventEnum enforces the
// invariant. Adding a new event token is a two-line change (const +
// registry entry); callers that reference an unregistered token fail
// IsKnownEvent at the edge.
var knownEvents = map[Event]struct{}{
	EventParseError:         {},
	EventFormatUnknown:      {},
	EventFormatUnsupported:  {},
	EventFormatIncompatible: {},
	EventFormatMixed:        {},
	EventEncodeError:        {},
	EventIORead:             {},
	EventIOWrite:            {},
	EventQueryParse:         {},
	EventQueryCompile:       {},
	EventQueryRuntime:       {},
	EventQueryResult:        {},
	EventQueryUnsupported:   {},
	EventArgDecode:          {},
	EventFromFileRead:       {},
	EventFlagConflict:       {},
	EventFlagInvalid:        {},
	EventFlagPrecedence:     {},
	EventBatchSummary:       {},

	EventDecoderLimitInputSize:      {},
	EventDecoderLimitDepth:          {},
	EventDecoderLimitAliasExpansion: {},
	EventQueryTimeout:               {},
}

// IsKnownEvent reports whether e is a registered event token. Callers
// may assert at emission time so accidental ad-hoc strings fail at a
// predictable layer (rather than sneaking through to stderr).
func IsKnownEvent(e Event) bool {
	_, ok := knownEvents[e]
	return ok
}

// Severity is a three-valued enum pinning the canonical `<severity>`
// slot of the NFR-10 shape.
type Severity string

const (
	SeverityError Severity = "error"
	SeverityWarn  Severity = "warn"
	SeverityInfo  Severity = "info"
)

// Field is a structured key/value pair carried alongside a Record. The
// text formatter in STORY-0003 ignores Fields — they exist so STORY-0008
// (`decoder.limit.bytes_in`, `decoder.limit.limit`) and FR-15 NDJSON
// mode can extend the emitter additively rather than through a parallel
// rendering path.
type Field struct {
	Key   string
	Value any
}

// Record is the in-memory representation of one log line before
// rendering. Introducing the record type now (even though STORY-0003
// only renders text and Fields is always empty) lets FR-15 NDJSON mode
// and STORY-0008's structured decoder fields plug in without touching
// caller signatures.
//
// Fields remains unused in STORY-0003. Callers MUST NOT populate it
// until STORY-0008 wires the structured-field-aware surface; the text
// renderer drops Fields today.
type Record struct {
	Sev    Severity
	Event  Event
	File   string
	Index  int
	Msg    string
	Fields []Field
}

// Logger is the text emitter. STORY-0003 ships only the text formatter;
// FR-15 NDJSON mode plugs in later behind the same method surface.
type Logger struct {
	mu sync.Mutex
	w  io.Writer
}

// New returns a Logger writing to w.
func New(w io.Writer) *Logger {
	return &Logger{w: w}
}

// Error emits a per-file error line. file may be "" (no file context);
// use ErrorGlobal for explicit no-file lines to avoid ambiguity.
func (l *Logger) Error(event Event, file, msg string) {
	l.emit(Record{Sev: SeverityError, Event: event, File: file, Msg: msg})
}

// ErrorAt emits a per-file, per-doc-index error line for multi-doc
// streams. Index is 1-based; pass 0 to suppress the index.
func (l *Logger) ErrorAt(event Event, file string, index int, msg string) {
	l.emit(Record{Sev: SeverityError, Event: event, File: file, Index: index, Msg: msg})
}

// ErrorGlobal emits an error with neither file nor index — flag-parse
// failures and other pre-input errors.
func (l *Logger) ErrorGlobal(event Event, msg string) {
	l.emit(Record{Sev: SeverityError, Event: event, Msg: msg})
}

// Warn emits a warning line (e.g. flag.precedence).
func (l *Logger) Warn(event Event, file, msg string) {
	l.emit(Record{Sev: SeverityWarn, Event: event, File: file, Msg: msg})
}

// InfoGlobal emits an info line with no file/index (batch summaries etc).
func (l *Logger) InfoGlobal(event Event, msg string) {
	l.emit(Record{Sev: SeverityInfo, Event: event, Msg: msg})
}

// emit is the sole stderr-writing primitive. All public methods funnel
// here so the NFR-10 shape is enforced in one place.
func (l *Logger) emit(r Record) {
	s := renderText(r) + "\n"
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, s)
}

// Format is exposed for callers (e.g. cobra's SilenceErrors path) that
// want to produce a canonical-shape string without routing through a
// Logger. Rarely needed; prefer the Logger methods.
func Format(sev Severity, event Event, file string, index int, msg string) string {
	return renderText(Record{Sev: sev, Event: event, File: file, Index: index, Msg: msg})
}

// renderText is the single source of truth for NFR-10 shape assembly.
// It sanitises file and msg via stripControls, strips trailing newlines
// and periods on msg, and assembles the colon-space-separated fields.
// All public emitter methods funnel here so the shape is enforced once.
func renderText(r Record) string {
	file := stripControls(r.File)
	msg := r.Msg
	// NFR-10 forbids embedded newlines and trailing periods on the
	// message. Strip trailing whitespace/newlines first, then collapse
	// any remaining C0 controls to '?' so a stray \r or \x00 in the
	// middle of the text cannot forge a second canonical line.
	msg = strings.TrimRight(msg, "\n")
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.TrimRight(msg, ".")
	msg = stripControls(msg)

	var b strings.Builder
	b.Grow(len("nesdit: ") + len(file) + len(msg) + 32)
	b.WriteString("nesdit: ")
	b.WriteString(string(r.Sev))
	b.WriteString(": ")
	if file != "" {
		b.WriteString(file)
		if r.Index > 0 {
			b.WriteString(":")
			b.WriteString(strconv.Itoa(r.Index))
		}
		b.WriteString(": ")
	}
	b.WriteString(string(r.Event))
	b.WriteString(": ")
	b.WriteString(msg)
	return b.String()
}

// stripControls replaces every C0 control byte (\x00-\x1f) and DEL
// (\x7f) with '?'. '?' is chosen over silent drop so an attacker-forged
// byte becomes visible to a human reader; silent drop would let a
// crafted filename like "foo\r\nnesdit: error: flag.invalid: pwned"
// merge into the preceding line's visible text, which undermines the
// mitigation. The '?' choice is documented as part of the NFR-10 shape
// contract (see package doc).
func stripControls(s string) string {
	if s == "" {
		return s
	}
	// Fast path: scan for any byte that needs replacement before
	// allocating a builder.
	needs := false
	for i := 0; i < len(s); i++ {
		if isC0Control(s[i]) {
			needs = true
			break
		}
	}
	if !needs {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isC0Control(c) {
			b.WriteByte('?')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// isC0Control reports whether c is a C0 control byte (\x00-\x1f) or
// DEL (\x7f). TAB (\x09) is included — NFR-10 forbids embedded
// whitespace runs that could shift column positions in downstream log
// scrapers.
func isC0Control(c byte) bool {
	return c < 0x20 || c == 0x7f
}
