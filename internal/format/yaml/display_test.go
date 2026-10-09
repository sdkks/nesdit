package yaml

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/goccy/go-yaml/lexer"
	"github.com/sdkks/nesdit/internal/display"
	"github.com/sdkks/nesdit/internal/omap"
	"gopkg.in/yaml.v3"
)

func assertYAMLSpans(t *testing.T, data []byte, roles []display.Role) []display.Span {
	t.Helper()
	spans := DisplaySpans(data)
	if len(spans) != len(roles) {
		view, coords := yamlCoordinates(data)
		for _, tk := range lexer.Tokenize(view) {
			offset, valid := coords.offset(tk.Position.Line, tk.Position.Column)
			t.Logf("token %v value=%q pos=%v byte=%v valid=%v", tk.Type, tk.Value, tk.Position, offset, valid)
		}
		var root yaml.Node
		_ = yaml.Unmarshal(data, &root)
		var logNode func(*yaml.Node)
		logNode = func(n *yaml.Node) {
			t.Logf("node kind=%v value=%q tag=%v style=%v pos=%d:%d", n.Kind, n.Value, n.Tag, n.Style, n.Line, n.Column)
			for _, c := range n.Content {
				logNode(c)
			}
		}
		logNode(&root)
		t.Fatalf("payload=%q spans=%+v want roles=%v", data, spans, roles)
	}
	for i, role := range roles {
		if spans[i].Role != role {
			t.Fatalf("payload=%q span=%+v want role=%v", data, spans[i], role)
		}
	}
	var styled bytes.Buffer
	if err := display.Write(&styled, data, spans); err != nil {
		t.Fatal(err)
	}
	// Remove only the insertions at tracked span boundaries, not source escape
	// sequences or anything that happens to match an ANSI regular expression.
	styles := map[display.Role]string{display.Key: "\x1b[1;39m", display.String: "\x1b[32m", display.Number: "\x1b[36m", display.Bool: "\x1b[35m", display.Null: "\x1b[39m"}
	var recovered bytes.Buffer
	cursor, original := 0, 0
	for _, s := range spans {
		gap := s.Start - original
		recovered.Write(styled.Bytes()[cursor : cursor+gap])
		cursor += gap
		style := styles[s.Role]
		if string(styled.Bytes()[cursor:cursor+len(style)]) != style {
			t.Fatal("missing expected style")
		}
		cursor += len(style)
		size := s.End - s.Start
		recovered.Write(styled.Bytes()[cursor : cursor+size])
		cursor += size
		if string(styled.Bytes()[cursor:cursor+4]) != "\x1b[0m" {
			t.Fatal("missing reset")
		}
		cursor += 4
		original = s.End
	}
	recovered.Write(styled.Bytes()[cursor:])
	if !bytes.Equal(recovered.Bytes(), data) {
		t.Fatalf("lost original bytes: %q != %q", recovered.Bytes(), data)
	}
	return spans
}

func encodeYAMLFixture(t *testing.T, v omap.Value) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := EncodeValue(&buf, v); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestYAMLDisplayActualStringKeysAndValues(t *testing.T) {
	fixtures := []string{"", "ordinary", "12", "true", "null", "yes", "no", "on", "off", "a: b", "#hash", "slash/a", "http://a/#b", "a:b", "a # b", "a'b", "a\"b", "é🙂", "a\x01b", "a\tb", "\\x\\u\\U", "a\rb", "a\r\nb", "line\nkey", "line\nkey\n", "line\nkey\n\n", "\n", "\n\n", " \n", "  line\n  key\n", "a\u0085b", "a\u2028b", "a\u2029b", "a\u2028b\n", "line\na\u2028b\n", "line\na\u2029b\n", "line\na\u0085b\n", "\u0085", "\u2028", "\u2029", "trailing ", "a\n\nb", "\x1b[32m", strings.Repeat("long", 300), strings.Repeat("é", 1200)}
	mixed := omap.New()
	var mixedRoles []display.Role
	for _, s := range fixtures {
		mixed.Set(s, omap.StrValue(s))
		mixedRoles = append(mixedRoles, display.Key, display.String)
		t.Run(strings.ReplaceAll(s, "\n", "NL"), func(t *testing.T) {
			d := omap.New()
			d.Set(s, omap.StrValue(s))
			data := encodeYAMLFixture(t, omap.MapValue(d))
			spans := assertYAMLSpans(t, data, []display.Role{display.Key, display.String})
			if prefix := strings.TrimSpace(string(data[:spans[0].Start])); prefix != "" && prefix != "?" {
				t.Fatalf("unclassified key prefix %q", prefix)
			}
			if gap := strings.TrimSpace(string(data[spans[0].End:spans[1].Start])); gap != ":" {
				t.Fatalf("incomplete scalar or styled punctuation: gap=%q", gap)
			}
			if suffix := strings.TrimSpace(string(data[spans[1].End:])); suffix != "" {
				t.Fatalf("unclassified value suffix %q", suffix)
			}
			// Exercise unchanged original framing, including non-LF endings.
			for _, ending := range []string{"\r\n", "\r"} {
				modified := bytes.ReplaceAll(data, []byte("\n"), []byte(ending))
				assertYAMLSpans(t, modified, []display.Role{display.Key, display.String})
			}
		})
	}
	assertYAMLSpans(t, encodeYAMLFixture(t, omap.MapValue(mixed)), mixedRoles)
}

