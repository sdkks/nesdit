package flatview

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"

	"encoding/json"

	"github.com/sdkks/nesdit/internal/omap"
)

// Decode interprets one complete ordinary payload against the prepared
// schema plan. The whole body is validated before any value is returned;
// unknown payload fields are omitted without being certified.
func (s *Schema) Decode(ctx context.Context, body []byte) (omap.Value, error) {
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	if len(body) > rawLimit {
		return omap.Value{}, fmt.Errorf("FlatBuffers payload exceeds 10 MiB")
	}
	if len(body) < 4 {
		return omap.Value{}, fmt.Errorf("%w: missing root offset", errPayload)
	}
	if s.fileIdent != "" {
		if len(body) < 8 || string(body[4:8]) != s.fileIdent {
			return omap.Value{}, fmt.Errorf("%w: file identifier mismatch", errPayload)
		}
	}
	root, err := (checked{buf: body}).u32(0)
	if err != nil {
		return omap.Value{}, err
	}
	if int64(root) < 4 || int64(root)+4 > int64(len(body)) {
		return omap.Value{}, fmt.Errorf("%w: invalid root offset", errPayload)
	}
	w := &walker{s: s, c: checked{buf: body}, ctx: ctx}
	v, err := w.table(s.root, int(root), 0, 0)
	if err != nil {
		if errors.Is(err, errSchemaWire) {
			err = fmt.Errorf("%w: %s", errPayload, err.Error())
		}
		return omap.Value{}, err
	}
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	return v, nil
}

type walker struct {
	s   *Schema
	c   checked
	ctx context.Context

	nodes, text   int // decoded semantic budget
	vnodes, vtext int // actual JSON view budget
}

var (
	errDecoded = errors.New("FlatBuffers decoded value limit exceeded")
	errView    = errors.New("FlatBuffers JSON view limit exceeded")
)

func (w *walker) decCharge(depth, nodes int) error {
	if depth > depthLimit || nodes > nodeLimit-w.nodes {
		return errDecoded
	}
	w.nodes += nodes
	return nil
}

func (w *walker) decText(n int) error {
	if n > textLimit-w.text {
		return errDecoded
	}
	w.text += n
	return nil
}

func (w *walker) viewCharge(depth, nodes, text int) error {
	if depth > depthLimit || nodes > nodeLimit-w.vnodes || text > textLimit-w.vtext {
		return errView
	}
	w.vnodes += nodes
	w.vtext += text
	return nil
}

// payloadErr rewraps shared wire errors as payload failures with position.
func (w *walker) fail(err error) error {
	if errors.Is(err, errSchemaWire) {
		return fmt.Errorf("%w: %w", errPayload, err)
	}
	return err
}

// alignment validates buffer-origin alignment for an inline value.
func (w *walker) alignment(pos, width int) error {
	if width > 0 && pos%width != 0 {
		return fmt.Errorf("%w: misaligned value at byte %d", errPayload, pos)
	}
	return nil
}

