package logx_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/logx"
)

// canonicalLineRe is the regex from SPEC-0001 NFR-10 that every stderr
// line produced by nesdit in human mode must match. Fields:
//
//	nesdit: <severity>: [<file>[:<index>]: ]<event>: <message>
//
// The optional file/index group is present for per-doc errors; absent
// for flag-parse / global errors. No trailing period on the message.
//
// The `<file>` slot is defined as "no colon, no C0 control byte" —
// colon is structural (it separates fields), and control bytes are
// rejected to prevent CR-smuggling via attacker-supplied filenames.
// nesdit's path-resolution layer rejects colon-bearing paths before
// they reach the emitter; logx's stripControls replaces C0 bytes with
// '?' defensively so the regex still matches if a colon-free but
// control-bearing path leaks through.
var canonicalLineRe = regexp.MustCompile(
	`^nesdit: (error|warn|info): (?:[^:\x00-\x1f\x7f]+(?::[0-9]+)?: )?[a-z][a-z0-9_.]*: [^.\s].*[^.]$`,
)

// TestCanonicalErrorShape asserts that every line emitted by the logx
// text formatter conforms to the NFR-10 canonical shape. This pins the
// single source of truth for human-mode stderr emission so no subsystem
// may drift into ad-hoc strings.
func TestCanonicalErrorShape(t *testing.T) {
	cases := []struct {
		name   string
		run    func(l *logx.Logger)
		expect *regexp.Regexp
	}{
		{
			name: "error with file and single-doc (no index)",
			run: func(l *logx.Logger) {
				l.Error(logx.EventParseError, "config.yaml", `mapping key duplicated: "replicas" at line 12`)
			},
			expect: regexp.MustCompile(
				`^nesdit: error: config\.yaml: parse\.error: mapping key duplicated: "replicas" at line 12\n$`,
			),
		},
		{
			name: "error with file and doc index",
			run: func(l *logx.Logger) {
				l.ErrorAt(logx.EventQueryRuntime, "stream.yaml", 3, "expected object at .x")
			},
			expect: regexp.MustCompile(
				`^nesdit: error: stream\.yaml:3: query\.runtime: expected object at \.x\n$`,
			),
		},
		{
			name: "flag-parse error (no file, no index)",
			run: func(l *logx.Logger) {
				l.ErrorGlobal(logx.EventFlagInvalid, "--backup requires -i")
			},
			expect: regexp.MustCompile(
				`^nesdit: error: flag\.invalid: --backup requires -i\n$`,
			),
		},
		{
			name: "warn precedence notice",
			run: func(l *logx.Logger) {
				l.Warn(logx.EventFlagConflict, "", "`-i` ignored because --dry-run is set")
			},
			expect: regexp.MustCompile(
				`^nesdit: warn: flag\.conflict: .*\n$`,
			),
		},
		{
			name: "info batch summary (no file, no index)",
			run: func(l *logx.Logger) {
				l.InfoGlobal(logx.EventBatchSummary, "3 changed, 1 unchanged, 0 errored")
			},
			expect: regexp.MustCompile(
				`^nesdit: info: batch\.summary: 3 changed, 1 unchanged, 0 errored\n$`,
			),
		},
		{
			name: "stdin-as-file uses literal '-'",
			run: func(l *logx.Logger) {
				l.Error(logx.EventParseError, "-", "unexpected EOF")
			},
			expect: regexp.MustCompile(
				`^nesdit: error: -: parse\.error: unexpected EOF\n$`,
			),
		},
		{
			// S3: EventFlagPrecedence is declared but would otherwise
			// have no emitter-level test pinning its token. This case
			// catches accidental rename in STORY-0004/STORY-0008.
			name: "warn flag.precedence notice",
			run: func(l *logx.Logger) {
				l.Warn(logx.EventFlagPrecedence, "", "`--dry-run` overrides `-i`")
			},
			expect: regexp.MustCompile(
				"^nesdit: warn: flag\\.precedence: `--dry-run` overrides `-i`\n$",
			),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := logx.New(&buf)
			tc.run(l)
			got := buf.String()
			if !tc.expect.MatchString(got) {
				t.Fatalf("line did not match expected regex\n want regex: %s\n got: %q",
					tc.expect, got)
			}
			// Also assert the canonical regex from NFR-10.
			for i, line := range splitLines(got) {
				if line == "" {
					continue
				}
				if !canonicalLineRe.MatchString(line) {
					t.Errorf("line %d failed NFR-10 canonical regex\n regex: %s\n line: %q",
						i, canonicalLineRe, line)
				}
				// NFR-10: no trailing period.
				if strings.HasSuffix(line, ".") {
					t.Errorf("line %d has trailing period (NFR-10 forbids): %q", i, line)
				}
			}
		})
	}
}

