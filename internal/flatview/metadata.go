package flatview

import (
	"context"
	"fmt"
	"sort"
)

// Fixed slot counts of the pinned reflection metadata tables. Populated
// slots beyond these counts are rejected globally.
const (
	slotsSchema     = 8
	slotsObject     = 8
	slotsField      = 14
	slotsType       = 6
	slotsEnumVal    = 6
	slotsEnum       = 7
	slotsKeyValue   = 2
	slotsRPCCall    = 5
	slotsService    = 5
	slotsSchemaFile = 2
)

// TypeRef is a validated metadata type reference.
type TypeRef struct {
	Base        int
	Element     int
	Index       int
	FixedLength int
}

// Field is one validated metadata field declaration.
type Field struct {
	Name            string
	ID              int
	Offset          int
	Type            TypeRef
	DefaultInteger  int64
	DefaultReal     float64
	DefaultRealSet  bool
	Deprecated      bool
	Required        bool
	Key             bool
	Optional        bool
	Padding         int
	Offset64        bool
	Enum            *Enum // non-nil when the field is enum- or union-typed
	UnionMembers    *Enum // set on union value fields
	Disc            *Field
	StructType      *Object // set for struct-typed fields and struct elements
	TableType       *Object // set for table-typed fields and elements
	VectorElement   *TypeRef
	ElementEnum     *Enum
	ElementStruct   *Object
	ElementTable    *Object
	IsDiscriminator bool
}

// Object is one validated table or struct declaration with fields in
// field-ID order.
type Object struct {
	Name     string
	Fields   []*Field
	IsStruct bool
	MinAlign int
	ByteSize int
	Reach    bool
}

// EnumVal is one validated enum value. Value keeps the reflected signed-long
// bit pattern; unsigned underlying types reinterpret it.
type EnumVal struct {
	Name      string
	Value     int64
	UnionType TypeRef
	UnionObj  *Object
}

// Enum is one validated enum or union declaration.
type Enum struct {
	Name         string
	IsUnion      bool
	Underlying   TypeRef
	Values       []*EnumVal
	ByValue      map[int64]*EnumVal
	IsUnsigned   bool
	UnderlyWidth int
}

// Schema is the immutable, finite root-reachable plan compiled from BFBS.
type Schema struct {
	objects   []*Object
	enums     []*Enum
	root      *Object
	fileIdent string
	objIndex  map[int]*Object
}

type declKey struct {
	kind byte
	off  int
}

const (
	declObject declKind = iota
	declField
	declEnum
	declEnumVal
	declService
	declRPCCall
)

type declKind byte

type bootstrap struct {
	c       checked
	ctx     context.Context
	work    int
	strText int
	decls   map[declKey]bool
	objects map[int]*Object
	enums   map[int]*Enum
	schema  *Schema
}

