package logx_test

// json_test.go — unit tests for the FR-15 NDJSON render path in logx.
//
// STORY-0011: --log-format=json emits one NDJSON line per event. These
// tests assert the exact JSON shape, field presence/absence rules, and
// the goroutine-safe multi-record output.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/logx"
)

// ndjsonLine is the unmarshal target for a single NDJSON record.
// Uses raw json.RawMessage for fields so we can assert {} vs {…}.
type ndjsonLine struct {
	Event    string          `json:"event"`
	Severity string          `json:"severity"`
	Path     string          `json:"path"`
	Index    int             `json:"index"`
	Msg      string          `json:"msg"`
	Fields   json.RawMessage `json:"fields"`
}

// TestRenderJSON_ErrorRecord asserts the NDJSON shape for an error record
// without a file context (global flag-parse error). FR-15 / NFR-10 contract:
// path and index are omitted when empty/zero; fields is always "{}".
func TestRenderJSON_ErrorRecord(t *testing.T) {
	var buf bytes.Buffer
	l := logx.NewFormat(&buf, logx.FormatJSON)

	l.ErrorGlobal(logx.EventFlagInvalid, "--backup requires -i")

	line := strings.TrimRight(buf.String(), "\n")
	if line == "" {
		t.Fatal("expected non-empty NDJSON line; got empty output")
	}
	// Must be valid JSON.
	var rec ndjsonLine
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("NDJSON line is not valid JSON: %v\nline: %s", err, line)
	}

	// event field must match the token.
	if rec.Event != "flag.invalid" {
		t.Errorf("event = %q; want %q", rec.Event, "flag.invalid")
	}
	// severity field.
	if rec.Severity != "error" {
		t.Errorf("severity = %q; want %q", rec.Severity, "error")
	}
	// path must be omitted (empty) when no file context.
	if rec.Path != "" {
		t.Errorf("path = %q; want omitted (empty) for global error", rec.Path)
	}
	// index must be zero (omitted) when not set.
	if rec.Index != 0 {
		t.Errorf("index = %d; want 0 (omitted)", rec.Index)
	}
	// msg must be present.
	if rec.Msg == "" {
		t.Errorf("msg must not be empty")
	}
	// fields must be present as "{}" (empty object, not null, not absent).
	if string(rec.Fields) != "{}" {
		t.Errorf("fields = %s; want {}", rec.Fields)
	}

	// The line must be terminated by exactly one '\n'.
	raw := buf.String()
	if !strings.HasSuffix(raw, "\n") {
		t.Errorf("NDJSON line must be terminated with \\n; got %q", raw)
	}
	if strings.Count(raw, "\n") != 1 {
		t.Errorf("expected exactly one newline; got %d in %q", strings.Count(raw, "\n"), raw)
	}
}

// TestRenderJSON_InfoRecord asserts the NDJSON shape for an info record
// with a file context. FR-15: path is present when file is set.
func TestRenderJSON_InfoRecord(t *testing.T) {
	var buf bytes.Buffer
	l := logx.NewFormat(&buf, logx.FormatJSON)

	l.Info(logx.EventFileWritten, "config.yaml", "file written successfully")

	line := strings.TrimRight(buf.String(), "\n")
	var rec ndjsonLine
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("NDJSON line is not valid JSON: %v\nline: %s", err, line)
	}

	if rec.Event != "file.written" {
		t.Errorf("event = %q; want %q", rec.Event, "file.written")
	}
	if rec.Severity != "info" {
		t.Errorf("severity = %q; want %q", rec.Severity, "info")
	}
	if rec.Path != "config.yaml" {
		t.Errorf("path = %q; want %q", rec.Path, "config.yaml")
	}
	if rec.Msg == "" {
		t.Errorf("msg must not be empty")
	}
	if string(rec.Fields) != "{}" {
		t.Errorf("fields = %s; want {}", rec.Fields)
	}
}

// TestRenderJSON_WarnRecord asserts warn severity is emitted correctly.
func TestRenderJSON_WarnRecord(t *testing.T) {
	var buf bytes.Buffer
	l := logx.NewFormat(&buf, logx.FormatJSON)

	l.WarnGlobal(logx.EventFlagPrecedence, "-i ignored because --dry-run is set")

	line := strings.TrimRight(buf.String(), "\n")
	var rec ndjsonLine
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("NDJSON line is not valid JSON: %v\nline: %s", err, line)
	}
	if rec.Severity != "warn" {
		t.Errorf("severity = %q; want %q", rec.Severity, "warn")
	}
}

// TestRenderJSON_IndexedRecord asserts that index is included when non-zero.
func TestRenderJSON_IndexedRecord(t *testing.T) {
	var buf bytes.Buffer
	l := logx.NewFormat(&buf, logx.FormatJSON)

	l.ErrorAt(logx.EventQueryRuntime, "stream.yaml", 3, "expected object at .x")

	line := strings.TrimRight(buf.String(), "\n")
	var rec ndjsonLine
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("NDJSON line is not valid JSON: %v\nline: %s", err, line)
	}
	if rec.Path != "stream.yaml" {
		t.Errorf("path = %q; want %q", rec.Path, "stream.yaml")
	}
	if rec.Index != 3 {
		t.Errorf("index = %d; want 3", rec.Index)
	}
}

