// Package json decodes and encodes a JSON document into omap.Doc while
// preserving key insertion order (NFR-3) and integer precision via
// json.Number (NFR-4). It uses the standard library's encoding/json
// Tokenizer API to walk the input in source order; map iteration is
// driven by omap.Doc.Keys on encode.
//
// Comments are not a JSON grammar feature and are not preserved (NFR-6).
package json

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/sdkks/nesdit/internal/format"
	"github.com/sdkks/nesdit/internal/omap"
)

// Decode reads a single JSON document from r and returns its top-level
// representation as *omap.Doc. The top level MUST be a JSON object; arrays
// and scalars at the root are rejected with a typed error.
//
// Deprecated: Use DecodeValue, which accepts any RFC 8259 top-level value
// (object, array, string, number, boolean, null). Decode is retained for
// callers that specifically require a map-rooted result.
func Decode(r io.Reader) (*omap.Doc, error) {
	v, err := DecodeValue(r)
	if err != nil {
		return nil, err
	}
	if v.Kind != omap.KindMap {
		return nil, fmt.Errorf("json: top-level value must be an object, got %v", v.Kind)
	}
	return v.Map, nil
}

// DecodeValue reads a single JSON document from r and returns its top-level
// value. Per RFC 8259 any value is permitted at the root — object, array,
// string, number, boolean, or null. This is the BUG-0001 fix entry point;
// the CLI pipeline uses DecodeValue so top-level arrays and scalars round-
// trip without an artificial object-only constraint.
//
// DecodeValue applies no resource bounds. CLI callers MUST use
// DecodeValueWithLimits; this unlimited form exists for tests and in-
// process callers that have already sized their input.
func DecodeValue(r io.Reader) (omap.Value, error) {
	return DecodeValueWithLimits(r, format.Limits{})
}

// DecodeValueWithLimits is DecodeValue with STORY-0008 resource bounds.
// Applies limits.MaxBytes via format.ReadAllLimited before parsing, then
// enforces limits.MaxDepth while walking the token stream. The YAML node
// count cap (MaxYAMLNodes) is YAML-specific and is ignored here.
//
// A zero Limits value means "no bounds" — useful for tests.
func DecodeValueWithLimits(r io.Reader, limits format.Limits) (omap.Value, error) {
	data, err := format.ReadAllLimited(r, limits.MaxBytes, "json")
	if err != nil {
		return omap.Value{}, err
	}
	dec := stdjson.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValueBounded(dec, 0, limits.MaxDepth)
	if err != nil {
		return omap.Value{}, err
	}
	// RFC 8259: one value per document. Reject trailing content so callers
	// get a clear error rather than silently ignoring the tail.
	if dec.More() {
		return omap.Value{}, fmt.Errorf("json: unexpected trailing content after top-level value")
	}
	return v, nil
}

// Encode writes d as a JSON object to w, preserving key order. No trailing
// newline is written; callers add framing (JSONL / single-doc) as needed.
//
// Before any bytes are written, Encode walks the document and rejects any
// numeric value JSON cannot represent per RFC 8259 §6 (NaN, +Inf, -Inf) with
// a path-aware *omap.EncodeError (NFR-5, FR-19 parity with YAML/TOML).
func Encode(w io.Writer, d *omap.Doc) error {
	if err := checkJSONRepresentable(omap.MapValue(d)); err != nil {
		return err
	}
	return encodeDoc(w, d)
}

// EncodeValue writes any omap.Value as a JSON document to w. Unlike Encode
// (which requires a *Doc / map root), EncodeValue accepts any top-level
// value — matching RFC 8259. BUG-0001: this is the map-agnostic entry point
// used by the CLI pipeline.
//
// Representability checks (NaN/Inf rejection) run before any bytes are
// written, consistent with Encode.
func EncodeValue(w io.Writer, v omap.Value) error {
	if err := checkJSONRepresentable(v); err != nil {
		return err
	}
	return encodeValue(w, v)
}

// checkJSONRepresentable walks v and returns an *omap.EncodeError for any
// numeric value whose lexical form is NaN, +Inf, -Inf, or Infinity — none of
// which are valid JSON per RFC 8259 §6. Null is valid in JSON and is not
// rejected here.
func checkJSONRepresentable(v omap.Value) *omap.EncodeError {
	return omap.WalkValue(omap.RootPath(), v, func(p omap.Path, v omap.Value) *omap.EncodeError {
		if v.Kind != omap.KindNum {
			return nil
		}
		s := v.Num.String()
		if kind := nonFiniteJSONKind(s); kind != "" {
			return &omap.EncodeError{Path: p, Kind: kind, Format: "json"}
		}
		return nil
	})
}

