package protobufview

import (
	"bytes"
	"context"
	"errors"
	"sort"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/sdkks/nesdit/internal/format"
	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/omap"
)

func (d *decoder) decode(ctx context.Context, body []byte) (omap.Value, error) {
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	if len(body) > RawLimit {
		return omap.Value{}, errors.New("decode: raw body exceeds 10 MiB")
	}
	m := d.message.New()
	err := (proto.UnmarshalOptions{Resolver: d.types, RecursionLimit: depthLimit + 1, DiscardUnknown: true}).Unmarshal(body, m.Interface())
	if err != nil {
		return omap.Value{}, errors.New("decode: malformed wire data, missing required fields, or recursion limit")
	}
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	p := newPreflight(ctx, d)
	if err := p.decodedMessage(m, 0); err != nil {
		return omap.Value{}, err
	}
	if err := p.viewMessage(m, 0, ""); err != nil {
		return omap.Value{}, err
	}
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	// Official marshaling repeats Any decoding. These calls are synchronous,
	// not preemptible; the certificate is not a process-heap guarantee.
	data, err := (protojson.MarshalOptions{Resolver: d.types}).MarshalAppend(make([]byte, 0, p.certificate), m.Interface())
	if err != nil {
		return omap.Value{}, errors.New("marshal: invalid ProtoJSON value")
	}
	if err := checkSerialized(len(data), p.certificate); err != nil {
		return omap.Value{}, err
	}
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	// This bridge counts containers starting at one; the subsequent value walk
	// enforces root-zero depth for scalars and containers alike.
	v, err := jsonfmt.DecodeValueWithLimits(bytes.NewReader(data), format.Limits{MaxBytes: serializedLimit, MaxDepth: depthLimit + 1})
	if err != nil {
		return omap.Value{}, errors.New("view: invalid serialized JSON or nesting limit")
	}
	b := budget{stage: "view"}
	return sortView(ctx, v, 0, &b)
}
func checkSerialized(size, certificate int) error {
	if size > certificate || size > serializedLimit {
		return errors.New("marshal: serialized size exceeds preflight certificate or 16 MiB")
	}
	return nil
}

// Validate the actual JSON independently, then impose product key ordering.
// Array order is never changed, and query projections are outside this walk.
func sortView(ctx context.Context, v omap.Value, depth int, b *budget) (omap.Value, error) {
	if err := b.node(ctx, depth); err != nil {
		return omap.Value{}, err
	}
	switch v.Kind {
	case omap.KindStr:
		if err := b.addText(len(v.Str)); err != nil {
			return omap.Value{}, err
		}
	case omap.KindSeq:
		for i, child := range v.Seq {
			sorted, err := sortView(ctx, child, depth+1, b)
			if err != nil {
				return omap.Value{}, err
			}
			v.Seq[i] = sorted
		}
	case omap.KindMap:
		keys := v.Map.Keys()
		sort.Strings(keys)
		out := omap.New()
		for _, key := range keys {
			if err := b.addText(len(key)); err != nil {
				return omap.Value{}, err
			}
			child, _ := v.Map.Get(key)
			sorted, err := sortView(ctx, child, depth+1, b)
			if err != nil {
				return omap.Value{}, err
			}
			out.Set(key, sorted)
		}
		v.Map = out
	}
	return v, nil
}
