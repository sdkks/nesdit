// Package query wraps itchyny/gojq as the query engine for nesdit and
// crosses the order-preservation bridge at the boundary: decode into
// *omap.Doc, convert to the `any`-shape go-jq accepts, run the compiled
// query, and reconcile the result against the pre-query *omap.Doc
// snapshot so untouched subtrees keep their original key order (NFR-3)
// and numeric precision (NFR-4).
//
// STORY-0002 scope: single-output queries only. Multi-output queries
// (FR-2's `.items[]` streaming) land in a later story; Run returns the
// first produced value and ignores subsequent ones. Compile and runtime
// errors surface as *Error with an Op classifier so callers can
// distinguish compile vs. runtime failure.
package query

import (
	"context"
	"errors"
	"fmt"

	"github.com/itchyny/gojq"

	"github.com/sdkks/nesdit/internal/omap"
)

// Error classifies a query failure. Op is one of:
//
//	"parse"   — gojq.Parse rejected the query source.
//	"compile" — gojq.Compile failed (e.g. unknown function).
//	"runtime" — the compiled query errored during evaluation.
//	"result"  — the query produced no value, or a non-object top-level.
type Error struct {
	Op  string
	Err error
}

// Error renders as "query.<op>: <underlying>".
func (e *Error) Error() string {
	if e == nil || e.Err == nil {
		return "query." + e.Op + ": <nil>"
	}
	return "query." + e.Op + ": " + e.Err.Error()
}

// Unwrap exposes the underlying cause for errors.Is / errors.As.
func (e *Error) Unwrap() error { return e.Err }

// Run applies query against doc and returns a new *omap.Doc with key
// order reconciled against doc. Only the first output of the compiled
// jq program is used; queries emitting 2+ values are rejected with
// *Error{Op:"result"} (FR-2 multi-output is STORY-0003+ scope). A
// non-object top-level output also produces *Error{Op:"result"}.
//
// The ctx argument is plumbed into gojq.RunWithContext so callers can
// enforce execution deadlines or cancel long-running queries. A nil ctx
// is treated as context.Background(). STORY-0003's `--timeout` flag
// will cancel ctx; today every caller passes context.Background() and
// behaviour is unchanged for non-pathological queries.
func Run(ctx context.Context, doc *omap.Doc, query string) (*omap.Doc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if doc == nil {
		return nil, &Error{Op: "result", Err: errors.New("nil input doc")}
	}
	parsed, err := gojq.Parse(query)
	if err != nil {
		return nil, &Error{Op: "parse", Err: err}
	}
	code, err := gojq.Compile(parsed)
	if err != nil {
		return nil, &Error{Op: "compile", Err: err}
	}

	in := doc.ToAny()
	iter := code.RunWithContext(ctx, in)
	first, ok := iter.Next()
	if !ok {
		return nil, &Error{Op: "result", Err: errors.New("query produced no output")}
	}
	if runErr, isErr := first.(error); isErr {
		return nil, &Error{Op: "runtime", Err: runErr}
	}
	// Probe for a second value — multi-output queries are rejected in
	// v1 (FR-2 streaming is STORY-0003+). Silently dropping the tail
	// would be strictly worse than a clear error.
	if _, more := iter.Next(); more {
		return nil, &Error{Op: "result", Err: errors.New("multi-output queries are not supported in v1; query produced 2+ values (single-output only)")}
	}
	m, ok := first.(map[string]any)
	if !ok {
		return nil, &Error{Op: "result", Err: fmt.Errorf("top-level output is %T, want object", first)}
	}
	return omap.FromAny(m, doc), nil
}
