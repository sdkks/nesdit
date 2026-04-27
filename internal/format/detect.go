package format

import (
	"bytes"
	"io"
)

// detectPeekSize is the maximum number of bytes read to determine the format
// of a stream. Resolves SPEC-0001 Open Question 6.
const detectPeekSize = 512

// Detect peeks at the first detectPeekSize bytes of r and returns the most
// likely format name ("json", "jsonl", "yaml", or "toml"). JSON is preferred
// over YAML when the content is ambiguous (a JSON value is also valid YAML).
//
// Detection heuristics (applied in order):
//
//  1. JSON: first non-whitespace byte is `{` or `[`. A single-object/array
//     input is classified as "json". When multiple lines each start with `{`
//     or `[` the input is classified as "jsonl".
//  2. YAML: content contains `---` or has key: value pairs not preceded by
//     `[`/`{` (YAML-specific syntax).
//  3. TOML: content contains `=` assignments or `[` section headers on their
//     own line (characteristic TOML syntax).
//
// Returns "" when detection is inconclusive.
//
// Note: Detect may return "json" for single-document JSON input. Callers
// passing the result to stream.NewReader or stream.NewWriter should treat
// "json" and "jsonl" equivalently — both factory functions accept "json" as
// an alias for "jsonl".
//
// r is not consumed: Detect wraps the peeked bytes in a new reader that the
// caller MUST use instead of r. The returned io.Reader replays the peeked
// bytes before handing off to r.
func Detect(r io.Reader) (fmtName string, combined io.Reader) {
	peek := make([]byte, detectPeekSize)
	n, _ := io.ReadFull(r, peek)
	peek = peek[:n]
	// combined replays peeked bytes then continues reading r.
	combined = io.MultiReader(bytes.NewReader(peek), r)
	fmtName = detectBytes(peek)
	return fmtName, combined
}

// detectBytes applies the heuristic rules to a byte slice peeked from stdin.
func detectBytes(data []byte) string {
	if len(data) == 0 {
		return ""
	}

	// Strip leading whitespace/newlines to find the first content byte.
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if len(trimmed) == 0 {
		return ""
	}
	first := trimmed[0]

	// --- JSON / JSONL detection ---
	// First non-whitespace byte is `{` or `[`: this is JSON or JSONL.
	if first == '{' || first == '[' {
		// Count lines that start with `{` or `[` (non-empty lines).
		jsonLines := 0
		totalNonEmpty := 0
		for _, line := range bytes.Split(data, []byte("\n")) {
			stripped := bytes.TrimLeft(line, " \t")
			if len(stripped) == 0 {
				continue
			}
			totalNonEmpty++
			if stripped[0] == '{' || stripped[0] == '[' {
				jsonLines++
			}
		}
		// If multiple non-empty lines and all look like JSON objects/arrays,
		// classify as JSONL. If it's a single JSON value, classify as JSON.
		if totalNonEmpty > 1 && jsonLines == totalNonEmpty {
			return "jsonl"
		}
		return "json"
	}

	// --- YAML detection ---
	// YAML documents start with `---`, contain `: ` key-value pairs, or
	// have multi-line structure typical of YAML.
	if bytes.Contains(data, []byte("---")) {
		return "yaml"
	}
	if bytes.Contains(data, []byte(": ")) || bytes.Contains(data, []byte(":\n")) {
		// Looks like YAML key: value syntax.
		return "yaml"
	}

	// --- TOML detection ---
	// TOML documents contain `= ` or `=[` or `=true`/`=false`/`=number`
	// patterns, or `[section]` headers on their own line.
	if containsTOMLAssignment(data) {
		return "toml"
	}

	return ""
}

// containsTOMLAssignment reports whether data contains characteristic TOML
// syntax: `key = value` assignments or `[section]` headers on their own line.
func containsTOMLAssignment(data []byte) bool {
	for _, line := range bytes.Split(data, []byte("\n")) {
		stripped := bytes.TrimLeft(line, " \t")
		if len(stripped) == 0 {
			continue
		}
		// Section header: [foo] or [[foo]]
		if stripped[0] == '[' {
			return true
		}
		// Key assignment: contains `=`
		if bytes.Contains(stripped, []byte("=")) {
			return true
		}
	}
	return false
}
