package toml_test

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/display"
	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	"github.com/sdkks/nesdit/internal/omap"
)

type tomlToken struct {
	role display.Role
	text string
}

func TestTOMLSemanticSpans(t *testing.T) {
	cases := []struct {
		source string
		tokens []tomlToken
	}{
		{`"" = ""
"quote\"\\\n" = "é🙂\u001b[32m"
"é🙂" = "x"
123 = -1.25e+2
true = true
m = {false = false, "123.4" = {"" = "null"}}
a = [{0 = 2}, {true = "true"}]
empty = []
map = {}
`, []tomlToken{
			{display.Key, `""`}, {display.String, `""`},
			{display.Key, `"quote\"\\\n"`}, {display.String, `"é🙂\u001B[32m"`},
			{display.Key, `"é🙂"`}, {display.String, `"x"`},
			{display.Key, "123"}, {display.Number, "-1.25e+2"},
			{display.Key, "true"}, {display.Bool, "true"},
			{display.Key, "m"}, {display.Key, "false"}, {display.Bool, "false"},
			{display.Key, `"123.4"`}, {display.Key, `""`}, {display.String, `"null"`},
			{display.Key, "a"}, {display.Key, "0"}, {display.Number, "2"},
			{display.Key, "true"}, {display.String, `"true"`},
			{display.Key, "empty"}, {display.Key, "map"},
		}},
		{"matrix = [[1, 2], [3, 4]]\n", []tomlToken{
			{display.Key, "matrix"}, {display.Number, "1"}, {display.Number, "2"}, {display.Number, "3"}, {display.Number, "4"},
		}},
		{"utc = 1979-05-27T07:32:00Z\noffset = 1979-05-27T00:32:00-07:00\nlocal = 1979-05-27T07:32:00\ndate = 1979-05-27\ntime = 07:32:00.123\nquoted = \"1979-05-27\"\n", []tomlToken{
			{display.Key, "utc"}, {display.Key, "offset"}, {display.Key, "local"}, {display.Key, "date"}, {display.Key, "time"},
			{display.Key, "quoted"}, {display.String, `"1979-05-27"`},
		}},
		{"", nil},
	}
	codes := map[display.Role]string{display.Key: "\x1b[1;39m", display.String: "\x1b[32m", display.Number: "\x1b[36m", display.Bool: "\x1b[35m"}
	for _, tc := range cases {
		for _, pretty := range []bool{false, true} {
			v, err := tomlfmt.DecodeValue(strings.NewReader(tc.source))
			if err != nil {
				t.Fatal(err)
			}
			opts := tomlfmt.EncodeOptions{Pretty: pretty}
			var plain, observed, nilObserved, mapEncoded bytes.Buffer
			if err := tomlfmt.EncodeValueWithOptions(&plain, v, opts); err != nil {
				t.Fatal(err)
			}
			if err := tomlfmt.EncodeValueObserved(&nilObserved, v, opts, nil); err != nil {
				t.Fatal(err)
			}
			if err := tomlfmt.EncodeWithOptions(&mapEncoded, v.Map, opts); err != nil {
				t.Fatal(err)
			}
			if !pretty {
				var legacy, legacyValue bytes.Buffer
				if err := tomlfmt.Encode(&legacy, v.Map); err != nil {
					t.Fatal(err)
				}
				if err := tomlfmt.EncodeValue(&legacyValue, v); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(plain.Bytes(), legacy.Bytes()) || !bytes.Equal(plain.Bytes(), legacyValue.Bytes()) {
					t.Fatal("default entrypoint parity")
				}
			}
			var spans []display.Span
			if err := tomlfmt.EncodeValueObserved(&observed, v, opts, func(s display.Span) { spans = append(spans, s) }); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(plain.Bytes(), observed.Bytes()) || !bytes.Equal(plain.Bytes(), nilObserved.Bytes()) || !bytes.Equal(plain.Bytes(), mapEncoded.Bytes()) {
				t.Fatal("observer changed serialization")
			}
			var tokens []tomlToken
			var expected bytes.Buffer
			end := 0
			for _, s := range spans {
				if s.Start < end || s.End <= s.Start || s.End > observed.Len() {
					t.Fatalf("invalid span %+v", s)
				}
				literal := observed.String()[s.Start:s.End]
				tokens = append(tokens, tomlToken{s.Role, literal})
				expected.Write(observed.Bytes()[end:s.Start])
				expected.WriteString(codes[s.Role])
				expected.WriteString(literal)
				expected.WriteString("\x1b[0m")
				end = s.End
			}
			expected.Write(observed.Bytes()[end:])
			if !reflect.DeepEqual(tokens, tc.tokens) {
				t.Fatalf("pretty=%v tokens=%#v want=%#v", pretty, tokens, tc.tokens)
			}
			var colored bytes.Buffer
			if err := display.Write(&colored, observed.Bytes(), spans); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(colored.Bytes(), expected.Bytes()) {
				t.Fatalf("styled bytes=%q want=%q", colored.Bytes(), expected.Bytes())
			}
			// Remove only the insertion intervals, then parse the original bytes.
			var restored bytes.Buffer
			pos, end := 0, 0
			for _, s := range spans {
				gap := s.Start - end
				restored.Write(colored.Bytes()[pos : pos+gap])
				pos += gap + len(codes[s.Role])
				restored.Write(colored.Bytes()[pos : pos+s.End-s.Start])
				pos += s.End - s.Start + len("\x1b[0m")
				end = s.End
			}
			restored.Write(colored.Bytes()[pos:])
			if !bytes.Equal(restored.Bytes(), plain.Bytes()) {
				t.Fatal("insertion recovery changed original bytes")
			}
			decoded, err := tomlfmt.DecodeValue(&restored)
			if err != nil {
				t.Fatal(err)
			}
			var roundtrip bytes.Buffer
			if err := tomlfmt.EncodeValueWithOptions(&roundtrip, decoded, opts); err != nil || !bytes.Equal(roundtrip.Bytes(), plain.Bytes()) {
				t.Fatalf("native roundtrip changed: %v, %q", err, roundtrip.Bytes())
			}
		}
	}
}

