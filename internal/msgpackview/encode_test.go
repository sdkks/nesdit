package msgpackview

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
	"github.com/vmihailenco/msgpack/v5"
)

func importEncode(t *testing.T, text string, profile Interpretation) []byte {
	t.Helper()
	v, err := ReadJSON(context.Background(), []byte(text), profile)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := Encode(context.Background(), v, profile)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestEncodeNumericWire(t *testing.T) {
	for _, tt := range []struct{ source, hex string }{
		{"0", "00"}, {"127", "7f"}, {"128", "cc80"}, {"256", "cd0100"},
		{"-1", "ff"}, {"-32", "e0"}, {"-33", "d0df"}, {"-129", "d1ff7f"},
		{"18446744073709551615", "cfffffffffffffffff"}, {"-9223372036854775808", "d38000000000000000"},
		{"-0", "cb8000000000000000"}, {"-0.0", "cb8000000000000000"}, {"-0e99999999999999", "cb8000000000000000"},
		{"0e99999999999999", "cb0000000000000000"}, {"0.1", "cb3fb999999999999a"},
		{"1e3", "cb408f400000000000"}, {"1000", "cd03e8"}, {"5e-324", "cb0000000000000001"},
		{"2.2250738585072014e-308", "cb0010000000000000"},
		{"1.00000000000000011102230246251565404236316680908203125", "cb3ff0000000000000"},
	} {
		t.Run(tt.source, func(t *testing.T) {
			got := hex.EncodeToString(importEncode(t, tt.source, OrdinaryJSON))
			if got != tt.hex {
				t.Fatalf("got %s want %s", got, tt.hex)
			}
		})
	}
	for _, text := range []string{"18446744073709551616", "-9223372036854775809", "1e309", "1e-4000", "2e-324"} {
		v, err := ReadJSON(context.Background(), []byte(text), OrdinaryJSON)
		if err != nil {
			t.Fatal("valid ordinary source rejected", err)
		}
		if out, err := Encode(context.Background(), v, OrdinaryJSON); err == nil || len(out) != 0 {
			t.Fatal("unrepresentable target accepted", text)
		}
	}
	for _, tt := range []struct{ source, hex string }{
		{`{"$float":"NaN"}`, "cb7ff8000000000000"},
		{`{"$float":"Infinity"}`, "cb7ff0000000000000"},
		{`{"$float":"-Infinity"}`, "cbfff0000000000000"},
		{`{"$float":"-0"}`, "cb8000000000000000"},
		{`{"$float":"1"}`, "cb3ff0000000000000"},
		{`{"$float":"+01.00e+0"}`, "cb3ff0000000000000"},
		{`{"$float":".5"}`, "cb3fe0000000000000"},
		{`{"$float":"1."}`, "cb3ff0000000000000"},
		{`{"$bin":""}`, "c400"}, {`{"$str64":"/w=="}`, "a1ff"},
		{`{"$ext":[-128,""]}`, "c70080"},
		{`{"$time":["4294967295",0]}`, "d6ffffffffff"},
		{`{"$time":["4294967296",0]}`, "d7ff0000000100000000"},
		{`{"$time":["17179869184",0]}`, "c70cff000000000000000400000000"},
		{`{"$time":["-1",999999999]}`, "c70cff3b9ac9ffffffffffffffffff"},
	} {
		t.Run(tt.source, func(t *testing.T) {
			got := hex.EncodeToString(importEncode(t, tt.source, RichJSON))
			if got != tt.hex {
				t.Fatalf("got %s want %s", got, tt.hex)
			}
		})
	}
	// Float32 promotes its IEEE value exactly, not its shorter float32 spelling.
	v, err := Decode(context.Background(), []byte{0xca, 0x3d, 0xcc, 0xcc, 0xcd})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Encode(context.Background(), v, RichJSON)
	if err != nil || hex.EncodeToString(out) != "cb3fb99999a0000000" {
		t.Fatal(hex.EncodeToString(out), err)
	}
}

func TestJSONStrictSource(t *testing.T) {
	for _, text := range []string{
		"", " ", "null null", "null\v", "\xef\xbb\xbfnull", "truex", "[1,]", "{\"x\":1,}",
		`{"a":1,"\u0061":2}`, `{"x":{"a":1,"a":2}}`,
		`"\ud800"`, `"\udc00"`, `"\ud800\u0000"`, `"\ud800x"`, `"\u000x"`, `"\x20"`,
		"\"\xff\"", "\"\n\"", "+1", "01", "1.", ".1", "1e", "NaN", "1e+", "--1",
	} {
		if _, err := ReadJSON(context.Background(), []byte(text), OrdinaryJSON); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	v, err := ReadJSON(context.Background(), []byte(" {\"z\":1e3,\"a\":\"\\ud83d\\ude00\\u0000\"} \t\r\n"), OrdinaryJSON)
	if err != nil {
		t.Fatal(err)
	}
	key, num := v.Map.At(0)
	if key != "z" || num.Num.String() != "1e3" {
		t.Fatal("lost order or numeric lexeme")
	}
	_, str := v.Map.At(1)
	if str.Str != "😀\x00" {
		t.Fatal("surrogate pair decoded incorrectly")
	}
	// Same keys in different objects are not duplicates.
	importEncode(t, `[{"a":1},{"a":2}]`, OrdinaryJSON)
}

func TestRichGrammarRejections(t *testing.T) {
	for _, text := range []string{
		`1`, `{}`, `{"unknown":null}`, `{"$int":"1","x":null}`, `{"$int":1}`,
		`{"$int":"+1"}`, `{"$int":"01"}`, `{"$int":"-0"}`, `{"$int":"1e0"}`,
		`{"$int":"18446744073709551616"}`, `{"$int":"-9223372036854775809"}`,
		`{"$float":1}`, `{"$float":"0x1p0"}`, `{"$float":"1_0"}`, `{"$float":" NaN"}`, `{"$float":"nan"}`,
		`{"$float":"1e309"}`, `{"$float":"1e-4000"}`, `{"$float":""}`,
		`{"$bin":"/w"}`, `{"$bin":"/x=="}`, `{"$bin":"AAB="}`, `{"$bin":"/w==\n"}`, `{"$bin":" /w=="}`,
		`{"$bin":"===="}`, `{"$bin":"_w=="}`, `{"$str64":""}`, `{"$str64":"YQ=="}`,
		`{"$ext":[-1,""]}`, `{"$ext":[128,""]}`, `{"$ext":[-129,""]}`, `{"$ext":[1.0,""]}`,
		`{"$ext":["1",""]}`, `{"$ext":[1,"",null]}`, `{"$ext":[1,null]}`,
		`{"$time":["-0",0]}`, `{"$time":["9223372036854775808",0]}`, `{"$time":["0",1000000000]}`,
		`{"$time":["0",-1]}`, `{"$time":["0",0.0]}`, `{"$time":[0,0]}`, `{"$time":["0"]}`,
		`{"$map":{}}`, `{"$map":[[]]}`, `{"$map":[[null]]}`, `{"$map":[[null,null,null]]}`,
		`{"$map":[[null,1]]}`, `[true,2]`,
	} {
		t.Run(text, func(t *testing.T) {
			if _, err := ReadJSON(context.Background(), []byte(text), RichJSON); err == nil {
				t.Fatal("accepted invalid rich source")
			}
		})
	}
	if a, b := importEncode(t, `{"$int":"7"}`, OrdinaryJSON), importEncode(t, `{"$int":"7"}`, RichJSON); bytes.Equal(a, b) || !bytes.Equal(b, []byte{7}) {
		t.Fatal("tag interpretation collision")
	}
}

func TestEncodeIndependentFixtureViews(t *testing.T) {
	// These expected views were generated by the independent Python fixture
	// producer. Exact-wire assertions above cover encoding policy separately.
	data, err := os.ReadFile("testdata/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Hex string
		View      json.RawMessage
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			wire, err := hex.DecodeString(f.Hex)
			if err != nil {
				t.Fatal(err)
			}
			root, err := Decode(context.Background(), wire)
			if err != nil {
				t.Fatal(err)
			}
			fromWire, err := Encode(context.Background(), root, RichJSON)
			if err != nil {
				t.Fatal(err)
			}
			fromJSON := importEncode(t, string(f.View), RichJSON)
			if !bytes.Equal(fromWire, fromJSON) {
				t.Fatal("equivalent selected values differ")
			}
		})
	}
}

