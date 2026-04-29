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
//
// FR-18 YAML dialect selection (--yaml-version):
//
// gopkg.in/yaml.v3 (v3.0.1) implements YAML 1.2 resolution. It does NOT
// tag `yes`, `no`, `on`, `off` as !!bool — those arrive as plain !!str
// scalars. Only `true`/`True`/`TRUE`/`false`/`False`/`FALSE` are tagged
// !!bool by the library.
//
// In 1.1 mode, DecodeValueWithLimitsAndOpts post-processes !!str scalars:
// if the unquoted value matches the YAML 1.1 boolean vocabulary it is
// converted to an omap.BoolValue before the pipeline sees it. Quoted values
// (single or double) bypass this conversion — a `"yes"` is always a string.
//
// Spec deviation note (FR-18): go-yaml v3 has no first-class version
// switch. Octal literal handling (0777 vs 0o777) and anchor-alias
// semantics differences between YAML 1.1 and 1.2 are NOT implemented.
// Only the boolean vocabulary is affected.
package yaml

import (
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/sdkks/nesdit/internal/format"
	"github.com/sdkks/nesdit/internal/omap"
)

// DecodeOpts controls optional decoder behaviour beyond resource limits.
// The zero value is the strict YAML 1.2 default.
type DecodeOpts struct {
	// YAMLVersion selects the boolean vocabulary used during scalar
	// interpretation. Accepted values: "1.1" and "1.2" (default "1.2").
	//
	// In 1.2 mode (default), only true/false/True/False/TRUE/FALSE are
	// treated as booleans — this matches the YAML 1.2 core schema.
	//
	// In 1.1 mode the extended YAML 1.1 boolean vocabulary is recognised:
	// yes/no/on/off and their case variants are mapped to bool values.
	// This is needed for legacy Kubernetes/Helm files that rely on that
	// coercion.
	//
	// Spec deviation: go-yaml v3 has no formal version switch; octal
	// literal and anchor-alias behavioural differences between 1.1 and 1.2
	// are NOT implemented. Only the boolean vocabulary is affected.
	YAMLVersion string
}

// Decode reads a single YAML document from r and returns its top-level
// mapping as *omap.Doc.
//
// Deprecated for CLI use: prefer DecodeValue, which accepts any YAML 1.2
// top-level node (mapping, sequence, or scalar). This function is retained
// for tests and callers that specifically want a map root.
func Decode(r io.Reader) (*omap.Doc, error) {
	v, err := DecodeValue(r)
	if err != nil {
		return nil, err
	}
	if v.Kind != omap.KindMap {
		return nil, fmt.Errorf("yaml: top-level value must be a mapping, got kind=%v", v.Kind)
	}
	return v.Map, nil
}

// DecodeValue reads a single YAML document from r and returns its top-level
// node as an omap.Value. YAML 1.2 permits any node (mapping, sequence, or
// scalar) at the root. This is the BUG-0001 fix entry point the CLI uses.
//
// DecodeValue applies no resource bounds. CLI callers MUST use
// DecodeValueWithLimits; this unlimited form exists for tests and in-
// process callers that have already sized their input.
func DecodeValue(r io.Reader) (omap.Value, error) {
	return DecodeValueWithLimits(r, format.Limits{})
}

// DecodeValueWithLimits is DecodeValue with STORY-0008 resource bounds.
// Applies all three bounds:
//
//   - limits.MaxBytes via format.ReadAllLimited before parsing.
//   - limits.MaxDepth while walking the node tree.
//   - limits.MaxYAMLNodes while materialising nodes (billion-laughs
//     mitigation, M1). Every node materialisation — alias-driven or
//     not — counts, so a small input that references a deeply nested
//     anchor 10000 times is rejected with
//     &format.LimitError{Kind: LimitYAMLNodeCount}.
//
// A zero Limits value means "no bounds" — useful for tests.
func DecodeValueWithLimits(r io.Reader, limits format.Limits) (omap.Value, error) {
	return DecodeValueWithLimitsAndOpts(r, limits, DecodeOpts{})
}

