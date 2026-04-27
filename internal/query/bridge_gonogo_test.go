package query_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/query"
)

// TestBridgeGoNoGo is the DR-005 Plan A go/no-go gate, extended with the
// DR-007 array-reshape lock-in suite.
//
// Two sub-harnesses run over the corpus:
//
//  1. Idempotency (Queries field): for every (document, query) pair:
//     - decodes the source bytes → *omap.Doc (Doc_0).
//     - runs the query once → Doc_1; encodes Doc_1 → bytes_1.
//     - decodes bytes_1 → Doc_1'; runs the same query → Doc_2; encodes → bytes_2.
//     - asserts bytes_2 == bytes_1 (idempotency, NFR-2).
//     - for the identity query ".", asserts bytes_1 == source bytes.
//
//  2. Reshape (Reshape field, DR-007): for every reshapeCase:
//     - decodes the source → *omap.Doc.
//     - runs the case query once → Doc_1; encodes → bytes_1.
//     - asserts bytes_1 == case.Expect.
//     This pins the positional array-reconciliation contract from DR-007
//     so any silent switch to identity matching (or a key-set heuristic)
//     breaks CI loudly. Queries here are NOT required to be self-stable
//     under a second application (reverse, grow, shrink explicitly are
//     not) — idempotency is asserted by the Queries field only.
//
// Any idempotency failure authorises the Plan B switch (DR-005) in this
// same story.
func TestBridgeGoNoGo(t *testing.T) {
	t.Parallel()

	for _, c := range bridgeCorpus() {
		for _, q := range c.Queries {
			c := c
			q := q
			t.Run(c.Name+"/"+sanitize(q), func(t *testing.T) {
				t.Parallel()
				runBridgeCase(t, c, q)
			})
		}
		for _, r := range c.Reshape {
			c := c
			r := r
			t.Run(c.Name+"/"+r.Name, func(t *testing.T) {
				t.Parallel()
				runReshapeCase(t, c, r)
			})
		}
	}
}

// Test_IdentityQuery_ByteIdentical is a narrow slice of TestBridgeGoNoGo:
// for every corpus format, the identity query “.“ over source bytes
// produces byte-identical output. Kept as a separate entry point so a
// regression in pure identity is instantly obvious without reading the
// full matrix log.
func Test_IdentityQuery_ByteIdentical(t *testing.T) {
	t.Parallel()

	for _, c := range bridgeCorpus() {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			t.Parallel()
			doc, err := decode(c.Format, c.Source)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			out, err := query.Run(context.Background(), doc, ".")
			if err != nil {
				t.Fatalf("query.Run: %v", err)
			}
			got, err := encode(c.Format, out)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			if !bytes.Equal(got, c.Source) {
				t.Fatalf("identity query changed bytes\n--- want ---\n%s\n--- got ---\n%s",
					string(c.Source), string(got))
			}
		})
	}
}

func runBridgeCase(t *testing.T, c bridgeCase, q string) {
	t.Helper()

	doc0, err := decode(c.Format, c.Source)
	if err != nil {
		t.Fatalf("decode source: %v", err)
	}

	// Run 1.
	doc1, err := query.Run(context.Background(), doc0, q)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	bytes1, err := encode(c.Format, doc1)
	if err != nil {
		t.Fatalf("encode 1: %v", err)
	}

	// Run 2 — decode bytes1, run same query, encode, compare.
	doc1p, err := decode(c.Format, bytes1)
	if err != nil {
		t.Fatalf("decode bytes1: %v\nbytes1=%s", err, string(bytes1))
	}
	doc2, err := query.Run(context.Background(), doc1p, q)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	bytes2, err := encode(c.Format, doc2)
	if err != nil {
		t.Fatalf("encode 2: %v", err)
	}

	if !bytes.Equal(bytes1, bytes2) {
		t.Fatalf("idempotency failure for %s / %q\n--- bytes1 ---\n%s\n--- bytes2 ---\n%s",
			c.Name, q, string(bytes1), string(bytes2))
	}

	// For pure identity, first run must equal input.
	if q == "." && !bytes.Equal(c.Source, bytes1) {
		t.Fatalf("identity query %q changed source\n--- source ---\n%s\n--- bytes1 ---\n%s",
			q, string(c.Source), string(bytes1))
	}
}

