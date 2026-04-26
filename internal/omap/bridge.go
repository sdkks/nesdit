package omap

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
)

// ToAny converts d into the `any`-shaped representation go-jq accepts:
// nested values become map[string]any, []any, json.Number, string, bool,
// or nil. Scalar tag information (Value.Tag) is dropped — go-jq has no
// tag concept — but callers reconcile the post-query tree against the
// pre-query *Doc snapshot via FromAny, and that snapshot carries the
// original tags forward for keys that were not touched.
//
// Numbers are emitted as json.Number so int64 / arbitrary-precision
// values (NFR-4, e.g. 9007199254740993) survive the bridge.
func (d *Doc) ToAny() map[string]any {
	if d == nil {
		return nil
	}
	out := make(map[string]any, len(d.keys))
	for _, k := range d.keys {
		v, _ := d.Get(k)
		out[k] = valueToAny(v)
	}
	return out
}

func valueToAny(v Value) any {
	switch v.Kind {
	case KindNull:
		return nil
	case KindBool:
		return v.Bool
	case KindNum:
		return v.Num
	case KindStr:
		return v.Str
	case KindSeq:
		out := make([]any, len(v.Seq))
		for i, it := range v.Seq {
			out[i] = valueToAny(it)
		}
		return out
	case KindMap:
		if v.Map == nil {
			return map[string]any{}
		}
		return v.Map.ToAny()
	}
	return nil
}

// ValueToAny converts any omap.Value into the `any`-shaped representation
// go-jq accepts. Mirrors (*Doc).ToAny but handles non-map tops: arrays become
// []any, scalars stay as their Go equivalents, and maps delegate to Doc.ToAny.
//
// BUG-0001: JSON (RFC 8259) and YAML (1.2) permit any value at the document
// root, not just maps. This function is the map-agnostic counterpart to
// ToAny so the CLI pipeline can carry a top-level array or scalar from
// decode through query to encode.
func ValueToAny(v Value) any { return valueToAny(v) }

// ValueFromAny converts a go-jq result rooted at any value type back into
// an omap.Value. It uses prev (the pre-query snapshot at the same path) to
// preserve key insertion order in nested maps (same reconciliation rules as
// FromAny — see its doc for DR-007 positional seq reconciliation). Pass the
// zero Value when no prev exists.
//
// BUG-0001: the map-rooted FromAny remains for callers that specifically
// want *Doc; this is the top-level-agnostic version.
func ValueFromAny(v any, prev Value) Value { return anyToValue(v, prev) }

// FromAny converts a go-jq result (map[string]any, []any, json.Number,
// string, bool, nil, and the numeric Go types go-jq produces internally
// — int, float64, *big.Int) back into an *omap.Doc. Key order is
// reconciled against prev: at every map boundary, keys present in prev
// (in prev order) emit first, followed by keys new to the jq result in
// lexicographic order (SPEC-0001 §2, Open Question 7).
//
// Recursion rules:
//   - Map value at a key: if prev has a *Doc at the same key, use it as
//     the child prev; otherwise the child prev is nil (all keys become
//     "new" and sort lexicographically).
//   - Seq value at a key: iterate element-wise; for each element, if
//     prev has a Seq at the same key and a same-index element, use that
//     element's inner Doc (if any) as the per-element prev; else nil.
//
// Scalar tags from prev are preserved when the jq result still carries
// the same scalar kind at the same leaf, so e.g. !!timestamp strings
// that jq did not touch round-trip with their tag intact.
//
// ARRAY-OF-MAPS RECONCILIATION IS POSITIONAL (DR-007, SPEC-0001).
// For []any values, element-wise reconciliation is by index: result[i]
// inherits map-order from prev[i]. This is correct-by-construction for
// identity, scalar-assignment, and del() queries (result[i] IS the
// lineal descendant of prev[i]). Array-reshaping jq filters (sort_by,
// reverse, unique_by, map() producing restructured elements, slice
// operations) will produce key orders reflecting positional match,
// NOT element identity — deterministic but potentially surprising.
// See SPEC-0001 DR-007 for the full rationale and the lock-in corpus
// in internal/query.TestBridgeGoNoGo (json_dr007_reshape case). The
// v1.1 roadmap item tracks revisiting if real-world usage surfaces
// pain; do not change this behaviour without re-opening DR-007.
func FromAny(v any, prev *Doc) *Doc {
	m, ok := v.(map[string]any)
	if !ok {
		// go-jq sometimes returns non-map results for non-object
		// inputs; for nesdit's v1 the top level is always an object,
		// but we tolerate this by returning an empty doc rather than
		// panicking. The caller (internal/query.Run) enforces the
		// top-level-object invariant separately.
		return New()
	}
	return fromAnyMap(m, prev)
}

func fromAnyMap(m map[string]any, prev *Doc) *Doc {
	out := New()
	seen := make(map[string]bool, len(m))

	// 1. Emit keys from prev in prev order, when still present in m.
	if prev != nil {
		for _, k := range prev.Keys() {
			rawNew, ok := m[k]
			if !ok {
				continue
			}
			prevV, _ := prev.Get(k)
			out.Set(k, anyToValue(rawNew, prevV))
			seen[k] = true
		}
	}

	// 2. Collect new keys and emit them lexicographically.
	if len(seen) < len(m) {
		newKeys := make([]string, 0, len(m)-len(seen))
		for k := range m {
			if !seen[k] {
				newKeys = append(newKeys, k)
			}
		}
		sort.Strings(newKeys)
		for _, k := range newKeys {
			out.Set(k, anyToValue(m[k], Value{}))
		}
	}
	return out
}

// anyToValue converts a single go-jq leaf/branch into an omap.Value,
// using prev (a Value from the pre-query snapshot at the same path) to
// preserve ordering in nested maps/seqs and scalar tags on untouched
// leaves. prev.Kind == KindNull means "no prev at this path" for map
// and seq recursion.
func anyToValue(v any, prev Value) Value {
	switch x := v.(type) {
	case nil:
		return NullValue()
	case bool:
		return BoolValue(x)
	case string:
		// Preserve a tag from prev only when the prev was a string with
		// the same bytes — jq may have rewritten the string, and the
		// !!timestamp tag would be wrong for the new content.
		if prev.Kind == KindStr && prev.Str == x && prev.Tag != "" {
			return StrValueTagged(x, prev.Tag)
		}
		return StrValue(x)
	case json.Number:
		return NumValue(x)
	case int:
		return NumValue(json.Number(strconv.FormatInt(int64(x), 10)))
	case int64:
		return NumValue(json.Number(strconv.FormatInt(x, 10)))
	case float64:
		return NumValue(json.Number(strconv.FormatFloat(x, 'g', -1, 64)))
	case *big.Int:
		return NumValue(json.Number(x.String()))
	case *big.Float:
		return NumValue(json.Number(x.Text('g', -1)))
	case []any:
		var prevSeq []Value
		if prev.Kind == KindSeq {
			prevSeq = prev.Seq
		}
		out := make([]Value, len(x))
		for i, it := range x {
			var pv Value
			if i < len(prevSeq) {
				pv = prevSeq[i]
			}
			out[i] = anyToValue(it, pv)
		}
		return Value{Kind: KindSeq, Seq: out}
	case map[string]any:
		var pd *Doc
		if prev.Kind == KindMap {
			pd = prev.Map
		}
		return MapValue(fromAnyMap(x, pd))
	default:
		// Fallback: best-effort stringify. gojq should not produce
		// other concrete types for json-shaped values, but be safe.
		return StrValue(fmt.Sprintf("%v", v))
	}
}
