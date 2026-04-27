package logx_test

import (
	"testing"

	"github.com/sdkks/nesdit/internal/logx"
)

// Test_Logx_EventEnum pins the closed-enum taxonomy of event tokens
// shared by the human-mode formatter and (future) FR-15 NDJSON mode,
// per SPEC-0001 NFR-10 and DR-004.
//
// STORY-0003 establishes the initial set covering every error/warn/info
// site that exists today plus every site the cobra tree adds. STORY-0008
// will extend this enum with decoder.limit.* and query.timeout tokens;
// this test intentionally does NOT pin the full surface — it pins the
// tokens STORY-0003 must ship so accidental renames become a CI error.
func Test_Logx_EventEnum(t *testing.T) {
	required := []logx.Event{
		// Decode / parse errors across formats.
		logx.EventParseError,
		// Encode errors (including cross-format incompatibility, FR-19).
		logx.EventEncodeError,
		logx.EventFormatIncompatible,
		// IO errors reading a source file.
		logx.EventIORead,
		logx.EventIOWrite,
		// Query engine errors.
		logx.EventQueryParse,
		logx.EventQueryCompile,
		logx.EventQueryRuntime,
		logx.EventQueryResult,
		// Flag-parse and flag-interaction rejections (FR-21, DR-001).
		logx.EventFlagConflict,
		logx.EventFlagInvalid,
		// --arg / --argjson parse failures (FR-7, FR-8).
		logx.EventArgDecode,
		// -f / --from-file errors.
		logx.EventFromFileRead,
		// Unknown / unsupported format detection.
		logx.EventFormatUnknown,
		// STORY-0008 decoder hardening (M1/M2/M3 + M4 timeout).
		logx.EventDecoderLimitInputSize,
		logx.EventDecoderLimitDepth,
		logx.EventDecoderLimitYAMLNodeCount,
		logx.EventQueryTimeout,
		// TASK-0018 S-2 — context cancellation distinct from timeout.
		logx.EventQueryCancelled,
	}

	seen := map[logx.Event]bool{}
	for _, e := range required {
		if string(e) == "" {
			t.Errorf("event token is empty string — all tokens must be non-empty")
		}
		if seen[e] {
			t.Errorf("event token %q appears twice in the required list — tokens must be unique", e)
		}
		seen[e] = true
	}

	// Every required token must be registered as a known event so the
	// canonical formatter can round-trip it and the lint below catches
	// stray ad-hoc strings.
	for _, e := range required {
		if !logx.IsKnownEvent(e) {
			t.Errorf("logx.IsKnownEvent(%q) = false; required tokens must be registered in the closed enum", e)
		}
	}

	// Token shape: [a-z][a-z0-9_.]* per NFR-10 regex.
	for _, e := range required {
		if !isValidEventToken(string(e)) {
			t.Errorf("event token %q does not match required shape [a-z][a-z0-9_.]*", e)
		}
	}
}

// isValidEventToken mirrors the NFR-10 regex for an event token.
func isValidEventToken(s string) bool {
	if s == "" {
		return false
	}
	first := s[0]
	if first < 'a' || first > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '_' || c == '.'
		if !ok {
			return false
		}
	}
	return true
}
