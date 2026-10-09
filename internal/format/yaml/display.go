package yaml

import (
	"bytes"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/token"
	"github.com/sdkks/nesdit/internal/display"
	"gopkg.in/yaml.v3"
)

// DisplaySpans classifies one successfully serialized YAML payload. Native
// nodes own roles; lexer coordinates supply ordinary plain-scalar boundaries.
// All ranges refer to the original bytes, never Token.Origin or rune offsets.
// This is deliberately an adapter for our encoder, not a YAML-input lexer.
// Unknown shapes return no spans before the display sink writes anything.
func DisplaySpans(data []byte) []display.Span {
	if !utf8.Valid(data) {
		return nil
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil
	}
	view, coordinates := yamlCoordinates(data)
	tokens := lexer.Tokenize(view)
	at := make(map[int]int, len(tokens))
	for i, t := range tokens {
		if p, ok := coordinates.offset(t.Position.Line, t.Position.Column); ok {
			at[p] = i
		}
	}
	var spans []display.Span
	var blocks []display.Span
	var visit func(*yaml.Node, bool) bool
	visit = func(n *yaml.Node, key bool) bool {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode, yaml.MappingNode:
			for i, child := range n.Content {
				if !visit(child, n.Kind == yaml.MappingNode && i%2 == 0) {
					return false
				}
			}
			return true
		case yaml.ScalarNode:
		default:
			return false // Our serializer never emits aliases.
		}
		start, ok := coordinates.offset(n.Line, n.Column)
		if !ok || start >= len(data) {
			return false
		}
		content := start
		plainIndex, havePlain := at[start]
		// Native coordinates include explicit tags; leave the tag neutral.
		if data[content] == '!' {
			// The lexer reports tagged plain content at the preceding space.
			// Anchor it to the native tag start and use its following token.
			if havePlain && tokens[plainIndex].Type == token.TagType {
				plainIndex++
				havePlain = plainIndex < len(tokens)
			} else {
				havePlain = false
			}
			for content < len(data) && data[content] != ' ' && data[content] != '\n' && data[content] != '\r' {
				content++
			}
			for content < len(data) && data[content] == ' ' {
				content++
			}
		}
		if content >= len(data) {
			return false
		}
		end := -1
		switch {
		case n.Style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) != 0:
			end = yamlQuoteEnd(data, content)
		case n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0:
			end = yamlBlockEnd(data, start, content)
			if end > content && bytes.HasPrefix(data[content:end], []byte("|2+")) && strings.Trim(n.Value, "\n") == "" {
				markerEnd := end
				for markerEnd < len(data) && data[markerEnd] == ' ' {
					markerEnd++
				}
				blocks = append(blocks, display.Span{Start: content, End: markerEnd})
			}
		default:
			if havePlain && yamlPlainToken(tokens[plainIndex].Type) {
				end = len(data)
				if plainIndex+1 < len(tokens) {
					p := tokens[plainIndex+1].Position
					var mapped bool
					end, mapped = coordinates.offset(p.Line, p.Column)
					if !mapped {
						return false
					}
				}
				if end > content && end <= len(data) {
					end = content + len(bytes.TrimRight(data[content:end], " \t\r\n"))
				}
			}
		}
		if end <= content || end > len(data) {
			return false
		}
		role := display.Role(0)
		if key {
			role = display.Key
		} else {
			switch n.Tag {
			case "!!str", "!!binary":
				role = display.String
			case "!!int", "!!float":
				role = display.Number
			case "!!bool":
				role = display.Bool
			case "!!null":
				role = display.Null
			}
		}
		if role != 0 {
			spans = append(spans, display.Span{Start: content, End: end, Role: role})
		}
		return true
	}
	if !visit(&root, false) {
		return nil
	}
	// goccy marks yaml.v3's blank |2+ bodies invalid. Accept those markers
	// only inside a native block extent; other lexical failures stay plain.
	for _, t := range tokens {
		if t.Type != token.InvalidType {
			continue
		}
		p, ok := coordinates.offset(t.Position.Line, t.Position.Column)
		covered := false
		for _, b := range blocks {
			covered = covered || (ok && p >= b.Start && p <= b.End)
		}
		if !covered {
			return nil
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	end := 0
	for _, s := range spans {
		if s.Start < end || s.End <= s.Start || s.End > len(data) {
			return nil
		}
		end = s.End
	}
	return spans
}

type yamlCoordinate [][]int

func (c yamlCoordinate) offset(line, column int) (int, bool) {
	if line < 1 || line > len(c) || column < 1 || column > len(c[line-1]) {
		return 0, false
	}
	return c[line-1][column-1], true
}

// yaml.v3 counts Unicode runes, and treats CRLF, CR, NEL, LS and PS as
// line breaks. Give goccy (which recognizes CR/LF only) a normalized view,
// while mapping each coordinate back to the untouched source byte offset.
func yamlCoordinates(data []byte) (string, yamlCoordinate) {
	positions := yamlCoordinate{nil}
	var view strings.Builder
	for i := 0; i < len(data); {
		line := len(positions) - 1
		positions[line] = append(positions[line], i)
		r, size := utf8.DecodeRune(data[i:])
		if r == '\r' || r == '\n' || r == '\u0085' || r == '\u2028' || r == '\u2029' {
			if r == '\r' && i+size < len(data) && data[i+size] == '\n' {
				size++
			}
			view.WriteByte('\n')
			positions = append(positions, nil)
		} else {
			view.WriteRune(r)
		}
		i += size
	}
	line := len(positions) - 1
	positions[line] = append(positions[line], len(data))
	return view.String(), positions
}

func yamlPlainToken(t token.Type) bool {
	switch t {
	case token.StringType, token.NullType, token.BoolType, token.IntegerType,
		token.BinaryIntegerType, token.OctetIntegerType, token.HexIntegerType,
		token.FloatType, token.InfinityType, token.NanType, token.MergeKeyType:
		return true
	}
	return false
}

func yamlQuoteEnd(data []byte, start int) int {
	quote := data[start]
	if quote != '\'' && quote != '"' {
		return -1
	}
	for i := start + 1; i < len(data); i++ {
		if quote == '"' && data[i] == '\\' {
			i++ // Escaped quotes/backslashes cannot terminate the scalar.
		} else if data[i] == quote {
			if quote == '\'' && i+1 < len(data) && data[i+1] == '\'' {
				i++
				continue
			}
			return i + 1
		}
	}
	return -1
}

// Block boundaries use indentation of the actual emitted header and body.
// The native scalar start anchors structural indentation; the content start
// anchors the block marker, after any explicit tag. This also covers newline-only
// |2+ keys/values, whose body has no token.
func yamlBlockEnd(data []byte, scalarStart, contentStart int) int {
	if data[contentStart] != '|' && data[contentStart] != '>' {
		return -1
	}
	lineStart := scalarStart
	for lineStart > 0 && data[lineStart-1] != '\n' && data[lineStart-1] != '\r' {
		lineStart--
	}
	base := 0
	for lineStart+base < scalarStart && data[lineStart+base] == ' ' {
		base++
	}
	// A mapping in a sequence begins after the dash, not at the physical
	// line's indentation (e.g. "  - ? |2+" or "  - key: |-"). Stop at
	// the native scalar start so its own dash ("- !!binary |") is not counted.
	for lineStart+base+2 < scalarStart && bytes.HasPrefix(data[lineStart+base:scalarStart], []byte("- ")) {
		base += 2
	}
	indent := 0
	i := contentStart + 1
	for i < len(data) && data[i] != '\n' && data[i] != '\r' {
		if data[i] >= '1' && data[i] <= '9' {
			indent = base + int(data[i]-'0')
		} else if data[i] != '+' && data[i] != '-' {
			return -1
		}
		i++
	}
	for i < len(data) {
		switch data[i] {
		case '\r':
			i++
			if i < len(data) && data[i] == '\n' {
				i++
			}
		case '\n':
			i++
		}
		begin := i
		spaces := 0
		for i < len(data) && data[i] == ' ' {
			spaces++
			i++
		}
		if i == len(data) {
			return i
		}
		if data[i] != '\n' && data[i] != '\r' {
			if indent == 0 {
				indent = spaces
			}
			if spaces <= base || spaces < indent {
				return begin
			}
		}
		for i < len(data) && data[i] != '\n' && data[i] != '\r' {
			i++
		}
	}
	return i
}
