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

// EncodeErrorKind names the representability-failure kind reported by
// [EncodeError]. Today's four kinds are EncodeKindNull (only TOML rejects
// null) plus the three non-finite numeric tokens EncodeKindNaN,
// EncodeKindPosInf, EncodeKindNegInf. The enum is the single source of
// truth for these tokens and is designed to absorb the NFR-10 stderr
// event taxonomy (heterogeneous array, cross-format incompat, etc.) as
// later stories extend the set.
//
// Declared as a type alias to `string` so existing format-package call
// sites that write `Kind: "null"` keep compiling while new callers use
// the typed constants below. A later migration promotes this to a
// defined string type once every call site uses the constants (see
// TASK-0004 Followups).
type EncodeErrorKind = string

// Exported EncodeErrorKind constants for the kinds currently emitted by
// the JSON, YAML, and TOML encoders (TASK-0004). Values match the
// historical free-form strings so the error rendering and FR-19/NFR-5
// acceptance tests are unchanged.
const (
	// EncodeKindNull marks a KindNull encountered by a format that cannot
	// represent null (TOML).
	EncodeKindNull EncodeErrorKind = "null"
	// EncodeKindNaN marks a numeric NaN in a format that rejects it
	// (JSON per RFC 8259 §6, YAML-strict, TOML).
	EncodeKindNaN EncodeErrorKind = "NaN"
	// EncodeKindPosInf marks a numeric +Inf in a format that rejects it
	// (JSON, YAML-strict, TOML).
	EncodeKindPosInf EncodeErrorKind = "+Inf"
	// EncodeKindNegInf marks a numeric -Inf in a format that rejects it
	// (JSON, YAML-strict, TOML).
	EncodeKindNegInf EncodeErrorKind = "-Inf"
)

// EncodeError is the canonical path-aware encode error used by the JSON,
// YAML, and TOML encoders when a value cannot be represented in the target
// format (FR-19, NFR-5).
//
// Error renders as "<path>: <kind> is not representable in <format>" where
// <path> is a JSON-path (FR-19 convention) like "$.users[2].score".
type EncodeError struct {
	Path Path
	// Kind is the typed representability-failure kind (TASK-0004).
	// Use the Encode* constants — free-form strings still compile via
	// the type alias but are deprecated and will break once the type
	// is promoted to a defined type.
	Kind   EncodeErrorKind
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
		// Walk internal slices directly to avoid per-call Keys() clones.
		for i, k := range v.Map.keys {
			if err := WalkValue(p.MapStep(k), v.Map.vals[i], check); err != nil {
				return err
			}
		}
	}
	return nil
}
