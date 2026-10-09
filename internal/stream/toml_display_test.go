package stream

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	"github.com/sdkks/nesdit/internal/omap"
)

func tomlDisplayDoc(v omap.Value) omap.Value {
	d := omap.New()
	d.Set("key", v)
	return omap.MapValue(d)
}

func TestTOMLDisplayPayloadWriteFailures(t *testing.T) {
	v := tomlDisplayDoc(omap.StrValue("value"))
	sentinel := errors.New("broken pipe")
	for _, failure := range []error{nil, sentinel} {
		// Styling boundaries, keys, separators, values, resets and final newline.
		for at := 1; at <= 8; at++ {
			output := &recordFailureWriter{failAt: at, err: failure}
			writer := &tomlDocWriter{w: output, color: true}
			err := writer.WriteDoc(v)
			want := failure
			if want == nil {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) || output.calls != at || !writer.firstWritten {
				t.Fatalf("at=%d error=%v calls=%d state=%v", at, err, output.calls, writer.firstWritten)
			}
		}
		for at := 1; at <= 4; at++ {
			output := &recordFailureWriter{failAt: at, err: failure}
			writer, err := NewDisplayWriter("toml", output)
			if err != nil {
				t.Fatal(err)
			}
			err = writer.WriteDoc(v)
			if !errors.Is(err, failure) {
				t.Fatalf("plain error=%v want=%v", err, failure)
			}
			wantCalls := at
			if failure == nil {
				wantCalls = 4 // Retain the plain encoder's direct short-write policy.
			}
			if output.calls != wantCalls {
				t.Fatalf("plain calls=%d want=%d", output.calls, wantCalls)
			}
		}
	}
}

func TestTOMLDisplayFramingStateOnEncodeErrors(t *testing.T) {
	for _, color := range []bool{false, true} {
		var buf bytes.Buffer
		writer := &tomlDocWriter{w: &buf, color: color}
		invalid := tomlDisplayDoc(omap.NullValue())
		var encodeErr *omap.EncodeError
		if err := writer.WriteDoc(invalid); !errors.As(err, &encodeErr) || buf.Len() != 0 || !writer.firstWritten {
			t.Fatalf("first failure: err=%v bytes=%q state=%v", err, buf.Bytes(), writer.firstWritten)
		}
		if err := writer.WriteDoc(tomlDisplayDoc(omap.BoolValue(true))); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteDoc(invalid); !errors.As(err, &encodeErr) {
			t.Fatalf("later failure: %v", err)
		}
		if err := writer.WriteDoc(tomlDisplayDoc(omap.BoolValue(false))); err != nil {
			t.Fatal(err)
		}
		want := "+++\nkey = true\n+++\n+++\nkey = false\n"
		if color {
			want = "+++\n\x1b[1;39mkey\x1b[0m = \x1b[35mtrue\x1b[0m\n+++\n+++\n\x1b[1;39mkey\x1b[0m = \x1b[35mfalse\x1b[0m\n"
		}
		if buf.String() != want {
			t.Fatalf("color=%v framing=%q want=%q", color, buf.String(), want)
		}
	}
}

func TestTOMLDisplaySeparatorWriteFailures(t *testing.T) {
	v := tomlDisplayDoc(omap.StrValue("value"))
	sentinel := errors.New("broken pipe")
	for _, color := range []bool{false, true} {
		for _, failure := range []error{nil, sentinel} {
			output := &recordFailureWriter{err: failure}
			writer := &tomlDocWriter{w: output, color: color}
			if err := writer.WriteDoc(v); err != nil {
				t.Fatal(err)
			}
			at := output.calls + 1
			output.failAt = at
			before := output.buf.String()
			err := writer.WriteDoc(v)
			want := failure
			if failure == nil && color {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) || !writer.firstWritten {
				t.Fatalf("color=%v err=%v state=%v", color, err, writer.firstWritten)
			}
			if want != nil && (output.calls != at || output.buf.String() != before+"++" || !strings.Contains(err.Error(), "write separator")) {
				t.Fatalf("retried after separator failure: calls=%d bytes=%q err=%v", output.calls, output.buf.Bytes(), err)
			}
		}
	}
}

func TestTOMLDisplayPlainOptions(t *testing.T) {
	v := tomlDisplayDoc(omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{omap.IntValue(1), omap.IntValue(2)}})
	for _, pretty := range []bool{false, true} {
		var plain, displayed bytes.Buffer
		opts := tomlfmt.EncodeOptions{Pretty: pretty}
		writers := []DocWriter{NewTOMLWriterWithOptions(&plain, opts), NewTOMLDisplayWriterWithOptions(&displayed, opts)}
		for _, w := range writers {
			if err := w.WriteDoc(v); err != nil {
				t.Fatal(err)
			}
		}
		if !bytes.Equal(plain.Bytes(), displayed.Bytes()) {
			t.Fatal("display options changed nonterminal bytes")
		}
	}
}
