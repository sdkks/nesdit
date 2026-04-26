package omap

import (
	"fmt"
	"strconv"
	"strings"
)

// Path is a JSON-path-like location used in encode errors (FR-19, NFR-5).
// The root is represented as "$". Map segments are ".key" when the key
// matches ^[A-Za-z_][A-Za-z0-9_]*$, otherwise ["key"] with quoted key.
// Array indices are "[N]".
type Path struct {
	segs []string // each segment already includes its leading "." or "[..]"
}

// RootPath returns a fresh Path rooted at "$".
func RootPath() Path { return Path{} }

// MapStep returns p extended by a map key access.
func (p Path) MapStep(key string) Path {
	seg := encodeKey(key)
	out := Path{segs: make([]string, len(p.segs), len(p.segs)+1)}
	copy(out.segs, p.segs)
	out.segs = append(out.segs, seg)
	return out
}

// SeqStep returns p extended by a sequence/array index access.
func (p Path) SeqStep(i int) Path {
	seg := "[" + strconv.Itoa(i) + "]"
	out := Path{segs: make([]string, len(p.segs), len(p.segs)+1)}
	copy(out.segs, p.segs)
	out.segs = append(out.segs, seg)
	return out
}

// String renders the path as a JSON-path string, e.g. $.users[2].score.
func (p Path) String() string {
	var b strings.Builder
	b.WriteByte('$')
	for _, s := range p.segs {
		b.WriteString(s)
	}
	return b.String()
}

func encodeKey(k string) string {
	if isBareKey(k) {
		return "." + k
	}
	// Quote for [""] form.
	return "[" + strconv.Quote(k) + "]"
}

func isBareKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}

// EncodeError is the canonical path-aware encode error used by the JSON,
// YAML, and TOML encoders when a value cannot be represented in the target
// format (FR-19, NFR-5).
//
// Error renders as "<path>: <kind> is not representable in <format>" where
// <path> is a JSON-path (FR-19 convention) like "$.users[2].score".
type EncodeError struct {
	Path   Path
	Kind   string // human description: "null", "NaN", "+Inf", "-Inf", "yaml !!binary", etc.
	Format string // "json", "yaml", "toml"
	Cause  error  // optional underlying error
}

// Error satisfies the error interface.
func (e *EncodeError) Error() string {
	msg := fmt.Sprintf("%s: %s is not representable in %s", e.Path.String(), e.Kind, e.Format)
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap returns the cause for errors.Is / errors.As.
func (e *EncodeError) Unwrap() error { return e.Cause }

// WalkValue visits every leaf in v (and for compound kinds, recursively every
// leaf in their children) invoking check at each scalar node with its Path.
// If check returns a non-nil *EncodeError, WalkValue returns it immediately
// without visiting further nodes. WalkValue also invokes check on map values
// themselves so that KindNull at a map position is reportable (TOML cannot
// represent null anywhere, including at a map slot).
//
// This helper exists so each encoder (json, yaml, toml) can express its
// per-format representability constraint as a short leaf predicate, without
// re-implementing the tree walk.
func WalkValue(p Path, v Value, check func(Path, Value) *EncodeError) *EncodeError {
	if err := check(p, v); err != nil {
		return err
	}
	switch v.Kind {
	case KindSeq:
		for i, it := range v.Seq {
			if err := WalkValue(p.SeqStep(i), it, check); err != nil {
				return err
			}
		}
	case KindMap:
		if v.Map == nil {
			return nil
		}
		for _, k := range v.Map.Keys() {
			sub, _ := v.Map.Get(k)
			if err := WalkValue(p.MapStep(k), sub, check); err != nil {
				return err
			}
		}
	}
	return nil
}