// Prepare validates the complete fixed metadata bootstrap and compiles a
// finite root-reachable type plan. It rejects unknown global feature bits,
// populated unknown metadata slots, unknown base types, inconsistent
// references, invalid layouts and root-reachable unsupported constructs
// before any payload byte is acquired.
func Prepare(ctx context.Context, bfbs []byte) (*Schema, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(bfbs) > SchemaLimit {
		return nil, fmt.Errorf("BFBS schema exceeds 4 MiB")
	}
	if len(bfbs) < 8 || string(bfbs[4:8]) != "BFBS" {
		return nil, fmt.Errorf("%w: missing BFBS identifier", errSchemaWire)
	}
	b := &bootstrap{
		c:       checked{buf: bfbs},
		ctx:     ctx,
		decls:   make(map[declKey]bool),
		objects: make(map[int]*Object),
		enums:   make(map[int]*Enum),
		schema:  &Schema{objIndex: make(map[int]*Object)},
	}
	rootOff, err := b.c.u32(0)
	if err != nil {
		return nil, err
	}
	if int64(rootOff) < 8 || int64(rootOff)+4 > int64(len(bfbs)) {
		return nil, fmt.Errorf("%w: invalid schema root offset", errSchemaWire)
	}
	schema, err := b.c.readTable(int(rootOff))
	if err != nil {
		return nil, err
	}
	if err := b.checkUnknownSlots(schema, slotsSchema, "schema"); err != nil {
		return nil, err
	}
	b.work++ // newly parsed Schema record
	// advanced_features (slot 6): reject bit 8 and any unknown bit globally.
	if off, ok := schema.slot(6); ok {
		features, err := b.c.u64(schema.pos + off)
		if err != nil {
			return nil, err
		}
		if features&^advancedFeaturesKnown != 0 {
			return nil, fmt.Errorf("%w: unsupported schema feature bits at byte %d", errSchemaWire, schema.pos+off)
		}
	}
	// file_ident (slot 2): nonempty identifiers must be four bytes.
	if off, ok := schema.slot(2); ok {
		identPos, err := b.reference(schema.pos + off)
		if err != nil {
			return nil, err
		}
		ident, err := b.stringAt(identPos)
		if err != nil {
			return nil, err
		}
		if ident != "" && len(ident) != 4 {
			return nil, fmt.Errorf("%w: invalid file identifier length at byte %d", errSchemaWire, schema.pos+off)
		}
		b.schema.fileIdent = ident
	}
	// file_ext (slot 3) is known metadata: structurally validated only.
	if off, ok := schema.slot(3); ok {
		extPos, err := b.reference(schema.pos + off)
		if err != nil {
			return nil, err
		}
		if _, err := b.stringAt(extPos); err != nil {
			return nil, err
		}
	}
	objectsOff, hasObjects := schema.slot(0)
	enumsOff, hasEnums := schema.slot(1)
	if !hasObjects || !hasEnums {
		return nil, fmt.Errorf("%w: schema missing required objects or enums", errSchemaWire)
	}
	var objectOffsets []int
	if objectOffsets, err = b.vectorOfOffsets(objectsOff, schema.pos, "objects"); err != nil {
		return nil, err
	}
	var enumOffsets []int
	if enumOffsets, err = b.vectorOfOffsets(enumsOff, schema.pos, "enums"); err != nil {
		return nil, err
	}
	// root_table (slot 4): must reference an entry of the objects vector.
	rootTableOff, hasRoot := schema.slot(4)
	if !hasRoot {
		return nil, fmt.Errorf("%w: schema missing root table", errSchemaWire)
	}
	rootObjPos, err := b.reference(schema.pos + rootTableOff)
	if err != nil {
		return nil, err
	}
	rootIndex := -1
	for i, off := range objectOffsets {
		if off == rootObjPos {
			rootIndex = i
			break
		}
	}
	if rootIndex < 0 {
		return nil, fmt.Errorf("%w: root table not declared in objects", errSchemaWire)
	}
	// services (slot 5): validated, never executed.
	if off, ok := schema.slot(5); ok {
		svcOffsets, err := b.vectorOfOffsets(off, schema.pos, "services")
		if err != nil {
			return nil, err
		}
		for _, so := range svcOffsets {
			if err := b.service(so); err != nil {
				return nil, err
			}
		}
	}
	// fbs_files (slot 7): SchemaFile metadata is data, never a resource.
	if off, ok := schema.slot(7); ok {
		fileOffsets, err := b.vectorOfOffsets(off, schema.pos, "fbs_files")
		if err != nil {
			return nil, err
		}
		for _, fo := range fileOffsets {
			if err := b.schemaFile(fo); err != nil {
				return nil, err
			}
		}
	}
	// Parse and validate every object and enum globally.
	names := make(map[string]bool, len(objectOffsets)+len(enumOffsets))
	for _, oo := range objectOffsets {
		obj, err := b.object(oo)
		if err != nil {
			return nil, err
		}
		if names[obj.Name] {
			return nil, fmt.Errorf("%w: duplicate declaration name at byte %d", errSchemaWire, oo)
		}
		names[obj.Name] = true
		b.schema.objects = append(b.schema.objects, obj)
	}
	for _, eo := range enumOffsets {
		en, err := b.enum(eo)
		if err != nil {
			return nil, err
		}
		if names[en.Name] {
			return nil, fmt.Errorf("%w: duplicate declaration name at byte %d", errSchemaWire, eo)
		}
		names[en.Name] = true
		b.schema.enums = append(b.schema.enums, en)
	}
	// Resolve field type references now that all declarations exist.
	for _, obj := range b.schema.objects {
		for _, f := range obj.Fields {
			if err := b.resolveField(obj, f); err != nil {
				return nil, err
			}
		}
	}
	for _, en := range b.schema.enums {
		for _, v := range en.Values {
			if err := b.resolveEnumVal(en, v); err != nil {
				return nil, err
			}
		}
	}
	// Validate struct layouts now that field references are resolved; nested
	// struct fields need StructType populated for width computation.
	for _, obj := range b.schema.objects {
		if obj.IsStruct {
			if err := b.validateStructLayout(obj); err != nil {
				return nil, err
			}
		}
	}
	root := b.schema.objects[rootIndex]
	if root.IsStruct {
		return nil, fmt.Errorf("%w: root table is a struct", errSchemaWire)
	}
	b.schema.root = root
	if err := b.checkBudgetsDone(); err != nil {
		return nil, err
	}
	// Compile the finite root-reachable plan and reject unsupported
	// root-reachable constructs even when absent from any payload.
	if err := b.plan(root); err != nil {
		return nil, err
	}
	return b.schema, nil
}

