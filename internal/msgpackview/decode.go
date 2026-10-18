// Package msgpackview converts one MessagePack value or explicitly interpreted
// JSON value using MessagePack JSON v1. Application maps are ordered entry arrays,
// never Go maps or hidden tags.
package msgpackview

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/sdkks/nesdit/internal/omap"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	rawLimit   = 10 << 20
	depthLimit = 100
	nodeLimit  = 1_000_000
	textLimit  = 16 << 20
)

var (
	errWire     = errors.New("malformed MessagePack")
	errSemantic = errors.New("MessagePack decoded value limit exceeded")
	errView     = errors.New("MessagePack JSON view limit exceeded")
)

// budget counts occurrences, not unique values. A root has depth zero.
type budget struct{ nodes, text int }

func (b *budget) fits(depth, nodes, text int) bool {
	return depth <= depthLimit && nodes >= 0 && text >= 0 && nodes <= nodeLimit-b.nodes && text <= textLimit-b.text
}

func (b *budget) add(depth, nodes, text int) bool {
	if !b.fits(depth, nodes, text) {
		return false
	}
	b.nodes += nodes
	b.text += text
	return true
}

type walker struct {
	ctx           context.Context
	body          []byte
	reader        *bytes.Reader
	decoder       *msgpack.Decoder
	decoded, view budget
}

// Decode admits the entire immutable body before a caller can filter it.
// The returned tree contains only ordinary JSON values. No application
// extension registry is consulted (the dependency has built-in init hooks).
func Decode(ctx context.Context, body []byte) (omap.Value, error) {
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	if len(body) > rawLimit {
		return omap.Value{}, errors.New("MessagePack input exceeds 10 MiB")
	}
	r := bytes.NewReader(body)
	w := walker{ctx: ctx, body: body, reader: r, decoder: msgpack.NewDecoder(r)}
	v, err := w.value(0, 0)
	if err == nil && r.Len() != 0 {
		err = errors.New("trailing bytes after MessagePack value")
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		// Do not expose library diagnostics or input bytes, including map keys.
		return omap.Value{}, fmt.Errorf("%w at byte %d", err, len(body)-r.Len())
	}
	return v, nil
}

func (w *walker) value(depth, viewDepth int) (omap.Value, error) {
	if !w.decoded.add(depth, 1, 0) {
		return omap.Value{}, errSemantic
	}
	if !w.view.add(viewDepth, 1, 0) {
		return omap.Value{}, errView
	}
	return w.reservedValue(depth, viewDepth)
}

// reservedValue consumes a root slot already charged by value or its parent.
// Container reservations include all outstanding siblings, preventing nested
// declarations from repeatedly spending the same remaining allocation budget.
func (w *walker) reservedValue(depth, viewDepth int) (omap.Value, error) {
	if err := w.ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	code, err := w.decoder.PeekCode()
	if err != nil {
		return omap.Value{}, errWire
	}
	// Every permissive library operation is gated by its exact wire family.
	switch {
	case code == 0xc0:
		if w.decoder.DecodeNil() != nil {
			return omap.Value{}, errWire
		}
		return omap.NullValue(), nil
	case code == 0xc2 || code == 0xc3:
		b, e := w.decoder.DecodeBool()
		if e != nil {
			return omap.Value{}, errWire
		}
		return omap.BoolValue(b), nil
	case code <= 0x7f || code >= 0xcc && code <= 0xcf:
		n, e := w.decoder.DecodeUint64()
		if e != nil {
			return omap.Value{}, errWire
		}
		return w.stringTag("$int", strconv.FormatUint(n, 10), viewDepth)
	case code >= 0xe0 || code >= 0xd0 && code <= 0xd3:
		n, e := w.decoder.DecodeInt64()
		if e != nil {
			return omap.Value{}, errWire
		}
		return w.stringTag("$int", strconv.FormatInt(n, 10), viewDepth)
	case code == 0xca || code == 0xcb:
		return w.float(code, viewDepth)
	case code >= 0xa0 && code <= 0xbf || code >= 0xd9 && code <= 0xdb:
		return w.bytes(false, viewDepth)
	case code >= 0xc4 && code <= 0xc6:
		return w.bytes(true, viewDepth)
	case code >= 0x90 && code <= 0x9f || code == 0xdc || code == 0xdd:
		return w.array(depth, viewDepth)
	case code >= 0x80 && code <= 0x8f || code == 0xde || code == 0xdf:
		return w.mapping(depth, viewDepth)
	case code >= 0xc7 && code <= 0xc9 || code >= 0xd4 && code <= 0xd8:
		return w.extension(viewDepth)
	default: // 0xc1 is reserved, never a value.
		return omap.Value{}, errWire
	}
}

