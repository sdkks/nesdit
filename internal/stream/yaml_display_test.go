package stream

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
)

func TestYAMLDisplayWriteFailures(t *testing.T) {
	d := omap.New()
	d.Set("key", omap.StrValue("value"))
	sentinel := errors.New("broken pipe")
	for _, color := range []bool{true, false} {
		calls := 9 // separator, styles, key/value, resets, gap, final LF
		if !color {
			calls = 2
		}
		for _, failure := range []error{nil, sentinel} {
			for at := 1; at <= calls; at++ {
				out := &recordFailureWriter{failAt: at, err: failure}
				writer := &yamlDocWriter{w: out, color: color}
				err := writer.WriteDoc(omap.MapValue(d))
				want := failure
				wantCalls := at
				if want == nil && color {
					want = io.ErrShortWrite
				} else if want == nil {
					// Preserve the original plain writer's short-write policy.
					wantCalls = 2
				}
				if !errors.Is(err, want) || out.calls != wantCalls {
					t.Fatalf("color=%v at=%d err=%v calls=%d", color, at, err, out.calls)
				}
				if at == 1 && want != nil && !strings.Contains(err.Error(), "write separator") {
					t.Fatalf("separator err=%v", err)
				}
			}
		}
	}
}

func TestYAMLDisplayFramingOnEncodeFailure(t *testing.T) {
	for _, color := range []bool{false, true} {
		var out bytes.Buffer
		writer := &yamlDocWriter{w: &out, color: color}
		if err := writer.WriteDoc(omap.IntValue(1)); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteDoc(omap.NumValue("NaN")); err == nil {
			t.Fatal("expected encode error")
		}
		if err := writer.WriteDoc(omap.IntValue(3)); err != nil {
			t.Fatal(err)
		}
		want := "---\n1\n---\n---\n3\n"
		if color {
			want = "---\n\x1b[36m1\x1b[0m\n---\n---\n\x1b[36m3\x1b[0m\n"
		}
		if out.String() != want {
			t.Fatalf("color=%v out=%q", color, out.String())
		}
	}
}

func TestYAMLDisplayNonterminalFactoryStaysPlain(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	var plain, displayed bytes.Buffer
	w, err := NewDisplayWriter("yaml", &displayed)
	if err != nil {
		t.Fatal(err)
	}
	for _, writer := range []DocWriter{NewYAMLWriter(&plain), w} {
		for _, v := range []omap.Value{omap.StrValue("\n\n"), omap.IntValue(1), omap.BoolValue(false)} {
			if err := writer.WriteDoc(v); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !bytes.Equal(plain.Bytes(), displayed.Bytes()) {
		t.Fatalf("plain=%q displayed=%q", plain.Bytes(), displayed.Bytes())
	}
}