// table materializes one table's known fields in field-ID order, including
// deprecated declarations. Unknown payload slots get basic structural range
// checks only.
func (w *walker) table(obj *Object, pos, depth, vdepth int) (omap.Value, error) {
	if err := w.ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	if err := w.decCharge(depth, 1); err != nil {
		return omap.Value{}, err
	}
	if err := w.viewCharge(vdepth, 1, 0); err != nil {
		return omap.Value{}, err
	}
	h, err := w.c.readTable(pos)
	if err != nil {
		return omap.Value{}, w.fail(err)
	}
	// Basic structural range checks on unknown payload slots.
	for id := len(obj.Fields); id < len(h.slots); id++ {
		if off := int(h.slots[id]); off != 0 {
			if off < 4 || off >= h.objSize || pos+off >= len(w.c.buf) {
				return omap.Value{}, fmt.Errorf("%w: invalid unknown field slot %d at byte %d", errPayload, id, pos)
			}
		}
	}
	doc := omap.New()
	for _, f := range obj.Fields {
		if err := w.ctx.Err(); err != nil {
			return omap.Value{}, err
		}
		off, present := h.slot(f.ID)
		var val omap.Value
		var emitted bool
		switch f.Type.Base {
		case btBool, btByte, btUByte, btShort, btUShort, btInt, btUInt, btLong, btULong, btFloat, btDouble, btUType:
			if present {
				val, err = w.scalarAt(f, pos+off, depth+1, vdepth+1, len(f.Name))
				emitted = err == nil
			} else if f.Optional {
				// Absent optional scalars are omitted entirely; the
				// conceptual node is still charged at child depth.
				err = w.decCharge(depth+1, 1)
			} else {
				val, err = w.absentScalar(f, depth+1, vdepth+1, len(f.Name))
				emitted = err == nil
			}
		case btString:
			if present {
				val, err = w.stringField(f, pos+off, depth+1, vdepth+1)
				emitted = err == nil
			} else {
				err = w.absentReference(f, depth+1, vdepth+1)
			}
		case btVector:
			if present {
				val, err = w.vectorField(f, pos+off, depth+1, vdepth+1)
				emitted = err == nil
			} else {
				err = w.absentReference(f, depth+1, vdepth+1)
			}
		case btObj:
			if present {
				val, err = w.objField(f, pos, pos+off, depth+1, vdepth+1)
				emitted = err == nil
			} else {
				err = w.absentReference(f, depth+1, vdepth+1)
			}
		case btUnion:
			val, err = w.unionField(f, h, pos, depth+1, vdepth+1)
			emitted = err == nil && val.Kind != omap.KindNull
		default:
			err = fmt.Errorf("%w: unsupported field base type", errPayload)
		}
		if err != nil {
			return omap.Value{}, err
		}
		if emitted {
			doc.Set(f.Name, val)
		}
	}
	return omap.MapValue(doc), nil
}

// absentScalar emits the BFBS-authoritative default for an ordinary absent
// scalar, or omits an absent optional scalar. Explicit equal defaults are
// indistinguishable from omission in this view.
func (w *walker) absentScalar(f *Field, depth, vdepth, nameLen int) (omap.Value, error) {
	return w.scalarValue(f, f.Type.Base, f.DefaultInteger, f.DefaultReal, depth, vdepth, nameLen)
}

func (w *walker) absentReference(f *Field, depth, _ int) error {
	if f.Required {
		return fmt.Errorf("%w: missing required field id %d", errPayload, f.ID)
	}
	return w.decCharge(depth, 1)
}

func (w *walker) scalarAt(f *Field, pos, depth, vdepth, nameLen int) (omap.Value, error) {
	base := f.Type.Base
	width := scalarWidth(base)
	if err := w.alignment(pos, width); err != nil {
		return omap.Value{}, err
	}
	var err error
	var i64v int64
	var f64v float64
	isFloat := false
	switch base {
	case btBool, btUType, btByte, btUByte:
		var v uint8
		if v, err = w.c.u8(pos); err == nil {
			i64v = int64(int8(v))
			if base != btByte {
				i64v = int64(v)
			}
		}
	case btShort, btUShort:
		var v uint16
		if v, err = w.c.u16(pos); err == nil {
			i64v = int64(int16(v))
			if base == btUShort {
				i64v = int64(v)
			}
		}
	case btInt, btUInt:
		var v uint32
		if v, err = w.c.u32(pos); err == nil {
			i64v = int64(int32(v))
			if base == btUInt {
				i64v = int64(v)
			}
		}
	case btLong, btULong:
		var v uint64
		if v, err = w.c.u64(pos); err == nil {
			i64v = int64(v)
		}
	case btFloat:
		f64v, err = w.c.f32(pos)
		isFloat = true
	case btDouble:
		f64v, err = w.c.f64(pos)
		isFloat = true
	}
	if err != nil {
		return omap.Value{}, w.fail(err)
	}
	if isFloat {
		return w.floatValue(f.Type.Base, f64v, depth, vdepth, nameLen)
	}
	if f.Enum != nil {
		return w.enumValue(f.Enum, f.Type.Base, i64v, depth, vdepth, nameLen)
	}
	return w.intValue(f.Type.Base, i64v, depth, vdepth, nameLen)
}

