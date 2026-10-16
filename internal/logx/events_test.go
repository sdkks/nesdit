package logx_test

import (
	"testing"

	"github.com/sdkks/nesdit/internal/logx"
)

// Test_Logx_EventEnum pins the closed-enum taxonomy of event tokens
// shared by the human-mode formatter and NDJSON mode.
// This test pins required error/warn/info tokens, including resource limits
// and cancellation, so accidental renames become a CI error. It does not
// enumerate every registered event.
func Test_Logx_EventEnum(t *testing.T) {
	required := []logx.Event{
		// Decode / parse errors across formats.
		logx.EventParseError,
		// Encode errors (including cross-format incompatibility).
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
		// Flag-parse and flag-interaction rejections.
		logx.EventFlagConflict,
		logx.EventFlagInvalid,
		// --arg / --argjson parse failures.
		logx.EventArgDecode,
		// -f / --from-file errors.
		logx.EventFromFileRead,
		// Unknown / unsupported format detection.
		logx.EventFormatUnknown,
		// Decoder resource limits and query timeout.
		logx.EventDecoderLimitInputSize,
		logx.EventDecoderLimitDepth,
		logx.EventDecoderLimitYAMLNodeCount,
		logx.EventQueryTimeout,
		// Context cancellation distinct from timeout.
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

	// Token shape: [a-z][a-z0-9_.]*.
	for _, e := range required {
		if !isValidEventToken(string(e)) {
			t.Errorf("event token %q does not match required shape [a-z][a-z0-9_.]*", e)
		}
	}
}

// isValidEventToken mirrors the canonical regex for an event token.
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
