// Package yaml decodes and encodes YAML 1.2 documents into omap.Doc via
// gopkg.in/yaml.v3's *yaml.Node. Decoding preserves mapping key insertion
// order (NFR-3) and records each scalar's resolved short YAML tag (!!str,
// !!timestamp, !!binary, ...) on omap.Value.Tag so that a round-trip
// through omap does not re-infer a different tag for an ambiguous value
// such as a string that looks like a timestamp.
//
// Comments are NOT preserved across the round-trip (NFR-6 — explicit
// non-goal for v1 of nesdit). Anchors/aliases are resolved on decode and
// not re-emitted.
//
// NaN and +/-Inf in numeric values are rejected at encode time with a
// path-aware omap.EncodeError (FR-19).
package yaml

import (
	stdjson "encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/sdkks/nesdit/internal/omap"
)

// Decode reads a single YAML document from r and returns its top-level
// mapping as *omap.Doc.
//
// Deprecated for CLI use: prefer DecodeValue, which accepts any YAML 1.2
// top-level node (mapping, sequence, or scalar). This function is retained
// for tests and callers that specifically want a map root.
func Decode(r io.Reader) (*omap.Doc, error) {
	var root yaml.Node
	dec := yaml.NewDecoder(r)
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	// root is DocumentNode wrapping the real content.
	var content *yaml.Node
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) != 1 {
			return nil, fmt.Errorf("yaml: document has %d content nodes, want 1", len(root.Content))
		}
		content = root.Content[0]
	} else {
		content = &root
	}
	if content.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("yaml: top-level value must be a mapping, got kind=%v", content.Kind)
	}
	return nodeToMap(content)
}

// DecodeValue reads a single YAML document from r and returns its top-level
// node as an omap.Value. YAML 1.2 permits any node (mapping, sequence, or
// scalar) at the root. This is the BUG-0001 fix entry point the CLI uses.
func DecodeValue(r io.Reader) (omap.Value, error) {
	var root yaml.Node
	dec := yaml.NewDecoder(r)
	if err := dec.Decode(&root); err != nil {
		return omap.Value{}, fmt.Errorf("yaml: %w", err)
	}
	var content *yaml.Node
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) != 1 {
			return omap.Value{}, fmt.Errorf("yaml: document has %d content nodes, want 1", len(root.Content))
		}
		content = root.Content[0]
	} else {
		content = &root
	}
	return nodeToValue(content)
}

// Encode writes d to w as a single YAML document with key insertion order
// preserved. Indentation is 2 spaces (yaml.v3's default).
func Encode(w io.Writer, d *omap.Doc) error {
	return EncodeValue(w, omap.MapValue(d))
}

// EncodeValue writes any omap.Value as a single YAML document. Unlike Encode
// (which wraps a *Doc), EncodeValue accepts sequence and scalar roots — the
// BUG-0001 fix entry point for the CLI.
func EncodeValue(w io.Writer, v omap.Value) error {
	// Walk the value for NaN/Inf first so the caller gets a path-aware error
	// before any partial bytes hit the writer.
	if err := checkYAMLRepresentable(v); err != nil {
		return err
	}

	root := valueToNode(v)

	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("yaml: %w", err)
	}
	return nil
}

// ----------------------------- decode -----------------------------

func nodeToMap(n *yaml.Node) (*omap.Doc, error) {
	if n.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("yaml: expected mapping, got kind=%v at line %d", n.Kind, n.Line)
	}
	d := omap.New()
	for i := 0; i+1 < len(n.Content); i += 2 {
		kn, vn := n.Content[i], n.Content[i+1]
		if kn.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("yaml: non-scalar map key at line %d (kind=%v)", kn.Line, kn.Kind)
		}
		v, err := nodeToValue(vn)
		if err != nil {
			return nil, err
		}
		d.Set(kn.Value, v)
	}
	return d, nil
}

func nodeToValue(n *yaml.Node) (omap.Value, error) {
	// Resolve aliases transparently on decode (anchors not re-emitted per NFR-6 sibling).
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		return nodeToValue(n.Alias)
	}
	switch n.Kind {
	case yaml.MappingNode:
		d, err := nodeToMap(n)
		if err != nil {
			return omap.Value{}, err
		}
		return omap.MapValue(d), nil
	case yaml.SequenceNode:
		items := make([]omap.Value, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := nodeToValue(c)
			if err != nil {
				return omap.Value{}, err
			}
			items = append(items, v)
		}
		return omap.Value{Kind: omap.KindSeq, Seq: items}, nil
	case yaml.ScalarNode:
		return scalarNodeToValue(n), nil
	default:
		return omap.Value{}, fmt.Errorf("yaml: unsupported node kind %v at line %d", n.Kind, n.Line)
	}
}

