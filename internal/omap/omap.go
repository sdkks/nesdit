// Package omap provides an ordered-map document type (Doc) and tagged-union
// value type (Value) used as the format-agnostic canonical representation of
// documents decoded from JSON, YAML, and TOML. It preserves key insertion
// order (NFR-3), integer precision via json.Number (NFR-4), and — where
// relevant — YAML scalar tag information needed by the YAML encoder.
//
// Comments are not preserved across any format's round-trip (NFR-6). This is
// an explicit non-goal for v1 of nesdit.
package omap

import (
	"encoding/json"
	"fmt"
	"slices"
)

// Kind enumerates the value kinds an omap.Value can hold.
type Kind int

const (
	// KindNull is the null/absent value.
	KindNull Kind = iota
	// KindBool is a boolean.
	KindBool
	// KindNum is a numeric value, stored as json.Number to preserve
	// int64 precision (NFR-4) and arbitrary decimal precision.
	KindNum
	// KindStr is a string.
	KindStr
	// KindSeq is a sequence (array / TOML array / YAML sequence / JSON array).
	KindSeq
	// KindMap is an ordered map (JSON object / YAML mapping / TOML table).
	KindMap
)

// Value is a tagged union over the data kinds an omap document can hold.
// Zero value is KindNull.
type Value struct {
	Kind Kind

	Bool bool
	Num  json.Number
	Str  string
	Seq  []Value
	Map  *Doc

	// Tag optionally carries a format-neutral scalar tag so tag-sensitive
	// values can round-trip faithfully. The tag vocabulary follows YAML
	// 1.2 short tags because the YAML encoder is the primary consumer,
	// but the field is format-neutral: the TOML decoder also sets
	// "!!timestamp" for TOML datetime/date/time scalars so they round-trip
	// without being re-parsed as strings. Known values:
	//
	//   - "!!str"       — explicit string (prevents YAML re-inferring
	//                     !!timestamp/!!int/!!float/etc. on decode).
	//   - "!!timestamp" — a timestamp-shaped scalar (set by YAML decoder
	//                     and by TOML decoder for datetime values).
	//   - "!!binary"    — a base64-encoded binary scalar.
	//
	// Empty string means "no explicit tag; infer on encode."
	Tag string
}

// NullValue returns a Value of KindNull.
func NullValue() Value { return Value{Kind: KindNull} }

// BoolValue returns a Value wrapping b.
func BoolValue(b bool) Value { return Value{Kind: KindBool, Bool: b} }

// NumValue returns a Value wrapping n as json.Number.
func NumValue(n json.Number) Value { return Value{Kind: KindNum, Num: n} }

// IntValue returns a Value wrapping the int64 i as a numeric value.
func IntValue(i int64) Value {
	return Value{Kind: KindNum, Num: json.Number(fmt.Sprintf("%d", i))}
}

// StrValue returns a Value wrapping s.
func StrValue(s string) Value { return Value{Kind: KindStr, Str: s} }

// StrValueTagged returns a Value wrapping s with the given scalar tag
// (see Value.Tag for the tag vocabulary).
func StrValueTagged(s, tag string) Value {
	return Value{Kind: KindStr, Str: s, Tag: tag}
}

// SeqValue returns a Value wrapping a sequence.
func SeqValue(items ...Value) Value { return Value{Kind: KindSeq, Seq: items} }

// MapValue returns a Value wrapping an ordered map.
func MapValue(d *Doc) Value { return Value{Kind: KindMap, Map: d} }

// Doc is an ordered string-keyed map preserving insertion order.
// Duplicate keys overwrite the previous value but retain the original
// insertion position.
//
// Nil-receiver policy (TASK-0004 API hygiene):
//   - Read methods (Len, Get, Has, Keys, Values, TryAt, Entries) are
//     safe to call on a nil *Doc and return the obvious zero result.
//   - Mutating methods (Set) panic on a nil *Doc — a nil *Doc has no
//     backing map to insert into, and the caller has a bug.
//   - Delete is a read-biased mutation: it is a no-op on a nil *Doc
//     or an absent key (idempotent-delete semantics).
//   - At panics on out-of-range or nil *Doc — use TryAt for the
//     bounds-checked form.
type Doc struct {
	keys  []string
	index map[string]int
	vals  []Value
}

