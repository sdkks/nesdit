package msgpackview

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"

	"github.com/sdkks/nesdit/internal/omap"
	"github.com/vmihailenco/msgpack/v5"
)

const outputLimit = 16 << 20

// Encode validates the complete selected value before staging any wire bytes.
// The caller owns the final write and must provide a safe nonterminal sink.
func Encode(ctx context.Context, value omap.Value, profile Interpretation) ([]byte, error) {
	if err := validateTarget(ctx, value, profile); err != nil {
		return nil, err
	}
	buf := &binaryBuffer{ctx: ctx, limit: outputLimit}
	e := wireEncoder{ctx: ctx, encoder: msgpack.NewEncoder(buf), sink: buf, profile: profile}
	// NewEncoder has compaction disabled. Explicitly keep Float values Float64,
	// even when integral; integer methods below intentionally choose minimal size.
	e.encoder.UseCompactFloats(false)
	if err := e.value(value); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return buf.data.Bytes(), nil
}

// Only Write is exposed: embedding bytes.Buffer would promote WriteByte and
// let the dependency's one-byte scalar/header writes bypass these guards.
type binaryBuffer struct {
	data  bytes.Buffer
	ctx   context.Context
	limit int
}

func (b *binaryBuffer) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > b.limit-b.data.Len() {
		return 0, errors.New("serialized MessagePack exceeds output limit (16 MiB)")
	}
	return b.data.Write(p)
}

type wireEncoder struct {
	ctx     context.Context
	encoder *msgpack.Encoder
	sink    *binaryBuffer
	profile Interpretation
}

func (e *wireEncoder) value(v omap.Value) error {
	if err := e.ctx.Err(); err != nil {
		return err
	}
	switch v.Kind {
	case omap.KindNull:
		return e.encoder.EncodeNil()
	case omap.KindBool:
		return e.encoder.EncodeBool(v.Bool)
	case omap.KindStr:
		return e.encoder.EncodeString(v.Str)
	case omap.KindSeq:
		if err := e.encoder.EncodeArrayLen(len(v.Seq)); err != nil {
			return err
		}
		for _, child := range v.Seq {
			if err := e.value(child); err != nil {
				return err
			}
		}
		return nil
	case omap.KindNum:
		key, text, err := ordinaryNumber(v.Num.String())
		if err != nil {
			return err
		}
		return e.number(key, text)
	case omap.KindMap:
		if e.profile == OrdinaryJSON {
			if err := e.encoder.EncodeMapLen(v.Map.Len()); err != nil {
				return err
			}
			for key, child := range v.Map.Entries {
				if err := e.encoder.EncodeString(key); err != nil {
					return err
				}
				if err := e.value(child); err != nil {
					return err
				}
			}
			return nil
		}
		key, arg := v.Map.At(0) // Full target validation established shape and types.
		switch key {
		case "$int", "$float":
			return e.number(key, arg.Str)
		case "$bin", "$str64", "$ext":
			var id int64
			text := arg.Str
			if key == "$ext" {
				id, _ = smallInteger(arg.Seq[0])
				text = arg.Seq[1].Str
			}
			data, err := base64.StdEncoding.Strict().DecodeString(text)
			if err != nil {
				return errProfile
			}
			if key == "$str64" {
				return e.encoder.EncodeString(string(data))
			}
			if key == "$bin" {
				err = e.encoder.EncodeBytesLen(len(data))
			} else {
				err = e.encoder.EncodeExtHeader(int8(id), len(data))
			}
			if err != nil {
				return err
			}
			_, err = e.sink.Write(data)
			return err
		case "$time":
			sec, nanos, err := timestampParts(arg)
			if err != nil {
				return err
			}
			return e.timestamp(sec, nanos)
		case "$map":
			if err := e.encoder.EncodeMapLen(len(arg.Seq)); err != nil {
				return err
			}
			for _, pair := range arg.Seq {
				for _, child := range pair.Seq {
					if err := e.value(child); err != nil {
						return err
					}
				}
			}
			return nil
		}
	}
	return errProfile
}
func (e *wireEncoder) number(key, text string) error {
	if key == "$float" {
		f, err := floatNumber(text, true)
		if err != nil {
			return err
		}
		return e.encoder.EncodeFloat64(f)
	}
	negative, signed, unsigned, err := integer(text)
	if err != nil {
		return err
	}
	if negative {
		return e.encoder.EncodeInt(signed)
	}
	return e.encoder.EncodeUint(unsigned)
}
func (e *wireEncoder) timestamp(sec int64, nanos uint32) error {
	var data [12]byte
	n := 12
	switch {
	case nanos == 0 && sec >= 0 && sec <= math.MaxUint32:
		n = 4
		binary.BigEndian.PutUint32(data[:4], uint32(sec))
	case sec >= 0 && sec < 1<<34:
		n = 8
		binary.BigEndian.PutUint64(data[:8], uint64(nanos)<<34|uint64(sec))
	default:
		binary.BigEndian.PutUint32(data[:4], nanos)
		binary.BigEndian.PutUint64(data[4:], uint64(sec))
	}
	if err := e.encoder.EncodeExtHeader(-1, n); err != nil {
		return err
	}
	_, err := e.sink.Write(data[:n])
	return err
}
