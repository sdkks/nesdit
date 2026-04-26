package omaptest

import (
	"fmt"

	"github.com/sdkks/nesdit/internal/omap"
)

// Equal reports whether a and b are structurally equal including key order.
// Returns (true, "") on equality, or (false, reason) describing the first
// divergence for test-failure messages. Tag is compared for string scalars
// only when both sides have a non-empty tag (so formats that do not preserve
// tags can still compare equal against a canonical doc).
func Equal(a, b omap.Value) (bool, string) {
	return eq("$", a, b)
}

// EqualDocs compares two docs at root.
func EqualDocs(a, b *omap.Doc) (bool, string) {
	return eq("$", omap.MapValue(a), omap.MapValue(b))
}

func eq(path string, a, b omap.Value) (bool, string) {
	if a.Kind != b.Kind {
		return false, fmt.Sprintf("%s: kind mismatch: %v vs %v", path, a.Kind, b.Kind)
	}
	switch a.Kind {
	case omap.KindNull:
		return true, ""
	case omap.KindBool:
		if a.Bool != b.Bool {
			return false, fmt.Sprintf("%s: bool %v vs %v", path, a.Bool, b.Bool)
		}
		return true, ""
	case omap.KindNum:
		if a.Num.String() != b.Num.String() {
			return false, fmt.Sprintf("%s: num %q vs %q", path, a.Num.String(), b.Num.String())
		}
		return true, ""
	case omap.KindStr:
		if a.Str != b.Str {
			return false, fmt.Sprintf("%s: str %q vs %q", path, a.Str, b.Str)
		}
		if a.Tag != "" && b.Tag != "" && a.Tag != b.Tag {
			return false, fmt.Sprintf("%s: tag %q vs %q", path, a.Tag, b.Tag)
		}
		return true, ""
	case omap.KindSeq:
		if len(a.Seq) != len(b.Seq) {
			return false, fmt.Sprintf("%s: seq len %d vs %d", path, len(a.Seq), len(b.Seq))
		}
		for i := range a.Seq {
			if ok, r := eq(fmt.Sprintf("%s[%d]", path, i), a.Seq[i], b.Seq[i]); !ok {
				return false, r
			}
		}
		return true, ""
	case omap.KindMap:
		ka, kb := a.Map.Keys(), b.Map.Keys()
		if len(ka) != len(kb) {
			return false, fmt.Sprintf("%s: map len %d vs %d (keys a=%v b=%v)", path, len(ka), len(kb), ka, kb)
		}
		for i := range ka {
			if ka[i] != kb[i] {
				return false, fmt.Sprintf("%s: key order at %d: %q vs %q (a=%v b=%v)", path, i, ka[i], kb[i], ka, kb)
			}
			va, _ := a.Map.Get(ka[i])
			vb, _ := b.Map.Get(kb[i])
			if ok, r := eq(fmt.Sprintf("%s.%s", path, ka[i]), va, vb); !ok {
				return false, r
			}
		}
		return true, ""
	}
	return false, fmt.Sprintf("%s: unknown kind %v", path, a.Kind)
}