func TestYAMLDisplayActualTypedRootsAndTags(t *testing.T) {
	cases := []struct {
		v     omap.Value
		roles []display.Role
	}{
		{omap.NullValue(), []display.Role{display.Null}},
		{omap.BoolValue(true), []display.Role{display.Bool}},
		{omap.BoolValue(false), []display.Role{display.Bool}},
		{omap.NumValue(json.Number("-12.5e+2")), []display.Role{display.Number}},
		{omap.IntValue(42), []display.Role{display.Number}},
		{omap.StrValue("yes"), []display.Role{display.String}},
		{omap.StrValue("no"), []display.Role{display.String}},
		{omap.StrValue("é🙂\x01"), []display.Role{display.String}},
		{omap.StrValue("\n\n"), []display.Role{display.String}},
		{omap.StrValueTagged("2026-10-09", "!!timestamp"), nil},
		{omap.StrValueTagged("42", "!custom"), nil},
		{omap.StrValueTagged("a: b", "!custom"), nil},
		{omap.StrValueTagged("line\nvalue\n", "!custom"), nil},
		{omap.StrValueTagged("42", "tag:example.com,2020:kind"), nil},
		{omap.StrValue("<<"), nil}, // Native yaml.v3 resolves this emitted scalar as !!merge.
		{omap.StrValueTagged("YWJj", "!!binary"), []display.Role{display.String}},
		{omap.StrValueTagged("YWJj\nZGVm\n", "!!binary"), []display.Role{display.String}},
		{omap.MapValue(omap.New()), nil},
		{omap.Value{Kind: omap.KindSeq}, nil},
	}
	for _, c := range cases {
		data := encodeYAMLFixture(t, c.v)
		spans := assertYAMLSpans(t, data, c.roles)
		if len(spans) == 1 && strings.TrimSpace(string(data[spans[0].End:])) != "" {
			t.Fatalf("incomplete styled scalar root %q", data)
		}
		d := omap.New()
		d.Set("key", c.v)
		assertYAMLSpans(t, encodeYAMLFixture(t, omap.MapValue(d)), append([]display.Role{display.Key}, c.roles...))
	}
}

func TestYAMLDisplayNestedAndBlankBlocks(t *testing.T) {
	d := omap.New()
	for _, s := range []string{"\n", "\n\n", "line\nkey", "a\u2028b", "a\u0085b", "after"} {
		d.Set(s, omap.StrValue(s))
	}
	d.Set("empty", omap.MapValue(omap.New()))
	outer := omap.New()
	d.Entries(func(k string, v omap.Value) bool { outer.Set(k, v); return true })
	outer.Set("seq", omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{omap.MapValue(d), omap.StrValue("\n\n"), omap.IntValue(3)}})
	// Keep the nested map finite by building it before adding the sequence.
	roles := []display.Role{}
	for range 6 {
		roles = append(roles, display.Key, display.String)
	}
	roles = append(roles, display.Key, display.Key)
	for range 6 {
		roles = append(roles, display.Key, display.String)
	}
	roles = append(roles, display.Key, display.String, display.Number)
	assertYAMLSpans(t, encodeYAMLFixture(t, omap.MapValue(outer)), roles)
}