// scalarValue renders a default from the reflected bit pattern, interpreted
// at the declared width and signedness. Float32 defaults are narrowed
// through the declared width before promotion, so genuinely stored negative
// zero survives while omitted defaults read as positive zero.
func (w *walker) scalarValue(f *Field, base int, pattern int64, flt float64, depth, vdepth, nameLen int) (omap.Value, error) {
	if base == btFloat || base == btDouble {
		if base == btFloat {
			flt = float64(float32(flt))
		}
		return w.floatValue(base, flt, depth, vdepth, nameLen)
	}
	if f.Enum != nil {
		return w.enumValue(f.Enum, base, pattern, depth, vdepth, nameLen)
	}
	if base == btBool {
		if err := w.chargeScalarNode(depth, vdepth, 0); err != nil {
			return omap.Value{}, err
		}
		return omap.BoolValue(pattern != 0), nil
	}
	return w.intValue(base, pattern, depth, vdepth, nameLen)
}

func (w *walker) chargeScalarNode(depth, vdepth, textLen int) error {
	if err := w.decCharge(depth, 1); err != nil {
		return err
	}
	return w.viewCharge(vdepth, 1, textLen)
}

// intValue renders 8/16/32-bit integers as JSON numbers and all 64-bit
// integers as exact decimal strings, without float intermediaries.
func (w *walker) intValue(base int, pattern int64, depth, vdepth, nameLen int) (omap.Value, error) {
	switch base {
	case btBool:
		if err := w.chargeScalarNode(depth, vdepth, 0); err != nil {
			return omap.Value{}, err
		}
		return omap.BoolValue(pattern != 0), nil
	case btByte, btUByte, btShort, btUShort, btInt, btUInt:
		if err := w.chargeScalarNode(depth, vdepth, 0); err != nil {
			return omap.Value{}, err
		}
		var v int64
		switch base {
		case btByte:
			v = int64(int8(pattern))
		case btUByte, btUType:
			v = int64(uint8(pattern))
		case btShort:
			v = int64(int16(pattern))
		case btUShort:
			v = int64(uint16(pattern))
		case btInt:
			v = int64(int32(pattern))
		case btUInt:
			v = int64(uint32(pattern))
		}
		return omap.NumValue(json.Number(strconv.FormatInt(v, 10))), nil
	case btLong:
		text := strconv.FormatInt(pattern, 10)
		if err := w.chargeScalarNode(depth, vdepth, nameLen+len(text)); err != nil {
			return omap.Value{}, err
		}
		return omap.StrValue(text), nil
	case btULong:
		text := strconv.FormatUint(uint64(pattern), 10)
		if err := w.chargeScalarNode(depth, vdepth, nameLen+len(text)); err != nil {
			return omap.Value{}, err
		}
		return omap.StrValue(text), nil
	}
	return omap.Value{}, fmt.Errorf("%w: unsupported integer width", errPayload)
}

// enumValue renders the underlying numeric enum value, including unknown
// values, preserving unsigned64 high-bit semantics. Only discriminator
// fields take this path; the value is rendered with the enum's underlying
// width and signedness.
func (w *walker) enumValue(_ *Enum, base int, pattern int64, depth, vdepth, nameLen int) (omap.Value, error) {
	return w.intValue(base, pattern, depth, vdepth, nameLen)
}

// floatValue renders finite floats as shortest round-trip JSON numbers,
// negative zero as the string "-0" and specials by name. Float32 values are
// promoted exactly to float64 before formatting.
func (w *walker) floatValue(_ int, v float64, depth, vdepth, nameLen int) (omap.Value, error) {
	switch {
	case math.IsNaN(v):
		if err := w.chargeScalarNode(depth, vdepth, nameLen+3); err != nil {
			return omap.Value{}, err
		}
		return omap.StrValue("NaN"), nil
	case math.IsInf(v, 1):
		if err := w.chargeScalarNode(depth, vdepth, nameLen+8); err != nil {
			return omap.Value{}, err
		}
		return omap.StrValue("Infinity"), nil
	case math.IsInf(v, -1):
		if err := w.chargeScalarNode(depth, vdepth, nameLen+9); err != nil {
			return omap.Value{}, err
		}
		return omap.StrValue("-Infinity"), nil
	case v == 0 && math.Signbit(v):
		if err := w.chargeScalarNode(depth, vdepth, nameLen+2); err != nil {
			return omap.Value{}, err
		}
		return omap.StrValue("-0"), nil
	default:
		text := strconv.FormatFloat(v, 'g', -1, 64)
		if err := w.chargeScalarNode(depth, vdepth, 0); err != nil {
			return omap.Value{}, err
		}
		return omap.NumValue(json.Number(text)), nil
	}
}