// tag is called only after all of its view costs have been admitted.
func tag(key string, value omap.Value) omap.Value {
	d := omap.New()
	d.Set(key, value)
	return omap.MapValue(d)
}

func (w *walker) stringTag(key, text string, depth int) (omap.Value, error) {
	// Decimal/float spellings are bounded by the scalar width, not input size.
	if !w.view.add(depth+1, 1, len(key)+len(text)) {
		return omap.Value{}, errView
	}
	return tag(key, omap.StrValue(text)), nil
}

func (w *walker) float(code byte, depth int) (omap.Value, error) {
	var n float64
	var err error
	if code == 0xca {
		var f float32
		f, err = w.decoder.DecodeFloat32()
		n = float64(f) // Exact widening, not shortest float32 decimal text.
	} else {
		n, err = w.decoder.DecodeFloat64()
	}
	if err != nil {
		return omap.Value{}, errWire
	}
	text := ""
	switch {
	case math.IsNaN(n):
		text = "NaN"
	case math.IsInf(n, 1):
		text = "Infinity"
	case math.IsInf(n, -1):
		text = "-Infinity"
	default:
		text = strconv.FormatFloat(n, 'g', -1, 64)
	}
	return w.stringTag("$float", text, depth)
}

// payload admits lengths before making a caller-sized ReadFull buffer.
func (w *walker) payload(n int) ([]byte, error) {
	if n < 0 || n > w.reader.Len() {
		return nil, errWire
	}
	if !w.decoded.add(0, 0, n) {
		return nil, errSemantic
	}
	start := len(w.body) - w.reader.Len()
	return w.body[start : start+n], nil
}

func (w *walker) readPayload(n int) ([]byte, error) {
	data := make([]byte, n)
	// Bound cooperative work between cancellation checks, without changing
	// ownership or introducing a second buffered reader.
	for offset := 0; offset < n; {
		if err := w.ctx.Err(); err != nil {
			return nil, err
		}
		end := min(offset+(32<<10), n)
		if w.decoder.ReadFull(data[offset:end]) != nil {
			return nil, errWire
		}
		offset = end
	}
	return data, nil
}

func (w *walker) bytes(isBinary bool, depth int) (omap.Value, error) {
	n, err := w.decoder.DecodeBytesLen()
	if err != nil {
		return omap.Value{}, errWire
	}
	raw, err := w.payload(n)
	if err != nil {
		return omap.Value{}, err
	}
	// The bounded raw slice is borrowed for classification only, never changed.
	if !isBinary && utf8.Valid(raw) {
		if !w.view.add(depth, 0, n) {
			return omap.Value{}, errView
		}
		data, e := w.readPayload(n)
		if e != nil {
			return omap.Value{}, e
		}
		return omap.StrValue(string(data)), nil
	}
	key := "$bin"
	if !isBinary {
		key = "$str64"
	}
	// n already fits the raw cap, so this calculation cannot overflow.
	encoded := base64.StdEncoding.EncodedLen(n)
	if !w.view.add(depth+1, 1, len(key)+encoded) {
		return omap.Value{}, errView
	}
	data, err := w.readPayload(n)
	if err != nil {
		return omap.Value{}, err
	}
	return tag(key, omap.StrValue(base64.StdEncoding.EncodeToString(data))), nil
}

