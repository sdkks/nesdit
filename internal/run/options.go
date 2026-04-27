package run

import (
	"context"
	"io"

	"github.com/sdkks/nesdit/internal/logx"
)

// RunOptions is the single struct through which all dependencies enter
// the run.Execute orchestrator. Replaces STORY-0002's WithIO(args,
// stdout, stderr) signature — the three-arg form was incompatible with
// the ctx / stdin / logger plumbing STORY-0003 and STORY-0008 need, and
// the flat struct keeps call sites unambiguous: "everything the run
// needs is in exactly one place."
//
// All fields are optional in the sense that Execute fills sensible
// defaults (Background ctx, empty stdin, a fresh Logger over Stderr)
// when a caller passes the zero value. Callers that care about
// capture (tests, embedders) SHOULD set Stdout/Stderr explicitly.
//
// The `RunOptions` spelling (rather than `Options`) is the contract
// pinned by STORY-0003 / Test_RunOptions_StructContract — tests
// elsewhere in the tree reference it by that exact name. The
// revive:disable:exported directive below documents the deliberate
// stutter.
//
//nolint:revive // STORY-0003 pins the name run.RunOptions
type RunOptions struct {
	// Args is the argv tail (os.Args[1:] for the real CLI). Required
	// for any non-trivial invocation.
	Args []string

	// Ctx is the context driving the run. STORY-0003 plumbs it through
	// to query.Run so STORY-0008's --timeout flag can cancel on
	// deadline without touching call sites.
	Ctx context.Context

	// Stdin is the input stream. STORY-0003 does not yet consume it
	// (no STDIN mode), but the field exists so STORY-0005 can wire
	// `nesdit --query '...'` on the same struct.
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
	// Defaults to "text" when empty. FR-15 / STORY-0011.
	LogFormat logx.Format
}
