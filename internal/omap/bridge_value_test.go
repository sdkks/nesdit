package omap_test

import (
	"encoding/json"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
)

// BUG-0001: ValueToAny/ValueFromAny support any top-level value, not only
// maps — required so the CLI pipeline can carry top-level arrays and scalars
// from decode through query to encode.

func TestValueToAny_RoundTripsArrayAtRoot(t *testing.T) {
	t.Parallel()
	v := omap.SeqValue(
		omap.IntValue(1),
		omap.IntValue(2),
		omap.StrValue("x"),
	)
	got := omap.ValueToAny(v)
	arr, ok := got.([]any)
	if !ok {
		t.Fatalf("ValueToAny kind=%T want []any", got)
	}
	if len(arr) != 3 {
		t.Fatalf("len=%d want 3", len(arr))
	}
	if _, ok := arr[0].(json.Number); !ok {
		t.Fatalf("arr[0] kind=%T want json.Number", arr[0])
	}
}

func TestValueToAny_RoundTripsScalarAtRoot(t *testing.T) {
	t.Parallel()
	if got := omap.ValueToAny(omap.StrValue("s")); got != "s" {
		t.Fatalf("str: got %v want s", got)
	}
	if got := omap.ValueToAny(omap.BoolValue(true)); got != true {
		t.Fatalf("bool: got %v want true", got)
	}
	if got := omap.ValueToAny(omap.NullValue()); got != nil {
		t.Fatalf("null: got %v want nil", got)
	}
	gotN := omap.ValueToAny(omap.IntValue(42))
	if n, ok := gotN.(json.Number); !ok || n.String() != "42" {
		t.Fatalf("int: got %v (%T) want json.Number 42", gotN, gotN)
	}
}

func TestValueFromAny_ScalarRoot(t *testing.T) {
	t.Parallel()
	if v := omap.ValueFromAny("x", omap.Value{}); v.Kind != omap.KindStr || v.Str != "x" {
		t.Fatalf("got %+v", v)
	}
	if v := omap.ValueFromAny(true, omap.Value{}); v.Kind != omap.KindBool || !v.Bool {
		t.Fatalf("got %+v", v)
	}
	if v := omap.ValueFromAny(nil, omap.Value{}); v.Kind != omap.KindNull {
		t.Fatalf("got %+v", v)
	}
}

func TestValueFromAny_ArrayRoot(t *testing.T) {
	t.Parallel()
	in := []any{json.Number("1"), "two", true}
	v := omap.ValueFromAny(in, omap.Value{})
	if v.Kind != omap.KindSeq {
		t.Fatalf("kind=%v want Seq", v.Kind)
	}
	if len(v.Seq) != 3 {
		t.Fatalf("len=%d want 3", len(v.Seq))
	}
}

func TestValueFromAny_ArrayRoot_PreservesPerElementPrev(t *testing.T) {
	t.Parallel()
	// Build a prev where the seq has maps at index 0 and 1 with key orders
	// that wouldn't sort lexicographically.
	m0 := omap.New()
	m0.Set("zulu", omap.IntValue(1))
	m0.Set("alpha", omap.IntValue(2))
	prev := omap.SeqValue(omap.MapValue(m0))

	// jq result: same element (identity); map-order should come from prev.
	in := []any{map[string]any{"zulu": json.Number("1"), "alpha": json.Number("2")}}
	v := omap.ValueFromAny(in, prev)
	if v.Kind != omap.KindSeq || len(v.Seq) != 1 {
		t.Fatalf("shape: %+v", v)
	}
	got := v.Seq[0]
	if got.Kind != omap.KindMap {
		t.Fatalf("elem kind=%v want Map", got.Kind)
	}
	keys := got.Map.Keys()
	if len(keys) != 2 || keys[0] != "zulu" || keys[1] != "alpha" {
		t.Fatalf("keys=%v want [zulu alpha]", keys)
	}
}