func splitLines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// TestCanonicalShape_ControlSanitization pins the CR-smuggling
// mitigation: neither attacker-controlled filenames nor message text
// may inject \r, \n, or other C0 control bytes into the canonical line.
// logx replaces every C0 byte (\x00-\x1f, \x7f) with '?' before
// rendering. The replacement preserves the record-boundary invariant
// even when a path-resolution layer upstream has been bypassed, and
// keeps the forgery visibly marked ('?') rather than silently dropped.
func TestCanonicalShape_ControlSanitization(t *testing.T) {
	cases := []struct {
		name string
		run  func(l *logx.Logger)
		// wantOneLine asserts exactly one \n-terminated line is emitted
		// (the forged sequence must NOT materialise a second apparent
		// line on stderr).
		// wantNoCR asserts the rendered output contains no \r or \n
		// bytes inside the line body.
		wantNoCR bool
	}{
		{
			name: "CRLF in filename is neutralised and does not forge a second line",
			run: func(l *logx.Logger) {
				// Attacker-supplied filename attempts to inject a
				// second canonical-shape line via embedded \r\n.
				forged := "foo.json\r\nnesdit: error: flag.invalid: pwned\r"
				l.Error(logx.EventParseError, forged, "unexpected EOF")
			},
			wantNoCR: true,
		},
		{
			name: "bare CR in filename (CR-smuggling on \\r-only terminals)",
			run: func(l *logx.Logger) {
				l.Error(logx.EventParseError, "evil.yaml\rOK", "unexpected EOF")
			},
			wantNoCR: true,
		},
		{
			name: "NUL and ESC in message are replaced",
			run: func(l *logx.Logger) {
				l.ErrorGlobal(logx.EventFlagInvalid, "pwn\x00ed\x1b[31mred")
			},
			wantNoCR: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			l := logx.New(&buf)
			tc.run(l)
			got := buf.String()

			// (a) Exactly one \n-terminated line is emitted.
			if !strings.HasSuffix(got, "\n") {
				t.Fatalf("expected trailing newline; got %q", got)
			}
			body := strings.TrimRight(got, "\n")
			if strings.Count(body, "\n") != 0 {
				t.Fatalf("expected exactly one line, got multi-line output: %q", got)
			}

			// (b) Rendered line contains NO embedded \r or \n bytes.
			if tc.wantNoCR {
				if strings.ContainsAny(body, "\r\n") {
					t.Fatalf("line contains a raw \\r or \\n byte (CR-smuggling mitigation failed): %q", got)
				}
				// Also: no C0 controls at all in the body.
				for i := 0; i < len(body); i++ {
					c := body[i]
					if c < 0x20 || c == 0x7f {
						t.Fatalf("line contains C0 control byte %#x (CR-smuggling mitigation failed): %q", c, got)
					}
				}
			}

			// (c) The emitted line still matches the NFR-10 regex.
			if !canonicalLineRe.MatchString(body) {
				t.Fatalf("sanitised line failed NFR-10 regex\n regex: %s\n line: %q",
					canonicalLineRe, body)
			}
		})
	}
}