// DecodeValueWithLimitsAndOpts extends DecodeValueWithLimits with FR-18
// dialect control. The opts.YAMLVersion field selects "1.1" or "1.2"
// boolean vocabulary; all other behaviour is unchanged.
func DecodeValueWithLimitsAndOpts(r io.Reader, limits format.Limits, opts DecodeOpts) (omap.Value, error) {
	switch opts.YAMLVersion {
	case "", "1.1", "1.2":
		// valid
	default:
		return omap.Value{}, fmt.Errorf("yaml: unsupported YAMLVersion %q: must be \"1.1\", \"1.2\", or \"\" (default 1.2)", opts.YAMLVersion)
	}
	data, err := format.ReadAllLimited(r, limits.MaxBytes, "yaml")
	if err != nil {
		return omap.Value{}, err
	}
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&root); err != nil {
		return omap.Value{}, fmt.Errorf("yaml: %w", err)
	}
	// TASK-0017: reject trailing documents so single-doc callers get a clear
	// error rather than silently discarding content. Mirrors the equivalent
	// check in json.DecodeValueWithLimits (BUG-0001 symmetry).
	var trailing yaml.Node
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return omap.Value{}, fmt.Errorf("yaml: unexpected trailing content after top-level document")
		}
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
	w := &yamlWalker{
		maxDepth:    limits.MaxDepth,
		maxNodes:    limits.MaxYAMLNodes,
		yamlVersion: opts.YAMLVersion,
	}
	return w.node(content, 0)
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

// yamlWalker applies STORY-0008 M1 + M3 bounds while turning a decoded
// yaml.Node tree into an omap.Value. Each materialisation of a node
// increments the counter; aliases are resolved by recursing into their
// Alias target, so a billion-laughs input naturally explodes the count
// and trips the yaml-node cap long before OOM. The counter covers all
// node materialisations, not only alias-driven ones, so the cap acts
// as a total work-budget on the yaml walk.
//
// TASK-0018 S-3: visiting is a per-walk visited set used exclusively for
// cycle detection during alias resolution. yaml.v3 does not permit circular
// aliases in its current implementation, but this set provides defense-in-
// depth if the library is ever swapped: a circular alias chain would recurse
// forever without it.
//
// maxDepth <= 0 disables the depth cap.
// maxNodes <= 0 disables the yaml-node count cap.
// yamlVersion "" or "1.2" selects YAML 1.2 boolean vocabulary (default).
// yamlVersion "1.1" enables the extended YAML 1.1 boolean vocabulary.
type yamlWalker struct {
	maxDepth    int
	maxNodes    int
	nodes       int                 // node materialisations so far
	visiting    map[*yaml.Node]bool // cycle detection for alias resolution
	yamlVersion string              // "" | "1.1" | "1.2"
}

// node materialises a single yaml.Node at tree-depth d. Follows aliases
// transparently so downstream consumers see a resolved value tree.
func (w *yamlWalker) node(n *yaml.Node, d int) (omap.Value, error) {
	w.nodes++
	if w.maxNodes > 0 && w.nodes > w.maxNodes {
		return omap.Value{}, &format.LimitError{
			Format:   "yaml",
			Kind:     format.LimitYAMLNodeCount,
			Limit:    int64(w.maxNodes),
			Observed: int64(w.nodes),
		}
	}
	if n.Kind == yaml.AliasNode && n.Alias != nil {
		// TASK-0018 S-3: guard against circular alias chains. yaml.v3
		// currently rejects them at parse time, but this visited-set
		// check is defense-in-depth against a future library swap.
		if w.visiting == nil {
			w.visiting = make(map[*yaml.Node]bool)
		}
		if w.visiting[n.Alias] {
			return omap.Value{}, fmt.Errorf("yaml: circular alias detected at line %d", n.Line)
		}
		w.visiting[n.Alias] = true
		v, err := w.node(n.Alias, d)
		delete(w.visiting, n.Alias)
		return v, err
	}
	switch n.Kind {
	case yaml.MappingNode:
		if w.maxDepth > 0 && d+1 > w.maxDepth {
			return omap.Value{}, &format.LimitError{
				Format:   "yaml",
				Kind:     format.LimitDepth,
				Limit:    int64(w.maxDepth),
				Observed: int64(d + 1),
			}
		}
		doc := omap.New()
		for i := 0; i+1 < len(n.Content); i += 2 {
			kn, vn := n.Content[i], n.Content[i+1]
			if kn.Kind != yaml.ScalarNode {
				return omap.Value{}, fmt.Errorf("yaml: non-scalar map key at line %d (kind=%v)", kn.Line, kn.Kind)
			}
			// YAML 1.1 merge key: <<: *anchor expands the anchor's map into
			// the current map. Explicit keys already set take precedence over
			// merged defaults, so only set keys that are not already present.
			if kn.Tag == "!!merge" {
				merged, err := w.node(vn, d+1)
				if err != nil {
					return omap.Value{}, err
				}
				if merged.Kind == omap.KindMap && merged.Map != nil {
					for _, mk := range merged.Map.Keys() {
						if !doc.Has(mk) {
							mv, _ := merged.Map.Get(mk)
							doc.Set(mk, mv)
						}
					}
				}
				continue
			}
			v, err := w.node(vn, d+1)
			if err != nil {
				return omap.Value{}, err
			}
			doc.Set(kn.Value, v)
		}
		return omap.MapValue(doc), nil
	case yaml.SequenceNode:
		if w.maxDepth > 0 && d+1 > w.maxDepth {
			return omap.Value{}, &format.LimitError{
				Format:   "yaml",
				Kind:     format.LimitDepth,
				Limit:    int64(w.maxDepth),
				Observed: int64(d + 1),
			}
		}
		items := make([]omap.Value, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := w.node(c, d+1)
			if err != nil {
				return omap.Value{}, err
			}
			items = append(items, v)
		}
		return omap.Value{Kind: omap.KindSeq, Seq: items}, nil
	case yaml.ScalarNode:
		return scalarNodeToValue(n, w.yamlVersion), nil
	default:
		return omap.Value{}, fmt.Errorf("yaml: unsupported node kind %v at line %d", n.Kind, n.Line)
	}
}

