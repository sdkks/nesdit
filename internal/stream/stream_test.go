package stream_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/stream"
)

// makeDoc builds a single-key omap.Doc for use in writer tests.
func makeDoc(t *testing.T, key string, val int64) omap.Value {
	t.Helper()
	doc := omap.New()
	doc.Set(key, omap.IntValue(val))
	return omap.MapValue(doc)
}

// TestTOMLWriter_SingleDoc verifies that a single WriteDoc call produces no
// `+++` separator in the output (SC-1).
func TestTOMLWriter_SingleDoc(t *testing.T) {
	var buf bytes.Buffer
	w := stream.NewTOMLWriter(&buf)
	if err := w.WriteDoc(makeDoc(t, "a", 1)); err != nil {
		t.Fatalf("WriteDoc: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "+++") {
		t.Errorf("single doc: unexpected +++ separator in output:\n%s", out)
	}
	if !strings.Contains(out, "a = 1") {
		t.Errorf("single doc: expected 'a = 1' in output, got:\n%s", out)
	}
}

// TestTOMLWriter_TwoDocs verifies that exactly one `+++\n` separator appears
// between two documents — not before the first, not after the second (SC-2).
func TestTOMLWriter_TwoDocs(t *testing.T) {
	var buf bytes.Buffer
	w := stream.NewTOMLWriter(&buf)
	if err := w.WriteDoc(makeDoc(t, "a", 1)); err != nil {
		t.Fatalf("WriteDoc(1): %v", err)
	}
	if err := w.WriteDoc(makeDoc(t, "b", 2)); err != nil {
		t.Fatalf("WriteDoc(2): %v", err)
	}
	out := buf.String()

	// Exactly one separator.
	count := strings.Count(out, "+++\n")
	if count != 1 {
		t.Errorf("two docs: want 1 +++ separator, got %d\noutput:\n%s", count, out)
	}

	// First doc first, no leading separator.
	if strings.HasPrefix(out, "+++") {
		t.Errorf("two docs: output must not start with +++ separator:\n%s", out)
	}

	// First doc before separator; second doc after.
	idx := strings.Index(out, "+++\n")
	if idx < 0 {
		t.Fatalf("two docs: missing +++ separator in output:\n%s", out)
	}
	before := out[:idx]
	after := out[idx+4:]
	if !strings.Contains(before, "a = 1") {
		t.Errorf("two docs: expected 'a = 1' before separator, got:\n%s", before)
	}
	if !strings.Contains(after, "b = 2") {
		t.Errorf("two docs: expected 'b = 2' after separator, got:\n%s", after)
	}

	// No trailing separator after last doc.
	trimmed := strings.TrimRight(out, "\n")
	if strings.HasSuffix(trimmed, "+++") {
		t.Errorf("two docs: output must not end with +++ separator:\n%s", out)
	}
}

// TestTOMLWriter_ThreeDocs verifies that exactly two `+++\n` separators appear
// for three documents (SC-3).
func TestTOMLWriter_ThreeDocs(t *testing.T) {
	var buf bytes.Buffer
	w := stream.NewTOMLWriter(&buf)
	if err := w.WriteDoc(makeDoc(t, "a", 1)); err != nil {
		t.Fatalf("WriteDoc(1): %v", err)
	}
	if err := w.WriteDoc(makeDoc(t, "b", 2)); err != nil {
		t.Fatalf("WriteDoc(2): %v", err)
	}
	if err := w.WriteDoc(makeDoc(t, "c", 3)); err != nil {
		t.Fatalf("WriteDoc(3): %v", err)
	}
	out := buf.String()

	count := strings.Count(out, "+++\n")
	if count != 2 {
		t.Errorf("three docs: want 2 +++ separators, got %d\noutput:\n%s", count, out)
	}
	if strings.HasPrefix(out, "+++") {
		t.Errorf("three docs: output must not start with +++ separator:\n%s", out)
	}
}

// switchWriter forwards writes to okW until switchAfter bytes have been
// written, then switches to failW. Used to let the first doc succeed entirely
// and then make any subsequent write (separator) fail.
type switchWriter struct {
	okW         io.Writer
	failW       io.Writer
	switchAfter int
	written     int
}

func (sw *switchWriter) Write(p []byte) (int, error) {
	if sw.written >= sw.switchAfter {
		return sw.failW.Write(p)
	}
	n, err := sw.okW.Write(p)
	sw.written += n
	return n, err
}

// alwaysFailWriter is an io.Writer that always returns the given error.
type alwaysFailWriter struct{ err error }

func (afw *alwaysFailWriter) Write(_ []byte) (int, error) { return 0, afw.err }

// TestTOMLWriter_SeparatorWriteError verifies that if writing the separator
// fails, WriteDoc returns the error wrapped with context and does not attempt
// to encode the document (SC-5 / FR-TOML-7).
func TestTOMLWriter_SeparatorWriteError(t *testing.T) {
	sentinelErr := errors.New("broken pipe")

	// Measure how many bytes the first doc produces so we can switch writers
	// after exactly that many bytes have been written.
	var probe bytes.Buffer
	probeW := stream.NewTOMLWriter(&probe)
	if err := probeW.WriteDoc(makeDoc(t, "a", 1)); err != nil {
		t.Fatalf("probe WriteDoc: %v", err)
	}
	firstDocLen := probe.Len()

	// Wire: first firstDocLen bytes go to a good buffer; all subsequent writes
	// (i.e. the `+++\n` separator on the second call) go to alwaysFailWriter.
	var goodBuf bytes.Buffer
	sw := &switchWriter{
		okW:         &goodBuf,
		failW:       &alwaysFailWriter{err: sentinelErr},
		switchAfter: firstDocLen,
	}
	w := stream.NewTOMLWriter(sw)

	// First WriteDoc must succeed entirely.
	if err := w.WriteDoc(makeDoc(t, "a", 1)); err != nil {
		t.Fatalf("WriteDoc(1): unexpected error: %v", err)
	}

	// Second WriteDoc: separator write must fail with wrapped error.
	err := w.WriteDoc(makeDoc(t, "b", 2))
	if err == nil {
		t.Fatal("WriteDoc(2): expected error when separator write fails, got nil")
	}
	if !errors.Is(err, sentinelErr) {
		t.Errorf("WriteDoc(2): expected sentinel error in chain, got: %v", err)
	}
	if !strings.Contains(err.Error(), "toml: write separator") {
		t.Errorf("WriteDoc(2): error should contain 'toml: write separator', got: %v", err)
	}
}
