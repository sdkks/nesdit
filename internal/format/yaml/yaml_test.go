package yaml_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/omap/omaptest"

	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
)

func TestRoundTrip_YAML(t *testing.T) {
	t.Parallel()
	for _, c := range omaptest.RoundTripCorpus() {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			if _, skip := c.FormatSkip["yaml"]; skip {
				t.Skipf("skip: %s", c.FormatSkip["yaml"])
			}
			var buf bytes.Buffer
			if err := yamlfmt.Encode(&buf, c.Doc); err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := yamlfmt.Decode(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("decode(%q): %v", buf.String(), err)
			}
			if ok, why := omaptest.EqualDocs(c.Doc, got); !ok {
				t.Fatalf("round-trip mismatch: %s\nencoded=\n%s", why, buf.String())
			}
			// Second round — stable.
			var buf2 bytes.Buffer
			if err := yamlfmt.Encode(&buf2, got); err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			got2, err := yamlfmt.Decode(bytes.NewReader(buf2.Bytes()))
			if err != nil {
				t.Fatalf("re-decode: %v", err)
			}
			if ok, why := omaptest.EqualDocs(c.Doc, got2); !ok {
				t.Fatalf("second-round mismatch: %s", why)
			}
		})
	}
}

func TestYAML_PreservesInsertionOrder(t *testing.T) {
	t.Parallel()
	src := "zulu: 1\nalpha: 2\nmike: 3\n"
	d, err := yamlfmt.Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := d.Keys()
	want := []string{"zulu", "alpha", "mike"}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("keys=%v want %v", got, want)
		}
	}
}

func TestYAML_StrTagPreserved(t *testing.T) {
	t.Parallel()
	// Encoding a string tagged !!str with a timestamp-like body must NOT
	// re-decode as !!timestamp on the round-trip.
	d := omap.New()
	d.Set("s", omap.StrValueTagged("2026-04-26T10:22:04Z", "!!str"))
	var buf bytes.Buffer
	if err := yamlfmt.Encode(&buf, d); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := yamlfmt.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, ok := got.Get("s")
	if !ok {
		t.Fatal("missing s")
	}
	if v.Kind != omap.KindStr {
		t.Fatalf("kind=%v want Str", v.Kind)
	}
	if v.Str != "2026-04-26T10:22:04Z" {
		t.Fatalf("str=%q", v.Str)
	}
	// Tag should be !!str (explicit) after round-trip.
	if v.Tag != "!!str" {
		t.Fatalf("Tag=%q want !!str (tag was lost on round-trip)", v.Tag)
	}
}

// TestYAML_DateOnlyStrTagPreserved covers a Code Reviewer finding: a bare
// !!str-tagged date-only string like "2026-04-26" (no 'T', no ':') was being
// emitted plain because needsQuoting required both a dash and a colon/'T'.
// yaml.v3's resolver then re-inferred it as !!timestamp on decode, losing
// the explicit !!str tag.
func TestYAML_DateOnlyStrTagPreserved(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("when", omap.StrValueTagged("2026-04-26", "!!str"))
	var buf bytes.Buffer
	if err := yamlfmt.Encode(&buf, d); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := yamlfmt.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, ok := got.Get("when")
	if !ok {
		t.Fatal("missing when")
	}
	if v.Kind != omap.KindStr {
		t.Fatalf("kind=%v want Str; encoded=%q", v.Kind, buf.String())
	}
	if v.Str != "2026-04-26" {
		t.Fatalf("str=%q encoded=%q", v.Str, buf.String())
	}
	if v.Tag != "!!str" {
		t.Fatalf("Tag=%q want !!str — tag was lost (encoded=%q)", v.Tag, buf.String())
	}
}

// TestYAML_IntLookingStrTagPreserved confirms the fix generalises beyond the
// date-only case: an int-looking string tagged !!str must not round-trip as
// !!int.
func TestYAML_IntLookingStrTagPreserved(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("zip", omap.StrValueTagged("42", "!!str"))
	var buf bytes.Buffer
	if err := yamlfmt.Encode(&buf, d); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := yamlfmt.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, ok := got.Get("zip")
	if !ok {
		t.Fatal("missing zip")
	}
	if v.Kind != omap.KindStr {
		t.Fatalf("kind=%v want Str; encoded=%q", v.Kind, buf.String())
	}
	if v.Str != "42" {
		t.Fatalf("str=%q encoded=%q", v.Str, buf.String())
	}
	if v.Tag != "!!str" {
		t.Fatalf("Tag=%q want !!str (encoded=%q)", v.Tag, buf.String())
	}
}

func TestYAML_IntPrecisionPreserved(t *testing.T) {
	t.Parallel()
	src := "big: 9007199254740993\n"
	d, err := yamlfmt.Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, _ := d.Get("big")
	if v.Num.String() != "9007199254740993" {
		t.Fatalf("big=%q want exact", v.Num.String())
	}
}

// TestDecodeValue_TrailingDocumentRejected verifies that DecodeValue returns an
// error when the input contains more than one YAML document. This mirrors the
// equivalent check in json.DecodeValueWithLimits (TASK-0017 / BUG-0001
// symmetry).
func TestDecodeValue_TrailingDocumentRejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "multi_doc",
			src:  "foo: bar\n---\nbaz: qux\n",
		},
		{
			name: "trailing_separator_only",
			src:  "foo: bar\n---\n",
		},
		{
			name: "three_documents",
			src:  "a: 1\n---\nb: 2\n---\nc: 3\n",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := yamlfmt.DecodeValue(strings.NewReader(tc.src))
			if err == nil {
				t.Fatalf("expected error for trailing content, got nil")
			}
			if !strings.Contains(err.Error(), "trailing content") {
				t.Fatalf("error %q does not mention 'trailing content'", err.Error())
			}
		})
	}
}

// TestDecodeValue_SingleDocAccepted verifies that a valid single-document YAML
// input is still accepted after the trailing-document check is in place.
func TestDecodeValue_SingleDocAccepted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
	}{
		{name: "plain_mapping", src: "foo: bar\n"},
		{name: "with_leading_separator", src: "---\nfoo: bar\n"},
		{name: "sequence_root", src: "- a\n- b\n"},
		{name: "scalar_root", src: "hello\n"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := yamlfmt.DecodeValue(strings.NewReader(tc.src))
			if err != nil {
				t.Fatalf("unexpected error for valid single-doc %q: %v", tc.src, err)
			}
		})
	}
}
