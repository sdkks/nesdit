package yaml_test

import (
	"bytes"
	"strings"
	"testing"

	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
	"github.com/sdkks/nesdit/internal/omap"
)

// BUG-0001 regression: YAML decoder previously required a top-level mapping.
// YAML 1.2 allows any node (mapping, sequence, scalar) at the top level.
// These tests exercise DecodeValue/EncodeValue — the top-level-agnostic API
// added to fix BUG-0001.

func TestYAMLDecodeValue_TopLevelSequence(t *testing.T) {
	t.Parallel()
	src := "- 1\n- 2\n- 3\n"
	v, err := yamlfmt.DecodeValue(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Kind != omap.KindSeq {
		t.Fatalf("kind=%v want Seq", v.Kind)
	}
	if len(v.Seq) != 3 {
		t.Fatalf("seq len=%d want 3", len(v.Seq))
	}

	var buf bytes.Buffer
	if err := yamlfmt.EncodeValue(&buf, v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if buf.String() != src {
		t.Fatalf("round-trip: got %q want %q", buf.String(), src)
	}
}

func TestYAMLDecodeValue_TopLevelScalar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		kind omap.Kind
	}{
		{"string", "hello\n", omap.KindStr},
		{"int", "42\n", omap.KindNum},
		{"float", "3.14\n", omap.KindNum},
		{"bool_true", "true\n", omap.KindBool},
		{"bool_false", "false\n", omap.KindBool},
		{"null", "null\n", omap.KindNull},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			v, err := yamlfmt.DecodeValue(strings.NewReader(c.src))
			if err != nil {
				t.Fatalf("decode %q: %v", c.src, err)
			}
			if v.Kind != c.kind {
				t.Fatalf("kind=%v want %v", v.Kind, c.kind)
			}
			var buf bytes.Buffer
			if err := yamlfmt.EncodeValue(&buf, v); err != nil {
				t.Fatalf("encode: %v", err)
			}
			if buf.String() != c.src {
				t.Fatalf("round-trip: got %q want %q", buf.String(), c.src)
			}
		})
	}
}

func TestYAMLDecodeValue_TopLevelMappingStillWorks(t *testing.T) {
	t.Parallel()
	src := "a: 1\nb: 2\n"
	v, err := yamlfmt.DecodeValue(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Kind != omap.KindMap {
		t.Fatalf("kind=%v want Map", v.Kind)
	}
	var buf bytes.Buffer
	if err := yamlfmt.EncodeValue(&buf, v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if buf.String() != src {
		t.Fatalf("round-trip: got %q want %q", buf.String(), src)
	}
}