func (w *walker) array(depth, viewDepth int) (omap.Value, error) {
	n, err := w.decoder.DecodeArrayLen()
	if err != nil || n < 0 || n > w.reader.Len() {
		return omap.Value{}, errWire
	}
	// Minimum one byte and one node per child, before the length-driven slice.
	if n > 0 && !w.decoded.add(depth+1, n, 0) {
		return omap.Value{}, errSemantic
	}
	if n > 0 && !w.view.add(viewDepth+1, n, 0) {
		return omap.Value{}, errView
	}
	values := make([]omap.Value, n)
	for i := range values {
		values[i], err = w.reservedValue(depth+1, viewDepth+1)
		if err != nil {
			return omap.Value{}, err
		}
	}
	return omap.SeqValue(values...), nil
}

func (w *walker) mapping(depth, viewDepth int) (omap.Value, error) {
	n, err := w.decoder.DecodeMapLen()
	if err != nil || n < 0 || n > w.reader.Len()/2 {
		return omap.Value{}, errWire
	}
	if n > 0 && (n > (nodeLimit-w.decoded.nodes)/2 || !w.decoded.add(depth+1, 2*n, 0)) {
		return omap.Value{}, errSemantic
	}
	// Root wrapper was charged by the caller; reserve its entries array,
	// pair arrays and all key/value root slots before allocating the pairs.
	if !w.view.add(viewDepth+1, 1, len("$map")) {
		return omap.Value{}, errView
	}
	if n > 0 && (n > (nodeLimit-w.view.nodes)/3 || !w.view.add(viewDepth+3, 3*n, 0)) {
		return omap.Value{}, errView
	}
	pairs := make([]omap.Value, n)
	for i := range pairs {
		key, e := w.reservedValue(depth+1, viewDepth+3)
		if e != nil {
			return omap.Value{}, e
		}
		val, e := w.reservedValue(depth+1, viewDepth+3)
		if e != nil {
			return omap.Value{}, e
		}
		pairs[i] = omap.SeqValue(key, val)
	}
	return tag("$map", omap.SeqValue(pairs...)), nil
}

func (w *walker) extension(depth int) (omap.Value, error) {
	id, n, err := w.decoder.DecodeExtHeader()
	if err != nil {
		return omap.Value{}, errWire
	}
	raw, err := w.payload(n)
	if err != nil {
		return omap.Value{}, err
	}
	if id == -1 {
		return w.timestamp(raw, depth)
	}
	encoded := base64.StdEncoding.EncodedLen(n)
	if !w.view.add(depth+2, 3, len("$ext")+encoded) {
		return omap.Value{}, errView
	}
	data, err := w.readPayload(n)
	if err != nil {
		return omap.Value{}, err
	}
	return tag("$ext", omap.SeqValue(omap.IntValue(int64(id)), omap.StrValue(base64.StdEncoding.EncodeToString(data)))), nil
}

func (w *walker) timestamp(raw []byte, depth int) (omap.Value, error) {
	var seconds int64
	var nanos uint32
	switch len(raw) {
	case 4:
		seconds = int64(binary.BigEndian.Uint32(raw))
	case 8:
		bits := binary.BigEndian.Uint64(raw)
		seconds = int64(bits & ((1 << 34) - 1))
		nanos = uint32(bits >> 34)
	case 12:
		nanos = binary.BigEndian.Uint32(raw)
		seconds = int64(binary.BigEndian.Uint64(raw[4:]))
	default:
		return omap.Value{}, errors.New("invalid MessagePack timestamp length")
	}
	if nanos >= 1_000_000_000 {
		return omap.Value{}, errors.New("invalid MessagePack timestamp nanoseconds")
	}
	text := strconv.FormatInt(seconds, 10)
	if !w.view.add(depth+2, 3, len("$time")+len(text)) {
		return omap.Value{}, errView
	}
	if _, err := w.readPayload(len(raw)); err != nil {
		return omap.Value{}, err
	}
	return tag("$time", omap.SeqValue(omap.StrValue(text), omap.IntValue(int64(nanos)))), nil
}
