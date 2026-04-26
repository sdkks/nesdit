package toml_test

import (
	"bytes"
	"strings"
	"testing"

	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	"github.com/sdkks/nesdit/internal/omap"
)

// BUG-0001 regression: TOML's top-level-table constraint MUST be preserved.
// Unlike JSON and YAML, TOML's spec mandates that the root be a table — so
// DecodeValue/EncodeValue on TOML should either accept a map or reject with
// a clear error referencing the format's constraint.

func TestTOMLDecodeValue_TopLevelTableStillWorks(t *testing.T) {
	t.Parallel()
	src := `a = 1
b = "two"
`
	v, err := tomlfmt.DecodeValue(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v.Kind != omap.KindMap {
		t.Fatalf("kind=%v want Map", v.Kind)
	}
	var buf bytes.Buffer
	if err := tomlfmt.EncodeValue(&buf, v); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if buf.String() != src {
		t.Fatalf("round-trip: got %q want %q", buf.String(), src)
	}
}

func TestTOMLEncodeValue_RejectsTopLevelArray(t *testing.T) {
	t.Parallel()
	v := omap.SeqValue(omap.IntValue(1), omap.IntValue(2))
	var buf bytes.Buffer
	err := tomlfmt.EncodeValue(&buf, v)
	if err == nil {
		t.Fatalf("expected error for TOML top-level array, got nil (output=%q)", buf.String())
	}
	// Error should reference TOML's table-only constraint clearly.
	if !strings.Contains(err.Error(), "toml") {
		t.Fatalf("error %q should mention toml", err.Error())
	}
	if !strings.Contains(err.Error(), "table") && !strings.Contains(err.Error(), "top-level") {
		t.Fatalf("error %q should mention table/top-level constraint", err.Error())
	}
}

func TestTOMLEncodeValue_RejectsTopLevelScalar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		v    omap.Value
	}{
		{"string", omap.StrValue("hi")},
		{"int", omap.IntValue(1)},
		{"bool", omap.BoolValue(true)},
		{"null", omap.NullValue()},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			err := tomlfmt.EncodeValue(&buf, c.v)
			if err == nil {
				t.Fatalf("expected error for TOML top-level %s, got nil", c.name)
			}
		})
	}
}
