// Package run — files.go implements the two-pass file/glob orchestrator
// for the -i (in-place) mode (STORY-0004 / NFR-7).
//
// Pass structure (NFR-7):
//  1. Detect mixed formats (FR-5): reject before any IO side-effect.
//  2. Decode all inputs.
//  3. Apply the jq query to every decoded value.
//  4. Encode all results (catching encode failures).
//  5. Validate: if any encode step failed, abort before any write.
//  6. Write atomically (per-file temp+rename).
//  7. Emit batch summary (FR-6).
//
// Non-transactional write behavior (NFR-9): each file's write is atomic
// but the batch is not. A write-time IO failure (disk full, permission)
// may leave earlier files already written and later files at their original
// bytes. The batch summary reflects what actually landed.
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sdkks/nesdit/internal/format"
	nesio "github.com/sdkks/nesdit/internal/io"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/query"
)

// fileResult holds the per-file processing outcome from the two pre-write
// passes (decode+query+encode). It is the unit of work carried between
// passes so no state is re-computed during write.
type fileResult struct {
	path      string // original path as supplied by the caller
	reencoded []byte // re-encoded original bytes (decode+encode without query; for unchanged detection)
	encoded   []byte // encoded output bytes (nil if encode failed or where-skipped)
	encErr    error  // non-nil if encode or query failed
	skipped   bool   // true when --where predicate did not match; file is not written
}

// runFiles is the in-place (-i) orchestrator. Callers pass the already-
// expanded list of file paths (glob expansion is the caller's responsibility).
//
// Mixed-format detection (FR-5) happens before any file is opened, using
// only extension-based detection on the path names. This ensures the
// rejection is a pure metadata operation with no IO side-effects.
//
// wherePredicate (FR-10 / STORY-0009): when non-empty, each file is decoded
// and tested against select(<wherePredicate>) before the query is applied.
// Files that do not satisfy the predicate are logged as where.skipped and
// counted in the batch summary as skipped (excluded from changed/unchanged).
//
// keepGoing (FR-17 / STORY-0013 --keep-going): when true, errored files are
// counted in the batch summary K errored slot and processing continues to the
// next file rather than aborting. No files are written for the batch until
// after all files are processed; errored files are excluded from the write
// phase. Exit code is 1 if any errors occurred.
//
// When backupSuffix is non-empty (--backup flag, FR-14), each file that will
// be changed is backed up to <path><suffix> BEFORE the atomic rename step.
// Unchanged files are not backed up.
func runFiles(
	ctx context.Context,
	opts RunOptions,
	paths []string,
	queryExpr string,
	wherePredicate string,
	overrideFormat string,
	args []query.Arg,
	limits format.Limits,
	timeout time.Duration,
	keepGoing bool,
	backupSuffix string,
	createMissing bool,
) error {
	// Deduplicate paths so two symlinks pointing to the same real file
	// do not cause double writes.
	paths = nesio.DeduplicatePaths(paths)

	// --- FR-5: mixed-format detection (before any file open) ---
	if err := detectMixedFormats(opts, paths, overrideFormat); err != nil {
		return err
	}

	// --- Passes 1-3: decode, query, encode all files ---
	results := make([]fileResult, len(paths))
	hasEncodeFailure := false

	for i, p := range paths {
		res := processOneFile(ctx, opts, p, queryExpr, wherePredicate, overrideFormat, args, limits, timeout, createMissing)
		results[i] = res
		if res.encErr != nil {
			hasEncodeFailure = true
		}
	}

	// --- NFR-7 / FR-17: if any encode/query step failed ---
	// Strict (default): abort before any write.
	// --keep-going: continue to the write phase; errored files are skipped
	// and counted in the batch summary.
	if hasEncodeFailure && !keepGoing {
		// Errors were already logged per-file by processOneFile.
		// Emit batch summary only for multi-file runs.
		if len(results) > 1 {
			emitBatchSummary(opts.Logger, results)
		}
		return &emittedError{cause: errors.New("one or more files failed encode/query; no files were written")}
	}

	// --- Write phase: atomic per-file (NFR-9: non-transactional batch) ---
	for i := range results {
		r := &results[i]
		if r.skipped {
			// where.skipped: predicate did not match; do not write this file.
			continue
		}
		if r.encErr != nil {
			// Errored file: do not write (the error was already logged).
			// Under --keep-going this is expected; under strict mode we only
			// reach here if there were no encode failures (guarded above).
			continue
		}
		// FR-14: unchanged files are not backed up. isUnchanged is true when
		// the encoded query output matches the re-encoded original.
		isUnchanged := r.reencoded != nil && bytes.Equal(r.reencoded, r.encoded)
		writeSuffix := ""
		if !isUnchanged {
			writeSuffix = backupSuffix
		}
		backupWritten, err := nesio.WriteAtomicWithBackup(r.path, r.encoded, writeSuffix)
		if err != nil {
			opts.Logger.Error(logx.EventIOWrite, r.path, err.Error())
			r.encErr = err // mark as errored for summary
			continue
		}
		if backupWritten {
			opts.Logger.Info(logx.EventFileBackupWritten, r.path, r.path+writeSuffix)
		}
	}

	// --- FR-6: batch summary (only for multi-file / glob operations) ---
	if len(results) > 1 {
		emitBatchSummary(opts.Logger, results)
	}

	// If any encode/query or write failed, exit non-zero.
	for _, r := range results {
		if r.encErr != nil {
			return &emittedError{cause: errors.New("one or more files failed")}
		}
	}
	return nil
}

