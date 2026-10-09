package stream

import (
	"bytes"
	stdjson "encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
)

type recordFailureWriter struct {
	buf           bytes.Buffer
	calls, failAt int
	err           error
}

func (w *recordFailureWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		n := len(p) / 2
		w.buf.Write(p[:n])
		return n, w.err
	}
	return w.buf.Write(p)
}

func TestJSONLDisplayWriteFailures(t *testing.T) {
	sentinel := errors.New("broken pipe")
	for _, color := range []bool{false, true} {
		for _, fail := range []error{nil, sentinel} {
			// Fail the payload and then the outside-color record newline.
			atEnd := 2
			if color {
				atEnd = 4
			}
			for _, at := range []int{1, atEnd} {
				output := &recordFailureWriter{failAt: at, err: fail}
				writer := &jsonlDocWriter{w: output, color: color}
				err := writer.WriteDoc(omap.StrValue("abc"))
				want := fail
				if want == nil {
					want = io.ErrShortWrite
				}
				if !errors.Is(err, want) || output.calls != at {
					t.Fatalf("color=%v err=%v calls=%d at=%d", color, err, output.calls, at)
				}
			}
		}
	}
}

func TestJSONLDisplayEncodeFailureAndReset(t *testing.T) {
	var buf bytes.Buffer
	writer := &jsonlDocWriter{w: &buf, color: true}
	if err := writer.WriteDoc(omap.NumValue(stdjson.Number("NaN"))); err == nil {
		t.Fatal("missing encode error")
	}
	if buf.Len() != 0 {
		t.Fatal("encode failure emitted bytes")
	}
	if err := writer.WriteDoc(omap.StrValue("x")); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteDoc(omap.NullValue()); err != nil {
		t.Fatal(err)
	}
	if want := "\x1b[32m\"x\"\x1b[0m\n\x1b[39mnull\x1b[0m\n"; buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
	if strings.Contains(buf.String(), "\n\x1b[0m") {
		t.Fatal("record newline styled")
	}
}
