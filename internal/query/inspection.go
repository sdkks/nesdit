package query

import (
	"context"
	"errors"
	"math"
	"math/big"
	"strings"

	"github.com/itchyny/gojq"
	"github.com/sdkks/nesdit/internal/omap"
)

const (
	inspectionTextLimit   = 1 << 20
	inspectionDepthLimit  = 1000
	inspectionNodeLimit   = 1_000_000
	inspectionStringLimit = 16 << 20
)

// Inspection compiles a trusted payload expression once for a live session.
// Its diagnostics never include query text or values returned by the evaluator.
type Inspection struct{ code *gojq.Code }

// CompileInspection validates and compiles one bounded inline expression.
func CompileInspection(ctx context.Context, text string) (*Inspection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(text) > inspectionTextLimit {
		return nil, errors.New("expression exceeds 1 MiB")
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("expression must not be blank")
	}
	parsed, err := gojq.Parse(text)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, errors.New("parse: invalid expression")
	}
	code, err := gojq.Compile(parsed)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, errors.New("compile: invalid expression")
	}
	return &Inspection{code: code}, nil
}

func (q *Inspection) one(ctx context.Context, input omap.Value) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Normalization and updates may mutate their input. Each evaluation owns a
	// fresh conversion, separate from both the original and the other program.
	iter := q.code.RunWithContext(ctx, omap.ValueToAny(input))
	first, ok := iter.Next()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, failed := first.(error); failed {
		return nil, errors.New("runtime: evaluation failed")
	}
	if !ok {
		return nil, errors.New("result: expected exactly one value, got none")
	}
	second, more := iter.Next()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, failed := second.(error); failed {
		return nil, errors.New("runtime: evaluation failed")
	}
	if more {
		return nil, errors.New("result: expected exactly one value, got multiple")
	}
	return first, nil
}

// Predicate requires exactly one boolean; false is ordinary suppression.
func (q *Inspection) Predicate(ctx context.Context, input omap.Value) (bool, error) {
	v, err := q.one(ctx, input)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, errors.New("result: filter must return exactly one boolean")
	}
	return b, nil
}

// Project creates a single JSON view without file-editing missing-path guards.
func (q *Inspection) Project(ctx context.Context, input omap.Value) (omap.Value, error) {
	v, err := q.one(ctx, input)
	if err != nil {
		return omap.Value{}, err
	}
	if err := inspectionPreflight(ctx, v); err != nil {
		return omap.Value{}, err
	}
	result := omap.ValueFromAny(v, input)
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	return result, nil
}

// Check raw results iteratively before the recursive bridge. These are result
// budgets, not a bound on allocations performed by gojq before it yields.
func inspectionPreflight(ctx context.Context, value any) error {
	type entry struct {
		value any
		depth int
	}
	stack := []entry{{value, 0}}
	nodes, textBytes := 1, 0
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch v := item.value.(type) {
		case nil, bool, int:
		case *big.Int:
			if v == nil {
				return errors.New("result: projection must return a JSON value")
			}
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return errors.New("result: projection must return a finite JSON number")
			}
		case string:
			textBytes += len(v)
		case []any:
			if item.depth >= inspectionDepthLimit {
				return errors.New("resource: projection exceeds depth 1000")
			}
			nodes += len(v)
			if nodes > inspectionNodeLimit {
				return errors.New("resource: projection exceeds 1000000 value nodes")
			}
			for _, child := range v {
				if err := ctx.Err(); err != nil {
					return err
				}
				stack = append(stack, entry{child, item.depth + 1})
			}
		case map[string]any:
			if item.depth >= inspectionDepthLimit {
				return errors.New("resource: projection exceeds depth 1000")
			}
			nodes += len(v)
			if nodes > inspectionNodeLimit {
				return errors.New("resource: projection exceeds 1000000 value nodes")
			}
			for key, child := range v {
				if err := ctx.Err(); err != nil {
					return err
				}
				textBytes += len(key)
				if textBytes > inspectionStringLimit {
					return errors.New("resource: projection exceeds 16 MiB key/string bytes")
				}
				stack = append(stack, entry{child, item.depth + 1})
			}
		default:
			return errors.New("result: projection must return a JSON value")
		}
		if textBytes > inspectionStringLimit {
			return errors.New("resource: projection exceeds 16 MiB key/string bytes")
		}
	}
	return nil
}
