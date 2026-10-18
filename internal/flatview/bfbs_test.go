package flatview

import (
	"encoding/binary"
	"math"
	"sort"
)

// miniBFBS assembles small BFBS schemas byte-by-byte for mutation tests.
// Sections are appended in reference order (schema first, strings last) so
// every offset word points forward; words are patched in finish().
type miniBFBS struct {
	b    []byte
	refs map[string][]int
	pos  map[string]int
}

func newMiniBFBS() *miniBFBS {
	return &miniBFBS{refs: map[string][]int{}, pos: map[string]int{}}
}

func (f *miniBFBS) alloc(n, align int) int {
	for len(f.b)%align != 0 {
		f.b = append(f.b, 0)
	}
	off := len(f.b)
	f.b = append(f.b, make([]byte, n)...)
	return off
}

// patchRef records an allocated word position to be patched to the named
// target position at finish time.
func (f *miniBFBS) patchRef(name string, off int) {
	f.refs[name] = append(f.refs[name], off)
}

func (f *miniBFBS) mark(name string, pos int) { f.pos[name] = pos }

func (f *miniBFBS) finish(rootName string) []byte {
	for name, spots := range f.refs {
		target, ok := f.pos[name]
		if !ok {
			continue
		}
		for _, off := range spots {
			binary.LittleEndian.PutUint32(f.b[off:], uint32(target-off))
		}
	}
	out := make([]byte, 8, 8+len(f.b))
	copy(out[4:], "BFBS")
	out = append(out, f.b...)
	if t, ok := f.pos[rootName]; ok {
		binary.LittleEndian.PutUint32(out[0:], uint32(8+t))
	}
	return out
}

// str appends a NUL-terminated counted string and returns its position.
func (f *miniBFBS) str(s string) int {
	off := f.alloc(4, 4)
	f.b = append(f.b, s...)
	f.b = append(f.b, 0)
	binary.LittleEndian.PutUint32(f.b[off:], uint32(len(s)))
	return off
}

// vtable writes a vtable; slots map field id to byte offsets within the
// upcoming object.
func (f *miniBFBS) vtable(osize int, slots map[int]int) int {
	ids := make([]int, 0, len(slots))
	for id := range slots {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	n := 0
	if len(ids) > 0 {
		n = ids[len(ids)-1] + 1
	}
	off := f.alloc(4+2*n, 2)
	binary.LittleEndian.PutUint16(f.b[off:], uint16(4+2*n))
	binary.LittleEndian.PutUint16(f.b[off+2:], uint16(osize))
	for id, v := range slots {
		binary.LittleEndian.PutUint16(f.b[off+4+2*id:], uint16(v))
	}
	return off
}

// table writes an object whose soffset refers to the given vtable; place
// writes field bytes at absolute positions within the allocated object.
func (f *miniBFBS) table(vt, osize int, place func(base int)) int {
	pos := f.alloc(osize, 4)
	binary.LittleEndian.PutUint32(f.b[pos:], uint32(int32(pos-vt)))
	place(pos)
	return pos
}

// vectorOfRefs writes a vector of offset words referencing named targets.
func (f *miniBFBS) vectorOfRefs(names []string) int {
	pos := f.alloc(4+4*len(names), 4)
	binary.LittleEndian.PutUint32(f.b[pos:], uint32(len(names)))
	for i, n := range names {
		f.patchRef(n, pos+4+4*i)
	}
	return pos
}

// negSchema builds a minimal valid schema, root table Neg { x:double }, with
// control over the stored default_real slot and an optional populated
// unknown Schema slot. Sections are emitted schema-first so references
// point forward.
func (f *miniBFBS) negSchema(storeDefault bool, defaultBits float64, unknownSlot bool) string {
	slots := map[int]int{0: 4, 1: 8, 4: 12}
	if unknownSlot {
		slots[8] = 16
	}
	schemaVT := f.vtable(20, slots)
	schemaPos := f.table(schemaVT, 20, func(base int) {
		f.patchRef("objects", base+4)
		f.patchRef("enums", base+8)
		f.patchRef("object", base+12)
		if unknownSlot {
			f.b[base+16] = 1
		}
	})
	f.mark("schema", schemaPos)
	objVec := f.vectorOfRefs([]string{"object"})
	f.mark("objects", objVec)
	enumVec := f.vectorOfRefs(nil)
	f.mark("enums", enumVec)
	objectVT := f.vtable(16, map[int]int{0: 4, 1: 8})
	objectPos := f.table(objectVT, 16, func(base int) {
		f.patchRef("name-neg", base+4)
		f.patchRef("fields", base+8)
	})
	f.mark("object", objectPos)
	fieldVec := f.vectorOfRefs([]string{"field-x"})
	f.mark("fields", fieldVec)
	fieldSlots := map[int]int{0: 4, 1: 8, 2: 12, 3: 14}
	if storeDefault {
		fieldSlots[5] = 16
	}
	fieldVT := f.vtable(24, fieldSlots)
	fieldPos := f.table(fieldVT, 24, func(base int) {
		f.patchRef("name-x", base+4)
		f.patchRef("type", base+8)
		binary.LittleEndian.PutUint16(f.b[base+14:], 4) // offset = 4
		if storeDefault {
			binary.LittleEndian.PutUint64(f.b[base+16:], math.Float64bits(defaultBits))
		}
	})
	f.mark("field-x", fieldPos)
	typeVT := f.vtable(8, map[int]int{0: 4})
	typePos := f.table(typeVT, 8, func(base int) {
		f.b[base+4] = byte(btDouble)
	})
	f.mark("type", typePos)
	f.mark("name-neg", f.str("Neg"))
	f.mark("name-x", f.str("x"))
	f.str("nt.Neg")
	return "schema"
}

// negPayload builds a root Neg table with optional stored x. The vtable is
// placed after the object, so the table uses a negative displacement.
func negPayload(storeX bool, x float64) []byte {
	objSize := 8
	if storeX {
		objSize = 24
	}
	vt := make([]byte, 8)
	binary.LittleEndian.PutUint16(vt, 8)
	binary.LittleEndian.PutUint16(vt[2:], uint16(objSize))
	if storeX {
		binary.LittleEndian.PutUint16(vt[4:], 8)
	}
	out := make([]byte, 8, 8+objSize+8)
	tablePos := 8
	for len(out)-tablePos < objSize {
		out = append(out, 0)
	}
	vtPos := tablePos + objSize
	out = append(out, vt...)
	binary.LittleEndian.PutUint32(out[0:], uint32(tablePos))
	binary.LittleEndian.PutUint32(out[tablePos:], uint32(int32(tablePos-vtPos)))
	if storeX {
		binary.LittleEndian.PutUint64(out[tablePos+8:], math.Float64bits(x))
	}
	return out
}
