package toml_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/omap/omaptest"

	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
)

// encodePretty is a helper that encodes d with pretty mode enabled.
func encodePretty(d *omap.Doc) (string, error) {
	var buf bytes.Buffer
	if err := tomlfmt.EncodeWithOptions(&buf, d, tomlfmt.EncodeOptions{Pretty: true}); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// encodeCompact is a helper that encodes d with compact (default) mode.
func encodeCompact(d *omap.Doc) (string, error) {
	var buf bytes.Buffer
	if err := tomlfmt.Encode(&buf, d); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// TestPretty_FlatDoc verifies that compact and pretty output differ for a
// flat doc with >1 key, and that pretty inserts blank lines between top-level
// keys (T1).
func TestPretty_FlatDoc(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("a", omap.StrValue("hello"))
	d.Set("b", omap.IntValue(42))
	d.Set("c", omap.BoolValue(true))

	compact, err := encodeCompact(d)
	if err != nil {
		t.Fatalf("compact encode: %v", err)
	}
	pretty, err := encodePretty(d)
	if err != nil {
		t.Fatalf("pretty encode: %v", err)
	}

	// They must differ (pretty has blank lines).
	if compact == pretty {
		t.Fatal("expected compact and pretty to differ for a 3-key flat doc")
	}

	want := "a = \"hello\"\n\nb = 42\n\nc = true\n"
	if pretty != want {
		t.Fatalf("pretty output mismatch:\ngot:\n%q\nwant:\n%q", pretty, want)
	}

	// Compact must have no blank lines.
	wantCompact := "a = \"hello\"\nb = 42\nc = true\n"
	if compact != wantCompact {
		t.Fatalf("compact output mismatch:\ngot:\n%q\nwant:\n%q", compact, wantCompact)
	}
}

// TestPretty_ArrayExpansionThreshold verifies the >1 element rule (T2).
func TestPretty_ArrayExpansionThreshold(t *testing.T) {
	t.Parallel()

	// 0-element array: stays inline.
	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		d := omap.New()
		d.Set("k", omap.Value{Kind: omap.KindSeq, Seq: nil})
		got, err := encodePretty(d)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		want := "k = []\n"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	// 1-element array: stays inline.
	t.Run("single", func(t *testing.T) {
		t.Parallel()
		d := omap.New()
		d.Set("k", omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{omap.StrValue("x")}})
		got, err := encodePretty(d)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		want := "k = [\"x\"]\n"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	// 2-element array: expands to multi-line with trailing comma.
	t.Run("two", func(t *testing.T) {
		t.Parallel()
		d := omap.New()
		d.Set("k", omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{
			omap.StrValue("a"),
			omap.StrValue("b"),
		}})
		got, err := encodePretty(d)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		want := "k = [\n  \"a\",\n  \"b\",\n]\n"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	// 3-element array: same expansion pattern.
	t.Run("three", func(t *testing.T) {
		t.Parallel()
		d := omap.New()
		d.Set("k", omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{
			omap.StrValue("a"),
			omap.StrValue("b"),
			omap.StrValue("c"),
		}})
		got, err := encodePretty(d)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		want := "k = [\n  \"a\",\n  \"b\",\n  \"c\",\n]\n"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})
}

// TestPretty_InlineTableStaysInline verifies that inline tables remain on a
// single line in pretty mode (TOML 1.0 does not support multi-line inline
// tables; only TOML 1.1 does). The round-trip must succeed (T3).
func TestPretty_InlineTableStaysInline(t *testing.T) {
	t.Parallel()

	// Empty map stays inline ({}).
	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		inner := omap.New()
		d := omap.New()
		d.Set("m", omap.MapValue(inner))
		got, err := encodePretty(d)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		want := "m = {}\n"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	// 1-key map stays inline ({k = v}).
	t.Run("single_key", func(t *testing.T) {
		t.Parallel()
		inner := omap.New()
		inner.Set("x", omap.IntValue(1))
		d := omap.New()
		d.Set("m", omap.MapValue(inner))
		got, err := encodePretty(d)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		want := "m = {x = 1}\n"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	// 2-key map stays inline in TOML 1.0 mode (multi-line inline tables
	// are only valid in TOML 1.1, which go-toml/v2 does not support by default).
	t.Run("two_keys_stay_inline", func(t *testing.T) {
		t.Parallel()
		inner := omap.New()
		inner.Set("x", omap.IntValue(1))
		inner.Set("y", omap.IntValue(2))
		d := omap.New()
		d.Set("m", omap.MapValue(inner))
		got, err := encodePretty(d)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		// Must stay inline to be valid TOML 1.0 and round-trip correctly.
		want := "m = {x = 1, y = 2}\n"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
		// Round-trip must succeed.
		d2, decErr := tomlfmt.Decode(bytes.NewReader([]byte(got)))
		if decErr != nil {
			t.Fatalf("decode round-trip failed: %v", decErr)
		}
		if ok, why := omaptest.EqualDocs(d, d2); !ok {
			t.Fatalf("round-trip mismatch: %s", why)
		}
	})
}

// TestPretty_NestedDepth verifies that a top-level array containing arrays
// uses depth-aware indentation (T4). Maps stay inline (TOML 1.0 constraint);
// the depth parameter is still threaded so nested arrays inside maps expand
// correctly at the right indentation level within the inline-table syntax.
func TestPretty_NestedDepth(t *testing.T) {
	t.Parallel()
	// A top-level array of 3 elements, where each element is itself an array
	// of 2 items. The outer array sits at depth 0, so elements are indented
	// by 2 spaces. The inner arrays (depth 1) also expand with 4-space indent.
	innerSeq := func(a, b int64) omap.Value {
		return omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{
			omap.IntValue(a),
			omap.IntValue(b),
		}}
	}
	d := omap.New()
	d.Set("matrix", omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{
		innerSeq(1, 2),
		innerSeq(3, 4),
		innerSeq(5, 6),
	}})
	got, err := encodePretty(d)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Outer array: depth=0 → child indent=2 spaces, close indent="".
	// Inner arrays: depth=1 → child indent=4 spaces, close indent=2 spaces.
	want := "matrix = [\n  [\n    1,\n    2,\n  ],\n  [\n    3,\n    4,\n  ],\n  [\n    5,\n    6,\n  ],\n]\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestPretty_RoundTrip verifies that pretty output round-trips correctly
// through the TOML decoder and that a second pretty-encode is byte-identical
// (FR-P8 / T5).
func TestPretty_RoundTrip(t *testing.T) {
	t.Parallel()
	for _, c := range omaptest.RoundTripCorpus() {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			if _, skip := c.FormatSkip["toml"]; skip {
				t.Skipf("skip: %s", c.FormatSkip["toml"])
			}

			// First pretty encode.
			var buf1 bytes.Buffer
			if err := tomlfmt.EncodeWithOptions(&buf1, c.Doc, tomlfmt.EncodeOptions{Pretty: true}); err != nil {
				t.Fatalf("encode1: %v", err)
			}

			// Decode.
			doc2, err := tomlfmt.Decode(bytes.NewReader(buf1.Bytes()))
			if err != nil {
				t.Fatalf("decode after first pretty encode: %v\nencoded:\n%s", err, buf1.String())
			}
			// Values must be identical.
			if ok, why := omaptest.EqualDocs(c.Doc, doc2); !ok {
				t.Fatalf("round-trip value mismatch: %s\nencoded:\n%s", why, buf1.String())
			}

			// Second pretty encode must be byte-identical.
			var buf2 bytes.Buffer
			if err := tomlfmt.EncodeWithOptions(&buf2, doc2, tomlfmt.EncodeOptions{Pretty: true}); err != nil {
				t.Fatalf("encode2: %v", err)
			}
			if !bytes.Equal(buf1.Bytes(), buf2.Bytes()) {
				t.Fatalf("pretty encode is not idempotent:\nfirst:\n%s\nsecond:\n%s", buf1.String(), buf2.String())
			}
		})
	}
}

// TestPretty_KeyOrder verifies that pretty mode preserves insertion order (T6).
func TestPretty_KeyOrder(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("zulu", omap.IntValue(1))
	d.Set("alpha", omap.IntValue(2))
	d.Set("mike", omap.IntValue(3))

	got, err := encodePretty(d)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Keys must appear in insertion order (zulu, alpha, mike), not sorted.
	want := "zulu = 1\n\nalpha = 2\n\nmike = 3\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

// TestPretty_CompactUnchanged verifies that compact mode output is byte-for-byte
// identical before and after this story's changes (regression guard, T7).
func TestPretty_CompactUnchanged(t *testing.T) {
	t.Parallel()
	// Re-run the golden compact test as an explicit assertion.
	inputPath := filepath.Join("testdata", "canonical.toml")
	goldenPath := filepath.Join("testdata", "canonical.golden.toml")

	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}
	doc, err := tomlfmt.Decode(bytes.NewReader(inputBytes))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var buf bytes.Buffer
	if err := tomlfmt.Encode(&buf, doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("compact output changed:\ngot:\n%s\nwant:\n%s", buf.String(), string(want))
	}
}

// TestGolden_TOML_Pretty decodes testdata/canonical.toml, pretty-encodes it,
// and byte-compares against testdata/canonical.pretty.golden.toml.
// Run UPDATE_GOLDEN=1 go test to regenerate the golden file.
func TestGolden_TOML_Pretty(t *testing.T) {
	t.Parallel()

	inputPath := filepath.Join("testdata", "canonical.toml")
	goldenPath := filepath.Join("testdata", "canonical.pretty.golden.toml")

	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}
	doc, err := tomlfmt.Decode(bytes.NewReader(inputBytes))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	var buf bytes.Buffer
	if err := tomlfmt.EncodeWithOptions(&buf, doc, tomlfmt.EncodeOptions{Pretty: true}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := buf.Bytes()

	if shouldUpdateGoldenTOML() {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated pretty golden: %s (%d bytes)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Bootstrap: write golden on first run.
			if writeErr := os.WriteFile(goldenPath, got, 0o644); writeErr != nil {
				t.Fatalf("bootstrap golden write: %v", writeErr)
			}
			t.Logf("bootstrapped pretty golden: %s", goldenPath)
			return
		}
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("pretty golden mismatch for %s:\ngot:\n%s\nwant:\n%s\n(run UPDATE_GOLDEN=1 go test to regenerate)",
			goldenPath, got, want)
	}

	// Second-round stability: pretty-encode → decode → pretty-encode must be byte-identical.
	doc2, err := tomlfmt.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("re-decode pretty golden: %v", err)
	}
	var buf2 bytes.Buffer
	if err := tomlfmt.EncodeWithOptions(&buf2, doc2, tomlfmt.EncodeOptions{Pretty: true}); err != nil {
		t.Fatalf("re-encode pretty golden: %v", err)
	}
	if !bytes.Equal(buf2.Bytes(), got) {
		t.Fatalf("pretty second-round non-idempotent:\nfirst:\n%s\nsecond:\n%s", string(got), buf2.String())
	}
}

