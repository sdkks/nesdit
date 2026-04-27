package yaml_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
)

// shouldUpdateGolden reports whether the UPDATE_GOLDEN env var is set.
// Usage: UPDATE_GOLDEN=1 go test ./... -run TestGolden_
func shouldUpdateGolden() bool { return os.Getenv("UPDATE_GOLDEN") != "" }

// TestGolden_YAML decodes a canonical YAML fixture from
// testdata/canonical.yaml, re-encodes it, and byte-compares the output
// against testdata/canonical.golden.yaml.
//
// Normalization documented here:
//   - Indentation: yaml.v3 with SetIndent(2) uses 4-space indent by default
//     for block sequences/mappings (the indent parameter sets the *additional*
//     offset per nesting level; yaml.v3's default block offset remains 4).
//     The golden captures the encoder's actual output — the input file uses
//     the same indent so input and golden are identical for this corpus.
//   - Quoting: the encoder force-quotes !!str scalars that would be re-inferred
//     as non-string types (e.g. timestamps, integers, booleans). The canonical
//     corpus uses only unambiguous plain strings and does not trigger quoting.
//   - The encoder always emits a trailing newline (yaml.v3 Encode contract).
func TestGolden_YAML(t *testing.T) {
	t.Parallel()

	inputPath := filepath.Join("testdata", "canonical.yaml")
	goldenPath := filepath.Join("testdata", "canonical.golden.yaml")

	inputBytes, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatalf("read input: %v", err)
	}

	doc, err := yamlfmt.Decode(bytes.NewReader(inputBytes))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	var buf bytes.Buffer
	if err := yamlfmt.Encode(&buf, doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got := buf.Bytes()

	if shouldUpdateGolden() {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("updated golden: %s (%d bytes)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Bootstrap: write golden on first run so CI doesn't fail on
			// a missing file before a developer runs UPDATE_GOLDEN=1.
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

	// Second-round stability: encode→decode→encode must be byte-identical to
	// the first encode (idempotency per NFR-2).
	doc2, err := yamlfmt.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("re-decode golden: %v", err)
	}
	var buf2 bytes.Buffer
	if err := yamlfmt.Encode(&buf2, doc2); err != nil {
		t.Fatalf("re-encode golden: %v", err)
	}
	if !bytes.Equal(buf2.Bytes(), got) {
		t.Fatalf("second-round non-idempotent:\nfirst:\n%s\nsecond:\n%s",
			strings.TrimRight(string(got), "\n"),
			strings.TrimRight(buf2.String(), "\n"))
	}
}
