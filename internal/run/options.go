package run

import (
	"context"
	"io"

	"github.com/sdkks/nesdit/internal/logx"
)

// RunOptions is the single struct through which all dependencies enter
// the run.Execute orchestrator. Replaces the old WithIO(args, stdout,
// stderr) signature: the flat struct accommodates context, stdin, and
// logging while keeping dependencies together and call sites unambiguous.
//
// All fields are optional in the sense that Execute fills sensible
// defaults (Background ctx, empty stdin, a fresh Logger over Stderr)
// when a caller passes the zero value. Callers that care about
// capture (tests, embedders) SHOULD set Stdout/Stderr explicitly.
//
// The `RunOptions` spelling (rather than `Options`) is the contract
// pinned by Test_RunOptions_StructContract — tests
// elsewhere in the tree reference it by that exact name. The
// nolint directive below documents the deliberate
// stutter.
//
//nolint:revive // Preserve the public name run.RunOptions.
type RunOptions struct {
	// Args is the argv tail (os.Args[1:] for the real CLI). Required
	// for any non-trivial invocation.
	Args []string

	// Ctx is the context driving the run. Query execution inherits it
	// so cancellation and --timeout deadlines reach the evaluator.
	Ctx context.Context

	// Stdin is the input stream for invocations without a file argument,
	// with an explicit "-" argument, or with --protobuf-stdin. All Execute
	// streams are borrowed: they are never automatically closed or deadlined.
	// Protobuf reads and writes are synchronous/cooperative; callers must
	// supply responsive I/O or arrange unblocking. Only Run has a watchdog.
	Stdin io.Reader

	// Stdout and Stderr are the output streams. Stderr is also the
	// default writer for Logger when Logger is nil.
	Stdout io.Writer
	Stderr io.Writer

	// Logger is the canonical stderr emitter (see internal/logx). When
	// nil, Execute constructs one over Stderr using LogFormat. Callers
	// that want to share a Logger across subprocesses (e.g. the testscript
	// harness) set it explicitly; in that case LogFormat is ignored.
	Logger *logx.Logger

	// LogFormat selects the rendering mode ("text" or "json"). When
	// Logger is nil, Execute constructs a Logger with this format.
	// Defaults to "text" when empty.
	LogFormat logx.Format
}
