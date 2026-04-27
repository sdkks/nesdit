package json_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/omap/omaptest"
)

func TestRoundTrip_JSON(t *testing.T) {
	t.Parallel()
	for _, c := range omaptest.RoundTripCorpus() {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			if _, skip := c.FormatSkip["json"]; skip {
				t.Skipf("skip: %s", c.FormatSkip["json"])
			}

			// encode → decode → assert structural equality incl. key order.
			var buf bytes.Buffer
			if err := jsonfmt.Encode(&buf, c.Doc); err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := jsonfmt.Decode(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatalf("decode(%q): %v", buf.String(), err)
			}
			if ok, why := omaptest.EqualDocs(c.Doc, got); !ok {
				t.Fatalf("round-trip mismatch: %s\nencoded=%s", why, buf.String())
			}

			// decode → encode → decode → still equal.
			var buf2 bytes.Buffer
			if err := jsonfmt.Encode(&buf2, got); err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			got2, err := jsonfmt.Decode(bytes.NewReader(buf2.Bytes()))
			if err != nil {
				t.Fatalf("re-decode: %v", err)
			}
			if ok, why := omaptest.EqualDocs(c.Doc, got2); !ok {
				t.Fatalf("second-round mismatch: %s", why)
			}
		})
	}
}

func TestDecode_UsesJSONNumber(t *testing.T) {
	t.Parallel()
	const big = "9007199254740993"
	src := `{"n": ` + big + `, "small": 42}`
	d, err := jsonfmt.Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, ok := d.Get("n")
	if !ok {
		t.Fatal("missing n")
	}
	if v.Kind != omap.KindNum {
		t.Fatalf("n kind=%v want Num", v.Kind)
	}
	if v.Num.String() != big {
		t.Fatalf("n=%q want %q (float64 would coerce to 9007199254740992)", v.Num.String(), big)
	}
}

func TestEncode_PreservesInsertionOrder(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("zulu", omap.IntValue(1))
	d.Set("alpha", omap.IntValue(2))
	d.Set("mike", omap.IntValue(3))
	var buf bytes.Buffer
	if err := jsonfmt.Encode(&buf, d); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Find positions of each key in the output.
	s := buf.String()
	zi := strings.Index(s, `"zulu"`)
	ai := strings.Index(s, `"alpha"`)
	mi := strings.Index(s, `"mike"`)
	if zi < 0 || ai < 0 || mi < 0 {
		t.Fatalf("keys missing in output: %q", s)
	}
	if zi >= ai || ai >= mi {
		t.Fatalf("keys out of insertion order in %q: zulu=%d alpha=%d mike=%d", s, zi, ai, mi)
	}
}

func TestDecode_Nested(t *testing.T) {
	t.Parallel()
	src := `{"a": {"b": {"c": [1, "two", null, true]}}, "z": 0}`
	d, err := jsonfmt.Decode(strings.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	keys := d.Keys()
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "z" {
		t.Fatalf("root keys=%v", keys)
	}
}

func TestEncode_JSONNumberIsVerbatim(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("n", omap.NumValue(json.Number("9007199254740993")))
	var buf bytes.Buffer
	if err := jsonfmt.Encode(&buf, d); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("9007199254740993")) {
		t.Fatalf("output missing exact numeric: %s", buf.String())
	}
	if bytes.Contains(buf.Bytes(), []byte("9007199254740992")) {
		t.Fatalf("float coerced output: %s", buf.String())
	}
}
