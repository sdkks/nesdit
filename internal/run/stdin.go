package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/sdkks/nesdit/internal/format"
	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
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
// Default (strict) behaviour: on the first document failure, runStdin halts
// with a non-zero exit via emittedError. Earlier documents already written to
// stdout are documented NFR-8 behavior (single-pass, not transactional).
//
// With keepGoing=true (FR-17 / STORY-0013 --keep-going): a query or encode
// error on one document is logged and processing continues to the next
// document. The errored document is NOT written to stdout. Exit code is
// non-zero at the end if any errors occurred.
//
// wherePredicate (FR-10 / STORY-0009): when non-empty, each document that does
// not satisfy select(<wherePredicate>) is written to stdout unchanged (passed
// through). The query is only applied to matching documents.
//
// fmtName is the effective input format string after auto-detection. It must
// be one of "json", "yaml", "jsonl", or "toml". "json" routes to
// runStdinJSON for single-document buffered decode. "toml" is single-document only;
// a multi-doc TOML input produces ErrTOMLMultiDoc (format.unsupported).
//
// outputFmtOverride (STORY-0015): when non-empty, each document is encoded in
// the specified output format instead of the input format. Pass-through docs
// (--where predicate mismatch) are still encoded in the input format so they
// are byte-identical to the input.
//
// timeout (when > 0) wraps ctx with a per-document deadline for the query
// phase only (consistent with runOnce).
//
// STORY-0018: pretty enables human-friendly TOML output when the effective
// output format is "toml". Silently ignored for other output formats.
func runStdin(ctx context.Context, opts RunOptions, fmtName, outputFmtOverride, queryExpr, wherePredicate string, args []query.Arg, limits format.Limits, timeout time.Duration, keepGoing, createMissing bool, yamlVersion string, pretty bool) error {
	// --format json on STDIN: the user explicitly requested single-document
	// JSON. Read the entire stdin into a buffer and decode as one document,
	// the same way runOnce handles a JSON file. This supports multi-line JSON
	// objects that a line-by-line JSONL reader would split incorrectly.
	if fmtName == "json" {
		return runStdinJSON(ctx, opts, outputFmtOverride, queryExpr, wherePredicate, args, limits, timeout, createMissing, keepGoing, pretty)
	}

	// Wire the reader (always in input format). FR-18: pass yamlVersion via
	// DecodeOpts; silently ignored for non-YAML formats.
	reader, err := stream.NewReaderWithOpts(fmtName, opts.Stdin, limits, yamlfmt.DecodeOpts{YAMLVersion: yamlVersion})
	if err != nil {
		opts.Logger.ErrorGlobal(logx.EventFormatUnsupported, err.Error())
		return &emittedError{cause: err}
	}
	// STORY-0015: resolve effective output format. Defaults to the input format.
	// Normalize "json" to "jsonl" for stdin streaming, consistent with how
	// the input format is normalised in stdinFormatName.
	var outFmtName string
	switch outputFmtOverride {
	case "":
		outFmtName = fmtName
	case "json":
		// Normalize "json" to "jsonl" for stdin streaming.
		outFmtName = "jsonl"
	default:
		outFmtName = outputFmtOverride
	}
	// Wire the writer (in effective output format).
	// STORY-0018: when pretty is set and the output format is toml, use the
	// pretty TOML writer. For all other formats, use the standard writer.
	var writer stream.DocWriter
	if pretty && outFmtName == "toml" {
		writer = stream.NewTOMLWriterWithOptions(opts.Stdout, tomlfmt.EncodeOptions{Pretty: true})
	} else {
		var werr error
		writer, werr = stream.NewWriter(outFmtName, opts.Stdout)
		if werr != nil {
			opts.Logger.ErrorGlobal(logx.EventFormatUnsupported, werr.Error())
			return &emittedError{cause: werr}
		}
	}
	// Wire a pass-through writer (always in input format) for --where
	// unmatched documents. Pass-through docs are emitted "unchanged" in
	// the input format even when --output-format specifies a different format.
	// Only constructed when --where is active, since it is never called otherwise.
	var passthroughWriter stream.DocWriter
	if wherePredicate != "" {
		var ptErr error
		passthroughWriter, ptErr = stream.NewWriter(fmtName, opts.Stdout)
		if ptErr != nil {
			opts.Logger.ErrorGlobal(logx.EventFormatUnsupported, ptErr.Error())
			return &emittedError{cause: ptErr}
		}
	}

	docIndex := 0
	hadError := false
	for reader.Next() {
		docIndex++
		val := reader.Value()

		// --where (FR-10 / STORY-0009): test the predicate before running the
		// query. Documents that do not match are written to stdout unchanged
		// (pass-through in input format). A where predicate error halts the stream
		// (even under --keep-going: this is a misconfiguration, not a per-doc
		// data error).
		if wherePredicate != "" {
			match, whereErr := query.ApplyWhere(ctx, val, wherePredicate)
			if whereErr != nil {
				opts.Logger.ErrorAt(classifyQueryErr(ctx, whereErr, 0), stdinFilename, docIndex, whereErr.Error())
				return &emittedError{cause: whereErr}
			}
			if !match {
				opts.Logger.WarnAt(logx.EventWhereSkipped, stdinFilename, docIndex, "--where predicate did not match; doc passed through")
				// NFR-8 partial-write trade-off: passthroughWriter writes
				// directly to the streaming stdout. A write failure here
				// (e.g. broken pipe or, theoretically, an encode error if the
				// value could not be re-serialised in the input format) may
				// leave a partial document on stdout before the error is
				// detected — the same NFR-8 single-pass trade-off that applies
				// to the main write path. Under --keep-going the error is
				// logged, hadError is set, and processing continues; there is
				// no rollback of bytes already emitted. Callers must treat
				// stdout as incomplete whenever exit code is non-zero.
				if wErr := passthroughWriter.WriteDoc(val); wErr != nil {
					opts.Logger.ErrorAt(classifyEncodeErr(wErr), stdinFilename, docIndex, wErr.Error())
					if !keepGoing {
						return &emittedError{cause: wErr}
					}
					hadError = true
				}
				continue
			}
		}

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
			if !keepGoing {
				return &emittedError{cause: qErr}
			}
			// --keep-going (FR-17): log error, skip this doc, continue to next.
			// The errored document is NOT written to stdout.
			hadError = true
			continue
		}

		// FR-16 / STORY-0012: reject missing-path creation unless --create-missing.
		if !createMissing {
			if mpErr := query.CheckNoMissingPaths(val, outVal); mpErr != nil {
				opts.Logger.ErrorAt(logx.EventQueryMissingPath, stdinFilename, docIndex, mpErr.Error())
				if !keepGoing {
					return &emittedError{cause: mpErr}
				}
				hadError = true
				continue
			}
		}

		if wErr := writer.WriteDoc(outVal); wErr != nil {
			opts.Logger.ErrorAt(classifyEncodeErr(wErr), stdinFilename, docIndex, wErr.Error())
			if !keepGoing {
				return &emittedError{cause: wErr}
			}
			hadError = true
			continue
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
		// A stream-level error halts even under --keep-going: once the stream
		// decoder has failed, the remaining document boundaries are unknown.
		return &emittedError{cause: rErr}
	}

	// FR-17: if any per-document errors were encountered under --keep-going,
	// exit 1 at end of run (non-zero, even though processing continued).
	if hadError {
		return &emittedError{cause: errors.New("one or more documents failed processing")}
	}

	return nil
}