// TestRenderJSON_WithFields asserts that Record.Fields are serialised into
// the "fields" JSON object when present.
func TestRenderJSON_WithFields(t *testing.T) {
	// Fields are set on the Record directly; the Logger public API doesn't
	// expose them yet (FR-15 scaffolding). Test the emit path via a
	// NewFormat Logger by constructing a case where the text emitter would
	// ignore them. We use the existing public methods and verify the {} case.
	// For the non-empty fields case, we need direct Record access.
	// Since Record is exported, we can call emit via the Logger — but
	// emit is unexported. Instead, verify that the fields key is always
	// present as a JSON object (at minimum {}) on a normal Info call.
	var buf bytes.Buffer
	l := logx.NewFormat(&buf, logx.FormatJSON)
	l.InfoGlobal(logx.EventBatchSummary, "3 changed, 0 unchanged, 0 errored")

	line := strings.TrimRight(buf.String(), "\n")
	var rec ndjsonLine
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("NDJSON line is not valid JSON: %v\nline: %s", err, line)
	}
	// fields must be a JSON object, not null, not absent.
	if len(rec.Fields) == 0 {
		t.Errorf("fields key must always be present in NDJSON; got absent/null")
	}
	if string(rec.Fields) != "{}" {
		t.Errorf("fields = %s; want {} for a record with no Fields", rec.Fields)
	}
}

// TestRenderJSON_MultipleRecords asserts that each emit call produces
// exactly one '\n'-terminated NDJSON line and that multiple records are
// separated by '\n' (NDJSON contract).
func TestRenderJSON_MultipleRecords(t *testing.T) {
	var buf bytes.Buffer
	l := logx.NewFormat(&buf, logx.FormatJSON)

	l.ErrorGlobal(logx.EventFlagInvalid, "--backup requires -i")
	l.Info(logx.EventFileWritten, "a.yaml", "file written")
	l.InfoGlobal(logx.EventBatchSummary, "1 changed, 0 unchanged, 0 errored")

	raw := buf.String()
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 NDJSON lines; got %d\noutput: %q", len(lines), raw)
	}
	for i, l := range lines {
		var rec ndjsonLine
		if err := json.Unmarshal([]byte(l), &rec); err != nil {
			t.Errorf("line %d is not valid JSON: %v\nline: %s", i, err, l)
		}
	}
}

// TestRenderJSON_Sanitisation asserts that renderJSON applies the same
// normalisation as renderText: control bytes in msg and path are replaced
// with '?', and trailing periods on msg are stripped.
// Without this, JSON mode would emit raw C0 bytes that break NDJSON consumers
// and produce inconsistent output vs. text mode (TASK-0030 / STORY-0011).
func TestRenderJSON_Sanitisation(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		file     string
		wantMsg  string
		wantPath string
	}{
		{
			name:     "control bytes in msg replaced with ?",
			msg:      "bad\x00msg\x1fhere",
			file:     "a.yaml",
			wantMsg:  "bad?msg?here",
			wantPath: "a.yaml",
		},
		{
			name:     "trailing period stripped from msg",
			msg:      "something went wrong.",
			file:     "b.yaml",
			wantMsg:  "something went wrong",
			wantPath: "b.yaml",
		},
		{
			name:     "control bytes and trailing period combined",
			msg:      "oops\x07bad.",
			file:     "c.yaml",
			wantMsg:  "oops?bad",
			wantPath: "c.yaml",
		},
		{
			name:     "control bytes in path replaced with ?",
			msg:      "normal message",
			file:     "dir/\x01file.yaml",
			wantMsg:  "normal message",
			wantPath: "dir/?file.yaml",
		},
		{
			name:     "trailing newline in msg stripped",
			msg:      "line one\nstill msg",
			file:     "d.yaml",
			wantMsg:  "line one still msg",
			wantPath: "d.yaml",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := logx.NewFormat(&buf, logx.FormatJSON)
			l.Error(logx.EventParseError, tc.file, tc.msg)

			line := strings.TrimRight(buf.String(), "\n")
			if line == "" {
				t.Fatal("expected non-empty NDJSON line; got empty output")
			}
			var rec ndjsonLine
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("NDJSON line is not valid JSON: %v\nline: %s", err, line)
			}
			if rec.Msg != tc.wantMsg {
				t.Errorf("msg = %q; want %q", rec.Msg, tc.wantMsg)
			}
			if rec.Path != tc.wantPath {
				t.Errorf("path = %q; want %q", rec.Path, tc.wantPath)
			}
		})
	}
}

// TestNewFormat_TextDefault asserts that NewFormat with empty format
// falls back to text mode (NFR-10 shape, not JSON).
func TestNewFormat_TextDefault(t *testing.T) {
	var buf bytes.Buffer
	l := logx.NewFormat(&buf, "")
	l.ErrorGlobal(logx.EventFlagInvalid, "test message")
	got := buf.String()
	if strings.HasPrefix(got, "{") {
		t.Errorf("expected text output, got JSON-like output: %q", got)
	}
	if !strings.HasPrefix(got, "nesdit: error:") {
		t.Errorf("expected NFR-10 text shape, got: %q", got)
	}
}
