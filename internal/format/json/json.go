// Package json decodes and encodes a JSON document into omap.Doc while
// preserving key insertion order (NFR-3) and integer precision via
// json.Number (NFR-4). It uses the standard library's encoding/json
// Tokenizer API to walk the input in source order; map iteration is
// driven by omap.Doc.Keys on encode.
//
// Comments are not a JSON grammar feature and are not preserved (NFR-6).
package json

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/sdkks/nesdit/internal/omap"
)

// Decode reads a single JSON document from r and returns its top-level
// representation as *omap.Doc. The top level MUST be a JSON object; arrays
// and scalars at the root are rejected with a typed error.
func Decode(r io.Reader) (*omap.Doc, error) {
	dec := stdjson.NewDecoder(r)
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	delim, ok := tok.(stdjson.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("json: top-level value must be an object, got %v", tok)
	}
	d, err := decodeObject(dec)
	if err != nil {
		return nil, err
	}
	return d, nil
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

// nonFiniteJSONKind reports the canonical error kind string ("NaN", "+Inf",
// "-Inf") for a json.Number lexical form that represents a non-finite value.
// Returns "" for finite numerics. Recognises both Go's strconv form ("NaN",
// "+Inf", "-Inf") and common YAML/JSON5 spellings ("Infinity", "-Infinity").
func nonFiniteJSONKind(s string) string {
	// Fast path: consult strconv. It accepts "NaN", "Inf", "+Inf", "-Inf",
	// "Infinity", "-Infinity" (case-insensitive) and rejects the rest.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if math.IsNaN(f) {
			return "NaN"
		}
		if math.IsInf(f, 1) {
			return "+Inf"
		}
		if math.IsInf(f, -1) {
			return "-Inf"
		}
		return ""
	}
	// ParseFloat failed — still classify obvious non-finite lexemes so a
	// malformed json.Number does not slip through as valid JSON.
	low := strings.ToLower(strings.TrimSpace(s))
	switch low {
	case "nan", "+nan", "-nan":
		return "NaN"
	case "inf", "+inf", "infinity", "+infinity":
		return "+Inf"
	case "-inf", "-infinity":
		return "-Inf"
	}
	return ""
}

// ----------------------------- decode -----------------------------

// At entry, dec has just consumed the object-open '{' token.
func decodeObject(dec *stdjson.Decoder) (*omap.Doc, error) {
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
		v, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		d.Set(key, v)
	}
	// consume '}'
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("json: %w", err)
	}
	return d, nil
}

// At entry, dec has just consumed the array-open '[' token.
func decodeArray(dec *stdjson.Decoder) ([]omap.Value, error) {
	var out []omap.Value
	for dec.More() {
		v, err := decodeValue(dec)
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

// decodeValue reads the next JSON value (which may recurse for objects
// or arrays).
func decodeValue(dec *stdjson.Decoder) (omap.Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return omap.Value{}, fmt.Errorf("json: %w", err)
	}
	switch t := tok.(type) {
	case stdjson.Delim:
		switch t {
		case '{':
			sub, err := decodeObject(dec)
			if err != nil {
				return omap.Value{}, err
			}
			return omap.MapValue(sub), nil
		case '[':
			items, err := decodeArray(dec)
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