func TestBinaryBufferGuardsLibraryBytes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := &binaryBuffer{ctx: ctx, limit: 3}
	if _, ok := any(b).(io.ByteWriter); ok {
		t.Fatal("exposes unguarded ByteWriter")
	}
	if _, ok := any(b).(io.StringWriter); ok {
		t.Fatal("exposes unguarded StringWriter")
	}
	e := msgpack.NewEncoder(b)
	for _, encode := range []func() error{e.EncodeNil, func() error { return e.EncodeBool(true) }, func() error { return e.EncodeInt(1) }} {
		if err := encode(); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.EncodeArrayLen(0); err == nil || b.data.Len() != 3 {
		t.Fatal("one-byte header bypassed cap")
	}
	b.limit = 10
	cancel()
	if err := e.EncodeExtHeader(0, 1); !errors.Is(err, context.Canceled) || b.data.Len() != 3 {
		t.Fatal("one-byte extension bypassed cancellation", err)
	}
}

func TestEncodingLimits(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int{100, 101} {
		text := strings.Repeat("[", n) + "null" + strings.Repeat("]", n)
		_, err := ReadJSON(ctx, []byte(text), OrdinaryJSON)
		if (err == nil) != (n == 100) {
			t.Fatal("source depth", n, err)
		}
	}
	for _, n := range []int{nodeLimit - 1, nodeLimit} {
		text := "[" + strings.Repeat("0,", n-1) + "0]"
		_, err := ReadJSON(ctx, []byte(text), OrdinaryJSON)
		if (err == nil) != (n == nodeLimit-1) {
			t.Fatal("source nodes", n, err)
		}
	}
	for _, n := range []int{rawLimit, rawLimit + 1} {
		text := "\"" + strings.Repeat("x", n-2) + "\""
		_, err := ReadJSON(ctx, []byte(text), OrdinaryJSON)
		if (err == nil) != (n == rawLimit) {
			t.Fatal("raw cap", n, err)
		}
	}
	// Direct selected values cover aggregate text beyond the raw-input cap.
	for _, n := range []int{textLimit, textLimit + 1} {
		v := omap.StrValue(strings.Repeat("x", n))
		if err := validateTarget(ctx, v, OrdinaryJSON); (err == nil) != (n == textLimit) {
			t.Fatal("text cap", n, err)
		}
		if err := validateTarget(ctx, v, RichJSON); (err == nil) != (n == textLimit) {
			t.Fatal("rich text cap", n, err)
		}
	}
	for _, n := range []int{outputLimit - 5, outputLimit - 4} {
		wire, err := Encode(ctx, omap.StrValue(strings.Repeat("x", n)), OrdinaryJSON)
		if n == outputLimit-5 {
			if err != nil || len(wire) != outputLimit {
				t.Fatal("exact output cap", len(wire), err)
			}
		} else if err == nil || wire != nil {
			t.Fatal("one-over output cap")
		}
	}
	// Ordinary maps acquire three view levels, so semantic depth alone is not enough.
	for _, n := range []int{33, 34} {
		v := omap.NullValue()
		for range n {
			d := omap.New()
			d.Set("k", v)
			v = omap.MapValue(d)
		}
		if err := validateTarget(ctx, v, OrdinaryJSON); (err == nil) != (n == 33) {
			t.Fatal("lifted map depth", n, err)
		}
	}
	// A million ordinary semantic nodes can exceed rich wrapper costs first.
	for _, n := range []int{(nodeLimit - 1) / 2, (nodeLimit-1)/2 + 1} {
		seq := make([]omap.Value, n)
		for i := range seq {
			seq[i] = omap.IntValue(0)
		}
		err := validateTarget(ctx, omap.SeqValue(seq...), OrdinaryJSON)
		if (err == nil) != (n == (nodeLimit-1)/2) {
			t.Fatal("lifted nodes", n, err)
		}
	}
	// Shared siblings must accumulate text, not reset per subtree.
	s := omap.StrValue(strings.Repeat("x", textLimit/2))
	if err := validateTarget(ctx, omap.SeqValue(s, s), RichJSON); err != nil {
		t.Fatal(err)
	}
	if err := validateTarget(ctx, omap.SeqValue(s, s, omap.StrValue("x")), RichJSON); err == nil {
		t.Fatal("sibling text reset")
	}
}

