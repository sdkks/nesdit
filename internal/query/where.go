// Package query — where.go implements the --where predicate evaluator (FR-10).
//
// The user supplies the inner predicate expression; this file wraps it as
// `select(<expr>)` for gojq. The select() builtin returns its input unchanged
// when the predicate is truthy and produces no output when it is falsy —
// exactly the semantics needed to filter docs in a stream or batch.
//
// ApplyWhere compiles `select(<predicate>)` once per call and runs it against
// the provided omap.Value. It returns true if the document satisfies the
// predicate, false if select produced no output (falsy predicate), and an
// error for compile or runtime failures.
package query

import (
	"context"
	"fmt"

	"github.com/itchyny/gojq"

	"github.com/sdkks/nesdit/internal/omap"
)

// ApplyWhere evaluates `select(<predicate>)` against doc.
//
// Returns:
//   - (true, nil)  — doc satisfies the predicate; apply the query.
//   - (false, nil) — doc does not satisfy the predicate; pass through / skip.
//   - (false, err) — predicate parse or runtime error.
//
// The predicate is the inner expression only (e.g. `.type == "service"`).
// ApplyWhere wraps it as `select(<predicate>)` internally; the caller MUST NOT
// include the `select(...)` wrapper.
func ApplyWhere(doc omap.Value, predicate string) (bool, error) {
	wrapped := fmt.Sprintf("select(%s)", predicate)
	parsed, err := gojq.Parse(wrapped)
	if err != nil {
		return false, &Error{Op: "parse", Err: fmt.Errorf("--where: %w", err)}
	}
	code, err := gojq.Compile(parsed)
	if err != nil {
		return false, &Error{Op: "compile", Err: fmt.Errorf("--where: %w", err)}
	}

	in := omap.ValueToAny(doc)
	iter := code.RunWithContext(context.Background(), in)
	first, ok := iter.Next()
	if !ok {
		// select produced no output — predicate was falsy.
		return false, nil
	}
	if runErr, isErr := first.(error); isErr {
		return false, &Error{Op: "runtime", Err: fmt.Errorf("--where: %w", runErr)}
	}
	// select returned the document — predicate was truthy.
	return true, nil
}