// New returns an empty *Doc.
func New() *Doc {
	return &Doc{index: make(map[string]int)}
}

// Len returns the number of entries.
func (d *Doc) Len() int {
	if d == nil {
		return 0
	}
	return len(d.keys)
}

// Set inserts or updates key k with value v. If k is new, it is appended
// to the end of the key order; if k already exists, its value is replaced
// in place without changing its position.
//
// Panics if d is nil — construct a *Doc with [New] before calling Set.
func (d *Doc) Set(k string, v Value) {
	if d == nil {
		panic("omap: Set on nil *Doc")
	}
	if d.index == nil {
		d.index = make(map[string]int)
	}
	if i, ok := d.index[k]; ok {
		d.vals[i] = v
		return
	}
	d.index[k] = len(d.keys)
	d.keys = append(d.keys, k)
	d.vals = append(d.vals, v)
}

// Get returns the value for key k and true if present.
func (d *Doc) Get(k string) (Value, bool) {
	if d == nil || d.index == nil {
		return Value{}, false
	}
	i, ok := d.index[k]
	if !ok {
		return Value{}, false
	}
	return d.vals[i], true
}

// Has reports whether key k is present.
func (d *Doc) Has(k string) bool {
	if d == nil || d.index == nil {
		return false
	}
	_, ok := d.index[k]
	return ok
}

// Delete removes k from the map, preserving the order of the remaining keys.
// No-op if k is absent.
func (d *Doc) Delete(k string) {
	if d == nil || d.index == nil {
		return
	}
	i, ok := d.index[k]
	if !ok {
		return
	}
	d.keys = append(d.keys[:i], d.keys[i+1:]...)
	d.vals = append(d.vals[:i], d.vals[i+1:]...)
	delete(d.index, k)
	for j := i; j < len(d.keys); j++ {
		d.index[d.keys[j]] = j
	}
}

// Keys returns a copy of the keys in insertion order. Returns nil when d
// is nil or empty. Callers may mutate the returned slice — mutations do
// not affect the Doc (TASK-0004: previously the internal slice leaked).
//
// Per-call allocation is O(n). Hot-path consumers that walk every entry
// should prefer [Doc.Entries], which yields (key, value) pairs without
// allocating an intermediate slice.
func (d *Doc) Keys() []string {
	if d == nil || len(d.keys) == 0 {
		return nil
	}
	return slices.Clone(d.keys)
}

// Values returns a copy of the values in the same order as [Doc.Keys].
// Returns nil when d is nil or empty. Callers may mutate the returned
// slice — mutations do not affect the Doc (TASK-0004: previously the
// internal slice leaked).
//
// Per-call allocation is O(n). Prefer [Doc.Entries] for paired iteration.
func (d *Doc) Values() []Value {
	if d == nil || len(d.vals) == 0 {
		return nil
	}
	return slices.Clone(d.vals)
}

// At returns the i-th (key, value) pair by insertion order.
//
// Preconditions (panics on violation):
//   - d must be non-nil.
//   - i must satisfy 0 <= i < d.Len().
//
// Callers that cannot guarantee the preconditions should use [Doc.TryAt].
func (d *Doc) At(i int) (string, Value) {
	return d.keys[i], d.vals[i]
}

// TryAt returns the i-th (key, value) pair in insertion order, with
// ok=false if i is out of range or d is nil. This is the non-panicking
// companion to [Doc.At] (TASK-0004).
func (d *Doc) TryAt(i int) (k string, v Value, ok bool) {
	if d == nil || i < 0 || i >= len(d.keys) {
		return "", Value{}, false
	}
	return d.keys[i], d.vals[i], true
}

// Entries iterates (key, value) pairs in insertion order and invokes
// yield for each. Iteration stops early when yield returns false. The
// callable shape matches Go 1.23 range-over-func — once the module
// targets Go 1.23+, `for k, v := range d.Entries` will work without any
// API change.
//
// Entries does not allocate a keys slice per call; format encoders that
// walk every entry should prefer it over [Doc.Keys] + [Doc.Get].
//
// Safe on a nil *Doc (yields nothing).
func (d *Doc) Entries(yield func(k string, v Value) bool) {
	if d == nil {
		return
	}
	for i, k := range d.keys {
		if !yield(k, d.vals[i]) {
			return
		}
	}
}
