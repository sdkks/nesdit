// Package run — dryrun.go implements the --dry-run (-n, FR-11) and
// --check (FR-12) flag logic.
//
// Separation of concerns:
//   - runDryRun: decode → query → encode, then emit a unified diff of
//     before/after to stdout; no file writes under any circumstance.
//   - runCheck: decode → query → encode, then compare the encoded result
//     to the re-encoded original (normalised form). Exit ExitDrift (2) if
//     they differ; ExitOK (0) if byte-identical; ExitError (1) on any
//     error. This is the ONLY source of ExitDrift in the codebase (DR-002).
//
// Both functions are single-file only — they accept one resolved path.
// The -i / multi-file paths delegate here when either flag is set,
// after DR-001 precedence resolution strips the -i intent.
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/sdkks/nesdit/internal/format"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/query"
)

// runDryRun implements FR-11 for a single file. It decodes the file,
// applies the query, encodes the result, and emits a unified diff of
// "before" (re-encoded original) vs "after" (encoded result) to stdout.
// No file writes occur regardless of whether the diff is empty or not.
// Exit code is always ExitOK on success (non-empty diff is informational).
//
// The "before" baseline is the re-encoded original — not the raw bytes on
// disk — so the diff reflects semantic changes only, not format noise from
// whitespace or trailing-newline differences that the encoder would
// normalise away.
func runDryRun(
	ctx context.Context,
	opts RunOptions,
	path, queryExpr, overrideFormat string,
	args []query.Arg,
	limits format.Limits,
	timeout time.Duration,
) error {
	fmtName := overrideFormat
	if fmtName == "" {
		fmtName = detectFormatByExt(path)
	}
	if fmtName == "" {
		msg := "cannot detect format (supported: json, yaml, yml, toml); use --format to override"
		opts.Logger.Error(logx.EventFormatUnknown, path, msg)
		return &emittedError{cause: fmt.Errorf("%s", msg)}
	}

	f, err := os.Open(path) //nolint:gosec // user-supplied path by design
	if err != nil {
		opts.Logger.Error(logx.EventIORead, path, err.Error())
		return &emittedError{cause: err}
	}
	origBytes, readErr := format.ReadAllLimited(f, limits.MaxBytes, fmtName)
	_ = f.Close()
	if readErr != nil {
		opts.Logger.Error(classifyDecodeErr(readErr), path, readErr.Error())
		return &emittedError{cause: readErr}
	}

	val, decErr := decodeFormatValueWithLimits(fmtName, bytes.NewReader(origBytes), limits)
	if decErr != nil {
		opts.Logger.Error(classifyDecodeErr(decErr), path, decErr.Error())
		return &emittedError{cause: decErr}
	}

	// Re-encode original without query for the diff "before" baseline.
	var beforeBuf bytes.Buffer
	if reencErr := encodeFormatValue(fmtName, &beforeBuf, val); reencErr != nil {
		opts.Logger.Error(logx.EventEncodeError, path, reencErr.Error())
		return &emittedError{cause: reencErr}
	}

	// Apply --timeout to the query phase only.
	queryCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	outVal, qErr := query.RunValueWithArgs(queryCtx, val, queryExpr, args)
	if qErr != nil {
		opts.Logger.Error(classifyQueryErr(queryCtx, qErr, timeout), path, qErr.Error())
		return &emittedError{cause: qErr}
	}

	// Encode the query result for the diff "after".
	var afterBuf bytes.Buffer
	if encErr := encodeFormatValue(fmtName, &afterBuf, outVal); encErr != nil {
		var encodeErr *omap.EncodeError
		if errors.As(encErr, &encodeErr) {
			opts.Logger.Error(logx.EventFormatIncompatible, path, encErr.Error())
		} else {
			opts.Logger.Error(logx.EventEncodeError, path, encErr.Error())
		}
		return &emittedError{cause: encErr}
	}

	// Emit unified diff to stdout. The diff is informational — an empty
	// diff (no change) is fine and still exits 0.
	//
	// Sanitise embedded newlines in the path before placing it in the
	// diff header fields (FR-11 / TASK-0025): a literal '\n' in a
	// filename would split the "--- path" header line, producing a
	// malformed diff.
	safeHeader := strings.ReplaceAll(path, "\n", `\n`)
	diff := difflib.UnifiedDiff{
		A:        difflib.SplitLines(beforeBuf.String()),
		B:        difflib.SplitLines(afterBuf.String()),
		FromFile: safeHeader,
		ToFile:   safeHeader,
		Context:  3,
	}
	diffText, diffErr := difflib.GetUnifiedDiffString(diff)
	if diffErr != nil {
		opts.Logger.Error(logx.EventEncodeError, path, diffErr.Error())
		return &emittedError{cause: diffErr}
	}
	if _, writeErr := fmt.Fprint(opts.Stdout, diffText); writeErr != nil {
		opts.Logger.Error(logx.EventIOWrite, path, writeErr.Error())
		return &emittedError{cause: writeErr}
	}
	return nil
}

