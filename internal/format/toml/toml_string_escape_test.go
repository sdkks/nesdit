package toml_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	"github.com/sdkks/nesdit/internal/omap"
)

// TestTOML_encodeTOMLString_Escapes exercises every escape branch in
// encodeTOMLString: backslash, double-quote, newline, carriage-return, tab,
// backspace, form-feed, other control characters below U+0020, unicode
// literals beyond ASCII, and plain ASCII strings that require no escaping.
//
// Each case is a round-trip: encode an omap.Doc with the string value, then
// decode it and assert the string is identical to the original. This proves
// the encoder emits syntactically valid TOML and the decoder round-trips it
// faithfully.
func TestTOML_encodeTOMLString_Escapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		// want is the expected TOML-quoted form. If empty only round-trip
		// fidelity is checked.
		want string
	}{
		// Backslash and double-quote — the two most critical escapes.
		{
			name:  "backslash",
			input: `C:\Users\nesdit`,
			want:  `"C:\\Users\\nesdit"`,
		},
		{
			name:  "double_quote",
			input: `say "hello"`,
			want:  `"say \"hello\""`,
		},

		// Standard C-style control character escapes.
		{
			name:  "newline",
			input: "line1\nline2",
			want:  `"line1\nline2"`,
		},
		{
			name:  "carriage_return",
			input: "line1\rline2",
			want:  `"line1\rline2"`,
		},
		{
			name:  "tab",
			input: "col1\tcol2",
			want:  `"col1\tcol2"`,
		},
		{
			name:  "backspace",
			input: "abc\bdef",
			want:  `"abc\bdef"`,
		},
		{
			name:  "form_feed",
			input: "page1\fpage2",
			want:  `"page1\fpage2"`,
		},

		// Other ASCII control characters below U+0020 are emitted as \uXXXX.
		// The encoder uses fmt.Fprintf(&b, `\u%04X`, r) for these. Only
		// round-trip fidelity is asserted here (want="" skips wire check)
		// because embedding literal control bytes in a Go test source string
		// makes the expected value unreadable.
		{name: "nul_byte", input: "a\x00b"},
		{name: "ctrl_soh", input: "a\x01b"},
		{name: "ctrl_us", input: "a\x1fb"},

		// Unicode scalars beyond ASCII must pass through unescaped (TOML spec
		// mandates valid UTF-8 and permits any Unicode scalar value in basic
		// strings). The encoder uses b.WriteRune for these.
		{
			name:  "unicode_bmp",
			input: "café",
			want:  `"café"`,
		},
		{
			name:  "unicode_cjk",
			input: "中文",
			want:  `"中文"`,
		},
		{
			name:  "unicode_emoji",
			input: "hi \U0001F600",
			want:  "\"hi \U0001F600\"",
		},

		// Mixed: combination of escapes and plain runes.
		{
			name:  "mixed",
			input: "a\\b\ncd\t",
			want:  `"a\\b\ncd\t"`,
		},

		// Empty string.
		{
			name:  "empty",
			input: "",
			want:  `""`,
		},

		// Plain ASCII — no escaping needed; still wrapped in double-quotes.
		{
			name:  "plain_ascii",
			input: "hello world",
			want:  `"hello world"`,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			d := omap.New()
			d.Set("v", omap.StrValue(c.input))
			var buf bytes.Buffer
			if err := tomlfmt.Encode(&buf, d); err != nil {
				t.Fatalf("encode: %v", err)
			}
			encoded := buf.String()

			// Verify exact wire representation when specified.
			if c.want != "" {
				expectedLine := "v = " + c.want + "\n"
				if encoded != expectedLine {
					t.Fatalf("encoded=%q want %q", encoded, expectedLine)
				}
			}

			// Round-trip: decode the encoded form and verify value is preserved.
			got, err := tomlfmt.Decode(strings.NewReader(encoded))
			if err != nil {
				t.Fatalf("decode(%q): %v", encoded, err)
			}
			v, ok := got.Get("v")
			if !ok {
				t.Fatal("missing key v after decode")
			}
			if v.Kind != omap.KindStr {
				t.Fatalf("kind=%v want KindStr", v.Kind)
			}
			if v.Str != c.input {
				t.Fatalf("str=%q want %q", v.Str, c.input)
			}
		})
	}
}

// TestTOML_encodeTOMLString_QuotedKey exercises the encodeTOMLString path
// reached via encodeKey when a TOML key contains characters that require
// quoting (spaces, dots, special chars).
func TestTOML_encodeTOMLString_QuotedKey(t *testing.T) {
	t.Parallel()

	keys := []struct {
		key  string
		want string // expected quoted key representation in output
	}{
		{"key with spaces", `"key with spaces"`},
		{"key.with.dots", `"key.with.dots"`},
		{"key\twith\ttabs", `"key\twith\ttabs"`},
		{`key"with"quotes`, `"key\"with\"quotes"`},
		{`key\backslash`, `"key\\backslash"`},
	}

	for _, kc := range keys {
		kc := kc
		t.Run(kc.key, func(t *testing.T) {
			t.Parallel()
			d := omap.New()
			d.Set(kc.key, omap.IntValue(1))
			var buf bytes.Buffer
			if err := tomlfmt.Encode(&buf, d); err != nil {
				t.Fatalf("encode: %v", err)
			}
			encoded := buf.String()
			if !strings.Contains(encoded, kc.want) {
				t.Fatalf("encoded=%q does not contain expected key repr %q", encoded, kc.want)
			}
			// Round-trip.
			got, err := tomlfmt.Decode(strings.NewReader(encoded))
			if err != nil {
				t.Fatalf("decode(%q): %v", encoded, err)
			}
			if _, ok := got.Get(kc.key); !ok {
				t.Fatalf("key %q missing after decode; encoded=%q", kc.key, encoded)
			}
		})
	}
}

// TestTOML_encodeTOMLString_SpecialStringsRoundTrip is a property-style test
// that checks a wide range of special strings round-trip correctly through
// TOML encode→decode without data loss.
func TestTOML_encodeTOMLString_SpecialStringsRoundTrip(t *testing.T) {
	t.Parallel()

	specials := []string{
		// All standard TOML named escapes combined.
		"\\\"\n\r\t\b\f",
		// Every code point below U+0020 without a named TOML escape.
		"\x00\x01\x02\x03\x04\x05\x06\x07" +
			"\x0b\x0e\x0f" +
			"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f",
		// Multi-byte UTF-8.
		"日本語テスト", // 日本語テスト
		// Mixed.
		"path: C:\\Users\\bob\n\tindented",
	}

	for i, s := range specials {
		s := s
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			t.Parallel()
			d := omap.New()
			d.Set("s", omap.StrValue(s))
			var buf bytes.Buffer
			if err := tomlfmt.Encode(&buf, d); err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := tomlfmt.Decode(strings.NewReader(buf.String()))
			if err != nil {
				t.Fatalf("decode(%q): %v", buf.String(), err)
			}
			v, ok := got.Get("s")
			if !ok {
				t.Fatal("missing s")
			}
			if v.Kind != omap.KindStr {
				t.Fatalf("kind=%v want KindStr", v.Kind)
			}
			if v.Str != s {
				t.Fatalf("round-trip mismatch:\ngot  %q\nwant %q\nencoded=%q", v.Str, s, buf.String())
			}
		})
	}
}
