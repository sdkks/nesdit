package toml_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/omap/omaptest"

	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
)

func TestRoundTrip_TOML(t *testing.T) {
	t.Parallel()
	for _, c := range omaptest.RoundTripCorpus() {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			if _, skip := c.FormatSkip["toml"]; skip {
				t.Skipf("skip: %s", c.FormatSkip["toml"])
			}
			var buf bytes.Buffer
			if err := tomlfmt.Encode(&buf, c.Doc); err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := tomlfmt.Decode(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("decode(%q): %v", buf.String(), err)
			}
			if ok, why := omaptest.EqualDocs(c.Doc, got); !ok {
				t.Fatalf("round-trip mismatch: %s\nencoded=\n%s", why, buf.String())
			}
			// Stable second round.
			var buf2 bytes.Buffer
			if err := tomlfmt.Encode(&buf2, got); err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			got2, err := tomlfmt.Decode(bytes.NewReader(buf2.Bytes()))
			if err != nil {
				t.Fatalf("re-decode: %v", err)
			}
			if ok, why := omaptest.EqualDocs(c.Doc, got2); !ok {
				t.Fatalf("second-round mismatch: %s", why)
			}
			// Encoders should be deterministic — byte-identical second output.
			if !bytes.Equal(buf.Bytes(), buf2.Bytes()) {
				t.Fatalf("encoder non-deterministic:\nfirst:\n%s\nsecond:\n%s", buf.String(), buf2.String())
			}
		})
	}
}

func TestTOML_PreservesInsertionOrder(t *testing.T) {
	t.Parallel()
	src := "zulu = 1\nalpha = 2\nmike = 3\n"
	d, err := tomlfmt.Decode(strings.NewReader(src))
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

func TestTOML_IntPrecisionPreserved(t *testing.T) {
	t.Parallel()
	src := "big = 9007199254740993\n"
	d, err := tomlfmt.Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, _ := d.Get("big")
	if v.Num.String() != "9007199254740993" {
		t.Fatalf("big=%q want exact", v.Num.String())
	}
	// And round-trip keeps it exact.
	d2 := omap.New()
	d2.Set("n", omap.NumValue(json.Number("9007199254740993")))
	var buf bytes.Buffer
	if err := tomlfmt.Encode(&buf, d2); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("9007199254740993")) {
		t.Fatalf("int not verbatim: %s", buf.String())
	}
}

func TestTOML_DecodeSectionTables(t *testing.T) {
	t.Parallel()
	// Sections and dotted keys are idiomatic TOML input even if our encoder
	// prefers inline tables. Decoding must handle them.
	src := `
top = 1

[a]
x = 10
y = 20

[[items]]
name = "first"

[[items]]
name = "second"
`
	d, err := tomlfmt.Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	keys := d.Keys()
	if len(keys) != 3 || keys[0] != "top" || keys[1] != "a" || keys[2] != "items" {
		t.Fatalf("keys=%v", keys)
	}
	a, _ := d.Get("a")
	if a.Kind != omap.KindMap {
		t.Fatalf("a.Kind=%v", a.Kind)
	}
	if a.Map.Keys()[0] != "x" || a.Map.Keys()[1] != "y" {
		t.Fatalf("a.keys=%v", a.Map.Keys())
	}
	items, _ := d.Get("items")
	if items.Kind != omap.KindSeq || len(items.Seq) != 2 {
		t.Fatalf("items=%+v", items)
	}
}

func TestTOML_NestedTables(t *testing.T) {
	t.Parallel()
	inner := omap.New()
	inner.Set("a", omap.IntValue(1))
	inner.Set("b", omap.StrValue("s"))
	d := omap.New()
	d.Set("root_key", omap.IntValue(5))
	d.Set("nested", omap.MapValue(inner))
	var buf bytes.Buffer
	if err := tomlfmt.Encode(&buf, d); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := tomlfmt.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ok, why := omaptest.EqualDocs(d, got); !ok {
		t.Fatalf("mismatch: %s\nenc:\n%s", why, buf.String())
	}
}
