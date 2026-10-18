package msgpackview

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/sdkks/nesdit/internal/omap"
)

var errProfile = errors.New("invalid MessagePack JSON v1 value")

type targetCheck struct {
	ctx            context.Context
	semantic, view budget
}

func validateTarget(ctx context.Context, v omap.Value, profile Interpretation) error {
	c := targetCheck{ctx: ctx}
	switch profile {
	case OrdinaryJSON:
		// Count the hypothetical rich expansion, but never build a second tree.
		return c.ordinary(v, 0, 0)
	case RichJSON:
		if err := c.actualView(v, 0); err != nil {
			return err
		}
		return c.rich(v, 0)
	default:
		return errors.New("unknown JSON interpretation")
	}
}

func (c *targetCheck) actualView(v omap.Value, depth int) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if !c.view.add(depth, 1, 0) {
		return errView
	}
	switch v.Kind {
	case omap.KindNull, omap.KindBool:
	case omap.KindNum:
		if _, _, ok := decimal(v.Num.String(), false); !ok {
			return errProfile
		}
	case omap.KindStr:
		if !c.view.add(depth, 0, len(v.Str)) {
			return errView
		}
		if !utf8.ValidString(v.Str) {
			return errProfile
		}
	case omap.KindSeq:
		for _, child := range v.Seq {
			if err := c.actualView(child, depth+1); err != nil {
				return err
			}
		}
	case omap.KindMap:
		for key, child := range v.Map.Entries {
			if !c.view.add(depth, 0, len(key)) {
				return errView
			}
			if !utf8.ValidString(key) {
				return errProfile
			}
			if err := c.actualView(child, depth+1); err != nil {
				return err
			}
		}
	default:
		return errProfile
	}
	return nil
}

func (c *targetCheck) ordinary(v omap.Value, depth, viewDepth int) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if !c.semantic.add(depth, 1, 0) {
		return errSemantic
	}
	if !c.view.add(viewDepth, 1, 0) {
		return errView
	}
	switch v.Kind {
	case omap.KindNull, omap.KindBool:
	case omap.KindStr:
		if !c.semantic.add(depth, 0, len(v.Str)) {
			return errSemantic
		}
		if !c.view.add(viewDepth, 0, len(v.Str)) {
			return errView
		}
		if !utf8.ValidString(v.Str) {
			return errProfile
		}
	case omap.KindNum:
		key, text, err := ordinaryNumber(v.Num.String())
		if err != nil {
			return err
		}
		if !c.view.add(viewDepth+1, 1, len(key)+len(text)) {
			return errView
		}
	case omap.KindSeq:
		for _, child := range v.Seq {
			if err := c.ordinary(child, depth+1, viewDepth+1); err != nil {
				return err
			}
		}
	case omap.KindMap:
		if !c.view.add(viewDepth+1, 1, len("$map")) {
			return errView
		}
		for key, child := range v.Map.Entries {
			if !c.view.add(viewDepth+2, 1, 0) {
				return errView
			} // pair array
			if err := c.ordinary(omap.StrValue(key), depth+1, viewDepth+3); err != nil {
				return err
			}
			if err := c.ordinary(child, depth+1, viewDepth+3); err != nil {
				return err
			}
		}
	default:
		return errProfile
	}
	return nil
}

func (c *targetCheck) rich(v omap.Value, depth int) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if !c.semantic.add(depth, 1, 0) {
		return errSemantic
	}
	switch v.Kind {
	case omap.KindNull, omap.KindBool:
		return nil
	case omap.KindStr:
		if !c.semantic.add(depth, 0, len(v.Str)) {
			return errSemantic
		}
		return nil // UTF-8 already certified by actualView.
	case omap.KindSeq:
		for _, child := range v.Seq {
			if err := c.rich(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	case omap.KindMap:
		if v.Map.Len() != 1 {
			return errProfile
		}
		key, arg := v.Map.At(0)
		switch key {
		case "$int":
			if arg.Kind != omap.KindStr {
				return errProfile
			}
			_, _, _, err := integer(arg.Str)
			return err
		case "$float":
			if arg.Kind != omap.KindStr {
				return errProfile
			}
			_, err := floatNumber(arg.Str, true)
			return err
		case "$bin", "$str64":
			data, err := c.binary(arg)
			if err != nil {
				return err
			}
			if key == "$str64" && utf8.Valid(data) {
				return errProfile
			}
			return nil
		case "$ext":
			if arg.Kind != omap.KindSeq || len(arg.Seq) != 2 {
				return errProfile
			}
			id, err := smallInteger(arg.Seq[0])
			if err != nil || id < -128 || id > 127 || id == -1 {
				return errProfile
			}
			_, err = c.binary(arg.Seq[1])
			return err
		case "$time":
			_, _, err := timestampParts(arg)
			return err
		case "$map":
			if arg.Kind != omap.KindSeq {
				return errProfile
			}
			for _, pair := range arg.Seq {
				if pair.Kind != omap.KindSeq || len(pair.Seq) != 2 {
					return errProfile
				}
				for _, child := range pair.Seq {
					if err := c.rich(child, depth+1); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	return errProfile
}

func smallInteger(v omap.Value) (int64, error) {
	if v.Kind != omap.KindNum {
		return 0, errProfile
	}
	n, err := strconv.ParseInt(v.Num.String(), 10, 64)
	if err != nil {
		return 0, errProfile
	}
	return n, nil
}

func timestampParts(v omap.Value) (int64, uint32, error) {
	if v.Kind != omap.KindSeq || len(v.Seq) != 2 || v.Seq[0].Kind != omap.KindStr {
		return 0, 0, errProfile
	}
	text := v.Seq[0].Str
	if _, _, _, err := integer(text); err != nil {
		return 0, 0, errProfile
	}
	sec, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, 0, errProfile
	}
	nanos, err := smallInteger(v.Seq[1])
	if err != nil || nanos < 0 || nanos >= 1_000_000_000 {
		return 0, 0, errProfile
	}
	return sec, uint32(nanos), nil
}

func (c *targetCheck) binary(v omap.Value) ([]byte, error) {
	if v.Kind != omap.KindStr {
		return nil, errProfile
	}
	s := v.Str
	n, err := base64Size(c.ctx, s)
	if err != nil {
		return nil, err
	}
	if !c.semantic.add(0, 0, n) {
		return nil, errSemantic
	}
	data, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || len(data) != n {
		return nil, errProfile
	}
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

// Certify syntax and exact decoded size before DecodeString allocates. The
// standard decoder tolerates newlines even in Strict mode; this grammar does not.
func base64Size(ctx context.Context, s string) (int, error) {
	if len(s)%4 != 0 {
		return 0, errProfile
	}
	padding := 0
	if s != "" && s[len(s)-1] == '=' {
		padding = 1
		if s[len(s)-2] == '=' {
			padding = 2
		}
	}
	last := byte(0)
	for i := 0; i < len(s)-padding; i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		switch ch := s[i]; {
		case ch >= 'A' && ch <= 'Z':
			last = ch - 'A'
		case ch >= 'a' && ch <= 'z':
			last = ch - 'a' + 26
		case ch >= '0' && ch <= '9':
			last = ch - '0' + 52
		case ch == '+':
			last = 62
		case ch == '/':
			last = 63
		default:
			return 0, errProfile
		}
	}
	if padding == 1 && last&3 != 0 || padding == 2 && last&15 != 0 {
		return 0, errProfile
	}
	return len(s)/4*3 - padding, ctx.Err()
}