func (b *bootstrap) charge(n int) error {
	if b.work+n > workLimit {
		return fmt.Errorf("BFBS metadata work limit exceeded")
	}
	b.work += n
	return nil
}

func (b *bootstrap) chargeText(n int) error {
	if b.strText+n > metaTextLimit {
		return fmt.Errorf("BFBS metadata string limit exceeded")
	}
	b.strText += n
	return nil
}

func (b *bootstrap) declaration(kind declKind, off int) error {
	key := declKey{byte(kind), off}
	if b.decls[key] {
		return b.charge(1) // memo hit edges still cost work
	}
	if len(b.decls) >= declLimit {
		return fmt.Errorf("BFBS declaration limit exceeded")
	}
	b.decls[key] = true
	return b.charge(2) // new record plus edge
}

func (b *bootstrap) cancel() error { return b.ctx.Err() }

// slotString reads the string referenced by a string-typed table slot.
func (b *bootstrap) slotString(t tableHeader, slot int) (string, error) {
	off, ok := t.slot(slot)
	if !ok {
		return "", fmt.Errorf("%w: missing string slot %d at byte %d", errSchemaWire, slot, t.pos)
	}
	pos, err := b.reference(t.pos + off)
	if err != nil {
		return "", err
	}
	return b.stringAt(pos)
}

// slotStringOpt reads an optional string-typed slot.
func (b *bootstrap) slotStringOpt(t tableHeader, slot int) (string, error) {
	off, ok := t.slot(slot)
	if !ok {
		return "", nil
	}
	pos, err := b.reference(t.pos + off)
	if err != nil {
		return "", err
	}
	return b.stringAt(pos)
}

// stringAt reads and charges one metadata string occurrence.
func (b *bootstrap) stringAt(pos int) (string, error) {
	s, err := b.c.readString(pos)
	if err != nil {
		return "", err
	}
	if err := b.charge(1); err != nil {
		return "", err
	}
	if err := b.chargeText(len(s)); err != nil {
		return "", err
	}
	return s, nil
}

