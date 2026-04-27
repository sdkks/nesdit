package json_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
)

// shouldUpdateGoldenJSON reports whether the UPDATE_GOLDEN environment variable
// is set.
// Usage: UPDATE_GOLDEN=1 go test ./... -run TestGolden_
func shouldUpdateGoldenJSON() bool { return os.Getenv("UPDATE_GOLDEN") != "" }

// TestGolden_JSON decodes a canonical JSON fixture from testdata/canonical.json,
// re-encodes it, and byte-compares the result against the golden file
// testdata/canonical.golden.json. This verifies that the encoder is compatible
// with a reference wire format, not just self-consistent.
//
// Normalization documented here:
//   - JSON encoder emits compact form (no spaces after : or ,).
//   - No trailing newline (RFC 8259 places no such requirement; the encoder
//     omits it for JSONL compatibility).
//   - Keys are in insertion order (NFR-3).
//   - The canonical input file may have a trailing newline which the decoder
//     ignores; the golden captures only the encoder output.
func TestGolden_JSON(t *testing.T) {
	t.Parallel()

	inputPath := filepath.Join("testdata", "canonical.json")
	goldenPath := filepath.Join("testdata", "canonical.golden.json")

	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}

	doc, err := jsonfmt.Decode(bytes.NewReader(inputBytes))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	var buf bytes.Buffer
	if err := jsonfmt.Encode(&buf, doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := buf.Bytes()

	if shouldUpdateGoldenJSON() {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated golden: %s (%d bytes)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Bootstrap: write golden on first run.
			if writeErr := os.WriteFile(goldenPath, got, 0o644); writeErr != nil {
				t.Fatalf("bootstrap golden write: %v", writeErr)
			}
			t.Logf("bootstrapped golden: %s", goldenPath)
			return
		}
		t.Fatalf("read golden: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch for %s:\ngot:\n%s\nwant:\n%s\n(run UPDATE_GOLDEN=1 go test to regenerate)",
			goldenPath, got, want)
	}

	// Second-round stability: encode→decode→encode must be byte-identical.
	doc2, err := jsonfmt.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("re-decode golden: %v", err)
	}
	var buf2 bytes.Buffer
	if err := jsonfmt.Encode(&buf2, doc2); err != nil {
		t.Fatalf("re-encode golden: %v", err)
	}
	if !bytes.Equal(buf2.Bytes(), got) {
		t.Fatalf("second-round non-idempotent:\ngot (1st):\n%s\ngot (2nd):\n%s",
			string(got), buf2.String())
	}
}