// nonFiniteJSONKind reports the canonical [omap.EncodeErrorKind] for a
// json.Number lexical form that represents a non-finite value. Returns
// "" for finite numerics. Recognises both Go's strconv form ("NaN",
// "+Inf", "-Inf") and common YAML/JSON5 spellings ("Infinity",
// "-Infinity").
func nonFiniteJSONKind(s string) omap.EncodeErrorKind {
	// Fast path: consult strconv. It accepts "NaN", "Inf", "+Inf", "-Inf",
	// "Infinity", "-Infinity" (case-insensitive) and rejects the rest.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if math.IsNaN(f) {
			return omap.EncodeKindNaN
		}
		if math.IsInf(f, 1) {
			return omap.EncodeKindPosInf
		}
		if math.IsInf(f, -1) {
			return omap.EncodeKindNegInf
		}
		return ""
	}
	// ParseFloat failed — still classify obvious non-finite lexemes so a
	// malformed json.Number does not slip through as valid JSON.
	low := strings.ToLower(strings.TrimSpace(s))
	switch low {
	case "nan", "+nan", "-nan":
		return omap.EncodeKindNaN
	case "inf", "+inf", "infinity", "+infinity":
		return omap.EncodeKindPosInf
	case "-inf", "-infinity":
		return omap.EncodeKindNegInf
	}
	return ""
}

// ----------------------------- decode -----------------------------

// decodeValueBounded is the depth-tracking counterpart to decodeValue.
// maxDepth <= 0 disables the cap; top-level depth is 0 and every nested
// mapping/sequence adds 1.
func decodeValueBounded(dec *stdjson.Decoder, depth, maxDepth int) (omap.Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return omap.Value{}, fmt.Errorf("json: %w", err)
	}
	switch t := tok.(type) {
	case stdjson.Delim:
		switch t {
		case '{':
			if maxDepth > 0 && depth+1 > maxDepth {
				return omap.Value{}, &format.LimitError{
					Format:   "json",
					Kind:     format.LimitDepth,
					Limit:    int64(maxDepth),
					Observed: int64(depth + 1),
				}
			}
			sub, err := decodeObjectBounded(dec, depth+1, maxDepth)
			if err != nil {
				return omap.Value{}, err
			}
			return omap.MapValue(sub), nil
		case '[':
			if maxDepth > 0 && depth+1 > maxDepth {
				return omap.Value{}, &format.LimitError{
					Format:   "json",
					Kind:     format.LimitDepth,
					Limit:    int64(maxDepth),
					Observed: int64(depth + 1),
				}
			}
			items, err := decodeArrayBounded(dec, depth+1, maxDepth)
			if err != nil {
				return omap.Value{}, err
			}
			return omap.Value{Kind: omap.KindSeq, Seq: items}, nil
		default:
			return omap.Value{}, fmt.Errorf("json: unexpected delim %q", t)
		}
	case string:
		return omap.StrValue(t), nil
	case bool:
		return omap.BoolValue(t), nil
	case stdjson.Number:
		return omap.NumValue(t), nil
	case nil:
		return omap.NullValue(), nil
	default:
		return omap.Value{}, fmt.Errorf("json: unexpected token type %T", tok)
	}
}

func decodeObjectBounded(dec *stdjson.Decoder, depth, maxDepth int) (*omap.Doc, error) {
	d := omap.New()
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("json: %w", err)
		}
		key, ok := tok.(string)
		if !ok {
			return nil, fmt.Errorf("json: expected string key, got %T: %v", tok, tok)
		}
		v, err := decodeValueBounded(dec, depth, maxDepth)
		if err != nil {
			return nil, err
		}
		d.Set(key, v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return d, nil
}

func decodeArrayBounded(dec *stdjson.Decoder, depth, maxDepth int) ([]omap.Value, error) {
	var out []omap.Value
	for dec.More() {
		v, err := decodeValueBounded(dec, depth, maxDepth)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return out, nil
}

// ----------------------------- encode -----------------------------

func encodeDoc(w io.Writer, d *omap.Doc) error {
	if _, err := io.WriteString(w, "{"); err != nil {
		return err
	}
	keys := d.Keys()
	for i, k := range keys {
		if i > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		kb, err := stdjson.Marshal(k)
		if err != nil {
			return fmt.Errorf("json: marshal key %q: %w", k, err)
		}
		if _, err := w.Write(kb); err != nil {
			return err
		}
		if _, err := io.WriteString(w, ":"); err != nil {
			return err
		}
		v, _ := d.Get(k)
		if err := encodeValue(w, v); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}")
	return err
}

func encodeValue(w io.Writer, v omap.Value) error {
	switch v.Kind {
	case omap.KindNull:
		_, err := io.WriteString(w, "null")
		return err
	case omap.KindBool:
		if v.Bool {
			_, err := io.WriteString(w, "true")
			return err
		}
		_, err := io.WriteString(w, "false")
		return err
	case omap.KindNum:
		s := v.Num.String()
		if s == "" {
			_, err := io.WriteString(w, "0")
			return err
		}
		// Validate as a finite numeric literal. checkJSONRepresentable (called
		// from Encode) has already rejected NaN/Inf; this catches any other
		// malformed lexical form (e.g. an Int64 overflow string manually
		// constructed by a caller).
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return fmt.Errorf("json: invalid number %q: %w", s, err)
		}
		_, err := io.WriteString(w, s)
		return err
	case omap.KindStr:
		b, err := stdjson.Marshal(v.Str)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	case omap.KindSeq:
		if _, err := io.WriteString(w, "["); err != nil {
			return err
		}
		for i, item := range v.Seq {
			if i > 0 {
				if _, err := io.WriteString(w, ","); err != nil {
					return err
				}
			}
			if err := encodeValue(w, item); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, "]")
		return err
	case omap.KindMap:
		return encodeDoc(w, v.Map)
	default:
		return fmt.Errorf("json: unknown value kind %v", v.Kind)
	}
}
