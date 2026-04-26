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
package logx

import (
	"fmt"
	"io"
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
	l.emit(SeverityError, event, file, 0, msg)
}

// ErrorAt emits a per-file, per-doc-index error line for multi-doc
// streams. Index is 1-based; pass 0 to suppress the index.
func (l *Logger) ErrorAt(event Event, file string, index int, msg string) {
	l.emit(SeverityError, event, file, index, msg)
}

// ErrorGlobal emits an error with neither file nor index — flag-parse
// failures and other pre-input errors.
func (l *Logger) ErrorGlobal(event Event, msg string) {
	l.emit(SeverityError, event, "", 0, msg)
}

// Warn emits a warning line (e.g. flag.precedence).
func (l *Logger) Warn(event Event, file, msg string) {
	l.emit(SeverityWarn, event, file, 0, msg)
}

// InfoGlobal emits an info line with no file/index (batch summaries etc).
func (l *Logger) InfoGlobal(event Event, msg string) {
	l.emit(SeverityInfo, event, "", 0, msg)
}

// emit is the sole stderr-writing primitive. All public methods funnel
// here so the NFR-10 shape is enforced in one place.
func (l *Logger) emit(sev Severity, event Event, file string, index int, msg string) {
	// Strip any trailing newline / period on the message — NFR-10
	// forbids embedded newlines and trailing periods. We sanitise
	// rather than fail so callers can't accidentally poison stderr.
	msg = strings.TrimRight(msg, "\n")
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.TrimRight(msg, ".")

	var b strings.Builder
	b.WriteString("nesdit: ")
	b.WriteString(string(sev))
	b.WriteString(": ")
	if file != "" {
		b.WriteString(file)
		if index > 0 {
			b.WriteString(":")
			fmt.Fprintf(&b, "%d", index)
		}
		b.WriteString(": ")
	}
	b.WriteString(string(event))
	b.WriteString(": ")
	b.WriteString(msg)
	b.WriteString("\n")

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, b.String())
}

// Format is exposed for callers (e.g. cobra's SilenceErrors path) that
// want to produce a canonical-shape string without routing through a
// Logger. Rarely needed; prefer the Logger methods.
func Format(sev Severity, event Event, file string, index int, msg string) string {
	msg = strings.TrimRight(msg, "\n")
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.TrimRight(msg, ".")
	var b strings.Builder
	b.WriteString("nesdit: ")
	b.WriteString(string(sev))
	b.WriteString(": ")
	if file != "" {
		b.WriteString(file)
		if index > 0 {
			b.WriteString(":")
			fmt.Fprintf(&b, "%d", index)
		}
		b.WriteString(": ")
	}
	b.WriteString(string(event))
	b.WriteString(": ")
	b.WriteString(msg)
	return b.String()
}
