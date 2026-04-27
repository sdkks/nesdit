package run_test

// Tests for the NFR-8 partial-write path in runStdin when a pass-through doc
// (--where non-matching doc) fails to write under --keep-going.
//
// A real *omap.EncodeError on a pass-through doc is not achievable in
// practice: a value decoded cleanly from YAML or JSONL will always
// re-encode in the same format without error. The failure mode that IS
// reachable is an underlying io.Writer error (e.g. broken pipe). This
// test exercises that path using a writer that returns an error on its
// first write call, confirming the NFR-8 design intent documented in
// stdin.go: --keep-going sets hadError and continues; stdout may be
// incomplete; exit code is 1.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/run"
)

// failAfterWriter wraps an underlying bytes.Buffer and returns errWriteFail
// once the total number of Write calls reaches failOnCall. Writes that
// succeed are still forwarded to the underlying buffer so callers can inspect
// partial output.
type failAfterWriter struct {
	buf        bytes.Buffer
	calls      int
	failOnCall int
}

var errWriteFail = errors.New("write: simulated IO failure")

func (f *failAfterWriter) Write(p []byte) (int, error) {
	f.calls++
	if f.calls >= f.failOnCall {
		return 0, errWriteFail
	}
	return f.buf.Write(p)
}

// Test_KeepGoing_PassThrough_WriteFail exercises the NFR-8 partial-write
// path documented in internal/run/stdin.go.
//
// GIVEN a 3-doc JSONL stream:
//   - doc 1: {"env":"prod","v":1}   — matches --where '.env == "prod"', query applied
//   - doc 2: {"env":"staging","v":2} — does NOT match, pass-through write is attempted
//   - doc 3: {"env":"prod","v":3}   — matches, query applied
//
// AND the stdout writer returns an error starting from the 2nd Write call,
// so doc 1 is written successfully but the pass-through write for doc 2 fails.
//
// WITH --keep-going:
//   - doc 1 is written (exit on first write call, which succeeds)
//   - doc 2 pass-through write fails; error is logged; hadError = true; loop continues
//   - doc 3 is attempted but the writer is still broken, so it also fails
//   - exit code is 1 (hadError set)
//   - stderr must contain an encode.error or similar event for doc 2 (index 2)
//
// WITHOUT --keep-going (--strict):
//   - doc 1 is written (first write succeeds)
//   - doc 2 pass-through write fails; runStdin returns immediately; exit code is 1
func Test_KeepGoing_PassThrough_WriteFail(t *testing.T) {
	t.Parallel()

	// 3-doc JSONL stream: doc 1 and 3 match --where; doc 2 does not.
	const input = `{"env":"prod","v":1}
{"env":"staging","v":2}
{"env":"prod","v":3}
`

	t.Run("keep-going logs error and continues", func(t *testing.T) {
		t.Parallel()

		stdout := &failAfterWriter{failOnCall: 2} // 2nd Write call fails
		var stderr bytes.Buffer

		code := run.Execute(run.RunOptions{
			Args: []string{
				"--format", "jsonl",
				"--where", `.env == "prod"`,
				"--query", ".v = 99",
				"--keep-going",
			},
			Ctx:    context.Background(),
			Stdin:  strings.NewReader(input),
			Stdout: stdout,
			Stderr: &stderr,
			Logger: logx.New(&stderr),
		})

		// exit code must be 1 (hadError set by the pass-through write failure).
		if code == 0 {
			t.Errorf("expected non-zero exit; got 0\nstderr: %s", stderr.String())
		}

		// stderr must contain an error event referencing doc index 2 (the
		// non-matching pass-through doc).
		stderrStr := stderr.String()
		if !strings.Contains(stderrStr, "-:2:") {
			t.Errorf("stderr missing per-doc error for index 2: %q", stderrStr)
		}
	})

	t.Run("strict halts on first pass-through write failure", func(t *testing.T) {
		t.Parallel()

		stdout := &failAfterWriter{failOnCall: 2} // 2nd Write call fails
		var stderr bytes.Buffer

		code := run.Execute(run.RunOptions{
			Args: []string{
				"--format", "jsonl",
				"--where", `.env == "prod"`,
				"--query", ".v = 99",
				// no --keep-going: strict (default)
			},
			Ctx:    context.Background(),
			Stdin:  strings.NewReader(input),
			Stdout: stdout,
			Stderr: &stderr,
			Logger: logx.New(&stderr),
		})

		// exit code must be non-zero (write failed; strict halts immediately).
		if code == 0 {
			t.Errorf("expected non-zero exit; got 0\nstderr: %s", stderr.String())
		}

		// stderr must contain an error event.
		if stderr.Len() == 0 {
			t.Errorf("expected stderr output on write failure; got none")
		}
	})
}
