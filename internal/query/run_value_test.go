package query_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/query"
)

// BUG-0001: RunValue/RunValueWithArgs accept any top-level omap.Value, not
// only a map-rooted *omap.Doc. This lets the CLI operate on documents whose
// root is a JSON array or scalar.

func TestRunValue_IdentityOnArray(t *testing.T) {
	t.Parallel()
	in := omap.SeqValue(omap.IntValue(1), omap.IntValue(2), omap.IntValue(3))
	out, err := query.RunValue(context.Background(), in, ".")
	if err != nil {
		t.Fatalf("RunValue: %v", err)
	}
	if out.Kind != omap.KindSeq {
		t.Fatalf("kind=%v want Seq", out.Kind)
	}
	if len(out.Seq) != 3 {
		t.Fatalf("len=%d want 3", len(out.Seq))
	}
}

func TestRunValue_IndexIntoArray(t *testing.T) {
	t.Parallel()
	in := omap.SeqValue(omap.IntValue(1), omap.IntValue(2), omap.IntValue(3))
	out, err := query.RunValue(context.Background(), in, ".[0]")
	if err != nil {
		t.Fatalf("RunValue: %v", err)
	}
	if out.Kind != omap.KindNum {
		t.Fatalf("kind=%v want Num", out.Kind)
	}
	if out.Num.String() != "1" {
		t.Fatalf("num=%q want 1", out.Num.String())
	}
}

func TestRunValue_IdentityOnScalar(t *testing.T) {
	t.Parallel()
	in := omap.NumValue(json.Number("42"))
	out, err := query.RunValue(context.Background(), in, ".")
	if err != nil {
		t.Fatalf("RunValue: %v", err)
	}
	if out.Kind != omap.KindNum || out.Num.String() != "42" {
		t.Fatalf("got %+v", out)
	}
}

func TestRunValue_IdentityOnMap(t *testing.T) {
	t.Parallel()
	d := omap.New()
	d.Set("a", omap.IntValue(1))
	d.Set("b", omap.IntValue(2))
	in := omap.MapValue(d)
	out, err := query.RunValue(context.Background(), in, ".")
	if err != nil {
		t.Fatalf("RunValue: %v", err)
	}
	if out.Kind != omap.KindMap {
		t.Fatalf("kind=%v want Map", out.Kind)
	}
	keys := out.Map.Keys()
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("keys=%v want [a b]", keys)
	}
}
