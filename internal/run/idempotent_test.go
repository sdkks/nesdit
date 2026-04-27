package run_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/run"
)

// captureExecute is the unit-test equivalent of e2e captureRun: it runs
// run.Execute with per-call buffers and returns (stdout, exitCode). It
// cannot fail — Execute always terminates and the buffers always accept
// writes, so there is no capture infrastructure to break.
func captureExecute(t *testing.T, args []string) ([]byte, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   args,
		Ctx:    context.Background(),
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return stdout.Bytes(), code
}

// Test_Execute_IdempotencyContract pins the two-consecutive-run NFR-2
// contract: running nesdit twice with identical argv produces byte-identical
// stdout and identical exit codes.
//
// The fixture is a JSON object with multiple keys — map iteration order in
// Go is deliberately randomised, so any encoder that serialises via a plain
// map[string]any would produce non-deterministic key order and fail this
// test. nesdit uses internal/omap (insertion-order preserving), so output
// is stable across runs. This test therefore validates both the idempotency
// harness logic AND the determinism of the encode path.
func Test_Execute_IdempotencyContract(t *testing.T) {
	t.Parallel()

	// Multi-key object: a naive map-iteration encoder would produce varying
	// key order across the two Execute calls, causing the bytes.Equal check
	// to fail intermittently (Go map iteration is randomised per run).
	const input = `{"z":3,"a":1,"m":2,"b":4}`
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte(input), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	args := []string{path, "--query", "."}

	first, code1 := captureExecute(t, args)
	second, code2 := captureExecute(t, args)

	if code1 != 0 || code2 != 0 {
		t.Fatalf("expected exit 0 on both runs; got code1=%d code2=%d", code1, code2)
	}
	if code1 != code2 {
		t.Fatalf("exit code drift: first=%d second=%d", code1, code2)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("stdout drift between runs\n--- first ---\n%s\n--- second ---\n%s",
			string(first), string(second))
	}

	// Sanity: output must contain all four keys, confirming nesdit didn't
	// silently drop any.
	out := string(first)
	for _, key := range []string{`"z"`, `"a"`, `"m"`, `"b"`} {
		if !strings.Contains(out, key) {
			t.Errorf("output missing key %s: %q", key, out)
		}
	}
}

// Test_Execute_IdempotencyContract_ErrorPath verifies that two consecutive
// runs of an error-producing invocation (bad --query) also produce identical
// exit codes. The idempotency invariant must hold on the failure path, not
// just the success path.
func Test_Execute_IdempotencyContract_ErrorPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	if err := os.WriteFile(path, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	args := []string{path, "--query", "invalid!!jq"}

	_, code1 := captureExecute(t, args)
	_, code2 := captureExecute(t, args)

	if code1 != code2 {
		t.Fatalf("exit code drift on error path: first=%d second=%d", code1, code2)
	}
	if code1 == 0 {
		t.Fatalf("expected non-zero exit on invalid query; got 0 on both runs")
	}
}
