package query_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/query"
)

// Test_Run_RejectsMultiOutput proves that a multi-output gojq program
// (here `.items[]`, which emits N values from an array) is rejected with
// an explicit *Error{Op:"result"} rather than silently dropping the tail.
//
// Multi-output is unsupported; silently dropping extra results is
// strictly worse than an actionable error.
func Test_Run_RejectsMultiOutput(t *testing.T) {
	t.Parallel()

	src := []byte(`{"items":[{"a":1},{"a":2},{"a":3}]}`)
	val, err := jsonfmt.DecodeValue(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	_, err = query.RunValue(context.Background(), val, ".items[]")
	if err == nil {
		t.Fatalf("expected non-nil error for multi-output query; got nil")
	}
	var qErr *query.Error
	if !errors.As(err, &qErr) {
		t.Fatalf("expected *query.Error, got %T: %v", err, err)
	}
	if qErr.Op != "result" {
		t.Fatalf("expected Op=%q, got %q", "result", qErr.Op)
	}
	if !strings.Contains(qErr.Error(), "multi-output") {
		t.Fatalf("expected error message to mention multi-output; got: %s", qErr.Error())
	}
}
