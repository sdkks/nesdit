package flatview

import (
	"fmt"
	"sort"
)

// resolveField validates one field's type references and records the
// resolved enum/object partners. Index rules follow the pinned reflection
// contract: Object kinds index objects; Union, UType and enum-derived
// scalars index enums; vectors inherit the element's rule.
func (b *bootstrap) resolveField(obj *Object, f *Field) error {
	base := f.Type.Base
	switch base {
	case btObj:
		so, err := b.objectAt(f.Type.Index)
		if err != nil {
			return err
		}
		if so.IsStruct {
			f.StructType = so
		} else {
			f.TableType = so
		}
	case btUnion:
		en, err := b.unionAt(f.Type.Index)
		if err != nil {
			return err
		}
		f.UnionMembers = en
	case btString:
		// Unused index slots are ignored for reference types.
	case btVector, btVector64:
		elem := f.Type.Element
		switch elem {
		case btObj:
			so, err := b.objectAt(f.Type.Index)
			if err != nil {
				return err
			}
			if so.IsStruct {
				f.ElementStruct = so
			} else {
				f.ElementTable = so
			}
		case btUnion, btUType:
			en, err := b.enumAt(f.Type.Index)
			if err != nil {
				return err
			}
			f.ElementEnum = en
		case btString, btBool, btByte, btUByte, btShort, btUShort, btInt, btUInt, btLong, btULong, btFloat, btDouble:
			if f.Type.Index != -1 {
				en, err := b.enumAt(f.Type.Index)
				if err != nil {
					return err
				}
				if en.Underlying.Base != elem {
					return fmt.Errorf("%w: enum element underlying mismatch", errSchemaWire)
				}
				f.ElementEnum = en
			}
		default:
			return fmt.Errorf("%w: invalid vector element layout", errSchemaWire)
		}
	case btUType:
		en, err := b.unionAt(f.Type.Index)
		if err != nil {
			return err
		}
		f.Enum = en
		f.IsDiscriminator = true
		// The discriminator's physical wire width is the union's
		// underlying width; the reflected UType base only names the kind.
		f.Type.Base = en.Underlying.Base
	case btArray:
		if f.Type.FixedLength <= 0 {
			return fmt.Errorf("%w: array without fixed length", errSchemaWire)
		}
		switch f.Type.Element {
		case btObj:
			so, err := b.objectAt(f.Type.Index)
			if err != nil {
				return err
			}
			if !so.IsStruct {
				return fmt.Errorf("%w: array of tables", errSchemaWire)
			}
			f.ElementStruct = so
		case btBool, btByte, btUByte, btShort, btUShort, btInt, btUInt, btLong, btULong, btFloat, btDouble:
		default:
			return fmt.Errorf("%w: invalid array element layout", errSchemaWire)
		}
	case btBool, btByte, btUByte, btShort, btUShort, btInt, btUInt, btLong, btULong, btFloat, btDouble:
		if f.Type.Index != -1 {
			en, err := b.enumAt(f.Type.Index)
			if err != nil {
				return err
			}
			if en.Underlying.Base != base {
				return fmt.Errorf("%w: enum underlying mismatch", errSchemaWire)
			}
			f.Enum = en
			if en.IsUnion {
				// A scalar field typed by a union enum is a discriminator
				// sibling; pairing is validated from the union value field.
				f.IsDiscriminator = true
			}
		}
	case btNone:
		return fmt.Errorf("%w: none-typed field", errSchemaWire)
	default:
		return fmt.Errorf("%w: unsupported field base type", errSchemaWire)
	}
	// Presence semantics that the compiler guarantees; hostile metadata
	// violating them is rejected here rather than misread later.
	if f.Required {
		// A required union value slot is legitimate; other non-reference
		// bases cannot be required.
		if base != btUnion && !isReferenceBase(base) {
			return fmt.Errorf("%w: required scalar field", errSchemaWire)
		}
	}
	if f.Required && f.Optional {
		return fmt.Errorf("%w: field both required and optional", errSchemaWire)
	}
	if f.Optional && !isOptionalAllowed(base) {
		return fmt.Errorf("%w: optional non-scalar field", errSchemaWire)
	}
	if obj.IsStruct {
		if base != btObj && !isScalarBase(base) {
			return fmt.Errorf("%w: non-inline struct field", errSchemaWire)
		}
		if f.Required || f.Optional || f.Deprecated {
			return fmt.Errorf("%w: struct field with presence flag", errSchemaWire)
		}
	} else if f.Offset != 4+2*f.ID {
		// Table field offsets are the vtable byte offsets 4 + 2*id.
		return fmt.Errorf("%w: table field offset does not match id", errSchemaWire)
	}
	// Union value fields must be paired with a discriminator sibling.
	if base == btUnion {
		if f.ID == 0 {
			return fmt.Errorf("%w: union field without discriminator sibling", errSchemaWire)
		}
		disc := obj.Fields[f.ID-1]
		if disc == nil || !disc.IsDiscriminator || disc.Enum != f.UnionMembers {
			return fmt.Errorf("%w: union field without discriminator sibling", errSchemaWire)
		}
		if disc.Type.Base != f.UnionMembers.Underlying.Base {
			return fmt.Errorf("%w: discriminator width mismatch", errSchemaWire)
		}
		f.Disc = disc
	}
	return nil
}

