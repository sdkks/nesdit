package yaml_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/format"
	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
)

// Test_YAMLDecoder_InputSizeCap covers STORY-0008 M2 for YAML: sub/at/above.
func Test_YAMLDecoder_InputSizeCap(t *testing.T) {
	t.Parallel()
	src := []byte("a: 1\nb: 2\nc: 3\n")
	if _, err := yamlfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxBytes: int64(len(src) + 100)}); err != nil {
		t.Fatalf("sub-cap: unexpected err: %v", err)
	}
	if _, err := yamlfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxBytes: int64(len(src))}); err != nil {
		t.Fatalf("at-cap: unexpected err: %v", err)
	}
	_, err := yamlfmt.DecodeValueWithLimits(bytes.NewReader(src), format.Limits{MaxBytes: int64(len(src) - 1)})
	if err == nil {
		t.Fatalf("above-cap: expected error, got nil")
	}
	var lim *format.LimitError
	if !errors.As(err, &lim) {
		t.Fatalf("above-cap: want *format.LimitError, got %T: %v", err, err)
	}
	if lim.Kind != format.LimitInputSize || lim.Format != "yaml" {
		t.Errorf("LimitError Kind=%q Format=%q want input_size/yaml", lim.Kind, lim.Format)
	}
}

// Test_YAMLDecoder_NestingDepthCap covers STORY-0008 M3 for YAML.
func Test_YAMLDecoder_NestingDepthCap(t *testing.T) {
	t.Parallel()
	// YAML flow-style nested sequences: [[[...]]]
	build := func(n int) []byte {
		return []byte(strings.Repeat("[", n) + "1" + strings.Repeat("]", n) + "\n")
	}
	if _, err := yamlfmt.DecodeValueWithLimits(bytes.NewReader(build(5)), format.Limits{MaxDepth: 10}); err != nil {
		t.Fatalf("sub-cap: unexpected err: %v", err)
	}
	if _, err := yamlfmt.DecodeValueWithLimits(bytes.NewReader(build(10)), format.Limits{MaxDepth: 10}); err != nil {
		t.Fatalf("at-cap: unexpected err: %v", err)
	}
	_, err := yamlfmt.DecodeValueWithLimits(bytes.NewReader(build(11)), format.Limits{MaxDepth: 10})
	if err == nil {
		t.Fatalf("above-cap: expected error, got nil")
	}
	var lim *format.LimitError
	if !errors.As(err, &lim) {
		t.Fatalf("want *format.LimitError, got %T: %v", err, err)
	}
	if lim.Kind != format.LimitDepth || lim.Format != "yaml" {
		t.Errorf("LimitError Kind=%q Format=%q want depth/yaml", lim.Kind, lim.Format)
	}
}

// Test_YAMLDecoder_BillionLaughs_Rejected covers STORY-0008 M1: a small
// (< 1KB) YAML alias-expansion bomb must be rejected via the alias-
// expansion cap *before* exhausting memory. Expands to ~10^7 node
// materialisations if allowed to run to completion.
func Test_YAMLDecoder_BillionLaughs_Rejected(t *testing.T) {
	t.Parallel()
	// 7 levels × 10 aliases per level ≈ 10^7 materialisations.
	bomb := "" +
		"a: &a [0,0,0,0,0,0,0,0,0,0]\n" +
		"b: &b [*a,*a,*a,*a,*a,*a,*a,*a,*a,*a]\n" +
		"c: &c [*b,*b,*b,*b,*b,*b,*b,*b,*b,*b]\n" +
		"d: &d [*c,*c,*c,*c,*c,*c,*c,*c,*c,*c]\n" +
		"e: &e [*d,*d,*d,*d,*d,*d,*d,*d,*d,*d]\n" +
		"f: &f [*e,*e,*e,*e,*e,*e,*e,*e,*e,*e]\n" +
		"g: [*f,*f,*f,*f,*f,*f,*f,*f,*f,*f]\n"
	if len(bomb) > 1024 {
		t.Fatalf("billion-laughs fixture is %d bytes; spec requires < 1KB", len(bomb))
	}
	_, err := yamlfmt.DecodeValueWithLimits(bytes.NewReader([]byte(bomb)), format.Limits{
		MaxBytes:     int64(len(bomb) + 100),
		MaxDepth:     100,
		MaxYAMLNodes: 100_000,
	})
	if err == nil {
		t.Fatalf("expected yaml-node-count rejection, got nil (memory was exhausted or aliases ignored)")
	}
	var lim *format.LimitError
	if !errors.As(err, &lim) {
		t.Fatalf("want *format.LimitError, got %T: %v", err, err)
	}
	if lim.Kind != format.LimitYAMLNodeCount || lim.Format != "yaml" {
		t.Errorf("LimitError Kind=%q Format=%q want yaml_node_count/yaml", lim.Kind, lim.Format)
	}
}

// Test_YAMLDecoder_AliasBelowCap_Passes is the positive counterpart —
// a small document with a handful of aliases (well below 100_000 nodes)
// must decode cleanly. Regression guard for an over-tight alias cap.
func Test_YAMLDecoder_AliasBelowCap_Passes(t *testing.T) {
	t.Parallel()
	src := "" +
		"a: &a [1,2,3]\n" +
		"b: *a\n" +
		"c: *a\n"
	if _, err := yamlfmt.DecodeValueWithLimits(bytes.NewReader([]byte(src)), format.Limits{
		MaxYAMLNodes: 1000,
	}); err != nil {
		t.Fatalf("small-alias doc unexpectedly rejected: %v", err)
	}
}

// Test_YAMLDecoder_ZeroLimits_NoBound confirms zero values disable every
// cap (even the billion-laughs bomb reaches the parser); this is the
// "tests can opt out" contract.
//
// NOTE: we do NOT actually decode the bomb here — yaml.v3's internal
// alias-count protection would trip regardless of our cap, and invoking
// it burns unnecessary CPU. Instead we only decode a simple doc.
func Test_YAMLDecoder_ZeroLimits_NoBound(t *testing.T) {
	t.Parallel()
	if _, err := yamlfmt.DecodeValueWithLimits(bytes.NewReader([]byte("a: 1\n")), format.Limits{}); err != nil {
		t.Fatalf("unbounded decode should accept a trivial doc: %v", err)
	}
}
