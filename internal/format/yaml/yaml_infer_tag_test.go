package yaml_test

import (
	"bytes"
	"testing"

	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
	"github.com/sdkks/nesdit/internal/omap"
)

// TestYAML_NullLookingStrTagPreserved exercises the nil branch in
// inferScalarTag: a !!str-tagged string whose plain representation yaml.v3
// would resolve to !!null ("null") must be force-quoted on encode so the
// round-trip does not collapse the string to a null value.
func TestYAML_NullLookingStrTagPreserved(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("v", omap.StrValueTagged("null", "!!str"))
	var buf bytes.Buffer
	if err := yamlfmt.Encode(&buf, d); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := yamlfmt.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("decode(%q): %v", buf.String(), err)
	}
	v, ok := got.Get("v")
	if !ok {
		t.Fatal("missing v")
	}
	if v.Kind != omap.KindStr {
		t.Fatalf("kind=%v want KindStr (encoded=%q)", v.Kind, buf.String())
	}
	if v.Str != "null" {
		t.Fatalf("str=%q want %q (encoded=%q)", v.Str, "null", buf.String())
	}
}

// TestYAML_BoolLookingStrTagPreserved exercises the bool branch in
// inferScalarTag: a !!str-tagged "true"/"false" string must be force-quoted
// so the round-trip does not collapse it to a bool value.
func TestYAML_BoolLookingStrTagPreserved(t *testing.T) {
	t.Parallel()
	cases := []string{"true", "false", "True", "False", "TRUE", "FALSE"}
	for _, val := range cases {
		val := val
		t.Run(val, func(t *testing.T) {
			t.Parallel()
			d := omap.New()
			d.Set("v", omap.StrValueTagged(val, "!!str"))
			var buf bytes.Buffer
			if err := yamlfmt.Encode(&buf, d); err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := yamlfmt.Decode(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("decode(%q): %v", buf.String(), err)
			}
			v, ok := got.Get("v")
			if !ok {
				t.Fatal("missing v")
			}
			if v.Kind != omap.KindStr {
				t.Fatalf("kind=%v want KindStr for %q (encoded=%q)", v.Kind, val, buf.String())
			}
			if v.Str != val {
				t.Fatalf("str=%q want %q (encoded=%q)", v.Str, val, buf.String())
			}
		})
	}
}

// TestYAML_FloatLookingStrTagPreserved exercises the float64 branch in
// inferScalarTag: a !!str-tagged numeric-looking string (e.g. "3.14") must
// be force-quoted so the round-trip does not collapse it to a number.
func TestYAML_FloatLookingStrTagPreserved(t *testing.T) {
	t.Parallel()
	cases := []string{"3.14", "0.0", "-1.5", "1e10", ".inf", "-.inf", ".nan"}
	for _, val := range cases {
		val := val
		t.Run(val, func(t *testing.T) {
			t.Parallel()
			d := omap.New()
			d.Set("v", omap.StrValueTagged(val, "!!str"))
			var buf bytes.Buffer
			if err := yamlfmt.Encode(&buf, d); err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := yamlfmt.Decode(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("decode(%q): %v", buf.String(), err)
			}
			v, ok := got.Get("v")
			if !ok {
				t.Fatal("missing v")
			}
			if v.Kind != omap.KindStr {
				t.Fatalf("kind=%v want KindStr for %q (encoded=%q)", v.Kind, val, buf.String())
			}
			if v.Str != val {
				t.Fatalf("str=%q want %q (encoded=%q)", v.Str, val, buf.String())
			}
		})
	}
}