// runReshapeCase is the DR-007 lock-in: run the reshape query exactly
// once against the source and assert the bytes equal the expected fixed
// output. This is not an idempotency check — reshape queries like
// reverse/grow/shrink are not required to be self-stable — it is a pin
// for the positional reconciliation contract under jq-driven array
// restructuring.
func runReshapeCase(t *testing.T, c bridgeCase, r reshapeCase) {
	t.Helper()
	doc0, err := decode(c.Format, c.Source)
	if err != nil {
		t.Fatalf("decode source: %v", err)
	}
	doc1, err := query.Run(context.Background(), doc0, r.Query)
	if err != nil {
		t.Fatalf("query.Run(%q): %v", r.Query, err)
	}
	got, err := encode(c.Format, doc1)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Equal(got, r.Expect) {
		t.Fatalf("reshape %q on %s produced unexpected bytes\n--- want ---\n%s\n--- got ---\n%s",
			r.Query, c.Name, string(r.Expect), string(got))
	}
}

// sanitize makes a t.Run name printable for a jq expression.
func sanitize(q string) string {
	r := strings.NewReplacer(
		".", "_",
		" ", "",
		"(", "_",
		")", "_",
		"[", "_",
		"]", "_",
		"=", "eq",
		"/", "",
	)
	s := r.Replace(q)
	if s == "" {
		s = "empty"
	}
	return s
}

// ---------------- codec dispatch ----------------