type tomlFailureWriter struct {
	buf           bytes.Buffer
	calls, failAt int
	err           error
}

func (w *tomlFailureWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		n := len(p) / 2
		w.buf.Write(p[:n])
		return n, w.err
	}
	return w.buf.Write(p)
}

func TestTOMLObservationWritePolicy(t *testing.T) {
	d := omap.New()
	d.Set("key", omap.StrValue("value"))
	v := omap.MapValue(d)
	sentinel := errors.New("write failure")
	for _, failure := range []error{nil, sentinel} {
		for at := 1; at <= 4; at++ {
			plain := &tomlFailureWriter{failAt: at, err: failure}
			nilObserved := &tomlFailureWriter{failAt: at, err: failure}
			plainErr := tomlfmt.EncodeValue(plain, v)
			nilErr := tomlfmt.EncodeValueObserved(nilObserved, v, tomlfmt.EncodeOptions{}, nil)
			if !errors.Is(nilErr, plainErr) || plain.buf.String() != nilObserved.buf.String() || plain.calls != nilObserved.calls {
				t.Fatal("nil-observer changed legacy error/write policy")
			}
			if failure == nil && (plainErr != nil || plain.calls != 4) {
				t.Fatal("plain short-write policy changed")
			}
			observed := &tomlFailureWriter{failAt: at, err: failure}
			var spans []display.Span
			err := tomlfmt.EncodeValueObserved(observed, v, tomlfmt.EncodeOptions{}, func(s display.Span) { spans = append(spans, s) })
			want := failure
			if want == nil {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) || observed.calls != at {
				t.Fatalf("at=%d error=%v calls=%d", at, err, observed.calls)
			}
			for _, s := range spans {
				if s.End > observed.buf.Len() || (at == 1 && s.Role == display.Key) || (at == 3 && s.Role == display.String) {
					t.Fatalf("observed failed token: %+v", s)
				}
			}
		}
	}
}

func TestTOMLObservedRepresentability(t *testing.T) {
	for _, v := range []omap.Value{omap.NullValue(), omap.IntValue(1), omap.MapValue(nil), omap.MapValue(omap.New())} {
		if v.Map != nil {
			v.Map.Set("null", omap.NullValue())
		}
		var buf bytes.Buffer
		err := tomlfmt.EncodeValueObserved(&buf, v, tomlfmt.EncodeOptions{}, func(_ display.Span) { t.Fatal("observed invalid document") })
		var encodeErr *omap.EncodeError
		if !errors.As(err, &encodeErr) || buf.Len() != 0 {
			t.Fatalf("error=%v bytes=%q", err, buf.Bytes())
		}
	}
	for _, n := range []string{"NaN", "+Inf", "-Inf"} {
		d := omap.New()
		d.Set("ok", omap.StrValue("x"))
		d.Set("bad", omap.NumValue(stdjson.Number(n)))
		var buf bytes.Buffer
		err := tomlfmt.EncodeValueObserved(&buf, omap.MapValue(d), tomlfmt.EncodeOptions{}, func(_ display.Span) { t.Fatal("observed invalid document") })
		if err == nil || buf.Len() != 0 {
			t.Fatalf("nonfinite error=%v bytes=%q", err, buf.Bytes())
		}
	}
}
