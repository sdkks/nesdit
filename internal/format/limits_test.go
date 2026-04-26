package format_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/format"
)

// TestDefaultLimits_Nonzero pins the default policy so a regression (e.g.
// "zero all defaults for speed") is caught at CI time. Every bound must
// be positive by default.
func TestDefaultLimits_Nonzero(t *testing.T) {
	t.Parallel()
	l := format.DefaultLimits()
	if l.MaxBytes <= 0 {
		t.Errorf("MaxBytes=%d; want > 0", l.MaxBytes)
	}
	if l.MaxDepth <= 0 {
		t.Errorf("MaxDepth=%d; want > 0", l.MaxDepth)
	}
	if l.MaxAliasExpansions <= 0 {
		t.Errorf("MaxAliasExpansions=%d; want > 0", l.MaxAliasExpansions)
	}
}

// TestReadAllLimited_SubCap covers the sub-cap path: input smaller than
// limit round-trips bytes unchanged with no LimitError.
func TestReadAllLimited_SubCap(t *testing.T) {
	t.Parallel()
	in := []byte("hello world")
	got, err := format.ReadAllLimited(bytes.NewReader(in), int64(len(in)+100), "json")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !bytes.Equal(got, in) {
		t.Errorf("got %q want %q", got, in)
	}
}

// TestReadAllLimited_AtCap tests the boundary — input exactly at the cap
// is accepted.
func TestReadAllLimited_AtCap(t *testing.T) {
	t.Parallel()
	in := bytes.Repeat([]byte("a"), 1024)
	got, err := format.ReadAllLimited(bytes.NewReader(in), int64(len(in)), "yaml")
	if err != nil {
		t.Fatalf("at-cap read err: %v", err)
	}
	if len(got) != len(in) {
		t.Errorf("got len=%d want %d", len(got), len(in))
	}
}

// TestReadAllLimited_AboveCap tests that inputs larger than the cap
// produce a *format.LimitError with Kind=LimitInputSize carrying the
// format name the caller requested.
func TestReadAllLimited_AboveCap(t *testing.T) {
	t.Parallel()
	in := bytes.Repeat([]byte("b"), 200)
	_, err := format.ReadAllLimited(bytes.NewReader(in), 100, "toml")
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	var lim *format.LimitError
	if !errors.As(err, &lim) {
		t.Fatalf("want *format.LimitError, got %T: %v", err, err)
	}
	if lim.Kind != format.LimitInputSize {
		t.Errorf("Kind=%q want %q", lim.Kind, format.LimitInputSize)
	}
	if lim.Format != "toml" {
		t.Errorf("Format=%q want %q", lim.Format, "toml")
	}
	if lim.Limit != 100 {
		t.Errorf("Limit=%d want 100", lim.Limit)
	}
	if !strings.Contains(err.Error(), "input_size limit exceeded") {
		t.Errorf("Error() = %q; missing canonical kind phrase", err.Error())
	}
}

// TestReadAllLimited_DisabledWhenZero confirms MaxBytes<=0 disables the
// cap and reads the full buffer regardless of size.
func TestReadAllLimited_DisabledWhenZero(t *testing.T) {
	t.Parallel()
	in := bytes.Repeat([]byte("x"), 50_000)
	got, err := format.ReadAllLimited(bytes.NewReader(in), 0, "yaml")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != len(in) {
		t.Errorf("got len=%d want %d", len(got), len(in))
	}
}

// TestReadAllLimited_ReadError propagates underlying reader errors
// untouched (no wrapping in a LimitError).
func TestReadAllLimited_ReadError(t *testing.T) {
	t.Parallel()
	want := errors.New("boom")
	_, err := format.ReadAllLimited(&erroringReader{err: want}, 1024, "json")
	if !errors.Is(err, want) {
		t.Errorf("got %v want wrap of %v", err, want)
	}
}

type erroringReader struct{ err error }

func (e *erroringReader) Read(_ []byte) (int, error) { return 0, e.err }

// TestLimitError_Unwrap_Sentinel sanity-checks the error is a
// distinguishable type (errors.As works) so CLI classifiers can match on
// it.
func TestLimitError_Unwrap_Sentinel(t *testing.T) {
	t.Parallel()
	e := &format.LimitError{Format: "yaml", Kind: format.LimitDepth, Limit: 10, Observed: 11}
	var lim *format.LimitError
	if !errors.As(e, &lim) {
		t.Fatalf("errors.As failed on %T", e)
	}
	if lim != e {
		t.Errorf("errors.As did not preserve pointer identity")
	}
	// Silence unused io import in some build configurations.
	_ = io.EOF
}
