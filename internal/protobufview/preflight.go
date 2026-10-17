package protobufview

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type budget struct {
	nodes, text int
	stage       string
}

func (b *budget) node(ctx context.Context, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > depthLimit {
		return fmt.Errorf("%s: depth exceeds 100", b.stage)
	}
	if b.nodes >= nodeLimit {
		return fmt.Errorf("%s: exceeds 1000000 value nodes", b.stage)
	}
	b.nodes++
	return nil
}
func (b *budget) addText(n int) error {
	if n > textLimit-b.text {
		return fmt.Errorf("%s: exceeds 16 MiB key/string bytes", b.stage)
	}
	b.text += n
	return nil
}

type preflight struct {
	ctx                  context.Context
	decoder              *decoder
	decoded, view        budget
	anyWire, certificate int
	expanded             map[protoreflect.Message]protoreflect.Message
}

func newPreflight(ctx context.Context, d *decoder) *preflight {
	return &preflight{ctx: ctx, decoder: d, decoded: budget{stage: "decoded"}, view: budget{stage: "view"}, expanded: make(map[protoreflect.Message]protoreflect.Message)}
}
func (p *preflight) size(n int) error {
	if n > serializedLimit-p.certificate {
		return errors.New("certificate: conservative ProtoJSON size exceeds 16 MiB")
	}
	p.certificate += n
	return nil
}

// The decoded walk counts message/list/map containers even when a WKT later
// collapses them. Each Any target descends from its enclosing Any and shares all
// counters. Expansions are immutable and used by the preflight walks; the
// official marshaler decodes them again rather than consuming this map.
func (p *preflight) decodedMessage(m protoreflect.Message, depth int) error {
	if err := p.decoded.node(p.ctx, depth); err != nil {
		return err
	}
	var result error
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if result = p.decoded.addText(len(f.JSONName())); result != nil {
			return false
		}
		result = p.decodedField(f, v, depth+1)
		return result == nil
	})
	if result != nil {
		return result
	}
	if m.Descriptor().FullName() != "google.protobuf.Any" {
		return nil
	}
	fields := m.Descriptor().Fields()
	url := m.Get(fields.ByNumber(1)).String()
	wire := m.Get(fields.ByNumber(2)).Bytes()
	if url == "" {
		if len(wire) != 0 {
			return errors.New("decode: Any value requires type URL")
		}
		return nil
	}
	if len(wire) > RawLimit-p.anyWire {
		return errors.New("decode: aggregate Any wire exceeds 10 MiB")
	}
	p.anyWire += len(wire)
	if depth >= depthLimit {
		return errors.New("decoded: expanded Any depth exceeds 100")
	}
	mt, err := p.decoder.types.FindMessageByURL(url)
	if err != nil {
		return errors.New("decode: Any target is not defined locally")
	}
	target := mt.New()
	err = (proto.UnmarshalOptions{Resolver: p.decoder.types, RecursionLimit: depthLimit + 1, DiscardUnknown: true}).Unmarshal(wire, target.Interface())
	if err != nil {
		return errors.New("decode: invalid Any wire data or missing required fields")
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	p.expanded[m] = target
	return p.decodedMessage(target, depth+1)
}
func (p *preflight) decodedField(f protoreflect.FieldDescriptor, v protoreflect.Value, depth int) error {
	if f.IsList() {
		if err := p.decoded.node(p.ctx, depth); err != nil {
			return err
		}
		list := v.List()
		for i := 0; i < list.Len(); i++ {
			if err := p.decodedScalar(f, list.Get(i), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if f.IsMap() {
		if err := p.decoded.node(p.ctx, depth); err != nil {
			return err
		}
		var result error
		v.Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
			key := k.String()
			if !utf8.ValidString(key) {
				result = errors.New("decode: invalid UTF-8 map key")
				return false
			}
			if result = p.decoded.addText(len(key)); result != nil {
				return false
			}
			result = p.decodedScalar(f.MapValue(), v, depth+1)
			return result == nil
		})
		return result
	}
	return p.decodedScalar(f, v, depth)
}
func (p *preflight) decodedScalar(f protoreflect.FieldDescriptor, v protoreflect.Value, depth int) error {
	if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
		return p.decodedMessage(v.Message(), depth)
	}
	if err := p.decoded.node(p.ctx, depth); err != nil {
		return err
	}
	// This is the reflection shape, not the mapped JSON scalar shape. Integer,
	// enum and special-float spellings charge text in the view walk only.
	// Bytes are the explicit exception: charge their base64 view length here.
	switch f.Kind() {
	case protoreflect.StringKind:
		if !utf8.ValidString(v.String()) {
			return errors.New("decode: invalid UTF-8 string")
		}
		return p.decoded.addText(len(v.String()))
	case protoreflect.BytesKind:
		return p.decoded.addText(base64.StdEncoding.EncodedLen(len(v.Bytes())))
	default:
		return nil
	}
}

// String size follows protobuf v1.36.12's compact JSON encoder: UTF-8 passes
// through, controls use at most six bytes, quotes/backslashes use two. It does
// not HTML-escape. Checkpoints bound our own scan, not library calls.
func stringSize(ctx context.Context, s string) (int, error) {
	if !utf8.ValidString(s) {
		return 0, errors.New("decode: invalid UTF-8 string")
	}
	n := 2
	for i := 0; i < len(s); i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		switch s[i] {
		case '"', '\\', '\b', '\f', '\n', '\r', '\t':
			n += 2
		default:
			if s[i] < ' ' {
				n += 6
			} else {
				n++
			}
		}
		if n > serializedLimit {
			return 0, errors.New("certificate: conservative ProtoJSON size exceeds 16 MiB")
		}
	}
	return n, nil
}

// Return mapped string bytes and an upper bound on the scalar spelling. 32
// covers finite float32/64 spellings (fixed below 1e21, scientific otherwise),
// intentionally permitting conservative rejection rather than measuring Marshal.
func scalarSize(ctx context.Context, f protoreflect.FieldDescriptor, v protoreflect.Value) (int, int, error) {
	var s string
	switch f.Kind() {
	case protoreflect.StringKind:
		s = v.String()
	case protoreflect.BytesKind:
		n := base64.StdEncoding.EncodedLen(len(v.Bytes()))
		return n, n + 2, nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		s = strconv.FormatInt(v.Int(), 10)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		s = strconv.FormatUint(v.Uint(), 10)
	case protoreflect.EnumKind:
		if f.Enum().FullName() == "google.protobuf.NullValue" {
			return 0, 4, nil
		}
		e := f.Enum().Values().ByNumber(v.Enum())
		if e == nil {
			return 0, 11, nil
		}
		s = string(e.Name())
	case protoreflect.BoolKind:
		return 0, 5, nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return 0, 11, nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return 0, 10, nil
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		n := v.Float()
		switch {
		case math.IsNaN(n):
			s = "NaN"
		case math.IsInf(n, 1):
			s = "Infinity"
		case math.IsInf(n, -1):
			s = "-Infinity"
		default:
			return 0, 32, nil
		}
	default:
		return 0, 0, errors.New("decode: unsupported scalar kind")
	}
	size, err := stringSize(ctx, s)
	return len(s), size, err
}
