// Package toml decodes and encodes TOML documents into omap.Doc.
//
// Decoding uses go-toml/v2's unstable AST parser (github.com/pelletier/
// go-toml/v2/unstable) so the source key order is preserved into the
// omap.Doc (NFR-3). Encoding is performed by a bespoke writer that emits
// TOML in the omap's insertion order — go-toml/v2's stable Marshal sorts
// map keys alphabetically, which would reshape Doc output.
//
// Before any bytes are written, Encode walks the document and rejects any
// value TOML cannot represent — null, NaN, +/-Inf — with a path-aware
// *omap.EncodeError (FR-19). Comments are not preserved (NFR-6).
package toml

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	tomlunstable "github.com/pelletier/go-toml/v2/unstable"

	"github.com/sdkks/nesdit/internal/format"
	"github.com/sdkks/nesdit/internal/omap"
)

// Decode reads a single TOML document from r and returns its top-level
// table as *omap.Doc, preserving source key order. No resource bounds
// are applied; CLI callers MUST use DecodeValueWithLimits.
func Decode(r io.Reader) (*omap.Doc, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("toml: read: %w", err)
	}
	return decodeBytes(data, 0)
}

// DecodeValue reads a single TOML document and returns the result as an
// omap.Value. Per the TOML spec the root MUST be a table — unlike JSON and
// YAML, TOML does not permit sequence/scalar tops (BUG-0001 preserves this
// constraint). DecodeValue therefore always returns a KindMap value when
// successful; non-table tops are rejected by the underlying parser.
//
// DecodeValue applies no resource bounds. CLI callers MUST use
// DecodeValueWithLimits; this unlimited form exists for tests and in-
// process callers that have already sized their input.
func DecodeValue(r io.Reader) (omap.Value, error) {
	return DecodeValueWithLimits(r, format.Limits{})
}

// DecodeValueWithLimits is DecodeValue with STORY-0008 resource bounds.
// Applies limits.MaxBytes via format.ReadAllLimited before parsing, then
// enforces limits.MaxDepth while walking the AST. The YAML node count cap
// (MaxYAMLNodes) is YAML-specific and ignored here.
//
// A zero Limits value means "no bounds" — useful for tests.
func DecodeValueWithLimits(r io.Reader, limits format.Limits) (omap.Value, error) {
	data, err := format.ReadAllLimited(r, limits.MaxBytes, "toml")
	if err != nil {
		return omap.Value{}, err
	}
	d, err := decodeBytes(data, limits.MaxDepth)
	if err != nil {
		return omap.Value{}, err
	}
	return omap.MapValue(d), nil
}

// Encode writes d as a TOML document to w using the key insertion order of
// every *omap.Doc it contains. Returns *omap.EncodeError when a value
// cannot be represented in TOML (null, NaN, +/-Inf).
func Encode(w io.Writer, d *omap.Doc) error {
	if err := checkTOMLRepresentable(omap.MapValue(d)); err != nil {
		return err
	}
	return newTOMLWriter(w).writeDoc(d)
}

// EncodeValue writes any omap.Value as a TOML document. Because TOML spec
// requires a top-level table, non-map roots (sequences, scalars, null) are
// rejected with a clear error — this preserves the TOML constraint while
// matching the format-neutral API added for BUG-0001 on JSON/YAML.
func EncodeValue(w io.Writer, v omap.Value) error {
	if v.Kind != omap.KindMap {
		return fmt.Errorf("toml: top-level value must be a table, got %s (TOML spec does not permit top-level %s)", kindName(v.Kind), kindName(v.Kind))
	}
	if v.Map == nil {
		return fmt.Errorf("toml: top-level table is nil")
	}
	return Encode(w, v.Map)
}

func kindName(k omap.Kind) string {
	switch k {
	case omap.KindNull:
		return "null"
	case omap.KindBool:
		return "bool"
	case omap.KindNum:
		return "number"
	case omap.KindStr:
		return "string"
	case omap.KindSeq:
		return "array"
	case omap.KindMap:
		return "table"
	default:
		return "unknown"
	}
}

// ----------------------------- decode -----------------------------

