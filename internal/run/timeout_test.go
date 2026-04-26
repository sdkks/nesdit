package run_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/run"
)

// Test_Run_TimeoutFlag covers STORY-0008 M4: `--timeout <dur>` wraps the
// query ctx with context.WithTimeout and a pathological gojq query is
// cancelled before wall-clock exceeds the deadline by a wide margin.
// Emitted event must be query.timeout, distinct from query.runtime.
func Test_Run_TimeoutFlag(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	if err := os.WriteFile(path, []byte(`{"x":0}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stdout, stderr bytes.Buffer
	start := time.Now()
	// [range(1e12)] would take minutes to run to completion; with a
	// 10ms timeout the Execute call must return quickly.
	code := run.Execute(run.RunOptions{
		Args:   []string{path, "--query", "[range(1000000000000)]", "--timeout", "10ms"},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
		Logger: logx.New(&stderr),
	})
	elapsed := time.Since(start)

	if code == 0 {
		t.Fatalf("expected non-zero exit on timeout; got 0\nstderr: %s", stderr.String())
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout did not fire: elapsed=%v (want well under 5s)", elapsed)
	}
	if !strings.Contains(stderr.String(), "query.timeout") {
		t.Errorf("stderr missing query.timeout event: %q", stderr.String())
	}
}

// Test_Run_TimeoutDisabledByDefault: omitting --timeout preserves the
// STORY-0002 behaviour — a fast query completes successfully with exit 0.
func Test_Run_TimeoutDisabledByDefault(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	if err := os.WriteFile(path, []byte(`{"x":1,"y":2}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   []string{path, "--query", ".x"},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
		Logger: logx.New(&stderr),
	})
	if code != 0 {
		t.Fatalf("default run failed: code=%d stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("expected empty stderr; got %q", stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "1" {
		t.Errorf("stdout=%q want %q", got, "1")
	}
}

// Test_Run_DecoderInputSizeCap_CLIEvent verifies the CLI's
// classifyDecodeErr maps *format.LimitError{Kind: InputSize} to the
// decoder.limit.input_size event on stderr. Stands in for a full e2e
// test but avoids the testscript harness startup overhead.
func Test_Run_DecoderInputSizeCap_CLIEvent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	if err := os.WriteFile(path, []byte(`{"a":"`+strings.Repeat("x", 500)+`"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   []string{path, "--query", ".", "--max-bytes", "100"},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
		Logger: logx.New(&stderr),
	})
	if code == 0 {
		t.Fatalf("expected non-zero exit on oversize input")
	}
	if !strings.Contains(stderr.String(), "decoder.limit.input_size") {
		t.Errorf("stderr missing decoder.limit.input_size event: %q", stderr.String())
	}
}
