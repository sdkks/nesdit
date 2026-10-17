package run

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"time"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/natsinspect"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/textview"
)

func assertTextJSONSize(t *testing.T, value omap.Value) {
	t.Helper()
	for _, pretty := range []bool{false, true} {
		var actual bytes.Buffer
		if err := jsonfmt.EncodeValueLayout(&actual, value, pretty, nil); err != nil {
			t.Fatal(err)
		}
		actual.WriteByte('\n')
		n, err := inspectionJSONSize(context.Background(), value, pretty, natsEventLimit)
		if err != nil || n != actual.Len() {
			t.Fatalf("pretty=%t estimate=%d actual=%d err=%v", pretty, n, actual.Len(), err)
		}
		if _, err = inspectionJSONSize(context.Background(), value, pretty, actual.Len()); err != nil {
			t.Fatal("exact size rejected", err)
		}
		if _, err = inspectionJSONSize(context.Background(), value, pretty, actual.Len()-1); err == nil {
			t.Fatal("one-over admitted")
		}
		staged, _, err := inspectionJSON(context.Background(), value, pretty, false)
		if err != nil || !bytes.Equal(staged, actual.Bytes()) {
			t.Fatal("staging mismatch", err)
		}
	}
}

func TestTextJSONPreflightOracle(t *testing.T) {
	raw := make([]byte, 256)
	for i := range raw {
		raw[i] = byte(i)
	}
	stringsToTest := []string{"", string(raw), "<>&\"\\\b\f\n\r\t\x00\x1f\x7f", "世界\u2028\u2029\ufffd\xff\xc0\xed\xa0\x80", strings.Repeat("é世", 2048)}
	values := []omap.Value{omap.NullValue(), omap.BoolValue(true), omap.BoolValue(false), omap.NumValue(""), omap.NumValue("-0"), omap.NumValue("1.25e+10"), omap.NumValue("18446744073709551615"), omap.MapValue(omap.New()), {Kind: omap.KindSeq}}
	for _, str := range stringsToTest {
		values = append(values, omap.StrValue(str))
		d := omap.New()
		d.Set(str, omap.StrValue(str))
		values = append(values, omap.MapValue(d))
	}
	values = append(values, omap.Value{Kind: omap.KindSeq, Seq: append([]omap.Value(nil), values...)})
	for _, value := range values {
		assertTextJSONSize(t, value)
	}
	// Arbitrary bytes exercise encoding/json's per-invalid-byte replacement,
	// including valid replacement runes next to incomplete UTF-8 sequences.
	random := rand.New(rand.NewSource(17))
	for i := 0; i < 1000; i++ {
		data := make([]byte, random.Intn(1024))
		if _, err := random.Read(data); err != nil {
			t.Fatal(err)
		}
		assertTextJSONSize(t, omap.StrValue(string(data)))
	}
	nested := omap.NullValue()
	for i := 0; i < 1000; i++ {
		nested = omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{nested}}
	}
	assertTextJSONSize(t, nested)
	for _, n := range []json.Number{"NaN", "Infinity", "1e999", "not-number"} {
		if _, err := inspectionJSONSize(context.Background(), omap.NumValue(n), false, natsEventLimit); err == nil {
			t.Fatal(n)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inspectionJSONSize(ctx, omap.StrValue(""), false, natsEventLimit); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestTextJSONOutputBoundary(t *testing.T) {
	for _, pretty := range []bool{false, true} {
		value := omap.StrValue(strings.Repeat("x", natsEventLimit-3))
		data, _, err := inspectionJSON(context.Background(), value, pretty, false)
		if err != nil || len(data) != natsEventLimit {
			t.Fatal(len(data), err)
		}
		value.Str += "x"
		data, _, err = inspectionJSON(context.Background(), value, pretty, false)
		if err == nil || data != nil {
			t.Fatal("oversized staged output")
		}
	}
	// Exact, not six-times-string conservative admission.
	assertTextJSONSize(t, omap.StrValue(strings.Repeat("x", 10<<20)))
}

func TestTextNativeEnvelopePreflight(t *testing.T) {
	record := natsinspect.Record{Subject: "orders.<&\u2028", Headers: map[string][]string{"<\xff": {"\x00\"\\", "\u2029&"}}, Data: []byte("\ufeffhello\x00\r\n")}
	for _, history := range []bool{false, true} {
		if history {
			record.History = &natsinspect.HistoryMetadata{Stream: "<ORDERS>", Sequence: 42, StoredTime: time.Unix(1, 2)}
		}
		for _, pretty := range []bool{false, true} {
			old, _, err := natsDecodedEvent(context.Background(), record, pretty, false, nil, nil, textview.Decode)
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := natsDecodedEventPreflight(context.Background(), record, pretty, false, nil, nil, textview.Decode, true)
			if err != nil || !bytes.Equal(got, old) {
				t.Fatalf("full envelope oracle: %v", err)
			}
		}
	}
	// Payload alone fits; envelope metadata pushes the authoritative event over.
	// A prepared projection can be up to 16 MiB; raw admission still applies first.
	decode := func(context.Context, []byte) (omap.Value, error) {
		return omap.StrValue(strings.Repeat("x", natsEventLimit-3)), nil
	}
	data, _, err := natsDecodedEventPreflight(context.Background(), record, false, false, nil, nil, decode, true)
	if err == nil || data != nil {
		t.Fatal("envelope escaped size was not charged")
	}
}
