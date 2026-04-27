package json_test

import (
	"bytes"
	"strings"
	"testing"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/omap"
)

// BUG-0001 regression: JSON decoder previously rejected any top-level value
// that was not an object. RFC 8259 permits any value at the top level. These
// tests exercise DecodeValue/EncodeValue — the top-level-agnostic API added
// to fix BUG-0001 — and confirm that arrays, strings, numbers, booleans, and
// nulls round-trip through the pipeline.

func TestDecodeValue_EmptyInput(t *testing.T) {
	t.Parallel()
	_, err := jsonfmt.DecodeValue(strings.NewReader(""))
	if err == nil {
		t.Fatal("expected non-nil error on empty input, got nil")
	}
}

func TestDecodeValue_TopLevelArray(t *testing.T) {
	t.Parallel()
	src := `[1,2,3]`
	v, err := jsonfmt.DecodeValue(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Kind != omap.KindSeq {
		t.Fatalf("top-level kind=%v want Seq", v.Kind)
	}
	if len(v.Seq) != 3 {
		t.Fatalf("seq len=%d want 3", len(v.Seq))
	}

	var buf bytes.Buffer
	if err := jsonfmt.EncodeValue(&buf, v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if got := buf.String(); got != src {
		t.Fatalf("round-trip: got %q want %q", got, src)
	}
}

func TestDecodeValue_TopLevelEmptyArray(t *testing.T) {
	t.Parallel()
	src := `[]`
	v, err := jsonfmt.DecodeValue(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Kind != omap.KindSeq {
		t.Fatalf("kind=%v want Seq", v.Kind)
	}
	if len(v.Seq) != 0 {
		t.Fatalf("seq len=%d want 0", len(v.Seq))
	}
	var buf bytes.Buffer
	if err := jsonfmt.EncodeValue(&buf, v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if buf.String() != src {
		t.Fatalf("round-trip: got %q want %q", buf.String(), src)
	}
}

func TestDecodeValue_TopLevelScalars(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
		kind omap.Kind
	}{
		{"string", `"hello"`, omap.KindStr},
		{"number", `42`, omap.KindNum},
		{"bigint", `9007199254740993`, omap.KindNum},
		{"float", `3.14`, omap.KindNum},
		{"true", `true`, omap.KindBool},
		{"false", `false`, omap.KindBool},
		{"null", `null`, omap.KindNull},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			v, err := jsonfmt.DecodeValue(strings.NewReader(c.src))
			if err != nil {
				t.Fatalf("decode %q: %v", c.src, err)
			}
			if v.Kind != c.kind {
				t.Fatalf("kind=%v want %v", v.Kind, c.kind)
			}
			var buf bytes.Buffer
			if err := jsonfmt.EncodeValue(&buf, v); err != nil {
				t.Fatalf("encode: %v", err)
			}
			if buf.String() != c.src {
				t.Fatalf("round-trip: got %q want %q", buf.String(), c.src)
			}
		})
	}
}

func TestDecodeValue_TopLevelObjectStillWorks(t *testing.T) {
	t.Parallel()
	src := `{"a":1,"b":2}`
	v, err := jsonfmt.DecodeValue(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Kind != omap.KindMap {
		t.Fatalf("kind=%v want Map", v.Kind)
	}
	var buf bytes.Buffer
	if err := jsonfmt.EncodeValue(&buf, v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if buf.String() != src {
		t.Fatalf("round-trip: got %q want %q", buf.String(), src)
	}
}

func TestDecodeValue_NestedArray(t *testing.T) {
	t.Parallel()
	src := `[{"x":1},{"y":2}]`
	v, err := jsonfmt.DecodeValue(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Kind != omap.KindSeq {
		t.Fatalf("kind=%v want Seq", v.Kind)
	}
	var buf bytes.Buffer
	if err := jsonfmt.EncodeValue(&buf, v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if buf.String() != src {
		t.Fatalf("round-trip: got %q want %q", buf.String(), src)
	}
}