// runStdinJSON handles --format json on STDIN as a single-document decode,
// matching the behaviour of runOnce for JSON files. The entire stdin is
// buffered and decoded as one JSON value; the result is then queried and
// written to stdout. This correctly handles multi-line JSON objects that a
// line-by-line JSONL reader would otherwise split on the first newline.
//
// keepGoing mirrors the FR-17 semantics from runStdin: when true and an error
// is an *emittedError, return nil instead of propagating. Since this function
// is single-document only, keepGoing does not continue iteration — it only
// determines whether a per-document error is suppressed (exit 0) or surfaced
// (exit 1). Where-predicate errors are configuration errors and are never
// suppressed regardless of keepGoing.
//
// STORY-0018: pretty enables human-friendly TOML output. Silently ignored
// for non-TOML output formats.
func runStdinJSON(ctx context.Context, opts RunOptions, outputFmtOverride, queryExpr, wherePredicate string, args []query.Arg, limits format.Limits, timeout time.Duration, createMissing, keepGoing, pretty bool) error {
	data, err := io.ReadAll(opts.Stdin)
	if err != nil {
		opts.Logger.Error(logx.EventIORead, stdinFilename, err.Error())
		return &emittedError{cause: err}
	}

	val, err := decodeFormatValueWithLimitsAndVersion("json", bytes.NewReader(data), limits, "")
	if err != nil {
		opts.Logger.Error(classifyDecodeErr(err), stdinFilename, err.Error())
		if keepGoing {
			return nil
		}
		return &emittedError{cause: err}
	}

	// Resolve effective output format. Defaults to "json". stream.NewWriter
	// accepts "json" as an alias for the JSONL (newline-terminated) writer.
	outFmtName := outputFmtOverride
	if outFmtName == "" {
		outFmtName = "json"
	}
	// STORY-0018: when pretty is set and the output format is toml, use the
	// pretty TOML writer. For all other formats, use the standard writer.
	var writer stream.DocWriter
	if pretty && outFmtName == "toml" {
		writer = stream.NewTOMLWriterWithOptions(opts.Stdout, tomlfmt.EncodeOptions{Pretty: true})
	} else {
		var werr error
		writer, werr = stream.NewWriter(outFmtName, opts.Stdout)
		if werr != nil {
			opts.Logger.ErrorGlobal(logx.EventFormatUnsupported, werr.Error())
			return &emittedError{cause: werr}
		}
	}

	// --where: test the predicate. Documents that do not match are written
	// unchanged (pass-through). A where predicate error halts even under
	// --keep-going (misconfiguration, not a per-doc data error).
	if wherePredicate != "" {
		match, whereErr := query.ApplyWhere(ctx, val, wherePredicate)
		if whereErr != nil {
			opts.Logger.ErrorAt(classifyQueryErr(ctx, whereErr, 0), stdinFilename, 1, whereErr.Error())
			return &emittedError{cause: whereErr}
		}
		if !match {
			opts.Logger.WarnAt(logx.EventWhereSkipped, stdinFilename, 1, "--where predicate did not match; doc passed through")
			passthroughWriter, ptErr := stream.NewWriter("json", opts.Stdout)
			if ptErr != nil {
				opts.Logger.ErrorGlobal(logx.EventFormatUnsupported, ptErr.Error())
				return &emittedError{cause: ptErr}
			}
			if wErr := passthroughWriter.WriteDoc(val); wErr != nil {
				opts.Logger.ErrorAt(classifyEncodeErr(wErr), stdinFilename, 1, wErr.Error())
				return &emittedError{cause: wErr}
			}
			return nil
		}
	}

	queryCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	outVal, qErr := query.RunValueWithArgs(queryCtx, val, queryExpr, args)
	if qErr != nil {
		opts.Logger.ErrorAt(classifyQueryErr(queryCtx, qErr, timeout), stdinFilename, 1, qErr.Error())
		if keepGoing {
			return nil
		}
		return &emittedError{cause: qErr}
	}

	if !createMissing {
		if mpErr := query.CheckNoMissingPaths(val, outVal); mpErr != nil {
			opts.Logger.ErrorAt(logx.EventQueryMissingPath, stdinFilename, 1, mpErr.Error())
			if keepGoing {
				return nil
			}
			return &emittedError{cause: mpErr}
		}
	}

	if wErr := writer.WriteDoc(outVal); wErr != nil {
		opts.Logger.ErrorAt(classifyEncodeErr(wErr), stdinFilename, 1, wErr.Error())
		if keepGoing {
			return nil
		}
		return &emittedError{cause: wErr}
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
//   - If overrideFormat is non-empty, it is used directly. "--format json" is
//     passed through as "json" so runStdin dispatches to runStdinJSON for
//     single-document buffered decode. All other explicit overrides are used as-is.
//   - Otherwise, format.Detect is called to peek at stdin. The peeked reader
//     replaces opts.Stdin so the full content is still available downstream.
//     When auto-detect returns "json" (the input looks like a single-document
//     JSON value, not a newline-delimited stream), it is returned as-is so
//     runStdin dispatches to runStdinJSON automatically.
//
// Returns the resolved format name and the (possibly replaced) opts, or an
// error when detection is inconclusive.
func stdinFormatName(opts RunOptions, overrideFormat string) (string, RunOptions, error) {
	if overrideFormat != "" {
		// Pass --format json through as "json" so runStdin can take the
		// single-document decode path (runStdinJSON). All other explicit
		// overrides are used as-is.
		return overrideFormat, opts, nil
	}
	// Auto-detect by peeking.
	detected, combined := format.Detect(opts.Stdin)
	opts.Stdin = combined
	if detected == "" {
		return "", opts, fmt.Errorf("cannot detect format from stdin; use --format to specify (json, jsonl, yaml, toml)")
	}
	// When auto-detect returns "json", runStdin's `if fmtName == "json"` guard
	// already dispatches to runStdinJSON for single-document buffered decode.
	// No normalisation to "jsonl" is needed here.
	return detected, opts, nil
}
