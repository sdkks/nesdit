package query

import (
	"bytes"
	"context"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/omap"
)

func TestInspectionResults(t *testing.T) {
	input, err := jsonfmt.DecodeValue(strings.NewReader(`{"z":1,"id":2,"show":true}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		text, want string
		predicate  bool
	}{
		{"true", "", true}, {"false", "", true}, {"null", "exactly one boolean", true},
		{`"text"`, "exactly one boolean", true}, {"1", "exactly one boolean", true},
		{"[]", "exactly one boolean", true}, {"{}", "exactly one boolean", true},
		{"empty", "got none", true}, {"true,false", "got multiple", true},
		{`error("PRIVATE")`, "runtime:", true}, {`true,error("PRIVATE")`, "runtime:", true},
		{`null,error("PRIVATE")`, "runtime:", true}, {`true,false,repeat(0)`, "got multiple", true},
		{"empty", "got none", false}, {"1,2", "got multiple", false},
		{`error("PRIVATE")`, "runtime:", false}, {`1,error("PRIVATE")`, "runtime:", false},
		{"nan", "finite JSON", false}, {"infinite", "finite JSON", false},
	} {
		t.Run(tc.text+map[bool]string{true: "/filter", false: "/project"}[tc.predicate], func(t *testing.T) {
			q, err := CompileInspection(context.Background(), tc.text)
			if err != nil {
				t.Fatal(err)
			}
			if tc.predicate {
				_, err = q.Predicate(context.Background(), input)
			} else {
				_, err = q.Project(context.Background(), input)
			}
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "PRIVATE") {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct{ text, want string }{
		{`{new: .id, z: .z}`, `{"z":1,"new":2}`}, {`[.id,true]`, `[2,true]`},
		{`"hello"`, `"hello"`}, {`12`, `12`}, {`true`, `true`}, {`null`, `null`},
	} {
		q, err := CompileInspection(context.Background(), tc.text)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			result, err := q.Project(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err = jsonfmt.EncodeValue(&out, result); err != nil || out.String() != tc.want {
				t.Fatal(out.String(), err)
			}
		}
	}
	// An updating filter must not change the subsequent projection or original.
	q, _ := CompileInspection(context.Background(), `.id = 99 | true`)
	if match, err := q.Predicate(context.Background(), input); err != nil || !match {
		t.Fatal(match, err)
	}
	p, _ := CompileInspection(context.Background(), `.id`)
	v, err := p.Project(context.Background(), input)
	if err != nil || v.Num.String() != "2" {
		t.Fatal(v, err)
	}
}

func TestInspectionCompileAndCancel(t *testing.T) {
	for _, text := range []string{"", " \n\t", `"PRIVATE`, "unknown_private_function", strings.Repeat(" ", inspectionTextLimit) + "."} {
		if _, err := CompileInspection(context.Background(), text); err == nil || strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "unknown_private") {
			t.Fatal(err)
		}
	}
	if _, err := CompileInspection(context.Background(), "."+strings.Repeat(" ", inspectionTextLimit-1)); err != nil {
		t.Fatal(err)
	}
	for _, expr := range []string{`true, (def forever: forever; forever)`, `def forever: forever; forever`} {
		q, err := CompileInspection(context.Background(), expr)
		if err != nil {
			t.Fatal(err)
		}
		for _, predicate := range []bool{true, false} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			if predicate {
				_, err = q.Predicate(ctx, omap.NullValue())
			} else {
				_, err = q.Project(ctx, omap.NullValue())
			}
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CompileInspection(ctx, "."); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := inspectionPreflight(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Deterministically cancel during traversal, rather than before its first check.
type inspectionCancelContext struct {
	context.Context
	remaining int
}

func (c *inspectionCancelContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestInspectionPreflightBoundaries(t *testing.T) {
	for _, value := range []any{make([]any, 100), map[string]any{"a": []any{1, 2, 3}}} {
		ctx := &inspectionCancelContext{Context: context.Background(), remaining: 3}
		if err := inspectionPreflight(ctx, value); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	var nested any
	for i := 0; i < inspectionDepthLimit; i++ {
		nested = []any{nested}
	}
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"depth-at", nested, ""}, {"depth-over", []any{nested}, "depth"},
		{"nodes-at", make([]any, inspectionNodeLimit-1), ""}, {"nodes-over", make([]any, inspectionNodeLimit), "nodes"},
		{"string-at", strings.Repeat("x", inspectionStringLimit), ""}, {"string-over", strings.Repeat("x", inspectionStringLimit+1), "key/string"},
		{"keys-at", map[string]any{"key": strings.Repeat("x", inspectionStringLimit-3)}, ""},
		{"keys-over", map[string]any{"key": strings.Repeat("x", inspectionStringLimit-2)}, "key/string"},
		{"bad-type", make(chan int), "JSON"}, {"nan", math.NaN(), "finite"}, {"inf", math.Inf(1), "finite"},
		{"big-int", big.NewInt(123), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := inspectionPreflight(context.Background(), tc.value)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
	for _, expr := range []string{`reduce range(1001) as $i (null; [.])`, `[range(1000000)]`, `"x" * 16777217`} {
		q, err := CompileInspection(context.Background(), expr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = q.Project(context.Background(), omap.NullValue()); err == nil || !strings.Contains(err.Error(), "resource:") {
			t.Fatal(err)
		}
	}
}
