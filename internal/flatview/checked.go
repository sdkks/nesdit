// Package flatview prepares checked, read-only known-field views of local
// ordinary FlatBuffers payloads using ordinary BFBS schemas compiled by the
// pinned official flatbuffers toolchain. The schema and payload are treated
// as untrusted: every offset, length, alignment and layout is validated by
// this package's own checked reader before use, with fixed work and output
// budgets. No runtime FlatBuffers dependency, generated accessor, CGO,
// schema lookup or network access is involved.
package flatview

import (
	"errors"
	"fmt"
	"math"
	"unicode/utf8"
)

// Inclusive caps shared by preparation and decoding. They bound logical
// component work, not RSS or CPU.
const (
	SchemaLimit      = 4 << 20  // raw BFBS bytes
	rawLimit         = 10 << 20 // payload bytes (enforced by the run layer)
	declLimit        = 10_000   // unique metadata declarations
	workLimit        = 1_000_000
	metaTextLimit    = 16 << 20
	depthLimit       = 100
	nodeLimit        = 1_000_000
	textLimit        = 16 << 20
	vectorCountLimit = 1 << 30 // sanity bound before per-element checks
)

// Metadata base types, mirroring the pinned reflection schema's BaseType.
const (
	btNone = iota
	btUType
	btBool
	btByte
	btUByte
	btShort
	btUShort
	btInt
	btUInt
	btLong
	btULong
	btFloat
	btDouble
	btString
	btVector
	btObj
	btUnion
	btArray
	btVector64
	btMaxBaseType
)

// advancedFeaturesKnown lists the only accepted advanced-feature bits:
// arrays (1), advanced unions (2) and optional scalars (4).
const advancedFeaturesKnown = 1 | 2 | 4

var (
	errSchemaWire = errors.New("malformed BFBS schema")
	errPayload    = errors.New("malformed FlatBuffers payload")
)

// scalarWidth returns the storage width in bytes for scalar base types and
// the offset word width for reference base types.
func scalarWidth(base int) int {
	switch base {
	case btBool, btUType, btByte, btUByte:
		return 1
	case btShort, btUShort:
		return 2
	case btInt, btUInt, btFloat:
		return 4
	case btLong, btULong, btDouble:
		return 8
	case btString, btVector, btObj, btUnion, btArray, btVector64:
		return 4
	default:
		return 0
	}
}

func isScalarBase(base int) bool {
	switch base {
	case btBool, btUType, btByte, btUByte, btShort, btUShort, btInt, btUInt, btLong, btULong, btFloat, btDouble:
		return true
	}
	return false
}

func isIntegralBase(base int) bool {
	switch base {
	case btUType, btByte, btUByte, btShort, btUShort, btInt, btUInt, btLong, btULong:
		return true
	}
	return false
}

func isSignedBase(base int) bool {
	switch base {
	case btByte, btShort, btInt, btLong:
		return true
	}
	return false
}

// checked provides bounds-checked little-endian reads over immutable bytes.
// Every accessor validates the full width before conversion.
type checked struct{ buf []byte }

func (c checked) fits(pos, width int) bool {
	return pos >= 0 && width >= 0 && pos <= len(c.buf)-width
}

func (c checked) u8(pos int) (uint8, error) {
	if !c.fits(pos, 1) {
		return 0, errWireAt(pos)
	}
	return c.buf[pos], nil
}

func (c checked) u16(pos int) (uint16, error) {
	if !c.fits(pos, 2) {
		return 0, errWireAt(pos)
	}
	return uint16(c.buf[pos]) | uint16(c.buf[pos+1])<<8, nil
}

func (c checked) u32(pos int) (uint32, error) {
	if !c.fits(pos, 4) {
		return 0, errWireAt(pos)
	}
	return uint32(c.buf[pos]) | uint32(c.buf[pos+1])<<8 | uint32(c.buf[pos+2])<<16 | uint32(c.buf[pos+3])<<24, nil
}

func (c checked) i32(pos int) (int32, error) {
	v, err := c.u32(pos)
	return int32(v), err
}

func (c checked) u64(pos int) (uint64, error) {
	if !c.fits(pos, 8) {
		return 0, errWireAt(pos)
	}
	var v uint64
	for i := 7; i >= 0; i-- {
		v = v<<8 | uint64(c.buf[pos+i])
	}
	return v, nil
}

