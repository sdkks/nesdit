package msgpackview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/sdkks/nesdit/internal/omap"
)

// Interpretation is explicit: ordinary objects never acquire tag semantics.
type Interpretation uint8

// OrdinaryJSON preserves application objects; RichJSON interprets explicit v1 tags.
const (
	OrdinaryJSON Interpretation = iota
	RichJSON
)

var errJSON = errors.New("invalid JSON source")

// ReadJSON certifies lexical structure and decoded text costs before allocating
// the ordered tree. Ordinary numeric lexemes are not target-validated here.
func ReadJSON(ctx context.Context, body []byte, profile Interpretation) (omap.Value, error) {
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	if profile != OrdinaryJSON && profile != RichJSON {
		return omap.Value{}, errors.New("unknown JSON interpretation")
	}
	if len(body) > rawLimit {
		return omap.Value{}, errors.New("JSON input exceeds 10 MiB")
	}
	if !utf8.Valid(body) {
		return omap.Value{}, errJSON
	}
	p := jsonScan{ctx: ctx, text: string(body)}
	if err := p.value(0); err != nil {
		return omap.Value{}, err
	}
	if err := p.space(); err != nil {
		return omap.Value{}, err
	}
	if p.pos != len(p.text) {
		return omap.Value{}, errJSON
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	v, err := jsonTree(ctx, d)
	if err != nil {
		return omap.Value{}, err
	}
	if _, endErr := d.Token(); endErr != io.EOF {
		return omap.Value{}, errJSON
	}
	if profile == RichJSON {
		if err := validateTarget(ctx, v, profile); err != nil {
			return omap.Value{}, err
		}
	}
	return v, ctx.Err()
}

type jsonScan struct {
	ctx            context.Context
	text           string
	pos, nextCheck int
	view           budget
}

func (p *jsonScan) check() error {
	if p.pos >= p.nextCheck {
		p.nextCheck = p.pos + 4096
		return p.ctx.Err()
	}
	return nil
}
func (p *jsonScan) space() error {
	for p.pos < len(p.text) {
		if err := p.check(); err != nil {
			return err
		}
		switch p.text[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return nil
		}
	}
	return p.ctx.Err()
}
func (p *jsonScan) take(c byte) bool {
	if p.pos < len(p.text) && p.text[p.pos] == c {
		p.pos++
		return true
	}
	return false
}
func (p *jsonScan) value(depth int) error {
	if err := p.space(); err != nil {
		return err
	}
	if !p.view.add(depth, 1, 0) {
		return errView
	}
	if p.pos == len(p.text) {
		return errJSON
	}
	switch p.text[p.pos] {
	case '"':
		return p.string()
	case '[', '{':
		object := p.text[p.pos] == '{'
		end := byte(']')
		if object {
			end = '}'
		}
		p.pos++
		if err := p.space(); err != nil {
			return err
		}
		if p.take(end) {
			return nil
		}
		for {
			if object {
				if err := p.string(); err != nil {
					return err
				}
				if err := p.space(); err != nil {
					return err
				}
				if !p.take(':') {
					return errJSON
				}
			}
			if err := p.value(depth + 1); err != nil {
				return err
			}
			if err := p.space(); err != nil {
				return err
			}
			if p.take(end) {
				return nil
			}
			if !p.take(',') {
				return errJSON
			}
			if err := p.space(); err != nil {
				return err
			}
		}
	case 'n', 't', 'f':
		literal := "null"
		if p.text[p.pos] == 't' {
			literal = "true"
		}
		if p.text[p.pos] == 'f' {
			literal = "false"
		}
		if len(p.text)-p.pos < len(literal) || p.text[p.pos:p.pos+len(literal)] != literal {
			return errJSON
		}
		p.pos += len(literal)
		return nil
	default:
		start := p.pos
		for p.pos < len(p.text) {
			c := p.text[p.pos]
			if c != '-' && c != '+' && c != '.' && c != 'e' && c != 'E' && (c < '0' || c > '9') {
				break
			}
			p.pos++
			if err := p.check(); err != nil {
				return err
			}
		}
		if _, _, ok := decimal(p.text[start:p.pos], false); !ok {
			return errJSON
		}
		return nil
	}
}

// string counts unescaped UTF-8 bytes, without building the string. Whole-body
// UTF-8 was checked first; escaped UTF-16 must form complete surrogate pairs.
func (p *jsonScan) string() error {
	if !p.take('"') {
		return errJSON
	}
	for p.pos < len(p.text) {
		if err := p.check(); err != nil {
			return err
		}
		c := p.text[p.pos]
		p.pos++
		if c == '"' {
			return nil
		}
		if c < 0x20 {
			return errJSON
		}
		n := 1
		if c == '\\' {
			if p.pos == len(p.text) {
				return errJSON
			}
			c = p.text[p.pos]
			p.pos++
			switch c {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				r, ok := p.hex4()
				if !ok {
					return errJSON
				}
				if r >= 0xd800 && r <= 0xdbff {
					if !p.take('\\') || !p.take('u') {
						return errJSON
					}
					lo, ok := p.hex4()
					if !ok || lo < 0xdc00 || lo > 0xdfff {
						return errJSON
					}
					r = 0x10000 + (r-0xd800)*1024 + lo - 0xdc00
				} else if r >= 0xdc00 && r <= 0xdfff {
					return errJSON
				}
				n = utf8.RuneLen(r)
			default:
				return errJSON
			}
		}
		if !p.view.add(0, 0, n) {
			return errView
		}
	}
	return errJSON
}
func (p *jsonScan) hex4() (rune, bool) {
	if len(p.text)-p.pos < 4 {
		return 0, false
	}
	var r rune
	for range 4 {
		c := p.text[p.pos]
		p.pos++
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r += rune(c - '0')
		case c >= 'a' && c <= 'f':
			r += rune(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			r += rune(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return r, true
}

func jsonTree(ctx context.Context, d *json.Decoder) (omap.Value, error) {
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	token, err := d.Token()
	if err != nil {
		return omap.Value{}, errJSON
	}
	switch t := token.(type) {
	case nil:
		return omap.NullValue(), nil
	case bool:
		return omap.BoolValue(t), nil
	case string:
		return omap.StrValue(t), nil
	case json.Number:
		return omap.NumValue(t), nil
	case json.Delim:
		if t == '[' {
			var values []omap.Value
			for d.More() {
				v, err := jsonTree(ctx, d)
				if err != nil {
					return omap.Value{}, err
				}
				values = append(values, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return omap.Value{}, errJSON
			}
			return omap.SeqValue(values...), nil
		}
		if t == '{' {
			doc := omap.New()
			for d.More() {
				token, err := d.Token()
				if err != nil {
					return omap.Value{}, errJSON
				}
				key, ok := token.(string)
				if !ok {
					return omap.Value{}, errJSON
				}
				if doc.Has(key) {
					return omap.Value{}, errors.New("duplicate JSON object key")
				}
				v, err := jsonTree(ctx, d)
				if err != nil {
					return omap.Value{}, err
				}
				doc.Set(key, v)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return omap.Value{}, errJSON
			}
			return omap.MapValue(doc), nil
		}
	}
	return omap.Value{}, errJSON
}