// scalarNodeToValue interprets a YAML scalar, resolving the implicit tag
// when none was explicit. Records the resolved tag on the omap.Value so
// round-trip encoders can re-assert it.
//
// yamlVersion controls boolean vocabulary:
//   - "" or "1.2": only true/false (canonical forms) are booleans.
//   - "1.1": the extended YAML 1.1 vocabulary (yes/no/on/off and case
//     variants) is also treated as boolean.
func scalarNodeToValue(n *yaml.Node, yamlVersion string) omap.Value {
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
		// In YAML 1.1 mode, check whether the scalar is a YAML 1.1 boolean
		// token. gopkg.in/yaml.v3 uses YAML 1.2 resolution (only true/false
		// are booleans), so `yes`/`no`/`on`/`off` arrive here with tag !!str.
		// The style check guards against explicitly-quoted values: a quoted
		// `"yes"` is unambiguously a string even in YAML 1.1.
		if yamlVersion == "1.1" && n.Style != yaml.SingleQuotedStyle && n.Style != yaml.DoubleQuotedStyle {
			if b, ok := yaml11Bool(n.Value); ok {
				return omap.BoolValue(b)
			}
		}
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

// yaml11Bool maps the extended YAML 1.1 boolean vocabulary to Go booleans.
// Returns (value, true) when s is a recognised 1.1 boolean token, or
// (false, false) when s is not in the 1.1 boolean set.
//
// Truthy: y, Y, yes, Yes, YES, true, True, TRUE, on, On, ON
// Falsy:  n, N, no, No, NO, false, False, FALSE, off, Off, OFF
//
// The canonical YAML 1.2 forms (true/True/TRUE/false/False/FALSE) are
// handled by the caller before yaml11Bool is invoked; they are included
// here for completeness but would not reach this function in practice.
func yaml11Bool(s string) (bool, bool) {
	switch s {
	case "y", "Y", "yes", "Yes", "YES", "on", "On", "ON":
		return true, true
	case "n", "N", "no", "No", "NO", "off", "Off", "OFF":
		return false, true
	}
	return false, false
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
			return &omap.EncodeError{Path: p, Kind: omap.EncodeKindNaN, Format: "yaml"}
		case math.IsInf(f, 1):
			return &omap.EncodeError{Path: p, Kind: omap.EncodeKindPosInf, Format: "yaml"}
		case math.IsInf(f, -1):
			return &omap.EncodeError{Path: p, Kind: omap.EncodeKindNegInf, Format: "yaml"}
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
		v.Map.Entries(func(k string, sub omap.Value) bool {
			kn := &yaml.Node{Kind: yaml.ScalarNode, Value: k}
			n.Content = append(n.Content, kn, valueToNode(sub))
			return true
		})
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
