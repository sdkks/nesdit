package run_test

// Tests for the --format json stdin single-document decode path (TASK-0039).
//
// The bug: passing --format json with a multi-line JSON object on stdin used to
// normalize "json" to "jsonl" in stdinFormatName, then feed the input to the
// line-by-line JSONL reader. The first line ("{"a": 1,") was treated as a
// complete JSON document and failed with json: EOF.
//
// The fix: --format json now routes through runStdinJSON, which buffers all
// of stdin and decodes as a single document (matching runOnce's file path).

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/run"
)

// captureStdin runs run.Execute with the given stdin content and args,
// returning (stdout, exitCode).
func captureStdin(t *testing.T, stdin string, args []string) (string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   args,
		Ctx:    context.Background(),
		Stdin:  strings.NewReader(stdin),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return stdout.String(), code
}

// Test_FormatJSON_MultilineStdin verifies that --format json with a multi-line
// JSON object succeeds. This is the primary regression test for TASK-0039.
func Test_FormatJSON_MultilineStdin(t *testing.T) {
	t.Parallel()

	const input = "{\n  \"a\": 1,\n  \"b\": 2\n}\n"
	out, code := captureStdin(t, input, []string{
		"--format", "json",
		"--query", ".",
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stdout: %q", code, out)
	}
	if !strings.Contains(out, `"a":1`) || !strings.Contains(out, `"b":2`) {
		t.Errorf("output missing expected fields: %q", out)
	}
}

// Test_FormatJSON_MultilineStdin_WithQuery verifies that --format json with a
// multi-line JSON object and a query that adds a field succeeds.
func Test_FormatJSON_MultilineStdin_WithQuery(t *testing.T) {
	t.Parallel()

	const input = "{\n  \"a\": 1,\n  \"b\": 2\n}\n"
	out, code := captureStdin(t, input, []string{
		"--format", "json",
		"--query", ".c = 4",
		"--create-missing",
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stdout: %q", code, out)
	}
	if !strings.Contains(out, `"a":1`) || !strings.Contains(out, `"b":2`) || !strings.Contains(out, `"c":4`) {
		t.Errorf("output missing expected fields: %q", out)
	}
}

// Test_FormatJSON_SingleLineStdin verifies that --format json with a
// single-line JSON object (the previously working case) still works.
func Test_FormatJSON_SingleLineStdin(t *testing.T) {
	t.Parallel()

	const input = `{"x":10}` + "\n"
	out, code := captureStdin(t, input, []string{
		"--format", "json",
		"--query", ".",
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stdout: %q", code, out)
	}
	if !strings.Contains(out, `"x":10`) {
		t.Errorf("output missing expected field: %q", out)
	}
}

// Test_FormatJSONL_StreamStdin verifies that --format jsonl with a
// newline-delimited stream still works after the json path change.
func Test_FormatJSONL_StreamStdin(t *testing.T) {
	t.Parallel()

	const input = `{"id":1}
{"id":2}
{"id":3}
`
	out, code := captureStdin(t, input, []string{
		"--format", "jsonl",
		"--query", ".ok = true",
		"--create-missing",
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stdout: %q", code, out)
	}
	// All three docs must appear in output.
	for _, id := range []string{`"id":1`, `"id":2`, `"id":3`} {
		if !strings.Contains(out, id) {
			t.Errorf("output missing %s: %q", id, out)
		}
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Errorf("output missing ok:true: %q", out)
	}
}

// Test_FormatJSON_KeepGoing_InvalidJSON verifies that --format json --keep-going
// with invalid JSON on stdin exits 0 (not 1). This is the regression test for
// must-fix 1 in TASK-0039 rework: runStdinJSON did not accept keepGoing.
func Test_FormatJSON_KeepGoing_InvalidJSON(t *testing.T) {
	t.Parallel()

	const input = "not valid json\n"
	_, code := captureStdin(t, input, []string{
		"--format", "json",
		"--keep-going",
	})
	if code != 0 {
		t.Fatalf("expected exit 0 with --keep-going on invalid JSON, got exit %d", code)
	}
}

// Test_AutoDetect_MultilineJSON verifies that a multi-line JSON object piped
// to stdin WITHOUT --format is auto-detected as "json" and routes through
// runStdinJSON (single-document buffered decode). This is the regression test
// for must-fix 2 in TASK-0039 rework: auto-detect used to normalise "json" to
// "jsonl", causing the first line of a multi-line object to be decoded as a
// complete document and failing with a parse error.
func Test_AutoDetect_MultilineJSON(t *testing.T) {
	t.Parallel()

	const input = "{\n  \"x\": 10,\n  \"y\": 20\n}\n"
	out, code := captureStdin(t, input, []string{
		"--query", ".",
	})
	if code != 0 {
		t.Fatalf("expected exit 0 for auto-detected multi-line JSON, got exit %d; stdout: %q", code, out)
	}
	if !strings.Contains(out, `"x":10`) || !strings.Contains(out, `"y":20`) {
		t.Errorf("output missing expected fields: %q", out)
	}
}

// Test_FormatJSON_OutputFormatYAML verifies that --format json --output-format yaml
// with a multi-line JSON object produces YAML output.
func Test_FormatJSON_OutputFormatYAML(t *testing.T) {
	t.Parallel()

	const input = "{\n  \"name\": \"alice\",\n  \"version\": 2\n}\n"
	out, code := captureStdin(t, input, []string{
		"--format", "json",
		"--output-format", "yaml",
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stdout: %q", code, out)
	}
	if !strings.Contains(out, "name: alice") || !strings.Contains(out, "version: 2") {
		t.Errorf("output missing expected YAML fields: %q", out)
	}
}