// reference follows one metadata offset edge with follow-depth accounting.
func (b *bootstrap) reference(pos int) (int, error) {
	if err := b.charge(1); err != nil {
		return 0, err
	}
	return b.c.relativeOffset(pos)
}

func (b *bootstrap) vectorOfOffsets(slotOff, tablePos int, what string) ([]int, error) {
	vecPos, err := b.reference(tablePos + slotOff)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid %s vector at byte %d", errSchemaWire, what, tablePos+slotOff)
	}
	count, start, err := b.c.vectorHeader(vecPos)
	if err != nil {
		return nil, err
	}
	if err := b.charge(1 + count); err != nil {
		return nil, err
	}
	if count*4 > len(b.c.buf)-start {
		return nil, fmt.Errorf("%w: %s vector out of range at byte %d", errSchemaWire, what, vecPos)
	}
	out := make([]int, count)
	for i := range out {
		off, err := b.c.relativeOffset(start + 4*i)
		if err != nil {
			return nil, err
		}
		out[i] = off
	}
	return out, nil
}

// checkUnknownSlots rejects populated slots beyond the fixed metadata
// contract, globally, whether or not the table is otherwise used.
func (b *bootstrap) checkUnknownSlots(t tableHeader, known int, what string) error {
	for id := known; id < len(t.slots); id++ {
		if t.slots[id] != 0 {
			return fmt.Errorf("%w: unknown %s slot %d at byte %d", errSchemaWire, what, id, t.pos)
		}
	}
	return nil
}

func (b *bootstrap) typeRef(pos int) (TypeRef, error) {
	t, err := b.c.readTable(pos)
	if err != nil {
		return TypeRef{}, err
	}
	if err := b.checkUnknownSlots(t, slotsType, "type"); err != nil {
		return TypeRef{}, err
	}
	if err := b.charge(1); err != nil {
		return TypeRef{}, err
	}
	tr := TypeRef{Index: -1} // reflected default for the index slot
	if off, ok := t.slot(0); ok {
		v, err := b.c.u8(t.pos + off)
		if err != nil {
			return TypeRef{}, err
		}
		tr.Base = int(int8(v))
	}
	if off, ok := t.slot(1); ok {
		v, err := b.c.u8(t.pos + off)
		if err != nil {
			return TypeRef{}, err
		}
		tr.Element = int(int8(v))
	}
	if off, ok := t.slot(2); ok {
		v, err := b.c.i32(t.pos + off)
		if err != nil {
			return TypeRef{}, err
		}
		tr.Index = int(v)
	}
	if off, ok := t.slot(3); ok {
		v, err := b.c.u16(t.pos + off)
		if err != nil {
			return TypeRef{}, err
		}
		tr.FixedLength = int(v)
	}
	// base_size/element_size are cross-checked hints, never allocation
	// authority.
	if off, ok := t.slot(4); ok {
		v, err := b.c.u32(t.pos + off)
		if err != nil {
			return TypeRef{}, err
		}
		if width := scalarWidth(tr.Base); v != 0 && width != 0 && int(v) != width {
			return TypeRef{}, fmt.Errorf("%w: inconsistent type size hint at byte %d", errSchemaWire, t.pos+off)
		}
	}
	if tr.Base < btNone || tr.Base >= btMaxBaseType {
		return TypeRef{}, fmt.Errorf("%w: unknown base type at byte %d", errSchemaWire, t.pos)
	}
	if tr.Element != btNone && (tr.Element < btNone || tr.Element >= btMaxBaseType) {
		return TypeRef{}, fmt.Errorf("%w: unknown element type at byte %d", errSchemaWire, t.pos)
	}
	return tr, nil
}

