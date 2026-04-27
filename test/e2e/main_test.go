//go:build e2e

// Package e2e_test hosts the rogpeppe/go-internal/testscript harness for
// nesdit's end-to-end fixtures. The in-process binary bridge is registered
// via testscript.RunMain so `nesdit` inside a .txtar script calls the same
// internal/run.Run entrypoint as the real CLI — fast, coverage-friendly,
// no PATH lookup.
//
// Three commands extend the script vocabulary:
//
//   - nesdit-idempotent <args...>
//     NFR-2 enforcement. Runs nesdit(args) twice with identical argv via the
//     same in-process Run entrypoint, capturing stdout each time, and asserts
//     byte-for-byte identical output across the two runs. Any drift fails the
//     script with the two stdout buffers.
//
//   - fake-editor <target-file>
//     Scriptable $EDITOR stub for --edit mode fixtures (STORY-0007). When env
//     var FAKE_EDITOR_REPLACE names a file in the script working directory,
//     that file's contents are written over <target-file> verbatim (simulating
//     "user edited the buffer and saved"). Without the env var, fake-editor
//     is a no-op ("opened and saved, no changes"). The protocol is documented
//     here and in the package doc so fixture authors can wire a deterministic
//     editor without shell glue.
//
//   - fail-editor
//     A $EDITOR stub (registered via RunMain) that always exits 1. Used to
//     test the FR-4 acceptance: when the editor exits non-zero, nesdit must
//     emit an error on stderr and exit 1.
package e2e_test

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/sdkks/nesdit/internal/run"
)

func TestMain(m *testing.M) {
	os.Exit(testscript.RunMain(m, map[string]func() int{
		"nesdit":      func() int { return run.Run(os.Args[1:]) },
		"fake-editor": fakeEditorMain,
		"fail-editor": func() int { return 1 },
		// "vi" is registered as a no-op editor stub so that the vi-fallback
		// fixture (fr04_edit_editor_vi_fallback) can prove the $EDITOR →
		// $VISUAL → "vi" resolution chain reaches "vi" without requiring a
		// real vi installation in the test environment.
		"vi": fakeEditorMain,
	}))
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/scripts",
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"nesdit-idempotent": nesditIdempotent,
			"fake-editor":       fakeEditor,
		},
	})
}

// nesditIdempotent runs nesdit(args) twice via the in-process Run entrypoint
// and asserts byte-identical stdout. This is the NFR-2 invariant harness.
// Fixture authors invoke it anywhere an operation should be a no-op on the
// second run.
func nesditIdempotent(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("nesdit-idempotent does not support negation")
	}
	if len(args) == 0 {
		ts.Fatalf("nesdit-idempotent requires at least one argument")
	}

	first, code1 := captureRun(args)
	second, code2 := captureRun(args)

	if code1 != code2 {
		ts.Fatalf("nesdit-idempotent: exit code drift: first=%d second=%d", code1, code2)
	}
	if !bytes.Equal(first, second) {
		ts.Fatalf("nesdit-idempotent: stdout drift between runs\n--- first ---\n%s\n--- second ---\n%s",
			string(first), string(second))
	}
}

// captureRun runs nesdit via run.Execute with per-call buffers, so
// the idempotency harness does not mutate the global os.Stdout. The
// original pipe-swap implementation was not goroutine-safe and caused
// data races when testscript ran multiple scripts in parallel.
//
// The function cannot fail: Execute always returns an exit code and
// buffers stdout; there is no capture infrastructure to break. Normal
// run failures surface as a non-zero code with captured stderr.
func captureRun(args []string) ([]byte, int) {
	var stdout, stderr bytes.Buffer
	code := run.Execute(run.RunOptions{
		Args:   args,
		Stdin:  bytes.NewReader(nil),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	return stdout.Bytes(), code
}

// fakeEditor implements the $EDITOR stub for use as a testscript in-process
// command (invoked from within a .txtar script as `fake-editor <target>`).
// It is kept for scripts that need to drive fake-editor directly — but for
// --edit mode tests, fakeEditorMain (registered via RunMain) is what nesdit
// actually calls via exec.Command("fake-editor", tmpfile).
//
// Protocol (both fakeEditor and fakeEditorMain share the same env-var contract):
//
//	env FAKE_EDITOR_REPLACE=<src>
//	fake-editor <target>
//
// With FAKE_EDITOR_REPLACE set, <target> is overwritten with <src>'s bytes
// (simulating "user edited the buffer and saved"). Without the env var,
// fake-editor is a no-op ("opened and saved, no changes").
func fakeEditor(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("fake-editor does not support negation")
	}
	if len(args) != 1 {
		ts.Fatalf("fake-editor requires exactly one argument: the target file")
	}
	target := ts.MkAbs(args[0])
	src := ts.Getenv("FAKE_EDITOR_REPLACE")
	if src == "" {
		return
	}
	data, err := os.ReadFile(ts.MkAbs(src))
	if err != nil {
		ts.Fatalf("fake-editor: reading replacement %s: %v", src, err)
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		ts.Fatalf("fake-editor: writing target %s: %v", target, err)
	}
}

// fakeEditorMain is the subprocess entry-point for fake-editor when it is
// invoked by nesdit via exec.Command("fake-editor", tmpfile). It is
// registered via RunMain so it runs as a real binary in the testscript PATH.
//
// Protocol:
//   - FAKE_EDITOR_REPLACE: if set to an absolute path, overwrite os.Args[1]
//     with that file's bytes (simulating a user edit). If empty, the target
//     is left unchanged (no-op save).
func fakeEditorMain() int {
	if len(os.Args) < 2 {
		_, _ = fmt.Fprintln(os.Stderr, "fake-editor: usage: fake-editor <target>")
		return 1
	}
	target := os.Args[1]
	src := os.Getenv("FAKE_EDITOR_REPLACE")
	if src == "" {
		// No-op: target unchanged.
		return 0
	}
	data, err := os.ReadFile(src)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "fake-editor: reading %s: %v\n", src, err)
		return 1
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "fake-editor: writing %s: %v\n", target, err)
		return 1
	}
	return 0
}
