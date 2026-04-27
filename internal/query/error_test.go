package query_test

// Tests for query.Error.Error() and query.Error.Unwrap().
//
// These tests directly exercise the two public methods on *Error that
// downstream callers use for string rendering (fmt, log) and error
// unwrapping (errors.Is / errors.As). Prior coverage only exercised the
// concrete-type match path of errors.As without calling either method.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sdkks/nesdit/internal/query"
)

// errSentinel is a fixed underlying error used in unwrap assertions.
var errSentinel = errors.New("sentinel error")

// Test_Error_Error_Rendering asserts that Error.Error() returns the
// expected "query.<op>: <underlying>" format for each valid Op value.
func Test_Error_Error_Rendering(t *testing.T) {
	t.Parallel()

	cases := []struct {
		op   string
		err  error
		want string
	}{
		{op: "parse", err: errors.New("unexpected token"), want: "query.parse: unexpected token"},
		{op: "compile", err: errors.New("unknown function"), want: "query.compile: unknown function"},
		{op: "runtime", err: errors.New("null has no key"), want: "query.runtime: null has no key"},
		{op: "result", err: errors.New("query produced no output"), want: "query.result: query produced no output"},
	}

	for _, c := range cases {
		c := c
		t.Run(c.op, func(t *testing.T) {
			t.Parallel()
			e := &query.Error{Op: c.op, Err: c.err}
			got := e.Error()
			if got != c.want {
				t.Errorf("Error() = %q, want %q", got, c.want)
			}
		})
	}
}

// Test_Error_Error_NonEmpty asserts that Error.Error() always returns a
// non-empty string, regardless of which Op is used.
func Test_Error_Error_NonEmpty(t *testing.T) {
	t.Parallel()

	e := &query.Error{Op: "parse", Err: errors.New("some failure")}
	got := e.Error()
	if got == "" {
		t.Error("Error() returned empty string, want non-empty")
	}
}

// Test_Error_Error_NilErr asserts that when Err is nil the rendered
// string still includes the op and a "<nil>" placeholder rather than
// panicking.
func Test_Error_Error_NilErr(t *testing.T) {
	t.Parallel()

	e := &query.Error{Op: "result", Err: nil}
	got := e.Error()
	want := "query.result: <nil>"
	if got != want {
		t.Errorf("Error() with nil Err = %q, want %q", got, want)
	}
}

// Test_Error_Unwrap asserts that Unwrap returns the wrapped error and
// that errors.Is correctly traverses the chain (i.e. Unwrap() works).
func Test_Error_Unwrap(t *testing.T) {
	t.Parallel()

	e := &query.Error{Op: "runtime", Err: errSentinel}

	if got := e.Unwrap(); got != errSentinel {
		t.Errorf("Unwrap() = %v, want sentinel", got)
	}
	if !errors.Is(e, errSentinel) {
		t.Errorf("errors.Is(e, errSentinel) = false, want true; Unwrap must be wired correctly")
	}
}

// Test_Error_Unwrap_NilErr asserts that Unwrap returns nil when the
// wrapped error field is nil.
func Test_Error_Unwrap_NilErr(t *testing.T) {
	t.Parallel()

	e := &query.Error{Op: "parse", Err: nil}
	if got := e.Unwrap(); got != nil {
		t.Errorf("Unwrap() with nil Err = %v, want nil", got)
	}
}

// Test_Error_ErrorsAs asserts that errors.As can unwrap a *query.Error
// out of a wrapping fmt.Errorf chain, confirming the type implements the
// errors package contract end-to-end.
func Test_Error_ErrorsAs(t *testing.T) {
	t.Parallel()

	inner := &query.Error{Op: "compile", Err: errors.New("bad function")}
	wrapped := fmt.Errorf("operation failed: %w", inner)

	var target *query.Error
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As did not find *query.Error in wrapped chain")
	}
	if target.Op != "compile" {
		t.Errorf("unwrapped Op = %q, want %q", target.Op, "compile")
	}
}

// Test_Error_NilSafety documents the nil-receiver behaviour of Error()
// and Unwrap(). Both methods perform a pointer dereference on the
// receiver before the nil guard takes effect, so a nil *query.Error
// receiver causes a panic. This is the current contract: callers must
// not call Error() or Unwrap() on a nil pointer.
//
// The test exercises this by catching the panic via recover() so the
// suite still passes; it serves as a contract pin — if the
// implementation is later made nil-safe these tests should be updated
// to assert the safe return values instead.
func Test_Error_NilSafety(t *testing.T) {
	t.Parallel()

	t.Run("Error_panics_on_nil_receiver", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic from (*query.Error)(nil).Error(), but did not panic")
			}
		}()
		var e *query.Error
		_ = e.Error() // expected to panic: nil dereference of e.Op inside Error()
	})

	t.Run("Unwrap_panics_on_nil_receiver", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic from (*query.Error)(nil).Unwrap(), but did not panic")
			}
		}()
		var e *query.Error
		_ = e.Unwrap() // expected to panic: nil dereference of e.Err inside Unwrap()
	})
}
