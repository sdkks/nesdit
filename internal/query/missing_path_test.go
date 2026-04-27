package query_test

// Tests for FR-16 / STORY-0012: CheckNoMissingPaths strict-by-default
// path-creation semantics.
//
// The probe showed that gojq transparently creates absent paths when using
// `=` assignment. CheckNoMissingPaths is the post-query guard that enforces
// the "strict by default" contract — absent paths are only created when
// --create-missing is explicitly set.

import (
	"context"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/query"
)

// helper builds a KindMap Value from alternating key/value pairs.
func mapVal(pairs ...any) omap.Value {
	d := omap.New()
	for i := 0; i < len(pairs); i += 2 {
		k := pairs[i].(string)
		v := pairs[i+1].(omap.Value)
		d.Set(k, v)
	}
	return omap.MapValue(d)
}

func TestCheckNoMissingPaths_ExistingKeyModified(t *testing.T) {
	t.Parallel()
	// Modifying an existing key's value is allowed.
	in := mapVal("a", omap.IntValue(1))
	out := mapVal("a", omap.IntValue(99))
	if err := query.CheckNoMissingPaths(in, out); err != nil {
		t.Fatalf("unexpected error for existing-key modification: %v", err)
	}
}

func TestCheckNoMissingPaths_KeyDeleted(t *testing.T) {
	t.Parallel()
	// Deleting a key (key in in, absent from out) is allowed.
	in := mapVal("a", omap.IntValue(1), "b", omap.IntValue(2))
	out := mapVal("a", omap.IntValue(1))
	if err := query.CheckNoMissingPaths(in, out); err != nil {
		t.Fatalf("unexpected error for key deletion: %v", err)
	}
}

func TestCheckNoMissingPaths_NewTopLevelKey(t *testing.T) {
	t.Parallel()
	// Adding a new top-level key should be rejected.
	in := mapVal("a", omap.IntValue(1))
	out := mapVal("a", omap.IntValue(1), "b", omap.IntValue(2))
	if err := query.CheckNoMissingPaths(in, out); err == nil {
		t.Fatal("expected error for new top-level key, got nil")
	}
}

func TestCheckNoMissingPaths_NewNestedKey(t *testing.T) {
	t.Parallel()
	// Adding a new nested key (.b.c on {"a":1}) should be rejected.
	in := mapVal("a", omap.IntValue(1))

	// Build output: {"a":1, "b": {"c": 2}}
	inner := omap.New()
	inner.Set("c", omap.IntValue(2))
	out := mapVal("a", omap.IntValue(1), "b", omap.MapValue(inner))

	if err := query.CheckNoMissingPaths(in, out); err == nil {
		t.Fatal("expected error for new nested key, got nil")
	}
}

func TestCheckNoMissingPaths_NewKeyInExistingMap(t *testing.T) {
	t.Parallel()
	// Adding a new key inside an existing nested map should be rejected.
	// in: {"a": {"x": 1}}
	// out: {"a": {"x": 1, "y": 2}}   — "y" is new inside "a"
	innerIn := omap.New()
	innerIn.Set("x", omap.IntValue(1))
	in := mapVal("a", omap.MapValue(innerIn))

	innerOut := omap.New()
	innerOut.Set("x", omap.IntValue(1))
	innerOut.Set("y", omap.IntValue(2))
	out := mapVal("a", omap.MapValue(innerOut))

	if err := query.CheckNoMissingPaths(in, out); err == nil {
		t.Fatal("expected error for new key in existing nested map, got nil")
	}
}

func TestCheckNoMissingPaths_IdentityQuery(t *testing.T) {
	t.Parallel()
	// Identity query (no change) must not produce an error.
	in := mapVal("a", omap.IntValue(1), "b", omap.IntValue(2))
	out := mapVal("a", omap.IntValue(1), "b", omap.IntValue(2))
	if err := query.CheckNoMissingPaths(in, out); err != nil {
		t.Fatalf("unexpected error for identity query: %v", err)
	}
}

