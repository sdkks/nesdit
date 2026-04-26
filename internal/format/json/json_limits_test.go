package json_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/format"
	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
)

// Test_JSONDecoder_InputSizeCap covers the STORY-0008 M2 boundary:
//   - sub-cap input decodes cleanly (positive test — protects against
//     an over-tight cap regressing real inputs).
//   - at-cap input decodes cleanly (len == cap is accepted).
//   - above-cap input is rejected with *format.LimitError{Kind: LimitInputSize}.
func Test_JSONDecoder_InputSizeCap(t *testing.T) {
	t.Parallel()
	src := []byte(`{"a":1,"b":2,"c":3}`)
	// sub-cap: generous ceiling.
	if _, err := jsonfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxBytes: int64(len(src) + 100)}); err != nil {
		t.Fatalf("sub-cap: unexpected err: %v", err)
	}
	// at-cap: exactly the length of the input.
	if _, err := jsonfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxBytes: int64(len(src))}); err != nil {
		t.Fatalf("at-cap: unexpected err: %v", err)
	}
	// above-cap: one byte less than the input — should be rejected.
	_, err := jsonfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxBytes: int64(len(src) - 1)})
	if err == nil {
		t.Fatalf("above-cap: expected error, got nil")
	}
	var lim *format.LimitError
	if !errors.As(err, &lim) {
		t.Fatalf("above-cap: want *format.LimitError, got %T: %v", err, err)
	}
	if lim.Kind != format.LimitInputSize || lim.Format != "json" {
		t.Errorf("above-cap: LimitError Kind=%q Format=%q want input_size/json", lim.Kind, lim.Format)
	}
}

// Test_JSONDecoder_NestingDepthCap covers STORY-0008 M3.
// Builds nested arrays with varying depth, then asserts:
//   - sub-cap: accepted
//   - at-cap: accepted (depth == max is fine)
//   - above-cap: rejected with LimitError{Kind: LimitDepth, Format: "json"}
func Test_JSONDecoder_NestingDepthCap(t *testing.T) {
	t.Parallel()
	// depth(n) == n nested arrays.
	build := func(n int) []byte {
		return []byte(strings.Repeat("[", n) + "1" + strings.Repeat("]", n))
	}
	// sub-cap: 5 < 10.
	if _, err := jsonfmt.DecodeValueWithLimits(bytes.NewReader(build(5)), format.Limits{MaxDepth: 10}); err != nil {
		t.Fatalf("sub-cap: unexpected err: %v", err)
	}
	// at-cap: 10 nested arrays with MaxDepth=10.
	if _, err := jsonfmt.DecodeValueWithLimits(bytes.NewReader(build(10)), format.Limits{MaxDepth: 10}); err != nil {
		t.Fatalf("at-cap: unexpected err: %v", err)
	}
	// above-cap: 11 nested arrays with MaxDepth=10 must fail.
	_, err := jsonfmt.DecodeValueWithLimits(bytes.NewReader(build(11)), format.Limits{MaxDepth: 10})
	if err == nil {
		t.Fatalf("above-cap: expected error, got nil")
	}
	var lim *format.LimitError
	if !errors.As(err, &lim) {
		t.Fatalf("want *format.LimitError, got %T: %v", err, err)
	}
	if lim.Kind != format.LimitDepth || lim.Format != "json" {
		t.Errorf("LimitError Kind=%q Format=%q want depth/json", lim.Kind, lim.Format)
	}
}

// Test_JSONDecoder_NestingDepthBehavior confirms the stdlib JSON
// tokeniser reliably tokenises a moderately deep input when the nesdit
// cap is disabled — ensuring our MaxDepth cap is the effective bound,
// not some hidden stdlib ceiling. Documents the boundary required by
// SPEC-0001 STORY-0008 acceptance.
func Test_JSONDecoder_NestingDepthBehavior(t *testing.T) {
	t.Parallel()
	// 500-deep is well within any sane stdlib ceiling and below the
	// 1000 default cap. With MaxDepth=0 (disabled) the input must decode.
	src := []byte(strings.Repeat("[", 500) + "42" + strings.Repeat("]", 500))
	if _, err := jsonfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{}); err != nil {
		t.Fatalf("500-deep with no cap: unexpected err: %v", err)
	}
}

// Test_JSONDecoder_ZeroLimits_NoBound confirms zero values disable both
// caps (backstop for the "zero = no bound" contract).
func Test_JSONDecoder_ZeroLimits_NoBound(t *testing.T) {
	t.Parallel()
	src := []byte(fmt.Sprintf(`{"big":%q}`, strings.Repeat("x", 50_000)))
	if _, err := jsonfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{}); err != nil {
		t.Fatalf("zero limits should accept any size; got %v", err)
	}
}
