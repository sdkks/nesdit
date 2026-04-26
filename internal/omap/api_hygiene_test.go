package omap_test

import (
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
)

// TASK-0004: omap API hygiene + typed EncodeError.Kind enum.
//
// These tests pin down the contracts added in TASK-0004:
//   1. Doc.Keys()/Values() return copies — caller mutation does not corrupt Doc.
//   2. Doc.TryAt(i) is a nil-safe, bounds-safe (Value, bool) form of At.
//   3. Nil-receiver reads on *Doc are uniformly safe (Len/Get/Has/Keys/Values/TryAt).
//   4. EncodeError.Kind values are the exported typed constants covering the
//      current kinds (null, NaN, +Inf, -Inf), leaving room for the NFR-10
//      event taxonomy.
//   5. Doc.Entries yields (key, value) pairs in insertion order via the Go 1.23
//      range-over-func shape so format encoders can iterate without allocating
//      a keys slice per call.

func TestDoc_KeysReturnsCopy_CallerMutationIsolated(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("a", omap.IntValue(1))
	d.Set("b", omap.IntValue(2))
	d.Set("c", omap.IntValue(3))

	ks := d.Keys()
	// Mutate the returned slice — must not corrupt d.
	ks[0] = "ZZZ"

	reread := d.Keys()
	if reread[0] != "a" || reread[1] != "b" || reread[2] != "c" {
		t.Fatalf("caller mutation leaked into Doc: %v", reread)
	}
}

func TestDoc_ValuesReturnsCopy_CallerMutationIsolated(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("a", omap.IntValue(1))
	d.Set("b", omap.IntValue(2))

	vs := d.Values()
	vs[0] = omap.StrValue("corrupted")

	v, _ := d.Get("a")
	if v.Kind != omap.KindNum || v.Num.String() != "1" {
		t.Fatalf("caller mutation of Values() leaked into Doc: got %+v", v)
	}
}

func TestDoc_TryAt_InBounds(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("a", omap.IntValue(1))
	d.Set("b", omap.IntValue(2))

	k, v, ok := d.TryAt(1)
	if !ok {
		t.Fatal("TryAt(1) ok=false, want true")
	}
	if k != "b" {
		t.Fatalf("key=%q want b", k)
	}
	if v.Kind != omap.KindNum || v.Num.String() != "2" {
		t.Fatalf("value=%+v want Num=2", v)
	}
}

func TestDoc_TryAt_OutOfRange(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("a", omap.IntValue(1))

	// Negative.
	if k, v, ok := d.TryAt(-1); ok || k != "" || v.Kind != omap.KindNull {
		t.Fatalf("TryAt(-1) = (%q,%+v,%v), want zero", k, v, ok)
	}
	// Past end.
	if k, v, ok := d.TryAt(5); ok || k != "" || v.Kind != omap.KindNull {
		t.Fatalf("TryAt(5) = (%q,%+v,%v), want zero", k, v, ok)
	}
}

func TestDoc_TryAt_NilReceiver(t *testing.T) {
	t.Parallel()
	var d *omap.Doc
	k, v, ok := d.TryAt(0)
	if ok || k != "" || v.Kind != omap.KindNull {
		t.Fatalf("TryAt on nil = (%q,%+v,%v), want zero", k, v, ok)
	}
}

func TestDoc_NilReceiver_ReadsAreUniform(t *testing.T) {
	t.Parallel()
	var d *omap.Doc

	if n := d.Len(); n != 0 {
		t.Errorf("nil.Len()=%d want 0", n)
	}
	if _, ok := d.Get("x"); ok {
		t.Error("nil.Get returned ok=true")
	}
	if d.Has("x") {
		t.Error("nil.Has returned true")
	}
	if ks := d.Keys(); ks != nil && len(ks) != 0 {
		t.Errorf("nil.Keys()=%v want nil/empty", ks)
	}
	if vs := d.Values(); vs != nil && len(vs) != 0 {
		t.Errorf("nil.Values()=%v want nil/empty", vs)
	}
}

func TestEncodeErrorKind_HasTypedConstantsForCurrentKinds(t *testing.T) {
	t.Parallel()
	// Assert the enum surface exists and values match the strings the
	// encoders emit today (null, NaN, +Inf, -Inf). The constants are the
	// single source of truth going forward; the logx NFR-10 event
	// taxonomy (STORY-0003+) extends this set.
	cases := []struct {
		name string
		got  omap.EncodeErrorKind
		want string
	}{
		{"null", omap.EncodeKindNull, "null"},
		{"NaN", omap.EncodeKindNaN, "NaN"},
		{"+Inf", omap.EncodeKindPosInf, "+Inf"},
		{"-Inf", omap.EncodeKindNegInf, "-Inf"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: constant=%q want %q", c.name, c.got, c.want)
		}
	}
}

func TestEncodeError_UsesTypedConstant(t *testing.T) {
	t.Parallel()
	// Build an EncodeError using the typed constant and confirm the field
	// value compares equal to the constant — this fails until Kind is the
	// EncodeErrorKind type (not a free-form string).
	e := &omap.EncodeError{
		Path:   omap.RootPath().MapStep("a"),
		Kind:   omap.EncodeKindNaN,
		Format: "json",
	}
	if e.Kind != omap.EncodeKindNaN {
		t.Fatalf("Kind=%q does not equal EncodeKindNaN=%q", e.Kind, omap.EncodeKindNaN)
	}
	// Error string still includes the kind's textual form.
	if got, want := e.Error(), "$.a: NaN is not representable in json"; got != want {
		t.Fatalf("Error()=%q want %q", got, want)
	}
}

func TestDoc_Entries_YieldsInsertionOrder(t *testing.T) {
	t.Parallel()
	d := omap.New()
	inserts := []string{"zulu", "alpha", "mike", "bravo"}
	for i, k := range inserts {
		d.Set(k, omap.IntValue(int64(i)))
	}

	var gotKeys []string
	var gotVals []string
	d.Entries(func(k string, v omap.Value) bool {
		gotKeys = append(gotKeys, k)
		gotVals = append(gotVals, v.Num.String())
		return true
	})
	for i, k := range inserts {
		if gotKeys[i] != k {
			t.Fatalf("key[%d]=%q want %q", i, gotKeys[i], k)
		}
	}
	if gotVals[0] != "0" || gotVals[3] != "3" {
		t.Fatalf("values=%v", gotVals)
	}
}

func TestDoc_Entries_EarlyStop(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("a", omap.IntValue(1))
	d.Set("b", omap.IntValue(2))
	d.Set("c", omap.IntValue(3))

	count := 0
	d.Entries(func(_ string, _ omap.Value) bool {
		count++
		return false // stop after first
	})
	if count != 1 {
		t.Fatalf("yield called %d times, want 1 (early-stop)", count)
	}
}

func TestDoc_Entries_NilReceiver(t *testing.T) {
	t.Parallel()
	var d *omap.Doc
	called := false
	d.Entries(func(_ string, _ omap.Value) bool {
		called = true
		return true
	})
	if called {
		t.Fatal("Entries on nil invoked yield")
	}
}
