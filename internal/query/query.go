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
	stdjson "encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/itchyny/gojq"

	"github.com/sdkks/nesdit/internal/omap"
)

// Arg binds a single $-variable in the compiled query. Raw is the raw
// text from the CLI (for --arg, the literal string; for --argjson, the
// JSON source). JSON=true activates JSON decoding at bind time.
type Arg struct {
	Name string // variable name without the leading "$"
	JSON bool
	Raw  string
}

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
	return RunWithArgs(ctx, doc, query, nil)
}

// RunWithArgs is Run plus `--arg`/`--argjson` variable bindings. Each
// Arg becomes a gojq variable at compile time; values are bound at
// call time in the same order. JSON args are decoded with UseNumber
// to preserve NFR-4 integer precision. String args are passed through
// as-is.
//
// Passing a nil or empty args slice is equivalent to Run.
func RunWithArgs(ctx context.Context, doc *omap.Doc, query string, args []Arg) (*omap.Doc, error) {
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

	// Build the variable binding lists in a stable order (the order
	// the caller passed args in). gojq wants `$name` style names.
	varNames := make([]string, 0, len(args))
	varValues := make([]any, 0, len(args))
	for _, a := range args {
		varNames = append(varNames, "$"+a.Name)
		v, err := bindArg(a)
		if err != nil {
			return nil, &Error{Op: "parse", Err: err}
		}
		varValues = append(varValues, v)
	}

	var compileOpts []gojq.CompilerOption
	if len(varNames) > 0 {
		compileOpts = append(compileOpts, gojq.WithVariables(varNames))
	}
	code, err := gojq.Compile(parsed, compileOpts...)
	if err != nil {
		return nil, &Error{Op: "compile", Err: err}
	}

	in := doc.ToAny()
	iter := code.RunWithContext(ctx, in, varValues...)
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

// RunValue is the top-level-agnostic counterpart to Run: it accepts any
// omap.Value as input (map, seq, scalar) and returns the first output value
// reconciled against the pre-query snapshot using ValueFromAny. Multi-output
// queries still produce *Error{Op:"result"}; the top-level-object
// restriction that Run enforces is deliberately relaxed here because RFC
// 8259 and YAML 1.2 permit any top-level value (BUG-0001 fix).
func RunValue(ctx context.Context, v omap.Value, query string) (omap.Value, error) {
	return RunValueWithArgs(ctx, v, query, nil)
}

// RunValueWithArgs is RunValue plus --arg/--argjson variable bindings.
// Same semantics as RunWithArgs except the input and output are omap.Value
// rather than *omap.Doc. See RunWithArgs for the full contract.
func RunValueWithArgs(ctx context.Context, v omap.Value, query string, args []Arg) (omap.Value, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	parsed, err := gojq.Parse(query)
	if err != nil {
		return omap.Value{}, &Error{Op: "parse", Err: err}
	}

	varNames := make([]string, 0, len(args))
	varValues := make([]any, 0, len(args))
	for _, a := range args {
		varNames = append(varNames, "$"+a.Name)
		bv, err := bindArg(a)
		if err != nil {
			return omap.Value{}, &Error{Op: "parse", Err: err}
		}
		varValues = append(varValues, bv)
	}

	var compileOpts []gojq.CompilerOption
	if len(varNames) > 0 {
		compileOpts = append(compileOpts, gojq.WithVariables(varNames))
	}
	code, err := gojq.Compile(parsed, compileOpts...)
	if err != nil {
		return omap.Value{}, &Error{Op: "compile", Err: err}
	}

	in := omap.ValueToAny(v)
	iter := code.RunWithContext(ctx, in, varValues...)
	first, ok := iter.Next()
	if !ok {
		return omap.Value{}, &Error{Op: "result", Err: errors.New("query produced no output")}
	}
	if runErr, isErr := first.(error); isErr {
		return omap.Value{}, &Error{Op: "runtime", Err: runErr}
	}
	if _, more := iter.Next(); more {
		return omap.Value{}, &Error{Op: "result", Err: errors.New("multi-output queries are not supported in v1; query produced 2+ values (single-output only)")}
	}
	return omap.ValueFromAny(first, v), nil
}

// bindArg converts an Arg to the go-jq-facing value. For --arg (JSON
// false), the value is the literal string. For --argjson, the value
// is the decoded JSON with UseNumber() so integer precision survives.
func bindArg(a Arg) (any, error) {
	if !a.JSON {
		return a.Raw, nil
	}
	dec := stdjson.NewDecoder(strings.NewReader(a.Raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("--argjson %s: %w", a.Name, err)
	}
	return v, nil
}
