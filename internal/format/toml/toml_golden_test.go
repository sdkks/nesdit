package toml_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
)

// shouldUpdateGoldenTOML reports whether the UPDATE_GOLDEN env var is set.
// Usage: UPDATE_GOLDEN=1 go test ./... -run TestGolden_
func shouldUpdateGoldenTOML() bool { return os.Getenv("UPDATE_GOLDEN") != "" }

// TestGolden_TOML decodes a canonical TOML fixture from
// testdata/canonical.toml, re-encodes it, and byte-compares the output
// against testdata/canonical.golden.toml.
//
// Normalization documented here (DR-006):
//   - The encoder always uses inline tables ({...}) for nested mappings,
//     regardless of how they appeared in the input. This is by design — inline
//     tables guarantee NFR-3 insertion-order preservation across round-trips,
//     where [header]-style tables would require scalars to precede sections.
//   - The encoder always uses inline arrays ([...]) for sequences.
//   - The canonical input is written in idiomatic TOML (header sections, bare
//     arrays); the golden captures the encoder's normalized inline form.
//   - A trailing newline is present (one "\n" per key-value line).
func TestGolden_TOML(t *testing.T) {
	t.Parallel()

	inputPath := filepath.Join("testdata", "canonical.toml")
	goldenPath := filepath.Join("testdata", "canonical.golden.toml")

	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}

	doc, err := tomlfmt.Decode(bytes.NewReader(inputBytes))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	var buf bytes.Buffer
	if err := tomlfmt.Encode(&buf, doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := buf.Bytes()

	if shouldUpdateGoldenTOML() {
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
	doc2, err := tomlfmt.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("re-decode golden: %v", err)
	}
	var buf2 bytes.Buffer
	if err := tomlfmt.Encode(&buf2, doc2); err != nil {
		t.Fatalf("re-encode golden: %v", err)
	}
	if !bytes.Equal(buf2.Bytes(), got) {
		t.Fatalf("second-round non-idempotent:\nfirst:\n%s\nsecond:\n%s",
			strings.TrimRight(string(got), "\n"),
			strings.TrimRight(buf2.String(), "\n"))
	}
}