// decodeBytes parses data into an *omap.Doc, tracking the cursor depth so
// the STORY-0008 M3 depth cap can reject pathological nesting. A maxDepth
// value <= 0 disables the cap; otherwise a [a.b.c]-style table header
// counts as depth 3, a [[a.b]] array-of-tables counts as depth 3 on its
// element table (header depth 2 + 1 for the table inside the array), and
// inline tables/arrays inside values extend the depth further via
// astValue.
func decodeBytes(data []byte, maxDepth int) (*omap.Doc, error) {
	p := &tomlunstable.Parser{}
	p.Reset(data)

	root := omap.New()
	// cursor is the current table into which KeyValue expressions insert.
	cursor := root
	cursorDepth := 0 // depth of the table cursor points at

	for p.NextExpression() {
		expr := p.Expression()
		switch expr.Kind {
		case tomlunstable.KeyValue:
			if err := applyKeyValue(p, cursor, expr, cursorDepth, maxDepth); err != nil {
				return nil, err
			}
		case tomlunstable.Table:
			parts := keyParts(expr.Key())
			if maxDepth > 0 && len(parts) > maxDepth {
				return nil, &format.LimitError{
					Format:   "toml",
					Kind:     format.LimitDepth,
					Limit:    int64(maxDepth),
					Observed: int64(len(parts)),
				}
			}
			sub, err := ensurePath(root, parts)
			if err != nil {
				return nil, err
			}
			cursor = sub
			cursorDepth = len(parts)
		case tomlunstable.ArrayTable:
			parts := keyParts(expr.Key())
			if len(parts) == 0 {
				return nil, fmt.Errorf("toml: array-of-tables with empty key")
			}
			// [[a.b]] opens a table inside the array at parts; that
			// inner table sits one level deeper than the array itself.
			if maxDepth > 0 && len(parts)+1 > maxDepth {
				return nil, &format.LimitError{
					Format:   "toml",
					Kind:     format.LimitDepth,
					Limit:    int64(maxDepth),
					Observed: int64(len(parts) + 1),
				}
			}
			parent, err := ensurePath(root, parts[:len(parts)-1])
			if err != nil {
				return nil, err
			}
			last := parts[len(parts)-1]
			existing, ok := parent.Get(last)
			var seq []omap.Value
			if ok {
				if existing.Kind != omap.KindSeq {
					return nil, fmt.Errorf("toml: [[%s]] conflicts with non-array at %q", strings.Join(parts, "."), last)
				}
				seq = existing.Seq
			}
			newTbl := omap.New()
			seq = append(seq, omap.MapValue(newTbl))
			parent.Set(last, omap.Value{Kind: omap.KindSeq, Seq: seq})
			cursor = newTbl
			cursorDepth = len(parts) + 1
		default:
			return nil, fmt.Errorf("toml: unexpected top-level node kind %v", expr.Kind)
		}
	}
	if err := p.Error(); err != nil {
		return nil, fmt.Errorf("toml: %w", err)
	}
	return root, nil
}

// ensurePath navigates or creates a chain of sub-tables under root and
// returns the leaf table.
func ensurePath(root *omap.Doc, parts []string) (*omap.Doc, error) {
	cur := root
	for _, p := range parts {
		existing, ok := cur.Get(p)
		if !ok {
			next := omap.New()
			cur.Set(p, omap.MapValue(next))
			cur = next
			continue
		}
		switch existing.Kind {
		case omap.KindMap:
			cur = existing.Map
		case omap.KindSeq:
			// Entering an [[array_of_tables]] path — extend into the last table.
			if len(existing.Seq) == 0 {
				return nil, fmt.Errorf("toml: path %q entered empty array-of-tables", p)
			}
			last := existing.Seq[len(existing.Seq)-1]
			if last.Kind != omap.KindMap {
				return nil, fmt.Errorf("toml: array-of-tables %q contains non-table", p)
			}
			cur = last.Map
		default:
			return nil, fmt.Errorf("toml: path %q is a scalar, not a table", p)
		}
	}
	return cur, nil
}

