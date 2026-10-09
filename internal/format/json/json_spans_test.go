package json

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/display"
	"github.com/sdkks/nesdit/internal/omap"
)

func TestSemanticSpans(t *testing.T) {
	cases := []struct {
		source string
		roles  []display.Role
	}{
		{`{"":"","quote\"\\\n":"é🙂\u001b[32m","é🙂":"x","null":null,"123":-1.25e+2,"true":true,"nested":{"x":[false,"null",0]}}`, []display.Role{display.Key, display.String, display.Key, display.String, display.Key, display.String, display.Key, display.Null, display.Key, display.Number, display.Key, display.Bool, display.Key, display.Key, display.Bool, display.String, display.Number}},
		{`["",-2,true,false,null,{},[]]`, []display.Role{display.String, display.Number, display.Bool, display.Bool, display.Null}},
		{`"é🙂\r\n\t\"\\"`, []display.Role{display.String}},
		{`123`, []display.Role{display.Number}},
		{`false`, []display.Role{display.Bool}},
		{`null`, []display.Role{display.Null}},
		{`{}`, nil}, {`[]`, nil},
	}
	codes := map[display.Role]string{display.Key: "\x1b[1;39m", display.String: "\x1b[32m", display.Number: "\x1b[36m", display.Bool: "\x1b[35m", display.Null: "\x1b[39m"}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			v, err := DecodeValue(strings.NewReader(tc.source))
			if err != nil {
				t.Fatal(err)
			}
			var plain, observed, nilObserved bytes.Buffer
			if err := EncodeValue(&plain, v); err != nil {
				t.Fatal(err)
			}
			if err := EncodeValueObserved(&nilObserved, v, nil); err != nil {
				t.Fatal(err)
			}
			var spans []display.Span
			if err := EncodeValueObserved(&observed, v, func(s display.Span) { spans = append(spans, s) }); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(plain.Bytes(), observed.Bytes()) || !bytes.Equal(plain.Bytes(), nilObserved.Bytes()) {
				t.Fatal("observer changed serialized bytes")
			}
			if v.Kind == omap.KindMap {
				var legacy bytes.Buffer
				if err := Encode(&legacy, v.Map); err != nil || !bytes.Equal(legacy.Bytes(), plain.Bytes()) {
					t.Fatalf("map entrypoint parity: %v", err)
				}
			}
			var roles []display.Role
			var colored bytes.Buffer
			if err := display.Write(&colored, observed.Bytes(), spans); err != nil {
				t.Fatal(err)
			}
			// Remove only the insertion intervals implied by these spans; do
			// not strip matching user text or use a broad ANSI expression.
			var restored bytes.Buffer
			pos, originalPos := 0, 0
			for _, s := range spans {
				roles = append(roles, s.Role)
				literal := observed.Bytes()[s.Start:s.End]
				if !stdjson.Valid(literal) {
					t.Fatalf("invalid token %q", literal)
				}
				gap := s.Start - originalPos
				restored.Write(colored.Bytes()[pos : pos+gap])
				pos += gap
				code := codes[s.Role]
				if string(colored.Bytes()[pos:pos+len(code)]) != code {
					t.Fatalf("wrong style for %q", literal)
				}
				pos += len(code)
				restored.Write(colored.Bytes()[pos : pos+len(literal)])
				pos += len(literal)
				if string(colored.Bytes()[pos:pos+4]) != "\x1b[0m" {
					t.Fatal("missing reset")
				}
				pos += 4
				originalPos = s.End
			}
			restored.Write(colored.Bytes()[pos:])
			if !bytes.Equal(restored.Bytes(), plain.Bytes()) {
				t.Fatalf("bytes changed: %q", restored.Bytes())
			}
			if !reflect.DeepEqual(roles, tc.roles) {
				t.Fatalf("roles = %v, want %v", roles, tc.roles)
			}
		})
	}
}

type failedSpanWriter struct{ err error }

func (w failedSpanWriter) Write(p []byte) (int, error) { return len(p) / 2, w.err }

func TestObservationErrors(t *testing.T) {
	sentinel := errors.New("write failure")
	var spans []display.Span
	if err := EncodeValueObserved(failedSpanWriter{sentinel}, omap.StrValue("abc"), func(s display.Span) { spans = append(spans, s) }); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
	if len(spans) != 0 {
		t.Fatal("observed unsuccessful token")
	}
	if err := EncodeValueObserved(failedSpanWriter{}, omap.StrValue("abc"), func(s display.Span) { spans = append(spans, s) }); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error = %v", err)
	}
	if len(spans) != 0 {
		t.Fatal("observed truncated token")
	}
	var buf bytes.Buffer
	v := omap.NumValue(stdjson.Number("NaN"))
	if err := EncodeValueObserved(&buf, v, func(_ display.Span) { t.Fatal("observed failed encode") }); err == nil {
		t.Fatal("missing encode error")
	}
	if buf.Len() != 0 {
		t.Fatal("nonfinite encode wrote bytes")
	}
	if err := EncodeValueObserved(io.Discard, omap.Value{Kind: omap.KindNum, Num: "bad"}, nil); err == nil {
		t.Fatal("missing invalid number error")
	}
}
