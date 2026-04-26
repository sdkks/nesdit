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
func (d *Doc) Set(k string, v Value) {
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

// Keys returns the keys in insertion order. Callers MUST NOT mutate the
// returned slice.
func (d *Doc) Keys() []string {
	if d == nil {
		return nil
	}
	return d.keys
}

// Values returns a parallel slice of values in the same order as Keys.
// Callers MUST NOT mutate the returned slice.
func (d *Doc) Values() []Value {
	if d == nil {
		return nil
	}
	return d.vals
}

// At returns the i-th (key, value) pair by insertion order.
func (d *Doc) At(i int) (string, Value) {
	return d.keys[i], d.vals[i]
}
