package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/sdkks/nesdit/internal/display"
	"github.com/sdkks/nesdit/internal/format"
	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/natsinspect"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/query"
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
	return natsQueriedEvent(ctx, record, pretty, color, nil, nil)
}

func natsQueriedEvent(ctx context.Context, record natsinspect.Record, pretty, color bool, filter, project *query.Inspection) ([]byte, []display.Span, error) {
	return natsDecodedEvent(ctx, record, pretty, color, filter, project, nil)
}

// A nil decoder preserves the JSON path, including its ordering and limits.
func natsDecodedEvent(ctx context.Context, record natsinspect.Record, pretty, color bool, filter, project *query.Inspection, decode func(context.Context, []byte) (omap.Value, error)) ([]byte, []display.Span, error) {
	return natsDecodedEventPreflight(ctx, record, pretty, color, filter, project, decode, false)
}

// Only new text views opt into exact size admission of the complete envelope.
func natsDecodedEventPreflight(ctx context.Context, record natsinspect.Record, pretty, color bool, filter, project *query.Inspection, decode func(context.Context, []byte) (omap.Value, error), preflight bool) ([]byte, []display.Span, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(record.Data) > 10<<20 || record.HeaderBytes > 64<<10 || len(record.Subject) > 4096 || len(record.Reply) > 4096 {
		return nil, nil, errors.New("resource: admitted payload or metadata exceeds limit")
	}
	var payload omap.Value
	var err error
	if decode != nil {
		payload, err = decode(ctx, record.Data)
		if err != nil {
			return nil, nil, err
		}
	} else {
		valid := json.Valid(record.Data)
		if !valid {
			return nil, nil, errors.New("decode: expected one complete JSON value")
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		payload, err = jsonfmt.DecodeValueWithLimits(bytes.NewReader(record.Data), format.DefaultLimits())
		if err != nil {
			return nil, nil, errors.New("decode: invalid JSON value or nesting limit exceeded")
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
	}
	if filter != nil {
		match, err := filter.Predicate(ctx, payload)
		if err != nil {
			return nil, nil, fmt.Errorf("filter: %w", err)
		}
		if !match {
			return nil, nil, nil
		}
	}
	if project != nil {
		payload, err = project.Project(ctx, payload)
		if err != nil {
			return nil, nil, fmt.Errorf("project: %w", err)
		}
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
	if record.History != nil {
		envelope.Set("stream", omap.StrValue(record.History.Stream))
		envelope.Set("sequence", omap.StrValue(strconv.FormatUint(record.History.Sequence, 10)))
		envelope.Set("stored_time", omap.StrValue(record.History.StoredTime.UTC().Format(time.RFC3339Nano)))
	}
	envelope.Set("subject", omap.StrValue(record.Subject))
	envelope.Set("headers", omap.MapValue(headers))
	envelope.Set("payload", payload)
	if preflight {
		data, spans, err := inspectionJSON(ctx, omap.MapValue(envelope), pretty, color)
		if err != nil {
			return nil, nil, fmt.Errorf("encode: %w", err)
		}
		return data, spans, nil
	}
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