// detectMixedFormats checks that all paths resolve to the same format.
// It uses extension-based detection (or overrideFormat when set) and
// produces a format.mixed error naming representative files if more than
// one format is found.
//
// This function reads no file bytes — it operates only on path names,
// so it has zero IO side-effects and satisfies FR-5's "before any write
// or output" contract.
func detectMixedFormats(opts RunOptions, paths []string, overrideFormat string) error {
	if overrideFormat != "" {
		// --format forces a single format for all inputs; no mixed-format possible.
		return nil
	}

	type formatSample struct {
		path string
		name string
	}

	// seen maps fmtName → first path with that format.
	seen := make(map[string]formatSample)

	for _, p := range paths {
		fmtName := detectFormatByExt(p)
		if fmtName == "" {
			// Unknown extension: treat the raw extension as a distinct class
			// so it contributes to mixed-format detection.
			fmtName = "unknown(" + filepath.Ext(p) + ")"
		}
		if _, ok := seen[fmtName]; !ok {
			seen[fmtName] = formatSample{path: p, name: fmtName}
		}
	}

	if len(seen) <= 1 {
		return nil
	}

	// Collect a representative pair of files with different formats for the
	// error message. Map iteration order is non-deterministic; pick two stable
	// entries by iterating and taking the first two.
	samples := make([]formatSample, 0, len(seen))
	for _, s := range seen {
		samples = append(samples, s)
	}
	a, b := samples[0], samples[1]
	msg := fmt.Sprintf(
		"mixed formats in input: %s is %s, %s is %s; all files must share one format",
		a.path, a.name, b.path, b.name,
	)
	opts.Logger.ErrorGlobal(logx.EventFormatMixed, msg)
	return &emittedError{cause: errors.New(msg)}
}