func (w *walker) stringField(f *Field, slotPos, depth, vdepth int) (omap.Value, error) {
	target, err := w.c.relativeOffset(slotPos)
	if err != nil {
		return omap.Value{}, w.fail(err)
	}
	s, err := w.c.readString(target)
	if err != nil {
		return omap.Value{}, w.fail(err)
	}
	if w.alignment(target, 4) != nil {
		return omap.Value{}, fmt.Errorf("%w: misaligned string at byte %d", errPayload, target)
	}
	if err := w.decCharge(depth, 1); err != nil {
		return omap.Value{}, err
	}
	if err := w.decText(len(s)); err != nil {
		return omap.Value{}, err
	}
	if err := w.viewCharge(vdepth, 1, len(f.Name)+len(s)); err != nil {
		return omap.Value{}, err
	}
	return omap.StrValue(s), nil
}

func (w *walker) objField(f *Field, _, slotPos, depth, vdepth int) (omap.Value, error) {
	if f.StructType != nil {
		// Struct fields in tables are stored inline at the slot offset.
		return w.structValue(f.StructType, slotPos, depth, vdepth, len(f.Name))
	}
	target, err := w.c.relativeOffset(slotPos)
	if err != nil {
		return omap.Value{}, w.fail(err)
	}
	return w.table(f.TableType, target, depth, vdepth)
}

// unionField decodes the union value under its schema-named discriminator
// sibling. The discriminator itself is emitted as an ordinary enum field by
// the table walk; this method handles only the value slot.
func (w *walker) unionField(f *Field, h tableHeader, pos, depth, vdepth int) (omap.Value, error) {
	tag := w.currentTag(f, h, pos)
	off, present := h.slot(f.ID)
	if tag == 0 {
		if present {
			return omap.Value{}, fmt.Errorf("%w: union value present with NONE tag at field id %d", errPayload, f.ID)
		}
		if f.Required {
			return omap.Value{}, fmt.Errorf("%w: required union absent with NONE tag at field id %d", errPayload, f.ID)
		}
		if err := w.decCharge(depth, 1); err != nil {
			return omap.Value{}, err
		}
		return omap.Value{}, nil
	}
	if !present {
		return omap.Value{}, fmt.Errorf("%w: union value missing for tag at field id %d", errPayload, f.ID)
	}
	member, ok := f.UnionMembers.ByValue[tag]
	if !ok || member.UnionType.Base == btNone {
		return omap.Value{}, fmt.Errorf("%w: unknown union tag %d at field id %d", errPayload, widenTag(f.UnionMembers, tag), f.ID)
	}
	target, err := w.c.relativeOffset(pos + off)
	if err != nil {
		return omap.Value{}, w.fail(err)
	}
	switch member.UnionType.Base {
	case btObj:
		if member.UnionObj.IsStruct {
			// Struct union members are offset-referenced.
			return w.structValue(member.UnionObj, target, depth, vdepth, len(f.Name))
		}
		return w.table(member.UnionObj, target, depth, vdepth)
	case btString:
		s, err := w.c.readString(target)
		if err != nil {
			return omap.Value{}, w.fail(err)
		}
		if w.alignment(target, 4) != nil {
			return omap.Value{}, fmt.Errorf("%w: misaligned string at byte %d", errPayload, target)
		}
		if err := w.decCharge(depth, 1); err != nil {
			return omap.Value{}, err
		}
		if err := w.decText(len(s)); err != nil {
			return omap.Value{}, err
		}
		if err := w.viewCharge(vdepth, 1, len(f.Name)+len(s)); err != nil {
			return omap.Value{}, err
		}
		return omap.StrValue(s), nil
	}
	return omap.Value{}, fmt.Errorf("%w: unsupported union member", errPayload)
}