func TestTargetIndependentBudgets(t *testing.T) {
	ctx := context.Background()
	for _, n := range []int{nodeLimit - 1, nodeLimit} {
		v := omap.SeqValue(make([]omap.Value, n)...)
		for _, profile := range []Interpretation{OrdinaryJSON, RichJSON} {
			err := validateTarget(ctx, v, profile)
			if (err == nil) != (n == nodeLimit-1) {
				t.Fatal("selected nodes", profile, n, err)
			}
		}
	}
	for _, n := range []int{depthLimit, depthLimit + 1} {
		v := omap.NullValue()
		for range n {
			v = omap.SeqValue(v)
		}
		for _, profile := range []Interpretation{OrdinaryJSON, RichJSON} {
			err := validateTarget(ctx, v, profile)
			if (err == nil) != (n == depthLimit) {
				t.Fatal("selected depth", profile, n, err)
			}
		}
	}
	// Source raw limits normally dominate decoded JSON text. Exercise the
	// lexical pass directly to pin decoded, not escaped, string/key accounting.
	for _, n := range []int{textLimit, textLimit + 1} {
		p := jsonScan{ctx: ctx, text: `["` + strings.Repeat("x", n-1) + `","\u0061"]`}
		err := p.value(0)
		if (err == nil) != (n == textLimit) {
			t.Fatal("lexical text", n, err)
		}
	}
	for _, n := range []int{textLimit - len("$map") - 1, textLimit - len("$map")} {
		d := omap.New()
		d.Set("k", omap.StrValue(strings.Repeat("x", n)))
		err := validateTarget(ctx, omap.MapValue(d), OrdinaryJSON)
		if (err == nil) != (n == textLimit-len("$map")-1) {
			t.Fatal("lifted view text", n, err)
		}
	}
	// Canonical base64 costs count cumulatively before decoding each sibling.
	data := strings.Repeat("A", 4*((textLimit/2)/4))
	v := omap.SeqValue(tag("$bin", omap.StrValue(data)), tag("$bin", omap.StrValue(data)))
	if err := validateTarget(ctx, v, RichJSON); !errors.Is(err, errView) {
		t.Fatal("base64 sibling view cost", err)
	}
}

func TestEncodeCancellationAndSelectedTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, p := range []Interpretation{OrdinaryJSON, RichJSON} {
		if _, err := ReadJSON(ctx, []byte("null"), p); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if _, err := Encode(ctx, omap.NullValue(), p); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if _, err := base64Size(ctx, "AAAA"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := Encode(context.Background(), omap.NumValue("1"), RichJSON); err == nil {
		t.Fatal("bare selected rich number")
	}
	if _, err := Encode(context.Background(), omap.StrValue("\xff"), OrdinaryJSON); err == nil {
		t.Fatal("invalid selected string")
	}
	f, err := floatNumber("0.1", false)
	if err != nil || math.Float64bits(f) != 0x3fb999999999999a {
		t.Fatal(f, err)
	}
}

func FuzzJSONImportEncode(f *testing.F) {
	for _, s := range []string{`null`, `{"x":1e3}`, `{"$bin":"/w=="}`, `{"$map":[[[null],{"$int":"1"}]]}`, `"\ud800"`, `1e9999999`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			t.Skip()
		}
		for _, p := range []Interpretation{OrdinaryJSON, RichJSON} {
			v, err := ReadJSON(context.Background(), data, p)
			if err != nil {
				continue
			}
			out, err := Encode(context.Background(), v, p)
			if err == nil && len(out) == 0 {
				t.Fatal("empty successful encoding")
			}
		}
	})
}
