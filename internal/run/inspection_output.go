package run

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/sdkks/nesdit/internal/display"
	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/omap"
)

// inspectionJSON stages only after exact layout admission. Presentation spans
// do not count toward authoritative JSON bytes; the final LF does.
func inspectionJSON(ctx context.Context, value omap.Value, pretty, color bool) ([]byte, []display.Span, error) {
	size, err := inspectionJSONSize(ctx, value, pretty, natsEventLimit)
	if err != nil {
		return nil, nil, err
	}
	buf := &protobufBuffer{ctx: ctx}
	var spans []display.Span
	var observe func(display.Span)
	if color {
		observe = func(s display.Span) { spans = append(spans, s) }
	}
	if err = jsonfmt.EncodeValueLayout(buf, value, pretty, observe); err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, errors.New("JSON value encoding failed")
	}
	if _, err = buf.WriteString("\n"); err != nil {
		return nil, nil, err
	}
	if buf.Len() != size {
		return nil, nil, errors.New("JSON output size mismatch")
	}
	return buf.Bytes(), spans, nil
}

// Mirror EncodeValueLayout without building escaped strings. The bound applies
// incrementally, before encoding/json.Marshal can allocate their expansion.
func inspectionJSONSize(ctx context.Context, value omap.Value, pretty bool, limit int) (int, error) {
	// Go's JSON implementations differ in spelling invalid UTF-8 replacement
	// (literal U+FFFD versus its escape). Probe one fixed byte, never input.
	replacement, _ := json.Marshal("\xff")
	s := jsonSize{ctx: ctx, pretty: pretty, remaining: limit, replacementSize: len(replacement) - 2}
	s.add(1) // final LF
	s.value(value, 0)
	return limit - s.remaining, s.err
}

type jsonSize struct {
	ctx             context.Context
	pretty          bool
	remaining       int
	replacementSize int
	err             error
}

func (s *jsonSize) add(n int) {
	if s.err != nil {
		return
	}
	if n > s.remaining {
		s.err = errors.New("serialized JSON exceeds output limit (16 MiB including layout and LF)")
		return
	}
	s.remaining -= n
}

func (s *jsonSize) newline(depth int) {
	if s.pretty {
		s.add(1 + 2*depth)
	}
}

func (s *jsonSize) string(value string) {
	s.add(2)
	nextCheck := 0
	for i := 0; i < len(value) && s.err == nil; {
		if i >= nextCheck {
			s.err = s.ctx.Err()
			nextCheck = i + 4096
			if s.err != nil {
				return
			}
		}
		c := value[i]
		if c < utf8.RuneSelf {
			switch c {
			case '\\', '"', '\n', '\r', '\t', '\b', '\f':
				s.add(2)
			case '<', '>', '&':
				s.add(6)
			default:
				if c < 0x20 {
					s.add(6)
				} else {
					s.add(1)
				}
			}
			i++
			continue
		}
		r, width := utf8.DecodeRuneInString(value[i:])
		if r == utf8.RuneError && width == 1 {
			s.add(s.replacementSize)
		} else if r == '\u2028' || r == '\u2029' {
			s.add(6)
		} else {
			s.add(width)
		}
		i += width
	}
}

func (s *jsonSize) value(v omap.Value, depth int) {
	if s.err != nil {
		return
	}
	if s.err = s.ctx.Err(); s.err != nil {
		return
	}
	switch v.Kind {
	case omap.KindNull:
		s.add(4)
	case omap.KindBool:
		if v.Bool {
			s.add(4)
		} else {
			s.add(5)
		}
	case omap.KindNum:
		n := v.Num.String()
		if n == "" {
			n = "0"
		}
		f, err := strconv.ParseFloat(n, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			s.err = errors.New("JSON number is not representable")
			return
		}
		s.add(len(n))
	case omap.KindStr:
		s.string(v.Str)
	case omap.KindSeq:
		s.add(2)
		for i, child := range v.Seq {
			if s.err != nil {
				return
			}
			if i > 0 {
				s.add(1)
			}
			s.newline(depth + 1)
			s.value(child, depth+1)
		}
		if len(v.Seq) > 0 {
			s.newline(depth)
		}
	case omap.KindMap:
		s.add(2)
		first := true
		v.Map.Entries(func(key string, child omap.Value) bool {
			if !first {
				s.add(1)
			}
			first = false
			s.newline(depth + 1)
			s.string(key)
			s.add(1)
			if s.pretty {
				s.add(1)
			}
			s.value(child, depth+1)
			return s.err == nil
		})
		if !first {
			s.newline(depth)
		}
	default:
		s.err = errors.New("JSON value kind is not supported")
	}
}