func (c checked) f32(pos int) (float64, error) {
	bits, err := c.u32(pos)
	if err != nil {
		return 0, err
	}
	return float64(math.Float32frombits(bits)), nil
}

func (c checked) f64(pos int) (float64, error) {
	bits, err := c.u64(pos)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(bits), nil
}

func errWireAt(pos int) error { return fmt.Errorf("%w at byte %d", errSchemaWire, pos) }

// tableHeader is a validated table (or struct-backed table view): the object
// position, its vtable position and the vtable slot entries.
type tableHeader struct {
	pos     int
	vtPos   int
	objSize int
	slots   []uint16
}

// readTable validates the soffset/vtable header chain of an ordinary table
// at pos and returns the parsed slot offsets. displacement is signed, so a
// shared vtable placed after the table is legal.
func (c checked) readTable(pos int) (tableHeader, error) {
	var h tableHeader
	h.pos = pos
	soff, err := c.i32(pos)
	if err != nil {
		return h, err
	}
	vtPos := int64(pos) - int64(soff)
	if vtPos < 0 || vtPos > int64(len(c.buf)) {
		return h, fmt.Errorf("%w: vtable out of range at byte %d", errSchemaWire, pos)
	}
	h.vtPos = int(vtPos)
	vsize, err := c.u16(h.vtPos)
	if err != nil {
		return h, err
	}
	if vsize < 4 || vsize&1 != 0 {
		return h, fmt.Errorf("%w: invalid vtable size at byte %d", errSchemaWire, h.vtPos)
	}
	objSize, err := c.u16(h.vtPos + 2)
	if err != nil {
		return h, err
	}
	h.objSize = int(objSize)
	if h.objSize < 4 || pos+h.objSize > len(c.buf) {
		return h, fmt.Errorf("%w: object extent out of range at byte %d", errSchemaWire, pos)
	}
	n := (int(vsize) - 4) / 2
	h.slots = make([]uint16, n)
	for i := 0; i < n; i++ {
		v, err := c.u16(h.vtPos + 4 + 2*i)
		if err != nil {
			return h, err
		}
		h.slots[i] = v
	}
	return h, nil
}

// slot returns the byte offset of field id within the table object and
// whether the field is present. A short vtable means trailing fields absent.
func (h tableHeader) slot(id int) (int, bool) {
	if id < 0 || id >= len(h.slots) {
		return 0, false
	}
	off := int(h.slots[id])
	return off, off != 0
}

// relativeOffset validates and resolves the forward uoffset word at pos.
// Zero, backward and overflowing offsets are rejected before addition.
func (c checked) relativeOffset(pos int) (int, error) {
	rel, err := c.u32(pos)
	if err != nil {
		return 0, err
	}
	if rel == 0 {
		return 0, fmt.Errorf("%w: zero reference at byte %d", errSchemaWire, pos)
	}
	target := int64(pos) + int64(rel)
	if target <= int64(pos) || target >= int64(len(c.buf)) {
		return 0, fmt.Errorf("%w: invalid reference at byte %d", errSchemaWire, pos)
	}
	return int(target), nil
}

// readString validates a counted string: length word, bytes, terminating
// NUL and UTF-8. Embedded NUL bytes remain content.
func (c checked) readString(pos int) (string, error) {
	n, err := c.u32(pos)
	if err != nil {
		return "", err
	}
	start := pos + 4
	if int64(start)+int64(n) >= int64(len(c.buf)) {
		return "", fmt.Errorf("%w: string out of range at byte %d", errSchemaWire, pos)
	}
	raw := c.buf[start : start+int(n)]
	if c.buf[start+int(n)] != 0 {
		return "", fmt.Errorf("%w: missing string terminator at byte %d", errSchemaWire, pos)
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("%w: invalid UTF-8 string at byte %d", errSchemaWire, pos)
	}
	return string(raw), nil
}

// vectorHeader validates a counted vector header and returns the element
// area start. Element extent is validated per element by width/stride.
func (c checked) vectorHeader(pos int) (count, start int, err error) {
	n, err := c.u32(pos)
	if err != nil {
		return 0, 0, err
	}
	if n > vectorCountLimit {
		return 0, 0, fmt.Errorf("%w: vector length out of range at byte %d", errSchemaWire, pos)
	}
	return int(n), pos + 4, nil
}
