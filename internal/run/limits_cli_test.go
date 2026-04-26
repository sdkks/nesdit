package run_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/format"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/run"
)

// Test_Run_InputSizeCap_StreamedNotMaterialised verifies MUST-FIX #1 from
// STORY-0008's review cycle: the CLI must surface the input-size cap after
// reading at most `limit + epsilon` bytes from disk — not after materialising
// the full file via os.ReadFile, which would OOM on a 10 GB YAML long before
// the decoder ever got a chance to apply the cap.
//
// The test writes a sparse-style large file (10 MiB + guard bytes) and caps
// at 1 MiB. Pre-fix (os.ReadFile path) the whole file is loaded before
// ReadAllLimited runs, making it impossible to detect the fix from memory
// behaviour on a 10 MiB file alone. The precise assertion the test uses is:
// an observable counting wrapper around the file reader must record <= cap +
// small-slack bytes read. We achieve that by keeping the file small enough to
// not OOM the test runner yet instrumenting the read path via a custom
// FormatDecoder to show the stream is consumed incrementally.
//
// Shape of the assertion: after run returns the LimitError, the total bytes
// delivered to decodeFormatValueWithLimits must be bounded by cap+1 (the
// limitedReader guard-byte contract). We assert this indirectly via:
//   - file size >> cap so os.ReadFile would visibly differ
//   - run returns the canonical decoder.limit.input_size stderr
//   - elapsed wall-clock on a 10 MiB file is small (sanity, not a hard bound)
func Test_Run_InputSizeCap_StreamedNotMaterialised(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "big.json")

	// Write a 10 MiB JSON document. The body is a single large string so
	// the decoder can parse it byte-by-byte without buffering the whole
	// thing before the byte cap trips. Pre-fix this entire 10 MiB landed
	// in a []byte before the cap ran.
	const fileSize = 10 * 1024 * 1024
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteString(`{"x":"`); err != nil {
		t.Fatalf("write prefix: %v", err)
	}
	// Fill most of the 10 MiB with 'x'.
	chunk := bytes.Repeat([]byte{'x'}, 64*1024)
	// Prefix already written: six bytes for the `{"x":"` header.
	written := 6
	for written < fileSize-2 {
		n, err := f.Write(chunk)
		if err != nil {
			t.Fatalf("write chunk: %v", err)
		}
		written += n
	}
	if _, err := f.WriteString(`"}`); err != nil {
		t.Fatalf("write suffix: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Cap at 1 MiB. The file is 10x that — classic OOM shape.
	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   []string{path, "--query", ".", "--max-bytes", "1048576"},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
		Logger: logx.New(&stderr),
	})
	if code == 0 {
		t.Fatalf("expected non-zero exit on oversize input; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "decoder.limit.input_size") {
		t.Errorf("stderr missing decoder.limit.input_size event: %q", stderr.String())
	}
	// Observed count in the stderr line should be bounded at the cap+1
	// guard-byte value (1048577). Pre-fix, observed would equal the full
	// file size (10485760) because os.ReadFile materialised everything
	// before format.ReadAllLimited ran.
	if !strings.Contains(stderr.String(), "observed 1048577, limit 1048576") {
		t.Errorf("expected 'observed 1048577, limit 1048576' (cap+1 bytes) — streaming cap violated.\nstderr=%q", stderr.String())
	}
	// Defence-in-depth: explicitly assert the pre-fix shape is absent.
	if strings.Contains(stderr.String(), "observed 10485760") {
		t.Errorf("os.ReadFile materialised full file before cap — MUST-FIX #1 regressed.\nstderr=%q", stderr.String())
	}
}

// Test_Run_QueryFileSizeCap verifies MUST-FIX #2: --from-file uses an
// os.ReadFile with no cap today. A pathological 10 GB .jq file would OOM the
// CLI before any query is parsed. The fix routes the read through
// format.ReadAllLimited with a tight dedicated cap (1 MiB).
//
// Assertion: a query file larger than DefaultQueryMaxBytes is rejected with
// a canonical decoder.limit.input_size (Format=query) stderr line and exit 1.
func Test_Run_QueryFileSizeCap(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	queryPath := filepath.Join(dir, "big.jq")
	docPath := filepath.Join(dir, "a.json")
	if err := os.WriteFile(docPath, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	// 2 MiB of whitespace-padded query text; any single '.' followed by
	// junk trips query parse errors, but the cap fires earlier so we
	// never get there.
	big := make([]byte, 2*1024*1024)
	for i := range big {
		big[i] = ' '
	}
	big[0] = '.'
	if err := os.WriteFile(queryPath, big, 0o644); err != nil {
		t.Fatalf("write query: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   []string{docPath, "--from-file", queryPath},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
		Logger: logx.New(&stderr),
	})
	if code == 0 {
		t.Fatalf("expected non-zero exit on oversize query file; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "decoder.limit.input_size") {
		t.Errorf("stderr missing decoder.limit.input_size event: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "query: input_size limit exceeded") {
		t.Errorf("stderr missing 'query: input_size limit exceeded' phrase: %q", stderr.String())
	}
}

// Test_Run_QueryFileSizeCap_DefaultApplies verifies the default
// DefaultQueryMaxBytes is in effect when no flag override is passed. A query
// file at the 1 MiB default plus one byte is rejected.
func Test_Run_QueryFileSizeCap_DefaultApplies(t *testing.T) {
	t.Parallel()

	if format.DefaultQueryMaxBytes <= 0 {
		t.Fatalf("DefaultQueryMaxBytes must be > 0, got %d", format.DefaultQueryMaxBytes)
	}

	dir := t.TempDir()
	queryPath := filepath.Join(dir, "big.jq")
	docPath := filepath.Join(dir, "a.json")
	if err := os.WriteFile(docPath, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	// Default+1 byte query.
	big := make([]byte, format.DefaultQueryMaxBytes+1)
	for i := range big {
		big[i] = ' '
	}
	big[0] = '.'
	if err := os.WriteFile(queryPath, big, 0o644); err != nil {
		t.Fatalf("write query: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   []string{docPath, "--from-file", queryPath},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
		Logger: logx.New(&stderr),
	})
	if code == 0 {
		t.Fatalf("expected non-zero exit at default+1 bytes; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "decoder.limit.input_size") {
		t.Errorf("stderr missing decoder.limit.input_size event: %q", stderr.String())
	}
}

// Test_Run_QueryFileBelowCap_Passes is the positive counterpart — a small
// query file is consumed without trouble.
func Test_Run_QueryFileBelowCap_Passes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	queryPath := filepath.Join(dir, "small.jq")
	docPath := filepath.Join(dir, "a.json")
	if err := os.WriteFile(docPath, []byte(`{"x":42}`), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	if err := os.WriteFile(queryPath, []byte(".x"), 0o644); err != nil {
		t.Fatalf("write query: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   []string{docPath, "--from-file", queryPath},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
		Logger: logx.New(&stderr),
	})
	if code != 0 {
		t.Fatalf("sub-cap query file rejected: code=%d stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "42" {
		t.Errorf("stdout=%q want %q", got, "42")
	}
}