// TestPretty_InlineTableRoundTrip verifies that inline tables (which stay on a
// single line in pretty mode for TOML 1.0 compatibility) round-trip correctly
// through the TOML decoder. This guards against the architect's must-fix: the
// output must be parseable by go-toml/v2 at runtime.
func TestPretty_InlineTableRoundTrip(t *testing.T) {
	t.Parallel()
	inner := omap.New()
	inner.Set("host", omap.StrValue("localhost"))
	inner.Set("port", omap.IntValue(5432))
	inner.Set("db", omap.StrValue("mydb"))
	d := omap.New()
	d.Set("database", omap.MapValue(inner))

	var buf bytes.Buffer
	if err := tomlfmt.EncodeWithOptions(&buf, d, tomlfmt.EncodeOptions{Pretty: true}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	encoded := buf.String()

	// Decode the pretty output; must not fail.
	d2, err := tomlfmt.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("decode pretty output failed: %v\noutput:\n%s", err, encoded)
	}

	// Values must be identical.
	if ok, why := omaptest.EqualDocs(d, d2); !ok {
		t.Fatalf("round-trip value mismatch: %s", why)
	}

	// Verify the output is a single-line inline table (TOML 1.0 compliant).
	// It must not contain a bare newline inside braces.
	if bytes.Contains(buf.Bytes(), []byte("{\n")) {
		t.Fatalf("inline table was expanded to multi-line (not TOML 1.0 compatible):\n%s", encoded)
	}
}
