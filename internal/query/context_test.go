package query_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/query"
)

// Test_Run_HonorsContextCancellation proves query.Run plumbs the caller's
// context into gojq's RunWithContext: a pathological query (materializing
// a billion-element range into an array) is aborted when the context's
// deadline fires. This is the seam STORY-0003's --timeout will cancel.
//
// The test asserts both:
//   - query.Run returns a non-nil error (gojq propagates ctx.Err()).
//   - wall-clock elapsed time is well below the query's uncancelled
//     cost (range(1e9) would take seconds to many seconds).
func Test_Run_HonorsContextCancellation(t *testing.T) {
	t.Parallel()

	src := []byte(`{"a":1}`)
	doc, err := jsonfmt.Decode(bytes.NewReader(src))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	// [range(1000000000)] materializes ~10^9 ints into an array; on an
	// uncancelled run this takes seconds and would OOM before finishing.
	// With a 10ms deadline, gojq should abort quickly.
	_, err = query.Run(ctx, doc, ".out = [range(1000000000)]")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected non-nil error from cancelled query; got nil (elapsed=%v)", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("query.Run took %v — expected cancellation well under 1s", elapsed)
	}
}