// applyKeyValue inserts a KeyValue expression into the cursor table, honoring
// dotted keys (e.g. a.b.c = 1 creates implicit sub-tables along the way).
// cursorDepth is the depth at which cursor sits (0 = root table); the leaf
// key sits at cursorDepth + len(parts). maxDepth <= 0 disables the cap.
func applyKeyValue(p *tomlunstable.Parser, cursor *omap.Doc, kv *tomlunstable.Node, cursorDepth, maxDepth int) error {
	parts := keyParts(kv.Key())
	if len(parts) == 0 {
		return fmt.Errorf("toml: keyvalue with empty key")
	}
	leafDepth := cursorDepth + len(parts)
	if maxDepth > 0 && leafDepth > maxDepth {
		return &format.LimitError{
			Format:   "toml",
			Kind:     format.LimitDepth,
			Limit:    int64(maxDepth),
			Observed: int64(leafDepth),
		}
	}
	parent, err := ensurePath(cursor, parts[:len(parts)-1])
	if err != nil {
		return err
	}
	val, err := astValue(p, kv.Value(), leafDepth, maxDepth)
	if err != nil {
		return err
	}
	parent.Set(parts[len(parts)-1], val)
	return nil
}

func keyParts(it tomlunstable.Iterator) []string {
	var out []string
	for it.Next() {
		n := it.Node()
		out = append(out, string(n.Data))
	}
	return out
}

// astValue converts a TOML AST value node to an omap.Value. depth is the
// nesting depth of the value itself (0 at top-level keys); maxDepth <= 0
// disables the depth cap. Container values (arrays, inline tables) recurse
// one level deeper; scalar values do not advance depth.
func astValue(p *tomlunstable.Parser, n *tomlunstable.Node, depth, maxDepth int) (omap.Value, error) {
	switch n.Kind {
	case tomlunstable.String:
		return omap.StrValue(string(n.Data)), nil
	case tomlunstable.Bool:
		return omap.BoolValue(string(n.Data) == "true"), nil
	case tomlunstable.Integer:
		return omap.NumValue(stdjson.Number(string(n.Data))), nil
	case tomlunstable.Float:
		return omap.NumValue(stdjson.Number(string(n.Data))), nil
	case tomlunstable.LocalDate, tomlunstable.LocalTime, tomlunstable.LocalDateTime, tomlunstable.DateTime:
		return omap.StrValueTagged(string(n.Data), "!!timestamp"), nil
	case tomlunstable.Array:
		if maxDepth > 0 && depth+1 > maxDepth {
			return omap.Value{}, &format.LimitError{
				Format:   "toml",
				Kind:     format.LimitDepth,
				Limit:    int64(maxDepth),
				Observed: int64(depth + 1),
			}
		}
		var items []omap.Value
		it := n.Children()
		for it.Next() {
			ch := it.Node()
			v, err := astValue(p, ch, depth+1, maxDepth)
			if err != nil {
				return omap.Value{}, err
			}
			items = append(items, v)
		}
		return omap.Value{Kind: omap.KindSeq, Seq: items}, nil
	case tomlunstable.InlineTable:
		// An inline table IS the value at `depth`; its keys sit at
		// depth+1. applyKeyValue's cursorDepth is the depth of the
		// containing table (so `leafDepth = cursorDepth + len(parts)`
		// lands on the key's actual depth). Pass `depth` itself, not
		// `depth+1`, so a single-part child of an inline table at
		// depth=d ends up at leafDepth=d+1 — matching the intuition
		// that root -> a -> b -> c is four levels, not five.
		d := omap.New()
		it := n.Children()
		for it.Next() {
			kv := it.Node() // each child is a KeyValue
			if kv.Kind != tomlunstable.KeyValue {
				return omap.Value{}, fmt.Errorf("toml: inline table child kind=%v", kv.Kind)
			}
			if err := applyKeyValue(p, d, kv, depth, maxDepth); err != nil {
				return omap.Value{}, err
			}
		}
		return omap.MapValue(d), nil
	default:
		return omap.Value{}, fmt.Errorf("toml: unexpected value kind %v", n.Kind)
	}
}

// ----------------------------- encode -----------------------------

