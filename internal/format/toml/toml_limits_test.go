package toml_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/format"
	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
)

// Test_TOMLDecoder_InputSizeCap covers STORY-0008 M2 for TOML.
func Test_TOMLDecoder_InputSizeCap(t *testing.T) {
	t.Parallel()
	src := []byte("a = 1\nb = 2\nc = 3\n")
	if _, err := tomlfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxBytes: int64(len(src) + 100)}); err != nil {
		t.Fatalf("sub-cap: unexpected err: %v", err)
	}
	if _, err := tomlfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxBytes: int64(len(src))}); err != nil {
		t.Fatalf("at-cap: unexpected err: %v", err)
	}
	_, err := tomlfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxBytes: int64(len(src) - 1)})
	if err == nil {
		t.Fatalf("above-cap: expected error, got nil")
	}
	var lim *format.LimitError
	if !errors.As(err, &lim) {
		t.Fatalf("above-cap: want *format.LimitError, got %T: %v", err, err)
	}
	if lim.Kind != format.LimitInputSize || lim.Format != "toml" {
		t.Errorf("LimitError Kind=%q Format=%q want input_size/toml", lim.Kind, lim.Format)
	}
}

// Test_TOMLDecoder_NestingDepthCap covers STORY-0008 M3 for TOML.
// Uses a dotted-key expression to drive depth — `a.b.c.d = 1` sits at
// depth 4 regardless of table headers.
func Test_TOMLDecoder_NestingDepthCap(t *testing.T) {
	t.Parallel()
	build := func(n int) []byte {
		// "a.b.c... = 1" — n dotted parts
		parts := make([]string, n)
		for i := range parts {
			parts[i] = string(rune('a' + (i % 26)))
		}
		return []byte(strings.Join(parts, ".") + " = 1\n")
	}
	// sub-cap: 5 < 10.
	if _, err := tomlfmt.DecodeValueWithLimits(bytes.NewReader(build(5)), format.Limits{MaxDepth: 10}); err != nil {
		t.Fatalf("sub-cap: unexpected err: %v", err)
	}
	// at-cap: 10 dotted parts with MaxDepth=10.
	if _, err := tomlfmt.DecodeValueWithLimits(bytes.NewReader(build(10)), format.Limits{MaxDepth: 10}); err != nil {
		t.Fatalf("at-cap: unexpected err: %v", err)
	}
	// above-cap: 11 dotted parts must fail.
	_, err := tomlfmt.DecodeValueWithLimits(bytes.NewReader(build(11)), format.Limits{MaxDepth: 10})
	if err == nil {
		t.Fatalf("above-cap: expected error, got nil")
	}
	var lim *format.LimitError
	if !errors.As(err, &lim) {
		t.Fatalf("want *format.LimitError, got %T: %v", err, err)
	}
	if lim.Kind != format.LimitDepth || lim.Format != "toml" {
		t.Errorf("LimitError Kind=%q Format=%q want depth/toml", lim.Kind, lim.Format)
	}
}

// Test_TOMLDecoder_InlineTableDepthCap exercises the inline-table path
// so the astValue depth propagation is covered (not just dotted keys).
func Test_TOMLDecoder_InlineTableDepthCap(t *testing.T) {
	t.Parallel()
	// root = { a = { b = { c = 1 } } } → depth 4.
	src := []byte(`root = { a = { b = { c = 1 } } }` + "\n")
	if _, err := tomlfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxDepth: 4}); err != nil {
		t.Fatalf("at-cap inline table: unexpected err: %v", err)
	}
	_, err := tomlfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxDepth: 3})
	if err == nil {
		t.Fatalf("above-cap inline table: expected error, got nil")
	}
	var lim *format.LimitError
	if !errors.As(err, &lim) || lim.Kind != format.LimitDepth {
		t.Errorf("want LimitDepth error, got %T: %v", err, err)
	}
}
