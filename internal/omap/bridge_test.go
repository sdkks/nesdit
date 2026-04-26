package omap_test

import (
	"encoding/json"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/omap/omaptest"
)

// Test_FromAny_NewKeysLexicographicAppend verifies the load-bearing
// reconciliation rule (SPEC-0001 §2, Open Question 7): when a jq result
// contains keys not present in the pre-query snapshot, existing keys emit
// first in snapshot order and new keys append at the end in lexicographic
// order (deterministic even when Go map iteration is not).
func Test_FromAny_NewKeysLexicographicAppend(t *testing.T) {
	t.Parallel()

	prev := omap.New()
	prev.Set("a", omap.IntValue(1))
	prev.Set("b", omap.IntValue(2))
	prev.Set("c", omap.IntValue(3))

	// jq returned {c, z, a, b, m} — Go map iteration is randomised so
	// we write it in arbitrary order to simulate real jq output.
	result := map[string]any{
		"c": json.Number("3"),
		"z": json.Number("26"),
		"a": json.Number("1"),
		"b": json.Number("2"),
		"m": json.Number("13"),
	}

	got := omap.FromAny(result, prev)
	want := []string{"a", "b", "c", "m", "z"}
	gotKeys := got.Keys()
	if len(gotKeys) != len(want) {
		t.Fatalf("len=%d want %d (keys=%v)", len(gotKeys), len(want), gotKeys)
	}
	for i, k := range want {
		if gotKeys[i] != k {
			t.Fatalf("key[%d]=%q want %q (full=%v)", i, gotKeys[i], k, gotKeys)
		}
	}
}

// Test_FromAny_DroppedPrevKey — a key present in prev but absent in result
// must be dropped (not re-emitted).
func Test_FromAny_DroppedPrevKey(t *testing.T) {
	t.Parallel()

	prev := omap.New()
	prev.Set("a", omap.IntValue(1))
	prev.Set("b", omap.IntValue(2))
	prev.Set("c", omap.IntValue(3))

	result := map[string]any{
		"a": json.Number("1"),
		"c": json.Number("3"),
		// b is missing (del(.b))
	}

	got := omap.FromAny(result, prev)
	want := []string{"a", "c"}
	gotKeys := got.Keys()
	if len(gotKeys) != 2 || gotKeys[0] != want[0] || gotKeys[1] != want[1] {
		t.Fatalf("got=%v want %v", gotKeys, want)
	}
}

// Test_FromAny_NestedReconciliation — reconciliation recurses into nested
// maps and respects positional prev for sequences-of-maps.
func Test_FromAny_NestedReconciliation(t *testing.T) {
	t.Parallel()

	inner := omap.New()
	inner.Set("x", omap.IntValue(1))
	inner.Set("y", omap.IntValue(2))

	prev := omap.New()
	prev.Set("zulu", omap.IntValue(0))
	prev.Set("nested", omap.MapValue(inner))
	prev.Set("alpha", omap.IntValue(9))

	// jq returned nested with x updated and a new key "new" added.
	result := map[string]any{
		"zulu": json.Number("0"),
		"nested": map[string]any{
			"y":   json.Number("2"),
			"x":   json.Number("100"),
			"new": json.Number("42"),
		},
		"alpha": json.Number("9"),
	}

	got := omap.FromAny(result, prev)
	if k := got.Keys(); !equalSlice(k, []string{"zulu", "nested", "alpha"}) {
		t.Fatalf("top-level keys drifted: %v", k)
	}
	nv, _ := got.Get("nested")
	if nv.Kind != omap.KindMap {
		t.Fatalf("nested kind %v", nv.Kind)
	}
	// prev had x,y — new has x,y,new. Expected order: x, y, new (prev first, new lex-appended).
	if k := nv.Map.Keys(); !equalSlice(k, []string{"x", "y", "new"}) {
		t.Fatalf("nested keys=%v want [x y new]", k)
	}
}

// Test_FromAny_ArrayOfMapsPositional — when reconciling an array-of-maps,
// the per-element prev is the prev's element at the same index (if any).
func Test_FromAny_ArrayOfMapsPositional(t *testing.T) {
	t.Parallel()

	item0 := omap.New()
	item0.Set("name", omap.StrValue("a"))
	item0.Set("x", omap.IntValue(1))

	item1 := omap.New()
	item1.Set("x", omap.IntValue(2))
	item1.Set("name", omap.StrValue("b"))

	prev := omap.New()
	prev.Set("items", omap.SeqValue(
		omap.MapValue(item0),
		omap.MapValue(item1),
	))

	result := map[string]any{
		"items": []any{
			map[string]any{"x": json.Number("100"), "name": "a"},
			map[string]any{"name": "b", "x": json.Number("2")},
		},
	}

	got := omap.FromAny(result, prev)
	seq, _ := got.Get("items")
	if seq.Kind != omap.KindSeq || len(seq.Seq) != 2 {
		t.Fatalf("bad seq %+v", seq)
	}
	// element 0 prev had [name, x] — reconciled result must be [name, x]
	if k := seq.Seq[0].Map.Keys(); !equalSlice(k, []string{"name", "x"}) {
		t.Fatalf("items[0]=%v want [name x]", k)
	}
	// element 1 prev had [x, name] — reconciled must be [x, name]
	if k := seq.Seq[1].Map.Keys(); !equalSlice(k, []string{"x", "name"}) {
		t.Fatalf("items[1]=%v want [x name]", k)
	}
}

// Test_JSONNumber_SurvivesBridge — the NFR-4 invariant: json.Number above
// 2^53 must round-trip through ToAny → FromAny unchanged.
func Test_JSONNumber_SurvivesBridge(t *testing.T) {
	t.Parallel()

	const big = "9007199254740993" // 2^53 + 1
	d := omap.New()
	d.Set("big", omap.NumValue(json.Number(big)))
	d.Set("neg", omap.NumValue(json.Number("-9007199254740993")))
	d.Set("small", omap.IntValue(42))

	any0 := d.ToAny()
	rt := omap.FromAny(any0, d)

	if ok, r := omaptest.EqualDocs(d, rt); !ok {
		t.Fatalf("round-trip drift: %s", r)
	}

	v, _ := rt.Get("big")
	if v.Num.String() != big {
		t.Fatalf("big got %q want %q", v.Num.String(), big)
	}
}

// Test_FromAny_AcceptsIntAndFloatFromGojq — gojq internally converts numbers
// to int/float64/*big.Int; FromAny must map those back into json.Number so
// the rest of the pipeline sees a stable numeric type.
func Test_FromAny_AcceptsIntAndFloatFromGojq(t *testing.T) {
	t.Parallel()

	prev := omap.New()
	prev.Set("i", omap.IntValue(1))
	prev.Set("f", omap.NumValue(json.Number("3.14")))

	// Simulate gojq's return: ints land as `int`, floats as `float64`.
	result := map[string]any{
		"i": 42,
		"f": 2.5,
	}
	got := omap.FromAny(result, prev)
	vi, _ := got.Get("i")
	if vi.Kind != omap.KindNum || vi.Num.String() != "42" {
		t.Fatalf("int not wrapped: got %+v", vi)
	}
	vf, _ := got.Get("f")
	if vf.Kind != omap.KindNum {
		t.Fatalf("float kind=%v", vf.Kind)
	}
	if _, err := vf.Num.Float64(); err != nil {
		t.Fatalf("float not parseable: %v", err)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