// checkTOMLRepresentable walks v and errors out on null, NaN, or +/-Inf.
// The tree walk is delegated to omap.WalkValue; this function only supplies
// the per-leaf predicate.
func checkTOMLRepresentable(v omap.Value) *omap.EncodeError {
	return omap.WalkValue(omap.RootPath(), v, func(p omap.Path, v omap.Value) *omap.EncodeError {
		switch v.Kind {
		case omap.KindNull:
			return &omap.EncodeError{Path: p, Kind: "null", Format: "toml"}
		case omap.KindNum:
			s := v.Num.String()
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return nil
			}
			switch {
			case math.IsNaN(f):
				return &omap.EncodeError{Path: p, Kind: "NaN", Format: "toml"}
			case math.IsInf(f, 1):
				return &omap.EncodeError{Path: p, Kind: "+Inf", Format: "toml"}
			case math.IsInf(f, -1):
				return &omap.EncodeError{Path: p, Kind: "-Inf", Format: "toml"}
			}
		}
		return nil
	})
}

type tomlWriter struct {
	w   io.Writer
	err error
}

func newTOMLWriter(w io.Writer) *tomlWriter { return &tomlWriter{w: w} }

func (tw *tomlWriter) writeStr(s string) {
	if tw.err != nil {
		return
	}
	_, tw.err = io.WriteString(tw.w, s)
}

// writeDoc writes the root table. It emits scalar / array / inline-table
// key-values first, then sub-tables (as [header] sections) in insertion
// order. Arrays of tables become [[header]] blocks per element.
func (tw *tomlWriter) writeDoc(d *omap.Doc) error {
	tw.writeTable(nil, d)
	return tw.err
}

// writeTable emits a flat table: every key is emitted as an inline
// key-value in insertion order. Nested maps are emitted as inline tables
// ({...}) and arrays of tables are emitted as inline arrays of inline
// tables ([{...}, {...}]). This avoids TOML's ordering constraint for
// [header] sections (where scalars must precede nested sections) so the
// encoded form preserves omap.Doc insertion order byte-for-byte across
// round-trips (NFR-3, NFR-2 idempotency). The trade-off is a less
// "TOML-idiomatic" output for deeply nested documents; in exchange we
// guarantee that decode→encode→decode is a stable fixed point.
func (tw *tomlWriter) writeTable(_ []string, d *omap.Doc) {
	for _, k := range d.Keys() {
		v, _ := d.Get(k)
		tw.writeStr(encodeKey(k))
		tw.writeStr(" = ")
		tw.writeInlineValue(v)
		tw.writeStr("\n")
	}
}

// writeInlineValue emits a single value in inline form (strings, numbers,
// bools, arrays, inline tables).
func (tw *tomlWriter) writeInlineValue(v omap.Value) {
	switch v.Kind {
	case omap.KindBool:
		if v.Bool {
			tw.writeStr("true")
		} else {
			tw.writeStr("false")
		}
	case omap.KindNum:
		tw.writeStr(v.Num.String())
	case omap.KindStr:
		if v.Tag == "!!timestamp" {
			tw.writeStr(v.Str) // emit as native datetime
			return
		}
		tw.writeStr(encodeTOMLString(v.Str))
	case omap.KindSeq:
		tw.writeStr("[")
		for i, it := range v.Seq {
			if i > 0 {
				tw.writeStr(", ")
			}
			tw.writeInlineValue(it)
		}
		tw.writeStr("]")
	case omap.KindMap:
		tw.writeStr("{")
		first := true
		for _, k := range v.Map.Keys() {
			if !first {
				tw.writeStr(", ")
			}
			first = false
			tw.writeStr(encodeKey(k))
			tw.writeStr(" = ")
			sub, _ := v.Map.Get(k)
			tw.writeInlineValue(sub)
		}
		tw.writeStr("}")
	case omap.KindNull:
		// Unreachable — checkTOMLRepresentable rejects earlier.
		tw.err = fmt.Errorf("toml: null reached encoder (bug)")
	}
}

// encodeKey returns a TOML key token — bare when safe, otherwise quoted.
func encodeKey(k string) string {
	if isBareTOMLKey(k) {
		return k
	}
	return encodeTOMLString(k)
}

func isBareTOMLKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// encodeTOMLString writes s as a basic double-quoted TOML string with the
// standard escapes.
func encodeTOMLString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
