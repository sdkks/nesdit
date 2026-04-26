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
var canonicalLineRe = regexp.MustCompile(
	`^nesdit: (error|warn|info): (?:[^:]+(?::[0-9]+)?: )?[a-z][a-z0-9_.]*: [^.\s].*[^.]$`,
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