func (b *bootstrap) object(pos int) (*Object, error) {
	if err := b.cancel(); err != nil {
		return nil, err
	}
	if obj, ok := b.objects[pos]; ok {
		if err := b.charge(1); err != nil { // memo hit edge
			return nil, err
		}
		return obj, nil
	}
	if err := b.declaration(declObject, pos); err != nil {
		return nil, err
	}
	t, err := b.c.readTable(pos)
	if err != nil {
		return nil, err
	}
	if err := b.checkUnknownSlots(t, slotsObject, "object"); err != nil {
		return nil, err
	}
	obj := &Object{}
	if obj.Name, err = b.slotString(t, 0); err != nil {
		return nil, err
	}
	if off, ok := t.slot(2); ok {
		v, err := b.c.u8(t.pos + off)
		if err != nil {
			return nil, err
		}
		obj.IsStruct = v != 0
	}
	if off, ok := t.slot(3); ok {
		v, err := b.c.i32(t.pos + off)
		if err != nil {
			return nil, err
		}
		obj.MinAlign = int(v)
	}
	if off, ok := t.slot(4); ok {
		v, err := b.c.i32(t.pos + off)
		if err != nil {
			return nil, err
		}
		obj.ByteSize = int(v)
	}
	// attributes (slot 5) and documentation (slot 6): known unused metadata,
	// still structurally validated.
	if off, ok := t.slot(5); ok {
		if err := b.keyValues(t.pos + off); err != nil {
			return nil, err
		}
	}
	if off, ok := t.slot(6); ok {
		if err := b.stringVector(t.pos + off); err != nil {
			return nil, err
		}
	}
	if _, err := b.slotStringOpt(t, 7); err != nil {
		return nil, err
	}
	fieldsOff, hasFields := t.slot(1)
	if !hasFields {
		return nil, fmt.Errorf("%w: object missing fields at byte %d", errSchemaWire, pos)
	}
	fieldOffsets, err := b.vectorOfOffsets(fieldsOff, t.pos, "fields")
	if err != nil {
		return nil, err
	}
	seenIDs := make(map[int]bool, len(fieldOffsets))
	seenNames := make(map[string]bool, len(fieldOffsets))
	for _, fo := range fieldOffsets {
		f, err := b.field(fo)
		if err != nil {
			return nil, err
		}
		if seenIDs[f.ID] {
			return nil, fmt.Errorf("%w: duplicate field id at byte %d", errSchemaWire, fo)
		}
		seenIDs[f.ID] = true
		if seenNames[f.Name] {
			return nil, fmt.Errorf("%w: duplicate field name at byte %d", errSchemaWire, fo)
		}
		seenNames[f.Name] = true
		obj.Fields = append(obj.Fields, f)
	}
	sort.Slice(obj.Fields, func(i, j int) bool { return obj.Fields[i].ID < obj.Fields[j].ID })
	for i, f := range obj.Fields {
		if f.ID != i {
			return nil, fmt.Errorf("%w: non-consecutive field ids at byte %d", errSchemaWire, pos)
		}
	}
	if !obj.IsStruct && (obj.MinAlign != 0 || obj.ByteSize != 0) {
		// Table objects carry no inline layout; reflected values are hints.
		if obj.MinAlign < 0 || obj.ByteSize < 0 {
			return nil, fmt.Errorf("%w: invalid object layout hint at byte %d", errSchemaWire, pos)
		}
	}
	// Struct layouts are validated after field references resolve, so nested
	// struct fields have their StructType set when widths are computed.
	b.objects[pos] = obj
	b.schema.objIndex[pos] = obj
	return obj, nil
}