// currentTag reads the discriminator sibling's value at its declared width,
// looking the numeric value up never by vector index or byte truncation.
func (w *walker) currentTag(f *Field, h tableHeader, pos int) int64 {
	disc := f.Disc
	off, present := h.slot(disc.ID)
	if !present {
		return 0 // discriminator defaults to NONE
	}
	p := pos + off
	var raw uint64
	switch disc.Type.Base {
	case btUType, btBool, btByte, btUByte:
		v, err := w.c.u8(p)
		if err != nil {
			return -1
		}
		raw = uint64(v)
		if disc.Type.Base == btByte {
			return int64(int8(v))
		}
	case btShort, btUShort:
		v, err := w.c.u16(p)
		if err != nil {
			return -1
		}
		raw = uint64(v)
		if disc.Type.Base == btShort {
			return int64(int16(v))
		}
	case btInt, btUInt:
		v, err := w.c.u32(p)
		if err != nil {
			return -1
		}
		raw = uint64(v)
		if disc.Type.Base == btInt {
			return int64(int32(v))
		}
	case btLong, btULong:
		v, err := w.c.u64(p)
		if err != nil {
			return -1
		}
		raw = v
		if disc.Type.Base == btLong {
			return int64(v)
		}
	default:
		return -1
	}
	// UType discriminators are unsigned bytes.
	if disc.Type.Base == btUType {
		return int64(uint8(raw))
	}
	return int64(raw)
}

// widenTag reinterprets a numeric tag in the enum's underlying signedness
// for diagnostics.
func widenTag(en *Enum, tag int64) int64 {
	if en.IsUnsigned || en.Underlying.Base == btUType {
		switch en.UnderlyWidth {
		case 1:
			return int64(uint8(tag))
		case 2:
			return int64(uint16(tag))
		case 4:
			return int64(uint32(tag))
		}
		return int64(uint64(tag))
	}
	return tag
}

// structValue materializes an inline struct: every declared field is
// present at its validated offset.
func (w *walker) structValue(so *Object, pos, depth, vdepth, nameLen int) (omap.Value, error) {
	if err := w.ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	if err := w.decCharge(depth, 1); err != nil {
		return omap.Value{}, err
	}
	if err := w.viewCharge(vdepth, 1, nameLen); err != nil {
		return omap.Value{}, err
	}
	if err := w.alignment(pos, so.MinAlign); err != nil {
		return omap.Value{}, err
	}
	if pos+so.ByteSize > len(w.c.buf) {
		return omap.Value{}, fmt.Errorf("%w: struct out of range at byte %d", errPayload, pos)
	}
	doc := omap.New()
	for _, f := range so.Fields {
		if err := w.ctx.Err(); err != nil {
			return omap.Value{}, err
		}
		var val omap.Value
		var err error
		switch f.Type.Base {
		case btObj:
			val, err = w.structValue(f.StructType, pos+f.Offset, depth+1, vdepth+1, len(f.Name))
		default:
			val, err = w.scalarAt(f, pos+f.Offset, depth+1, vdepth+1, len(f.Name))
		}
		if err != nil {
			return omap.Value{}, err
		}
		doc.Set(f.Name, val)
	}
	return omap.MapValue(doc), nil
}

func (w *walker) vectorField(f *Field, slotPos, depth, vdepth int) (omap.Value, error) {
	vecPos, err := w.c.relativeOffset(slotPos)
	if err != nil {
		return omap.Value{}, w.fail(err)
	}
	count, start, err := w.c.vectorHeader(vecPos)
	if err != nil {
		return omap.Value{}, w.fail(err)
	}
	elem := f.Type.Element
	width := scalarWidth(elem)
	if elem == btObj && f.ElementStruct != nil {
		width = f.ElementStruct.ByteSize
	}
	isOffsets := elem == btString || elem == btObj
	if isOffsets {
		width = 4
	}
	if width <= 0 {
		return omap.Value{}, fmt.Errorf("%w: unsupported vector element", errPayload)
	}
	// Count against remaining bytes before any length-driven allocation.
	avail := len(w.c.buf) - start
	if count > avail/width {
		return omap.Value{}, fmt.Errorf("%w: vector length out of range at byte %d", errPayload, vecPos)
	}
	// Minimum one decoded node per element before materialization.
	if count > 0 {
		if err := w.decCharge(depth+1, count); err != nil {
			return omap.Value{}, err
		}
		if err := w.viewCharge(vdepth+1, count, 0); err != nil {
			return omap.Value{}, err
		}
	}
	if err := w.decCharge(depth, 1); err != nil {
		return omap.Value{}, err
	}
	if err := w.viewCharge(vdepth, 1, len(f.Name)); err != nil {
		return omap.Value{}, err
	}
	values := make([]omap.Value, count)
	for i := 0; i < count; i++ {
		if i%64 == 0 {
			if err := w.ctx.Err(); err != nil {
				return omap.Value{}, err
			}
		}
		elemPos := start + i*width
		var val omap.Value
		var err error
		switch {
		case elem == btString:
			var target int
			target, err = w.c.relativeOffset(elemPos)
			if err == nil {
				var s string
				s, err = w.c.readString(target)
				if err == nil {
					if w.alignment(target, 4) != nil {
						err = fmt.Errorf("%w: misaligned string at byte %d", errPayload, target)
					} else if derr := w.decText(len(s)); derr != nil {
						err = derr
					} else if verr := w.viewCharge(vdepth+1, 1, len(s)); verr != nil {
						err = verr
					} else {
						val = omap.StrValue(s)
					}
				}
			}
		case elem == btObj && f.ElementStruct != nil:
			val, err = w.structValue(f.ElementStruct, elemPos, depth+1, vdepth+1, 0)
		case elem == btObj:
			var target int
			target, err = w.c.relativeOffset(elemPos)
			if err == nil {
				val, err = w.table(f.ElementTable, target, depth+1, vdepth+1)
			}
		case elem == btUType:
			return omap.Value{}, fmt.Errorf("%w: unsupported vector element", errPayload)
		default:
			val, err = w.elementScalar(elem, f.ElementEnum, elemPos, vdepth+1)
		}
		if err != nil {
			return omap.Value{}, w.fail(err)
		}
		values[i] = val
	}
	return omap.SeqValue(values...), nil
}