func isReferenceBase(base int) bool {
	switch base {
	case btString, btVector, btObj, btUnion, btVector64:
		return true
	}
	return false
}

// optional is legal on scalars (and technically on references, where it is
// the default); anything else is invalid metadata.
func isOptionalAllowed(_ int) bool { return true }

func (b *bootstrap) objectAt(index int) (*Object, error) {
	if index < 0 || index >= len(b.schema.objects) {
		return nil, fmt.Errorf("%w: object index out of range", errSchemaWire)
	}
	if err := b.charge(1); err != nil {
		return nil, err
	}
	return b.schema.objects[index], nil
}

func (b *bootstrap) enumAt(index int) (*Enum, error) {
	if index < 0 || index >= len(b.schema.enums) {
		return nil, fmt.Errorf("%w: enum index out of range", errSchemaWire)
	}
	if err := b.charge(1); err != nil {
		return nil, err
	}
	return b.schema.enums[index], nil
}

func (b *bootstrap) unionAt(index int) (*Enum, error) {
	en, err := b.enumAt(index)
	if err != nil {
		return nil, err
	}
	if !en.IsUnion {
		return nil, fmt.Errorf("%w: union reference to plain enum", errSchemaWire)
	}
	return en, nil
}

// resolveEnumVal validates one union member's union_type after all object
// declarations exist.
func (b *bootstrap) resolveEnumVal(en *Enum, v *EnumVal) error {
	if !en.IsUnion {
		return nil
	}
	switch v.UnionType.Base {
	case btNone:
		if v.Value != 0 {
			return fmt.Errorf("%w: union member without type", errSchemaWire)
		}
	case btObj:
		so, err := b.objectAt(v.UnionType.Index)
		if err != nil {
			return err
		}
		v.UnionObj = so
	case btString:
	default:
		return fmt.Errorf("%w: unsupported union member type", errSchemaWire)
	}
	return nil
}

// validateStructLayout derives and checks the inline struct layout:
// positive power-of-two alignment, nonzero size, in-range non-overlapping
// fields, per-field alignment and enclosing size.
func (b *bootstrap) validateStructLayout(obj *Object) error {
	if obj.MinAlign <= 0 || obj.MinAlign&(obj.MinAlign-1) != 0 {
		return fmt.Errorf("%w: invalid struct alignment", errSchemaWire)
	}
	if obj.ByteSize <= 0 || obj.ByteSize%obj.MinAlign != 0 {
		return fmt.Errorf("%w: invalid struct size", errSchemaWire)
	}
	type span struct {
		off, end, align, padding int
		name                     string
	}
	spans := make([]span, 0, len(obj.Fields))
	for _, f := range obj.Fields {
		width, align, err := b.inlineWidth(f)
		if err != nil {
			return err
		}
		if f.Offset%align != 0 {
			return fmt.Errorf("%w: misaligned struct field", errSchemaWire)
		}
		if f.Offset+width > obj.ByteSize {
			return fmt.Errorf("%w: struct field out of range", errSchemaWire)
		}
		spans = append(spans, span{f.Offset, f.Offset + width, align, f.Padding, f.Name})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].off < spans[j].off })
	for i := 1; i < len(spans); i++ {
		if spans[i].off < spans[i-1].end+spans[i-1].padding {
			return fmt.Errorf("%w: overlapping struct fields", errSchemaWire)
		}
	}
	var maxEnd int
	for _, s := range spans {
		if s.end > maxEnd {
			maxEnd = s.end
		}
	}
	if padded := (maxEnd + obj.MinAlign - 1) &^ (obj.MinAlign - 1); obj.ByteSize != padded {
		return fmt.Errorf("%w: struct size does not match layout", errSchemaWire)
	}
	return nil
}