func (b *bootstrap) field(pos int) (*Field, error) {
	if err := b.cancel(); err != nil {
		return nil, err
	}
	if err := b.declaration(declField, pos); err != nil {
		return nil, err
	}
	t, err := b.c.readTable(pos)
	if err != nil {
		return nil, err
	}
	if err := b.checkUnknownSlots(t, slotsField, "field"); err != nil {
		return nil, err
	}
	f := &Field{}
	if f.Name, err = b.slotString(t, 0); err != nil {
		return nil, err
	}
	typeOff, hasType := t.slot(1)
	if !hasType {
		return nil, fmt.Errorf("%w: field missing type at byte %d", errSchemaWire, pos)
	}
	typePos, err := b.reference(t.pos + typeOff)
	if err != nil {
		return nil, err
	}
	if f.Type, err = b.typeRef(typePos); err != nil {
		return nil, err
	}
	if off, ok := t.slot(2); ok {
		v, err := b.c.u16(t.pos + off)
		if err != nil {
			return nil, err
		}
		f.ID = int(v)
	}
	if off, ok := t.slot(3); ok {
		v, err := b.c.u16(t.pos + off)
		if err != nil {
			return nil, err
		}
		f.Offset = int(v)
	}
	if off, ok := t.slot(4); ok {
		v, err := b.c.u64(t.pos + off)
		if err != nil {
			return nil, err
		}
		f.DefaultInteger = int64(v)
	}
	if off, ok := t.slot(5); ok {
		if f.DefaultReal, err = b.c.f64(t.pos + off); err != nil {
			return nil, err
		}
		f.DefaultRealSet = true
	}
	boolSlot := func(slot int, dst *bool) error {
		if off, ok := t.slot(slot); ok {
			v, err := b.c.u8(t.pos + off)
			if err != nil {
				return err
			}
			*dst = v != 0
		}
		return nil
	}
	if err := boolSlot(6, &f.Deprecated); err != nil {
		return nil, err
	}
	if err := boolSlot(7, &f.Required); err != nil {
		return nil, err
	}
	if err := boolSlot(8, &f.Key); err != nil {
		return nil, err
	}
	if err := boolSlot(11, &f.Optional); err != nil {
		return nil, err
	}
	if err := boolSlot(13, &f.Offset64); err != nil {
		return nil, err
	}
	if off, ok := t.slot(12); ok {
		v, err := b.c.u16(t.pos + off)
		if err != nil {
			return nil, err
		}
		f.Padding = int(v)
	}
	if off, ok := t.slot(9); ok {
		if err := b.keyValues(t.pos + off); err != nil {
			return nil, err
		}
	}
	if off, ok := t.slot(10); ok {
		if err := b.stringVector(t.pos + off); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (b *bootstrap) enum(pos int) (*Enum, error) {
	if err := b.cancel(); err != nil {
		return nil, err
	}
	if en, ok := b.enums[pos]; ok {
		if err := b.charge(1); err != nil {
			return nil, err
		}
		return en, nil
	}
	if err := b.declaration(declEnum, pos); err != nil {
		return nil, err
	}
	t, err := b.c.readTable(pos)
	if err != nil {
		return nil, err
	}
	if err := b.checkUnknownSlots(t, slotsEnum, "enum"); err != nil {
		return nil, err
	}
	en := &Enum{ByValue: make(map[int64]*EnumVal)}
	if en.Name, err = b.slotString(t, 0); err != nil {
		return nil, err
	}
	if off, ok := t.slot(2); ok {
		v, err := b.c.u8(t.pos + off)
		if err != nil {
			return nil, err
		}
		en.IsUnion = v != 0
	}
	underOff, hasUnder := t.slot(3)
	if !hasUnder {
		return nil, fmt.Errorf("%w: enum missing underlying type at byte %d", errSchemaWire, pos)
	}
	underPos, err := b.reference(t.pos + underOff)
	if err != nil {
		return nil, err
	}
	if en.Underlying, err = b.typeRef(underPos); err != nil {
		return nil, err
	}
	if !isIntegralBase(en.Underlying.Base) {
		return nil, fmt.Errorf("%w: non-integral underlying type at byte %d", errSchemaWire, pos)
	}
	en.UnderlyWidth = scalarWidth(en.Underlying.Base)
	en.IsUnsigned = !isSignedBase(en.Underlying.Base) && en.Underlying.Base != btUType
	if off, ok := t.slot(4); ok {
		if err := b.keyValues(t.pos + off); err != nil {
			return nil, err
		}
	}
	if off, ok := t.slot(5); ok {
		if err := b.stringVector(t.pos + off); err != nil {
			return nil, err
		}
	}
	if _, err := b.slotStringOpt(t, 6); err != nil {
		return nil, err
	}
	valuesOff, hasValues := t.slot(1)
	if !hasValues {
		return nil, fmt.Errorf("%w: enum missing values at byte %d", errSchemaWire, pos)
	}
	valueOffsets, err := b.vectorOfOffsets(valuesOff, t.pos, "enum values")
	if err != nil {
		return nil, err
	}
	for _, vo := range valueOffsets {
		v, err := b.enumVal(vo)
		if err != nil {
			return nil, err
		}
		// The values vector is declaration-ordered for unions; lookup is by
		// numeric value, so only distinctness is required here.
		if _, dup := en.ByValue[v.Value]; dup {
			return nil, fmt.Errorf("%w: duplicate enum value at byte %d", errSchemaWire, vo)
		}
		en.ByValue[v.Value] = v
		en.Values = append(en.Values, v)
	}
	if len(en.Values) == 0 {
		return nil, fmt.Errorf("%w: empty enum at byte %d", errSchemaWire, pos)
	}
	if en.IsUnion {
		if none, ok := en.ByValue[0]; !ok || none.UnionType.Base != btNone {
			return nil, fmt.Errorf("%w: union missing NONE at byte %d", errSchemaWire, pos)
		}
	}
	b.enums[pos] = en
	return en, nil
}

func (b *bootstrap) enumVal(pos int) (*EnumVal, error) {
	if err := b.declaration(declEnumVal, pos); err != nil {
		return nil, err
	}
	t, err := b.c.readTable(pos)
	if err != nil {
		return nil, err
	}
	if err := b.checkUnknownSlots(t, slotsEnumVal, "enum value"); err != nil {
		return nil, err
	}
	v := &EnumVal{}
	if v.Name, err = b.slotString(t, 0); err != nil {
		return nil, err
	}
	if off, ok := t.slot(1); ok {
		bits, err := b.c.u64(t.pos + off)
		if err != nil {
			return nil, err
		}
		v.Value = int64(bits)
	}
	// object (slot 2) is a known deprecated slot: structurally validated,
	// never used as a substitute for current union_type metadata.
	if off, ok := t.slot(2); ok {
		objPos, err := b.reference(t.pos + off)
		if err != nil {
			return nil, err
		}
		if _, err := b.object(objPos); err != nil {
			return nil, err
		}
	}
	if off, ok := t.slot(3); ok {
		utPos, err := b.reference(t.pos + off)
		if err != nil {
			return nil, err
		}
		if v.UnionType, err = b.typeRef(utPos); err != nil {
			return nil, err
		}
	}
	if off, ok := t.slot(4); ok {
		if err := b.stringVector(t.pos + off); err != nil {
			return nil, err
		}
	}
	if off, ok := t.slot(5); ok {
		if err := b.keyValues(t.pos + off); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func (b *bootstrap) service(pos int) error {
	if err := b.declaration(declService, pos); err != nil {
		return err
	}
	t, err := b.c.readTable(pos)
	if err != nil {
		return err
	}
	if err := b.checkUnknownSlots(t, slotsService, "service"); err != nil {
		return err
	}
	if _, err = b.slotString(t, 0); err != nil {
		return err
	}
	if off, ok := t.slot(1); ok {
		callOffsets, err := b.vectorOfOffsets(off, t.pos, "calls")
		if err != nil {
			return err
		}
		for _, co := range callOffsets {
			if err := b.rpcCall(co); err != nil {
				return err
			}
		}
	}
	if off, ok := t.slot(2); ok {
		if err := b.keyValues(t.pos + off); err != nil {
			return err
		}
	}
	if off, ok := t.slot(3); ok {
		if err := b.stringVector(t.pos + off); err != nil {
			return err
		}
	}
	if _, err := b.slotStringOpt(t, 4); err != nil {
		return err
	}
	return nil
}

func (b *bootstrap) rpcCall(pos int) error {
	if err := b.declaration(declRPCCall, pos); err != nil {
		return err
	}
	t, err := b.c.readTable(pos)
	if err != nil {
		return err
	}
	if err := b.checkUnknownSlots(t, slotsRPCCall, "rpc call"); err != nil {
		return err
	}
	if _, err = b.slotString(t, 0); err != nil {
		return err
	}
	for _, slot := range []int{1, 2} {
		off, ok := t.slot(slot)
		if !ok {
			return fmt.Errorf("%w: rpc call missing object reference at byte %d", errSchemaWire, pos)
		}
		objPos, err := b.reference(t.pos + off)
		if err != nil {
			return err
		}
		// Request/response tables are validated, never executed.
		if _, err := b.object(objPos); err != nil {
			return err
		}
	}
	if off, ok := t.slot(3); ok {
		if err := b.keyValues(t.pos + off); err != nil {
			return err
		}
	}
	if off, ok := t.slot(4); ok {
		if err := b.stringVector(t.pos + off); err != nil {
			return err
		}
	}
	return nil
}

func (b *bootstrap) schemaFile(pos int) error {
	t, err := b.c.readTable(pos)
	if err != nil {
		return err
	}
	if err := b.checkUnknownSlots(t, slotsSchemaFile, "file entry"); err != nil {
		return err
	}
	if err := b.charge(1); err != nil {
		return err
	}
	if _, err = b.slotString(t, 0); err != nil {
		return err
	}
	if off, ok := t.slot(1); ok {
		if err := b.stringVector(t.pos + off); err != nil {
			return err
		}
	}
	return nil
}

func (b *bootstrap) keyValues(pos int) error {
	kvOffsets, err := b.vectorOfOffsetsAt(pos, "attributes")
	if err != nil {
		return err
	}
	for _, ko := range kvOffsets {
		t, err := b.c.readTable(ko)
		if err != nil {
			return err
		}
		if err := b.checkUnknownSlots(t, slotsKeyValue, "attribute"); err != nil {
			return err
		}
		if err := b.charge(1); err != nil {
			return err
		}
		if _, err = b.slotString(t, 0); err != nil {
			return err
		}
		if _, err := b.slotStringOpt(t, 1); err != nil {
			return err
		}
	}
	return nil
}

func (b *bootstrap) stringVector(pos int) error {
	strOffsets, err := b.vectorOfOffsetsAt(pos, "strings")
	if err != nil {
		return err
	}
	for _, so := range strOffsets {
		if _, err := b.stringAt(so); err != nil {
			return err
		}
	}
	return nil
}

// vectorOfOffsetsAt resolves a vector reference stored at the given byte
// position and returns element target offsets.
func (b *bootstrap) vectorOfOffsetsAt(pos int, what string) ([]int, error) {
	vecPos, err := b.reference(pos)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid %s vector at byte %d", errSchemaWire, what, pos)
	}
	count, start, err := b.c.vectorHeader(vecPos)
	if err != nil {
		return nil, err
	}
	if err := b.charge(1 + count); err != nil {
		return nil, err
	}
	if count*4 > len(b.c.buf)-start {
		return nil, fmt.Errorf("%w: %s vector out of range at byte %d", errSchemaWire, what, vecPos)
	}
	out := make([]int, count)
	for i := range out {
		off, err := b.c.relativeOffset(start + 4*i)
		if err != nil {
			return nil, err
		}
		out[i] = off
	}
	return out, nil
}

func (b *bootstrap) checkBudgetsDone() error {
	if b.work > workLimit || b.strText > metaTextLimit {
		return fmt.Errorf("BFBS metadata limits exceeded")
	}
	return nil
}
