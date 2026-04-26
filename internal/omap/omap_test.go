package omap_test

import (
	"encoding/json"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
)

func TestOrderedMap_Insertion_Preserves_Order(t *testing.T) {
	t.Parallel()
	d := omap.New()
	inserts := []string{"zulu", "alpha", "mike", "bravo", "yankee"}
	for i, k := range inserts {
		d.Set(k, omap.IntValue(int64(i)))
	}
	got := d.Keys()
	if len(got) != len(inserts) {
		t.Fatalf("len(keys)=%d want %d", len(got), len(inserts))
	}
	for i, k := range inserts {
		if got[i] != k {
			t.Errorf("key[%d]=%q want %q; full order=%v", i, got[i], k, got)
		}
	}
}

func TestOrderedMap_Update_Keeps_Position(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("a", omap.IntValue(1))
	d.Set("b", omap.IntValue(2))
	d.Set("c", omap.IntValue(3))
	d.Set("b", omap.IntValue(99)) // update
	want := []string{"a", "b", "c"}
	got := d.Keys()
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("order changed after update: got %v want %v", got, want)
		}
	}
	v, ok := d.Get("b")
	if !ok || v.Num.String() != "99" {
		t.Fatalf("b=%+v want Num=99", v)
	}
}

func TestOrderedMap_Delete_Preserves_Order(t *testing.T) {
	t.Parallel()
	d := omap.New()
	for _, k := range []string{"a", "b", "c", "d"} {
		d.Set(k, omap.StrValue(k))
	}
	d.Delete("b")
	want := []string{"a", "c", "d"}
	got := d.Keys()
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i, k := range want {
		if got[i] != k {
			t.Fatalf("after delete: got %v want %v", got, want)
		}
	}
}

func TestJSONNumber_Preserves_Int64_Precision(t *testing.T) {
	t.Parallel()
	// 9007199254740993 = 2^53 + 1 — not representable as a float64 without
	// loss. This test asserts that omap.Doc holding it produces the exact
	// same decimal string back.
	const big = "9007199254740993"
	d := omap.New()
	d.Set("n", omap.NumValue(json.Number(big)))
	v, ok := d.Get("n")
	if !ok {
		t.Fatal("key n missing")
	}
	if v.Num.String() != big {
		t.Fatalf("got %q want %q", v.Num.String(), big)
	}
	// And the helper IntValue around max int64 must round-trip too.
	d.Set("maxi", omap.IntValue(9223372036854775807))
	v2, _ := d.Get("maxi")
	if v2.Num.String() != "9223372036854775807" {
		t.Fatalf("maxi got %q", v2.Num.String())
	}
}