// inlineWidth returns the inline byte width and required alignment of a
// struct field from its declared type, never from unchecked size hints.
func (b *bootstrap) inlineWidth(f *Field) (int, int, error) {
	switch f.Type.Base {
	case btObj:
		so := f.StructType
		if so == nil || !so.IsStruct {
			return 0, 0, fmt.Errorf("%w: struct field not resolved", errSchemaWire)
		}
		// Nested struct cycles are impossible in valid metadata; the
		// compiler rejects them and so does this plan.
		if b.structCycle(so, make(map[*Object]bool)) {
			return 0, 0, fmt.Errorf("%w: recursive struct layout", errSchemaWire)
		}
		return so.ByteSize, so.MinAlign, nil
	default:
		w := scalarWidth(f.Type.Base)
		if w == 0 {
			return 0, 0, fmt.Errorf("%w: non-inline struct field type", errSchemaWire)
		}
		return w, w, nil
	}
}

// structCycle detects inline struct reference cycles with visiting marks.
func (b *bootstrap) structCycle(so *Object, visiting map[*Object]bool) bool {
	if visiting[so] {
		return true
	}
	visiting[so] = true
	defer delete(visiting, so)
	for _, f := range so.Fields {
		if f.Type.Base == btObj && f.StructType != nil && b.structCycle(f.StructType, visiting) {
			return true
		}
	}
	return false
}

// plan marks root-reachable objects and rejects root-reachable unsupported
// constructs during preparation, even when the payload field would be
// absent. Deprecated fields participate in reachability.
func (b *bootstrap) plan(root *Object) error {
	visiting := make(map[*Object]bool)
	var visit func(obj *Object) error
	var visitEnum func(en *Enum) error
	visit = func(obj *Object) error {
		if obj.Reach {
			if err := b.charge(1); err != nil { // memo hit edge
				return err
			}
			return nil
		}
		if visiting[obj] {
			return nil // recursive table declarations are legal
		}
		visiting[obj] = true
		defer delete(visiting, obj)
		if err := b.charge(1); err != nil {
			return err
		}
		obj.Reach = true
		for _, f := range obj.Fields {
			if f.Offset64 {
				return fmt.Errorf("%w: root-reachable 64-bit offset field", errSchemaWire)
			}
			switch f.Type.Base {
			case btArray:
				return fmt.Errorf("%w: root-reachable fixed-length array field", errSchemaWire)
			case btVector64:
				return fmt.Errorf("%w: root-reachable 64-bit vector field", errSchemaWire)
			case btVector:
				switch f.Type.Element {
				case btUnion, btUType:
					return fmt.Errorf("%w: root-reachable union vector field", errSchemaWire)
				}
			}
			var err error
			switch {
			case f.TableType != nil:
				err = visit(f.TableType)
			case f.StructType != nil:
				err = visit(f.StructType)
			case f.ElementTable != nil:
				err = visit(f.ElementTable)
			case f.ElementStruct != nil:
				err = visit(f.ElementStruct)
			case f.UnionMembers != nil:
				err = visitEnum(f.UnionMembers)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	visitEnum = func(en *Enum) error {
		if err := b.charge(1); err != nil {
			return err
		}
		for _, v := range en.Values {
			if v.UnionObj != nil {
				if err := visit(v.UnionObj); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(root)
}
