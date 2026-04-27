package run_test

// logformat_test.go — FR-15 / STORY-0011: unit tests for --log-format flag
// wiring in the run package. These tests assert that:
//   - --log-format=json causes the Logger to emit NDJSON on stderr.
//   - --log-format=text (default) continues to emit NFR-10 text lines.
//   - --log-format=<unknown> exits 1 before any IO.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/run"
)

// TestLogFormatJSON_BatchSummary verifies that --log-format=json causes
// the batch.summary event to be emitted as NDJSON, not text.
// Uses two files because single-file runs omit the batch.summary (by design).
func TestLogFormatJSON_BatchSummary(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.json")
	f2 := filepath.Join(dir, "b.json")
	if err := os.WriteFile(f1, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte(`{"y":2}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   []string{"-i", f1, f2, "--query", ".z = 3", "--create-missing", "--log-format=json"},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	t.Logf("exit code: %d", code)
	t.Logf("stderr: %q", stderr.String())

	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr: %s", code, stderr.String())
	}
	// stdout must be empty (in-place mode).
	if stdout.Len() != 0 {
		t.Errorf("expected empty stdout, got %q", stdout.String())
	}
	// stderr must contain a batch.summary NDJSON line.
	if !strings.Contains(stderr.String(), `"event":"batch.summary"`) {
		t.Errorf("expected NDJSON batch.summary in stderr\ngot: %q", stderr.String())
	}
	// The line must be valid JSON.
	for _, line := range strings.Split(strings.TrimRight(stderr.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("stderr line is not valid JSON: %v\nline: %s", err, line)
		}
	}
}

// TestLogFormatText_DefaultBatchSummary verifies that without --log-format,
// the batch.summary event is emitted in text mode (NFR-10 shape).
// Uses two files because single-file runs omit the batch.summary (by design).
func TestLogFormatText_DefaultBatchSummary(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.json")
	f2 := filepath.Join(dir, "b.json")
	if err := os.WriteFile(f1, []byte(`{"x":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte(`{"y":2}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   []string{"-i", f1, f2, "--query", ".z = 3", "--create-missing"},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d\nstderr: %s", code, stderr.String())
	}
	// stderr must contain the text-mode batch.summary line.
	if !strings.Contains(stderr.String(), "nesdit: info: batch.summary:") {
		t.Errorf("expected text-mode batch.summary in stderr\ngot: %q", stderr.String())
	}
}

// TestLogFormatInvalid_ExitOne verifies that an unknown --log-format value
// exits 1 before any IO, with a flag.invalid error on stderr.
func TestLogFormatInvalid_ExitOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   []string{"nonexistent.json", "--query", ".", "--log-format=bogus"},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag.invalid") {
		t.Errorf("expected flag.invalid in stderr\ngot: %q", stderr.String())
	}
	// No io.read error — the rejection must happen before file access.
	if strings.Contains(stderr.String(), "io.read") {
		t.Errorf("io.read appeared in stderr — rejection was not at flag-parse time\nstderr: %q", stderr.String())
	}
}

// TestLogFormatJSON_ErrorRecord verifies that errors are emitted as NDJSON
// with "severity":"error" when --log-format=json.
func TestLogFormatJSON_ErrorRecord(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   []string{"nonexistent.json", "--query", ".", "--log-format=json"},
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d\nstderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), `"severity":"error"`) {
		t.Errorf("expected NDJSON error record in stderr\ngot: %q", stderr.String())
	}
	// Must be valid JSON.
	line := strings.TrimRight(stderr.String(), "\n")
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Errorf("stderr line is not valid JSON: %v\nline: %s", err, line)
	}
}