// elementScalar renders one inline vector element (already node-charged).
func (w *walker) elementScalar(elem int, en *Enum, pos, vdepth int) (omap.Value, error) {
	if err := w.alignment(pos, scalarWidth(elem)); err != nil {
		return omap.Value{}, err
	}
	switch elem {
	case btBool:
		v, err := w.c.u8(pos)
		if err != nil {
			return omap.Value{}, err
		}
		return omap.BoolValue(v != 0), nil
	case btByte, btUByte:
		v, err := w.c.u8(pos)
		if err != nil {
			return omap.Value{}, err
		}
		if en != nil {
			return w.enumValue(en, elem, int64(int8(v)), 0, vdepth, 0)
		}
		if elem == btByte {
			return omap.NumValue(json.Number(strconv.FormatInt(int64(int8(v)), 10))), nil
		}
		return omap.NumValue(json.Number(strconv.FormatUint(uint64(v), 10))), nil
	case btShort, btUShort:
		v, err := w.c.u16(pos)
		if err != nil {
			return omap.Value{}, err
		}
		if en != nil {
			return w.enumValue(en, elem, int64(int16(v)), 0, vdepth, 0)
		}
		if elem == btShort {
			return omap.NumValue(json.Number(strconv.FormatInt(int64(int16(v)), 10))), nil
		}
		return omap.NumValue(json.Number(strconv.FormatUint(uint64(v), 10))), nil
	case btInt, btUInt:
		v, err := w.c.u32(pos)
		if err != nil {
			return omap.Value{}, err
		}
		if en != nil {
			return w.enumValue(en, elem, int64(int32(v)), 0, vdepth, 0)
		}
		if elem == btInt {
			return omap.NumValue(json.Number(strconv.FormatInt(int64(int32(v)), 10))), nil
		}
		return omap.NumValue(json.Number(strconv.FormatUint(uint64(v), 10))), nil
	case btLong, btULong:
		v, err := w.c.u64(pos)
		if err != nil {
			return omap.Value{}, err
		}
		if en != nil {
			return w.enumValue(en, elem, int64(v), 0, vdepth, 0)
		}
		text := strconv.FormatUint(v, 10)
		if elem == btLong {
			text = strconv.FormatInt(int64(v), 10)
		}
		// 64-bit elements render as decimal strings; charge their text
		// against the view budget like scalar fields do.
		if err := w.viewCharge(vdepth, 0, len(text)); err != nil {
			return omap.Value{}, err
		}
		return omap.StrValue(text), nil
	case btFloat:
		v, err := w.c.f32(pos)
		if err != nil {
			return omap.Value{}, err
		}
		return w.floatValue(elem, v, 0, vdepth, 0)
	case btDouble:
		v, err := w.c.f64(pos)
		if err != nil {
			return omap.Value{}, err
		}
		return w.floatValue(elem, v, 0, vdepth, 0)
	}
	return omap.Value{}, fmt.Errorf("%w: unsupported scalar element", errPayload)
}