func TestYAMLDisplayActualTaggedSequenceBlockBoundaries(t *testing.T) {
	for _, tag := range []string{"!!binary", "!custom"} {
		block := omap.StrValueTagged("YWJj\nZGVm\n", tag)
		sequence := omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{block, omap.IntValue(7)}}
		mapping := omap.New()
		mapping.Set("key", block)
		cases := []struct {
			name    string
			value   omap.Value
			payload string
			body    string
			keys    bool
		}{
			{"sequence", sequence, "- " + tag + " |\n  YWJj\n  ZGVm\n- 7\n", "|\n  YWJj\n  ZGVm\n", false},
			{"nested-sequence", omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{sequence}}, "- - " + tag + " |\n    YWJj\n    ZGVm\n  - 7\n", "|\n    YWJj\n    ZGVm\n", false},
			{"sequence-mapping", omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{omap.MapValue(mapping), omap.IntValue(7)}}, "- key: " + tag + " |\n    YWJj\n    ZGVm\n- 7\n", "|\n    YWJj\n    ZGVm\n", true},
			{"nested-sequence-mapping", omap.Value{Kind: omap.KindSeq, Seq: []omap.Value{{Kind: omap.KindSeq, Seq: []omap.Value{omap.MapValue(mapping), omap.IntValue(7)}}}}, "- - key: " + tag + " |\n      YWJj\n      ZGVm\n  - 7\n", "|\n      YWJj\n      ZGVm\n", true},
		}
		for _, c := range cases {
			t.Run(tag+"/"+c.name, func(t *testing.T) {
				data := encodeYAMLFixture(t, c.value)
				if string(data) != c.payload {
					t.Fatalf("actual encoder payload=%q want=%q", data, c.payload)
				}
				var roles []display.Role
				var want []string
				if c.keys {
					roles = append(roles, display.Key)
					want = append(want, "key")
				}
				if tag == "!!binary" {
					roles = append(roles, display.String)
					want = append(want, c.body)
				}
				roles = append(roles, display.Number)
				want = append(want, "7")
				// Require the complete body and only the following numeric scalar;
				// the helper also checks exact recovery of tracked ANSI insertions.
				spans := assertYAMLSpans(t, data, roles)
				for i, s := range spans {
					if string(data[s.Start:s.End]) != want[i] {
						t.Fatalf("span=%q want=%q", data[s.Start:s.End], want[i])
					}
				}
			})
		}
	}
}

func TestYAMLDisplayExactBoundaries(t *testing.T) {
	data := []byte("'a''b': \"é\\U0001F642\\x01\"\nempty: ''\nyes: no\nn: 42\nb: false\nz: null\nt: !!binary YWJj\n")
	roles := []display.Role{display.Key, display.String, display.Key, display.String, display.Key, display.String, display.Key, display.Number, display.Key, display.Bool, display.Key, display.Null, display.Key, display.String}
	spans := assertYAMLSpans(t, data, roles)
	want := []string{"'a''b'", "\"é\\U0001F642\\x01\"", "empty", "''", "yes", "no", "n", "42", "b", "false", "z", "null", "t", "YWJj"}
	for i, s := range spans {
		if string(data[s.Start:s.End]) != want[i] {
			t.Fatalf("span=%q want=%q", data[s.Start:s.End], want[i])
		}
	}
	for _, suffix := range []string{"", "\n\n", "\r\n"} {
		assertYAMLSpans(t, append(bytes.Clone(data), suffix...), roles)
	}
}

func TestYAMLDisplayUnknownAndInvalidUTF8StayPlain(t *testing.T) {
	for _, data := range [][]byte{[]byte("a: [broken"), {'k', ':', ' ', 0xff, '\n'}, []byte("a: &anchor x\nb: *anchor\n")} {
		if spans := DisplaySpans(data); len(spans) != 0 {
			t.Fatalf("unexpected spans %+v", spans)
		}
		var out bytes.Buffer
		if err := display.Write(&out, data, DisplaySpans(data)); err != nil || !bytes.Equal(out.Bytes(), data) {
			t.Fatal("fallback changed bytes")
		}
	}
}