// scalarNodeToValue interprets a YAML scalar, resolving the implicit tag
// when none was explicit. Records the resolved tag on the omap.Value so
// round-trip encoders can re-assert it.
func scalarNodeToValue(n *yaml.Node) omap.Value {
	tag := n.Tag
	if tag == "" {
		tag = inferScalarTag(n)
	}
	switch tag {
	case "!!null":
		return omap.NullValue()
	case "!!bool":
		switch n.Value {
		case "true", "True", "TRUE":
			return omap.BoolValue(true)
		case "false", "False", "FALSE":
			return omap.BoolValue(false)
		}
		// Non-canonical bool — fall back to string.
		return omap.StrValueTagged(n.Value, "!!str")
	case "!!int":
		// Preserve the original lexical form via json.Number.
		return omap.NumValue(jsonNumber(n.Value))
	case "!!float":
		return omap.NumValue(jsonNumber(n.Value))
	case "!!str":
		// Tag explicitly !!str — either the YAML had a quoted form or
		// the source carried an explicit tag. Retain so round-trip
		// doesn't re-infer timestamp/int/etc.
		return omap.StrValueTagged(n.Value, "!!str")
	case "!!timestamp", "!!binary":
		return omap.StrValueTagged(n.Value, tag)
	default:
		// Unknown custom tag — treat as string but retain tag.
		return omap.StrValueTagged(n.Value, tag)
	}
}

// inferScalarTag is a best-effort default when yaml.v3 did not populate a tag
// (some ScalarNode instances constructed by hand or aliased content).
func inferScalarTag(n *yaml.Node) string {
	// When Style is DoubleQuotedStyle or SingleQuotedStyle, this is a string.
	if n.Style == yaml.DoubleQuotedStyle || n.Style == yaml.SingleQuotedStyle {
		return "!!str"
	}
	// Plain/folded: unmarshal into interface and check type.
	var tmp any
	if err := n.Decode(&tmp); err != nil {
		return "!!str"
	}
	switch tmp.(type) {
	case nil:
		return "!!null"
	case bool:
		return "!!bool"
	case int, int64, uint64:
		return "!!int"
	case float64:
		return "!!float"
	case string:
		return "!!str"
	default:
		return "!!str"
	}
}

func jsonNumber(s string) stdjson.Number { return stdjson.Number(s) }

// ----------------------------- encode -----------------------------

// checkYAMLRepresentable walks v and returns an *omap.EncodeError for any
// numeric NaN or +/-Inf (FR-19: YAML encode must reject these with path
// context). The tree walk is delegated to omap.WalkValue; this function only
// supplies the per-leaf predicate.
func checkYAMLRepresentable(v omap.Value) *omap.EncodeError {
	return omap.WalkValue(omap.RootPath(), v, func(p omap.Path, v omap.Value) *omap.EncodeError {
		if v.Kind != omap.KindNum {
			return nil
		}
		s := v.Num.String()
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil
		}
		switch {
		case math.IsNaN(f):
			return &omap.EncodeError{Path: p, Kind: "NaN", Format: "yaml"}
		case math.IsInf(f, 1):
			return &omap.EncodeError{Path: p, Kind: "+Inf", Format: "yaml"}
		case math.IsInf(f, -1):
			return &omap.EncodeError{Path: p, Kind: "-Inf", Format: "yaml"}
		}
		return nil
	})
}

func valueToNode(v omap.Value) *yaml.Node {
	switch v.Kind {
	case omap.KindNull:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	case omap.KindBool:
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool"}
		if v.Bool {
			n.Value = "true"
		} else {
			n.Value = "false"
		}
		return n
	case omap.KindNum:
		s := v.Num.String()
		tag := "!!int"
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: s}
	case omap.KindStr:
		return scalarToStringNode(v)
	case omap.KindSeq:
		n := &yaml.Node{Kind: yaml.SequenceNode}
		for _, it := range v.Seq {
			n.Content = append(n.Content, valueToNode(it))
		}
		return n
	case omap.KindMap:
		n := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range v.Map.Keys() {
			kn := &yaml.Node{Kind: yaml.ScalarNode, Value: k}
			sub, _ := v.Map.Get(k)
			n.Content = append(n.Content, kn, valueToNode(sub))
		}
		return n
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
}

// scalarToStringNode emits a string scalar honoring any stored tag. When
// the stored tag is "!!str" on a value whose lexical form would be re-inferred
// as a non-string tag by yaml.v3's resolver (e.g. "2026-04-26" → !!timestamp,
// "42" → !!int, "true" → !!bool, "" → !!null), we force double-quote style so
// the round-trip preserves the string kind.
//
// The check is authoritative — we ask inferScalarTag what the resolver would
// do with a plain scalar of this lexical form, and if its answer differs from
// "!!str", we quote. No hand-maintained lexical heuristic.
func scalarToStringNode(v omap.Value) *yaml.Node {
	tag := v.Tag
	if tag == "" {
		tag = "!!str"
	}
	n := &yaml.Node{Kind: yaml.ScalarNode, Value: v.Str, Tag: tag}
	if tag == "!!str" && wouldReinferAsNonString(v.Str) {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

// wouldReinferAsNonString reports whether yaml.v3's resolver would assign a
// tag other than !!str to the plain scalar s. Used to decide whether to
// force-quote a !!str-tagged string so the round-trip does not collapse the
// explicit string tag into !!timestamp / !!int / !!float / !!bool / !!null.
//
// Implementation: build a plain-style scalar node with no tag and ask
// inferScalarTag what the resolver would conclude. This is exact by
// construction — any scalar yaml.v3 would re-infer as non-string is caught
// regardless of its lexical shape, without a hand-maintained heuristic.
func wouldReinferAsNonString(s string) bool {
	probe := &yaml.Node{Kind: yaml.ScalarNode, Value: s, Style: 0, Tag: ""}
	return inferScalarTag(probe) != "!!str"
}