func TestCheckNoMissingPaths_ScalarOutput(t *testing.T) {
	t.Parallel()
	// Scalar (non-map) output has no keys to check; should not error.
	in := omap.IntValue(1)
	out := omap.IntValue(99)
	if err := query.CheckNoMissingPaths(in, out); err != nil {
		t.Fatalf("unexpected error for scalar output: %v", err)
	}
}

func TestCheckNoMissingPaths_ReshapeMapToSeq(t *testing.T) {
	t.Parallel()
	// Reshaping a map to a seq (e.g. `.items` on a map with an items key)
	// is not a missing-path assignment; the kinds differ so the check is
	// skipped and no error is returned.
	in := mapVal("items", omap.SeqValue(omap.IntValue(1)))
	out := omap.SeqValue(omap.IntValue(1)) // .items extracted
	if err := query.CheckNoMissingPaths(in, out); err != nil {
		t.Fatalf("unexpected error for map→seq reshape: %v", err)
	}
}

func TestCheckNoMissingPaths_ReshapeMapToScalar(t *testing.T) {
	t.Parallel()
	// Extracting a scalar value from a map (e.g. `.x` on {"x":1}) is not
	// a missing-path assignment; the kinds differ so the check is skipped.
	in := mapVal("x", omap.IntValue(1))
	out := omap.IntValue(1) // .x extracted
	if err := query.CheckNoMissingPaths(in, out); err != nil {
		t.Fatalf("unexpected error for map→scalar reshape: %v", err)
	}
}

func TestCheckNoMissingPaths_ArrayNewElement(t *testing.T) {
	t.Parallel()
	// Appending to an array beyond original length is rejected.
	in := omap.SeqValue(omap.IntValue(1))
	out := omap.SeqValue(omap.IntValue(1), omap.IntValue(2))
	if err := query.CheckNoMissingPaths(in, out); err == nil {
		t.Fatal("expected error for new array element beyond original length, got nil")
	}
}

func TestCheckNoMissingPaths_ArrayInBoundsModify(t *testing.T) {
	t.Parallel()
	// Modifying an element within original bounds is allowed.
	in := omap.SeqValue(omap.IntValue(1), omap.IntValue(2))
	out := omap.SeqValue(omap.IntValue(99), omap.IntValue(2))
	if err := query.CheckNoMissingPaths(in, out); err != nil {
		t.Fatalf("unexpected error for in-bounds array element modification: %v", err)
	}
}

// TestCheckNoMissingPaths_ViaRunValue exercises the full query→check path.
// This is a round-trip integration test verifying that gojq's transparent
// path creation is caught by CheckNoMissingPaths.
func TestCheckNoMissingPaths_ViaRunValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		src       omap.Value
		q         string
		wantError bool
	}{
		{
			name:      "create_nested_missing",
			src:       mapVal("a", omap.IntValue(1)),
			q:         ".b.c = 2",
			wantError: true,
		},
		{
			name:      "modify_existing",
			src:       mapVal("a", omap.IntValue(1)),
			q:         ".a = 99",
			wantError: false,
		},
		{
			name: "nested_existing_modify",
			src: func() omap.Value {
				inner := omap.New()
				inner.Set("b", omap.IntValue(1))
				return mapVal("a", omap.MapValue(inner))
			}(),
			q:         ".a.b = 99",
			wantError: false,
		},
		{
			name:      "identity",
			src:       mapVal("x", omap.IntValue(42)),
			q:         ".",
			wantError: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, err := query.RunValue(context.Background(), tc.src, tc.q)
			if err != nil {
				t.Fatalf("RunValue: %v", err)
			}
			mpErr := query.CheckNoMissingPaths(tc.src, out)
			if tc.wantError && mpErr == nil {
				t.Fatalf("expected missing-path error, got nil")
			}
			if !tc.wantError && mpErr != nil {
				t.Fatalf("unexpected missing-path error: %v", mpErr)
			}
		})
	}
}
