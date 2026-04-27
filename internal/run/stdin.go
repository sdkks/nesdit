package run

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sdkks/nesdit/internal/format"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/query"
	"github.com/sdkks/nesdit/internal/stream"
)

// stdinFilename is the canonical file slot used in logx lines when the input
// source is STDIN. NFR-10 mandates `-` for STDIN, not "stdin" or "".
const stdinFilename = "-"

// runStdin is the NFR-8 single-pass STDIN pipeline. It reads one document at
// a time via a stream.DocReader, applies the query, and writes to stdout.
//
// On the first document failure, runStdin halts with a non-zero exit via
// emittedError. Earlier documents already written to stdout are documented
// NFR-8 behavior (single-pass, not transactional).
//
// fmtName is the effective format string after auto-detection. It must be
// one of "yaml", "jsonl", or "toml". "toml" is single-document only;
// a multi-doc TOML input produces ErrTOMLMultiDoc (format.unsupported).
//
// timeout (when > 0) wraps ctx with a per-document deadline for the query
// phase only (consistent with runOnce).
func runStdin(ctx context.Context, opts RunOptions, fmtName, queryExpr string, args []query.Arg, limits format.Limits, timeout time.Duration) error {
	// Wire the reader.
	reader, err := stream.NewReader(fmtName, opts.Stdin, limits)
	if err != nil {
		opts.Logger.ErrorGlobal(logx.EventFormatUnsupported, err.Error())
		return &emittedError{cause: err}
	}
	// Wire the writer.
	writer, err := stream.NewWriter(fmtName, opts.Stdout)
	if err != nil {
		opts.Logger.ErrorGlobal(logx.EventFormatUnsupported, err.Error())
		return &emittedError{cause: err}
	}

	docIndex := 0
	for reader.Next() {
		docIndex++
		val := reader.Value()

		// Apply --timeout to the query phase per doc (consistent with runOnce).
		queryCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			queryCtx, cancel = context.WithTimeout(ctx, timeout)
		}

		outVal, qErr := query.RunValueWithArgs(queryCtx, val, queryExpr, args)
		if cancel != nil {
			cancel()
		}
		if qErr != nil {
			opts.Logger.ErrorAt(classifyQueryErr(queryCtx, qErr, timeout), stdinFilename, docIndex, qErr.Error())
			return &emittedError{cause: qErr}
		}

		if wErr := writer.WriteDoc(outVal); wErr != nil {
			opts.Logger.ErrorAt(classifyEncodeErr(wErr), stdinFilename, docIndex, wErr.Error())
			return &emittedError{cause: wErr}
		}
	}

	// Check for a decode/stream error AFTER the loop. (For TOML multi-doc,
	// ErrTOMLMultiDoc surfaces here.)
	if rErr := reader.Err(); rErr != nil {
		var event logx.Event
		if errors.Is(rErr, stream.ErrTOMLMultiDoc) {
			event = logx.EventFormatUnsupported
		} else {
			event = classifyDecodeErr(rErr)
		}
		// Use index of the doc we were attempting to read (docIndex+1 if we
		// hadn't started, but for the TOML case we failed before any doc).
		if docIndex == 0 {
			opts.Logger.Error(event, stdinFilename, rErr.Error())
		} else {
			opts.Logger.ErrorAt(event, stdinFilename, docIndex+1, rErr.Error())
		}
		return &emittedError{cause: rErr}
	}

	return nil
}

// classifyEncodeErr maps an encode-phase error to the right logx event.
// Mirrors the logic in runOnce.
func classifyEncodeErr(err error) logx.Event {
	var encErr *omap.EncodeError
	if errors.As(err, &encErr) {
		return logx.EventFormatIncompatible
	}
	return logx.EventEncodeError
}

// stdinFormatName resolves the effective format for STDIN mode.
//
//   - If overrideFormat is non-empty, it is used directly (after mapping
//     "json" to "jsonl" for STDIN, since file JSON is single-doc but STDIN
//     JSON is streamed line-by-line for JSONL — caller must pass "jsonl"
//     explicitly or use auto-detect).
//   - Otherwise, format.Detect is called to peek at stdin. The peeked reader
//     replaces opts.Stdin so the full content is still available downstream.
//
// Returns the resolved format name and the (possibly replaced) opts, or an
// error when detection is inconclusive.
func stdinFormatName(opts RunOptions, overrideFormat string) (string, RunOptions, error) {
	if overrideFormat != "" {
		// Normalize "json" to "jsonl" for stdin streaming when user passes --format json.
		// Single-object JSON on stdin is handled as a 1-doc JSONL stream.
		if overrideFormat == "json" {
			overrideFormat = "jsonl"
		}
		return overrideFormat, opts, nil
	}
	// Auto-detect by peeking.
	detected, combined := format.Detect(opts.Stdin)
	opts.Stdin = combined
	if detected == "" {
		return "", opts, fmt.Errorf("cannot detect format from stdin; use --format to specify (json, jsonl, yaml, toml)")
	}
	// Normalize "json" to "jsonl" for stdin: all stdin JSON is streamed
	// as newline-separated documents (one per logical value). A single-
	// object stdin is handled as a 1-doc JSONL stream.
	if detected == "json" {
		detected = "jsonl"
	}
	return detected, opts, nil
}