// processOneFile runs the decode → where-test → query → encode pipeline for a
// single file and returns a fileResult. Errors are logged and stored in
// result.encErr. When wherePredicate is non-empty and the document does not
// satisfy it, result.skipped is set to true and the query/encode steps are
// skipped.
func processOneFile(
	ctx context.Context,
	opts RunOptions,
	path string,
	queryExpr string,
	wherePredicate string,
	overrideFormat string,
	args []query.Arg,
	limits format.Limits,
	timeout time.Duration,
	createMissing bool,
) fileResult {
	res := fileResult{path: path}

	fmtName := overrideFormat
	if fmtName == "" {
		fmtName = detectFormatByExt(path)
	}
	if fmtName == "" {
		msg := "cannot detect format (supported: json, yaml, yml, toml); use --format to override"
		opts.Logger.Error(logx.EventFormatUnknown, path, msg)
		res.encErr = errors.New(msg)
		return res
	}

	// Read original bytes from disk (also used for unchanged detection).
	f, err := os.Open(path) //nolint:gosec // user-supplied path by design
	if err != nil {
		opts.Logger.Error(logx.EventIORead, path, err.Error())
		res.encErr = err
		return res
	}
	origBytes, readErr := format.ReadAllLimited(f, limits.MaxBytes, fmtName)
	_ = f.Close()
	if readErr != nil {
		opts.Logger.Error(classifyDecodeErr(readErr), path, readErr.Error())
		res.encErr = readErr
		return res
	}
	// Decode.
	val, err := decodeFormatValueWithLimits(fmtName, bytes.NewReader(origBytes), limits)
	if err != nil {
		opts.Logger.Error(classifyDecodeErr(err), path, err.Error())
		res.encErr = err
		return res
	}

	// Re-encode the original (no query applied) for unchanged detection.
	// Comparing encoded(query(original)) == encoded(original) is the correct
	// idempotency check — it normalises away trailing-newline and whitespace
	// differences between what was on disk and what the encoder produces.
	var reencBuf bytes.Buffer
	if reencErr := encodeFormatValue(fmtName, &reencBuf, val); reencErr == nil {
		res.reencoded = reencBuf.Bytes()
	}
	// If re-encode fails (e.g. the file was already corrupt), reencoded stays
	// nil and the summary will count it as changed (conservative).

	// --where (FR-10 / STORY-0009): test the predicate before running the
	// query. Files that do not match are logged as where.skipped and returned
	// immediately; the query and encode steps are skipped.
	if wherePredicate != "" {
		match, whereErr := query.ApplyWhere(ctx, val, wherePredicate)
		if whereErr != nil {
			opts.Logger.Error(classifyQueryErr(ctx, whereErr, 0), path, whereErr.Error())
			res.encErr = whereErr
			return res
		}
		if !match {
			opts.Logger.Warn(logx.EventWhereSkipped, path, "--where predicate did not match; file skipped")
			res.skipped = true
			return res
		}
	}

	// Apply query with optional timeout.
	queryCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	outVal, err := query.RunValueWithArgs(queryCtx, val, queryExpr, args)
	if err != nil {
		opts.Logger.Error(classifyQueryErr(queryCtx, err, timeout), path, err.Error())
		res.encErr = err
		return res
	}

	// FR-16 / STORY-0012: reject missing-path creation unless --create-missing.
	if !createMissing {
		if mpErr := query.CheckNoMissingPaths(val, outVal); mpErr != nil {
			opts.Logger.Error(logx.EventQueryMissingPath, path, mpErr.Error())
			res.encErr = mpErr
			return res
		}
	}

	// Encode.
	var buf bytes.Buffer
	if encErr := encodeFormatValue(fmtName, &buf, outVal); encErr != nil {
		var encodeErr *omap.EncodeError
		if errors.As(encErr, &encodeErr) {
			opts.Logger.Error(logx.EventFormatIncompatible, path, encErr.Error())
		} else {
			opts.Logger.Error(logx.EventEncodeError, path, encErr.Error())
		}
		res.encErr = encErr
		return res
	}

	res.encoded = buf.Bytes()
	return res
}

// emitBatchSummary counts changed/unchanged/skipped/errored from results and
// emits the FR-6 `N changed, M unchanged, K errored` line on stderr via
// InfoGlobal. Skipped files (--where predicate mismatch) are excluded from
// changed/unchanged/errored — they appear only in the per-file where.skipped
// warn lines already emitted by processOneFile.
//
// Unchanged detection: compare encode(query(original)) with encode(original).
// This normalises away encoding differences (trailing newlines, whitespace) so
// a file that was already in canonical form is not counted as changed.
func emitBatchSummary(log *logx.Logger, results []fileResult) {
	changed, unchanged, errored := 0, 0, 0
	for _, r := range results {
		if r.skipped {
			// where.skipped: not counted in any summary bucket.
			continue
		}
		if r.encErr != nil {
			errored++
		} else if r.reencoded != nil && bytes.Equal(r.reencoded, r.encoded) {
			unchanged++
		} else {
			changed++
		}
	}
	msg := fmt.Sprintf("%d changed, %d unchanged, %d errored", changed, unchanged, errored)
	log.InfoGlobal(logx.EventBatchSummary, msg)
}
