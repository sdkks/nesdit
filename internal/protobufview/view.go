package protobufview

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func (p *preflight) key(s string) error {
	if err := p.view.addText(len(s)); err != nil {
		return err
	}
	n, err := stringSize(p.ctx, s)
	if err != nil {
		return err
	}
	return p.size(n + 1) // colon; compact ProtoJSON adds no colon space
}
func (p *preflight) string(s string, depth int) error {
	if err := p.view.node(p.ctx, depth); err != nil {
		return err
	}
	if err := p.view.addText(len(s)); err != nil {
		return err
	}
	n, err := stringSize(p.ctx, s)
	if err != nil {
		return err
	}
	return p.size(n)
}
func (p *preflight) container(depth, count int) error {
	if err := p.view.node(p.ctx, depth); err != nil {
		return err
	}
	// Two delimiters and up to one optional space after each comma.
	return p.size(2 + 2*max(0, count-1))
}
func (p *preflight) viewMessage(m protoreflect.Message, depth int, typeURL string) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	name := m.Descriptor().FullName()
	fields := m.Descriptor().Fields()
	switch name {
	case "google.protobuf.Any":
		target := p.expanded[m]
		if target == nil {
			return p.container(depth, 0)
		}
		url := m.Get(fields.ByNumber(1)).String()
		if !special(target.Descriptor().FullName()) {
			return p.viewMessage(target, depth, url)
		}
		if err := p.container(depth, 2); err != nil {
			return err
		}
		if err := p.key("@type"); err != nil {
			return err
		}
		if err := p.string(url, depth+1); err != nil {
			return err
		}
		if err := p.key("value"); err != nil {
			return err
		}
		return p.viewMessage(target, depth+1, "")
	case "google.protobuf.Struct":
		return p.viewMap(fields.ByNumber(1), m.Get(fields.ByNumber(1)).Map(), depth)
	case "google.protobuf.ListValue":
		return p.viewList(fields.ByNumber(1), m.Get(fields.ByNumber(1)).List(), depth)
	case "google.protobuf.Value":
		f := m.WhichOneof(m.Descriptor().Oneofs().ByName("kind"))
		if f == nil {
			return errors.New("decode: Value requires a selected kind")
		}
		if f.Number() == 2 && (math.IsNaN(m.Get(f).Float()) || math.IsInf(m.Get(f).Float(), 0)) {
			return errors.New("decode: Value requires a finite number")
		}
		return p.viewScalar(f, m.Get(f), depth)
	case "google.protobuf.Timestamp", "google.protobuf.Duration":
		s, err := timeString(m)
		if err != nil {
			return err
		}
		return p.string(s, depth)
	case "google.protobuf.FieldMask":
		return p.fieldMask(m, depth)
	}
	if special(name) { // the remaining special mappings are scalar wrappers
		f := fields.ByNumber(1)
		return p.viewScalar(f, m.Get(f), depth)
	}
	count := 0
	m.Range(func(protoreflect.FieldDescriptor, protoreflect.Value) bool { count++; return true })
	if typeURL != "" {
		count++
	}
	if err := p.container(depth, count); err != nil {
		return err
	}
	if typeURL != "" {
		if err := p.key("@type"); err != nil {
			return err
		}
		if err := p.string(typeURL, depth+1); err != nil {
			return err
		}
	}
	var result error
	m.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if result = p.key(f.JSONName()); result != nil {
			return false
		}
		switch {
		case f.IsList():
			result = p.viewList(f, v.List(), depth+1)
		case f.IsMap():
			result = p.viewMap(f, v.Map(), depth+1)
		default:
			result = p.viewScalar(f, v, depth+1)
		}
		return result == nil
	})
	return result
}
func (p *preflight) viewScalar(f protoreflect.FieldDescriptor, v protoreflect.Value, depth int) error {
	if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind {
		return p.viewMessage(v.Message(), depth, "")
	}
	if err := p.view.node(p.ctx, depth); err != nil {
		return err
	}
	text, size, err := scalarSize(p.ctx, f, v)
	if err != nil {
		return err
	}
	if err := p.view.addText(text); err != nil {
		return err
	}
	return p.size(size)
}
func (p *preflight) viewList(f protoreflect.FieldDescriptor, list protoreflect.List, depth int) error {
	if err := p.container(depth, list.Len()); err != nil {
		return err
	}
	for i := 0; i < list.Len(); i++ {
		if err := p.viewScalar(f, list.Get(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}
func (p *preflight) viewMap(f protoreflect.FieldDescriptor, m protoreflect.Map, depth int) error {
	if err := p.container(depth, m.Len()); err != nil {
		return err
	}
	var result error
	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		if result = p.key(k.String()); result != nil {
			return false
		}
		result = p.viewScalar(f.MapValue(), v, depth+1)
		return result == nil
	})
	return result
}
func (p *preflight) fieldMask(m protoreflect.Message, depth int) error {
	if err := p.view.node(p.ctx, depth); err != nil {
		return err
	}
	list := m.Get(m.Descriptor().Fields().ByNumber(1)).List()
	// No joined string is allocated. Paths are identifiers, so their mapped
	// spellings need no JSON escapes; commas are characters inside the string.
	n := max(0, list.Len()-1)
	for i := 0; i < list.Len(); i++ {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		s := list.Get(i).String()
		if !protoreflect.FullName(s).IsValid() {
			return errors.New("decode: invalid FieldMask path")
		}
		cc := camel(s)
		if snake(cc) != s {
			return errors.New("decode: irreversible FieldMask path")
		}
		if len(cc) > textLimit-n {
			return errors.New("view: exceeds 16 MiB key/string bytes")
		}
		n += len(cc)
	}
	if err := p.view.addText(n); err != nil {
		return err
	}
	return p.size(n + 2)
}
func timeString(m protoreflect.Message) (string, error) {
	fields := m.Descriptor().Fields()
	secs, nanos := m.Get(fields.ByNumber(1)).Int(), m.Get(fields.ByNumber(2)).Int()
	var s, suffix string
	if m.Descriptor().FullName() == "google.protobuf.Timestamp" {
		if secs < -62135596800 || secs > 253402300799 || nanos < 0 || nanos > 999999999 {
			return "", errors.New("decode: Timestamp out of range")
		}
		s = time.Unix(secs, nanos).UTC().Format("2006-01-02T15:04:05.000000000")
		suffix = "Z"
	} else {
		if secs < -315576000000 || secs > 315576000000 || nanos < -999999999 || nanos > 999999999 || (secs > 0 && nanos < 0) || (secs < 0 && nanos > 0) {
			return "", errors.New("decode: Duration out of range or inconsistent signs")
		}
		sign := ""
		if secs < 0 || nanos < 0 {
			sign = "-"
			secs = -secs
			nanos = -nanos
		}
		s = fmt.Sprintf("%s%d.%09d", sign, secs, nanos)
		suffix = "s"
	}
	s = strings.TrimSuffix(s, "000")
	s = strings.TrimSuffix(s, "000")
	s = strings.TrimSuffix(s, ".000")
	return s + suffix, nil
}
