package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"github.com/sdkks/nesdit/internal/display"
	"github.com/sdkks/nesdit/internal/format"
	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/natsinspect"
	"github.com/sdkks/nesdit/internal/omap"
)

const natsEventLimit = 16 << 20

type eventBuffer struct {
	bytes.Buffer
	ctx context.Context
}

func (b *eventBuffer) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > natsEventLimit-b.Len() {
		return 0, errors.New("encode: serialized event exceeds 16 MiB; try --nats-output jsonl for less whitespace")
	}
	return b.Buffer.Write(p)
}
func (b *eventBuffer) WriteString(s string) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(s) > natsEventLimit-b.Len() {
		return 0, errors.New("encode: serialized event exceeds 16 MiB")
	}
	return b.Buffer.WriteString(s)
}

func natsEvent(ctx context.Context, record natsinspect.Record, pretty, color bool) ([]byte, []display.Span, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(record.Data) > 10<<20 || record.HeaderBytes > 64<<10 || len(record.Subject) > 4096 || len(record.Reply) > 4096 {
		return nil, nil, errors.New("resource: admitted payload or metadata exceeds limit")
	}
	valid := json.Valid(record.Data)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !valid {
		return nil, nil, errors.New("decode: expected one complete JSON value")
	}
	payload, err := jsonfmt.DecodeValueWithLimits(bytes.NewReader(record.Data), format.DefaultLimits())
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if err != nil {
		return nil, nil, errors.New("decode: invalid JSON value or nesting limit exceeded")
	}
	headers := omap.New()
	keys := make([]string, 0, len(record.Headers))
	for key := range record.Headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := make([]omap.Value, 0, len(record.Headers[key]))
		for _, value := range record.Headers[key] {
			values = append(values, omap.StrValue(value))
		}
		headers.Set(key, omap.Value{Kind: omap.KindSeq, Seq: values})
	}
	envelope := omap.New()
	envelope.Set("subject", omap.StrValue(record.Subject))
	envelope.Set("headers", omap.MapValue(headers))
	envelope.Set("payload", payload)
	buf := &eventBuffer{ctx: ctx}
	var spans []display.Span
	var observe func(display.Span)
	if color {
		observe = func(s display.Span) { spans = append(spans, s) }
	}
	if err = jsonfmt.EncodeValueLayout(buf, omap.MapValue(envelope), pretty, observe); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		return nil, nil, errors.New("encode: invalid value or serialized event exceeds 16 MiB; try --nats-output jsonl")
	}
	if _, err = buf.WriteString("\n"); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), spans, nil
}

// A context check cannot revoke an arbitrary Write already in progress. Run's
// process-only watchdog provides that bound; Execute requires responsive sinks.
type natsWriter struct {
	ctx     context.Context
	w       io.Writer
	started bool
}

func (w *natsWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > 0 {
		w.started = true
	}
	n, err := w.w.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}