func decode(format string, data []byte) (*omap.Doc, error) {
	switch format {
	case "json":
		v, err := jsonfmt.DecodeValue(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if v.Kind != omap.KindMap {
			return nil, fmt.Errorf("json: top-level value must be an object, got %v", v.Kind)
		}
		return v.Map, nil
	case "yaml":
		v, err := yamlfmt.DecodeValue(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if v.Kind != omap.KindMap {
			return nil, fmt.Errorf("yaml: top-level value must be a mapping, got %v", v.Kind)
		}
		return v.Map, nil
	case "toml":
		v, err := tomlfmt.DecodeValue(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		if v.Kind != omap.KindMap {
			return nil, fmt.Errorf("toml: top-level value must be a table, got %v", v.Kind)
		}
		return v.Map, nil
	default:
		return nil, fmt.Errorf("unknown format %q", format)
	}
}

func encode(format string, d *omap.Doc) ([]byte, error) {
	var buf bytes.Buffer
	var err error
	switch format {
	case "json":
		err = jsonfmt.Encode(&buf, d)
	case "yaml":
		err = yamlfmt.Encode(&buf, d)
	case "toml":
		err = tomlfmt.Encode(&buf, d)
	default:
		return nil, fmt.Errorf("unknown format %q", format)
	}
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---------------- corpus ----------------

type bridgeCase struct {
	Name    string
	Format  string // "json" | "yaml" | "toml"
	Source  []byte
	Queries []string
	// Reshape carries DR-007 lock-in queries whose post-run bytes are
	// pinned explicitly (not asserted idempotent). These exist to pin
	// the positional array-of-maps reconciliation contract against a
	// future drift toward identity matching.
	Reshape []reshapeCase
}

// reshapeCase pins a single array-reshape query's expected output bytes.
// Each case becomes a subtest under TestBridgeGoNoGo named
// "<case.Name>/<reshape.Name>".
type reshapeCase struct {
	Name   string // e.g. "reshape_sortby"; becomes the t.Run leaf
	Query  string
	Expect []byte
}

// bridgeCorpus returns the DR-005 test corpus: ~20 (doc, queries) pairs
// covering the risk classes enumerated in SPEC-0001 §7 Risk 1 and Risk 7.
//
// Query suite defaults to {".", ".a.b=1", "del(.a.b)", ".items[0].x=2"} —
// any pair whose preconditions do not hold (e.g. no .a.b path) uses a
// narrower subset. All four queries are assignment-style, so they also
// exercise the Plan B AST surface if we land there.
func bridgeCorpus() []bridgeCase {
	// Default suite for docs that have both .a.b and .items[0].x paths.
	full := []string{".", ".a.b = 1", "del(.a.b)", ".items[0].x = 2"}
	ab := []string{".", ".a.b = 1", "del(.a.b)"}
	items := []string{".", ".items[0].x = 2"}
	id := []string{"."}

	cases := []bridgeCase{
		// --- JSON ---
		{
			Name:    "json_basic_nested",
			Format:  "json",
			Source:  []byte(`{"a":{"b":1,"c":2},"items":[{"x":10,"y":20},{"x":30}]}`),
			Queries: full,
		},
		{
			Name:    "json_big_int_2_53_plus_1",
			Format:  "json",
			Source:  []byte(`{"big":9007199254740993,"neg":-9007199254740993,"a":{"b":42}}`),
			Queries: ab,
		},
		{
			Name:    "json_unicode_keys",
			Format:  "json",
			Source:  []byte(`{"a":{"b":"ok"},"κλειδί":"τιμή","emoji😀":"yes"}`),
			Queries: ab,
		},
		{
			Name:    "json_deeply_nested",
			Format:  "json",
			Source:  []byte(`{"a":{"b":{"c":{"d":{"e":{"f":1}}}}}}`),
			Queries: ab,
		},
		{
			Name:    "json_mixed_scalars",
			Format:  "json",
			Source:  []byte(`{"a":{"b":null},"bool":true,"s":"hello","f":3.14}`),
			Queries: ab,
		},
		{
			Name:    "json_arrays_of_maps_key_order",
			Format:  "json",
			Source:  []byte(`{"items":[{"name":"a","x":1,"z":true},{"z":false,"x":2,"name":"b"}]}`),
			Queries: items,
		},
		{
			Name:    "json_empty_structures",
			Format:  "json",
			Source:  []byte(`{"a":{"b":{}},"items":[]}`),
			Queries: ab,
		},

		// --- DR-007 array-reshape lock-in ---
		// Positional reconciliation means result[i] inherits map-order
		// from prev[i], regardless of what jq did to the array. Each
		// case below pins a single query's expected bytes so a future
		// silent switch to key-set / identity matching fails CI.
		{
			Name:    "json_dr007_reshape",
			Format:  "json",
			Source:  []byte(`{"items":[{"name":"a","n":1},{"name":"b","n":2}]}`),
			Queries: id,
			Reshape: []reshapeCase{
				{
					// Test_FromAny_ArrayReshape_SortBy
					Name:   "reshape_sortby",
					Query:  ".items |= sort_by(.name)",
					Expect: []byte(`{"items":[{"name":"a","n":1},{"name":"b","n":2}]}`),
				},
				{
					// Test_FromAny_ArrayReshape_Reverse
					Name:   "reshape_reverse",
					Query:  ".items |= reverse",
					Expect: []byte(`{"items":[{"name":"b","n":2},{"name":"a","n":1}]}`),
				},
				{
					// Test_FromAny_ArrayGrow_NewElement
					// New element at i=2 has no prev — its keys sort lex.
					Name:   "reshape_grow_new_element",
					Query:  `.items += [{"name":"z","n":99}]`,
					Expect: []byte(`{"items":[{"name":"a","n":1},{"name":"b","n":2},{"n":99,"name":"z"}]}`),
				},
				{
					// Test_FromAny_ArrayShrink_Slice
					Name:   "reshape_shrink_slice",
					Query:  ".items |= .[0:1]",
					Expect: []byte(`{"items":[{"name":"a","n":1}]}`),
				},
				{
					// Test_FromAny_ArrayReshape_Idempotent_SortBy
					// Source is already sorted; sort_by is a no-op and
					// bytes equal the source.
					Name:   "reshape_idempotent_sortby",
					Query:  ".items |= sort_by(.name)",
					Expect: []byte(`{"items":[{"name":"a","n":1},{"name":"b","n":2}]}`),
				},
				{
					// Test_FromAny_ArrayReshape_MapRestructure
					// Replacement objects have DIFFERENT key sets than
					// prev — under positional reconciliation, the new
					// keys lex-sort (fromAnyMap's "new keys" branch).
					Name:   "reshape_map_restructure",
					Query:  ".items |= map({id: .name, count: .n})",
					Expect: []byte(`{"items":[{"count":1,"id":"a"},{"count":2,"id":"b"}]}`),
				},
			},
		},
		// Tech #4 — DR-007 heterogeneous-key sort_by: the "surprising
		// but deterministic" case DR-007 calls out by name. The source
		// array has elements with DIFFERENT key sets ({name,x} and
		// {name,y}); sort_by(.name) reverses the two elements. Under
		// positional reconciliation, result[0] inherits prev[0]'s
		// map-order slot (name-then-x) and result[1] inherits
		// prev[1]'s (name-then-y). The element swapped INTO slot 0
		// ({name:"a",y:2}) has a key set that is neither a subset nor
		// a superset of prev[0]'s key set — so:
		//   - "name" is the common key and stays in the slot prev[0]
		//     had for it (first).
		//   - "y" is a new-to-slot-0 key — it lex-sorts among the
		//     new keys (fromAnyMap's "new keys" branch) and appears
		//     after "name".
		// Symmetric logic applies to slot 1 (gets {name:"z",x:1}).
		// Pinning these exact bytes guards against any silent drift
		// from positional to identity matching or a key-set heuristic.
		{
			Name:    "json_dr007_reshape_heterogeneous",
			Format:  "json",
			Source:  []byte(`{"items":[{"name":"z","x":1},{"name":"a","y":2}]}`),
			Queries: id,
			Reshape: []reshapeCase{
				{
					Name:   "reshape_sortby_heterogeneous_keys",
					Query:  ".items |= sort_by(.name)",
					Expect: []byte(`{"items":[{"name":"a","y":2},{"name":"z","x":1}]}`),
				},
			},
		},

		// --- YAML ---
		{
			Name:   "yaml_basic_nested",
			Format: "yaml",
			// nesdit's YAML encoder uses 2-space indent (yaml.v3's default),
			// so the corpus sources match that shape for byte-identical identity.
			Source:  []byte("a:\n  b: 1\n  c: 2\nitems:\n  - x: 10\n    y: 20\n  - x: 30\n"),
			Queries: full,
		},
		{
			Name:   "yaml_tagged_timestamp",
			Format: "yaml",
			// The "when" value is explicitly quoted so yaml.v3 decoder
			// tags it !!str (not !!timestamp), matching what our
			// decoder would emit on re-encode of a !!str-tagged value.
			Source:  []byte("a:\n  b: 1\nwhen: \"2026-04-26T10:22:04Z\"\nname: nesdit\n"),
			Queries: ab,
		},
		{
			// Unquoted RFC 3339 datetime — yaml.v3 resolves this to the
			// !!timestamp tag at decode. This case exercises the
			// tag-preservation path in internal/omap/bridge.go
			// (bytes-equal gate at anyToValue for KindStr+prev.Tag) via:
			//   - ".": identity — tag must survive ToAny→FromAny→encode.
			//   - ".when |= .": no-op through gojq — the bridge must
			//     still carry prev.Tag forward because the scalar bytes
			//     match prev.
			// DR-005 explicitly called out !!timestamp as an input class;
			// prior to this entry the bridge's tag path had no e2e cover.
			Name:    "yaml_tagged_timestamp_unquoted",
			Format:  "yaml",
			Source:  []byte("a:\n  b: 1\nwhen: 2026-04-26T10:22:04Z\nname: nesdit\n"),
			Queries: []string{".", ".when |= ."},
		},
		{
			Name:    "yaml_big_int",
			Format:  "yaml",
			Source:  []byte("big: 9007199254740993\na:\n  b: 42\n"),
			Queries: ab,
		},
		{
			Name:    "yaml_unicode_keys",
			Format:  "yaml",
			Source:  []byte("a:\n  b: ok\nκλειδί: τιμή\n"),
			Queries: ab,
		},
		{
			Name:    "yaml_arrays_of_maps",
			Format:  "yaml",
			Source:  []byte("items:\n  - name: a\n    x: 1\n  - x: 2\n    name: b\n"),
			Queries: items,
		},
		{
			Name:    "yaml_mixed_scalars",
			Format:  "yaml",
			Source:  []byte("a:\n  b: 10\nbool: true\nf: 3.14\ns: hello\n"),
			Queries: ab,
		},

		// --- TOML ---
		// nesdit's TOML encoder emits every nested table as an inline
		// table (DR-006), so the corpus sources here use the same flat
		// inline-table shape the encoder produces.
		{
			Name:    "toml_basic_nested",
			Format:  "toml",
			Source:  []byte("a = {b = 1, c = 2}\nitems = [{x = 10, y = 20}, {x = 30}]\n"),
			Queries: full,
		},
		{
			Name:    "toml_big_int",
			Format:  "toml",
			Source:  []byte("big = 9007199254740993\na = {b = 42}\n"),
			Queries: ab,
		},
		{
			Name:    "toml_arrays_of_maps",
			Format:  "toml",
			Source:  []byte("items = [{name = \"a\", x = 1}, {x = 2, name = \"b\"}]\n"),
			Queries: items,
		},
		{
			Name:    "toml_unicode_keys",
			Format:  "toml",
			Source:  []byte("\"κλειδί\" = \"τιμή\"\na = {b = 1}\n"),
			Queries: ab,
		},
		{
			Name:    "toml_mixed_scalars",
			Format:  "toml",
			Source:  []byte("a = {b = 1}\nbool = true\nf = 3.14\ns = \"hello\"\n"),
			Queries: ab,
		},
		{
			Name:    "toml_identity_only_with_datetime",
			Format:  "toml",
			Source:  []byte("when = 2026-04-26T10:22:04Z\nname = \"nesdit\"\n"),
			Queries: id,
		},
		{
			Name:    "toml_empty_structures",
			Format:  "toml",
			Source:  []byte("a = {b = {}}\nitems = []\n"),
			Queries: ab,
		},
	}
	return cases
}