// runCheck implements FR-12 for a single file. It decodes, applies the
// query, and re-encodes both the original and the result. It then compares
// the two encoded forms:
//   - byte-identical → return ExitOK (0)
//   - different      → return a driftError (caller returns ExitDrift = 2)
//   - any other err  → return emittedError (caller returns ExitError = 1)
//
// DR-002: this function is the ONLY place in the codebase that may produce
// a driftError, which is the only route to ExitDrift. All other error paths
// return emittedError (exit 1) or nil (exit 0).
func runCheck(
	ctx context.Context,
	opts RunOptions,
	path, queryExpr, overrideFormat string,
	args []query.Arg,
	limits format.Limits,
	timeout time.Duration,
) error {
	fmtName := overrideFormat
	if fmtName == "" {
		fmtName = detectFormatByExt(path)
	}
	if fmtName == "" {
		msg := "cannot detect format (supported: json, yaml, yml, toml); use --format to override"
		opts.Logger.Error(logx.EventFormatUnknown, path, msg)
		return &emittedError{cause: fmt.Errorf("%s", msg)}
	}

	f, err := os.Open(path) //nolint:gosec // user-supplied path by design
	if err != nil {
		opts.Logger.Error(logx.EventIORead, path, err.Error())
		return &emittedError{cause: err}
	}
	origBytes, readErr := format.ReadAllLimited(f, limits.MaxBytes, fmtName)
	_ = f.Close()
	if readErr != nil {
		opts.Logger.Error(classifyDecodeErr(readErr), path, readErr.Error())
		return &emittedError{cause: readErr}
	}

	val, decErr := decodeFormatValueWithLimits(fmtName, bytes.NewReader(origBytes), limits)
	if decErr != nil {
		opts.Logger.Error(classifyDecodeErr(decErr), path, decErr.Error())
		return &emittedError{cause: decErr}
	}

	// Re-encode original for the baseline (normalised form).
	var beforeBuf bytes.Buffer
	if reencErr := encodeFormatValue(fmtName, &beforeBuf, val); reencErr != nil {
		opts.Logger.Error(logx.EventEncodeError, path, reencErr.Error())
		return &emittedError{cause: reencErr}
	}

	// Apply --timeout to the query phase only.
	queryCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	outVal, qErr := query.RunValueWithArgs(queryCtx, val, queryExpr, args)
	if qErr != nil {
		opts.Logger.Error(classifyQueryErr(queryCtx, qErr, timeout), path, qErr.Error())
		return &emittedError{cause: qErr}
	}

	// Encode the query result.
	var afterBuf bytes.Buffer
	if encErr := encodeFormatValue(fmtName, &afterBuf, outVal); encErr != nil {
		var encodeErr *omap.EncodeError
		if errors.As(encErr, &encodeErr) {
			opts.Logger.Error(logx.EventFormatIncompatible, path, encErr.Error())
		} else {
			opts.Logger.Error(logx.EventEncodeError, path, encErr.Error())
		}
		return &emittedError{cause: encErr}
	}

	// Compare. If they differ: drift → driftError (exit 2).
	if !bytes.Equal(beforeBuf.Bytes(), afterBuf.Bytes()) {
		return &driftError{}
	}
	return nil
}

// driftError is the sentinel returned by runCheck when the query would
// change the input. Execute recognises it and returns ExitDrift (2).
// This is the ONLY value that causes Execute to return 2 — no other
// code path may construct or return a driftError.
type driftError struct{}

func (e *driftError) Error() string { return "check: drift detected" }
