//go:build e2e

// Package e2e_test hosts the rogpeppe/go-internal/testscript harness for
// nesdit's end-to-end fixtures. The in-process binary bridge is registered
// via testscript.RunMain so `nesdit` inside a .txtar script calls the same
// internal/run.Run entrypoint as the real CLI — fast, coverage-friendly,
// no PATH lookup.
//
// Two custom commands extend the script vocabulary:
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
package e2e_test

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/sakkas-zendesk/nesdit/internal/run"
)

func TestMain(m *testing.M) {
	os.Exit(testscript.RunMain(m, map[string]func() int{
		"nesdit": func() int { return run.Run(os.Args[1:]) },
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

	first, code1, err := captureRun(args)
	if err != nil {
		ts.Fatalf("nesdit-idempotent: capture first run: %v", err)
	}
	second, code2, err := captureRun(args)
	if err != nil {
		ts.Fatalf("nesdit-idempotent: capture second run: %v", err)
	}

	if code1 != code2 {
		ts.Fatalf("nesdit-idempotent: exit code drift: first=%d second=%d", code1, code2)
	}
	if !bytes.Equal(first, second) {
		ts.Fatalf("nesdit-idempotent: stdout drift between runs\n--- first ---\n%s\n--- second ---\n%s",
			string(first), string(second))
	}
}

// captureRun redirects os.Stdout around run.Run and returns the captured
// bytes, the exit code, and any capture-infrastructure error. Used only
// inside the idempotency harness — the `nesdit` script command (registered
// via RunMain) goes through the normal testscript stdout path.
//
// Fail-closed contract: if we cannot set up the capture pipe, we MUST return
// a non-nil error so the harness fails the fixture explicitly. Silently
// falling back to an uncaptured run would produce a false-positive on the
// NFR-2 idempotency invariant (bytes.Equal(nil, nil) == true).
//
// Not goroutine-safe: this function mutates the global os.Stdout and is
// therefore incompatible with parallel fixtures. testscript runs script
// commands sequentially per-script today, so this is fine for TASK-0003;
// flag before STORY-0003 lands fixtures that use testscript -parallel.
func captureRun(args []string) ([]byte, int, error) {
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return nil, 0, err
	}
	os.Stdout = w
	done := make(chan []byte, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.Bytes()
	}()
	code := run.Run(args)
	_ = w.Close()
	os.Stdout = orig
	return <-done, code, nil
}

// fakeEditor implements the $EDITOR stub described in this file's doc
// comment. Protocol:
//
//	env FAKE_EDITOR_REPLACE=<src>
//	fake-editor <target>
//
// With FAKE_EDITOR_REPLACE set, <target> is overwritten with <src>'s bytes.
// Without it, fake-editor is a no-op.
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
