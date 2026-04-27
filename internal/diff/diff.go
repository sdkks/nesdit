// Package diff implements the --edit expression builder's diff engine
// (STORY-0007, FR-4, Solution §2 "Diff engine").
//
// Given a before and after omap.Value (decoded from the original and
// edited temp file), the engine:
//
//  1. Walks both trees in lockstep to collect a list of Change records.
//  2. Synthesises a jq-style query from the change list
//     (e.g. `.replicas = 5 | .image = "nginx"`).
//  3. Emits the formatted output in the H2-mandated order:
//     (a) invocation example  — `nesdit <file> --query '<query>'`
//     (b) suggested query     — one assignment per changed field
//     (c) source-format preview — the edited content with a `# <fmt>` hint
//
// --arg/--argjson heuristic: if a changed string value matches the pattern
// `^[A-Za-z_][A-Za-z0-9_-]{2,}$` (looks like a named identifier), or a
// changed value is a bare number, the engine also emits a parameterised
// `--arg` or `--argjson` variant.
//
// Fallback: if the change count exceeds maxChanges (50), or every depth
// in the tree diverges, a full-replace query at the document root is
// emitted with a note.
package diff

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/sdkks/nesdit/internal/omap"
)

const maxChanges = 50

// Change records a single leaf-level difference between before and after.
type Change struct {
	// Path is the dot-notation jq path to the changed field,
	// e.g. ".replicas" or ".metadata.labels[\"app\"]".
	Path string
	// NewVal is the after value in its encoded (JSON literal) form, e.g.
	// "5", "\"nginx\"", "true", "null".
	NewVal string
	// Deleted is true when the key existed in before but not in after.
	Deleted bool
}

// Diff walks the before and after values and returns the minimal list of
// Change records. A non-nil error is returned only for pathological inputs
// (encoding failures). If the change list is too large to synthesise a
// targeted query, a single full-replace Change at path "." is returned.
func Diff(before, after omap.Value) ([]Change, error) {
	var changes []Change
	if err := walkDiff("", before, after, &changes); err != nil {
		return nil, err
	}
	if len(changes) > maxChanges {
		// Fall back to full-replace at root.
		root, err := encodeValue(after)
		if err != nil {
			return nil, err
		}
		return []Change{{Path: ".", NewVal: root}}, nil
	}
	return changes, nil
}

// walkDiff recursively compares before and after at the given path prefix,
// appending discovered Changes to *out.
func walkDiff(pathPrefix string, before, after omap.Value, out *[]Change) error {
	// If both are maps, diff key-by-key.
	if before.Kind == omap.KindMap && after.Kind == omap.KindMap {
		return walkMaps(pathPrefix, before.Map, after.Map, out)
	}
	// If both are sequences of the same length, diff index-by-index.
	if before.Kind == omap.KindSeq && after.Kind == omap.KindSeq {
		if len(before.Seq) == len(after.Seq) {
			return walkSeqs(pathPrefix, before.Seq, after.Seq, out)
		}
		// Length differs: full-replace the array at this path.
		return appendChange(pathPrefix, after, out)
	}
	// Scalar / cross-type: encode both and compare.
	bEnc, err := encodeValue(before)
	if err != nil {
		return err
	}
	aEnc, err := encodeValue(after)
	if err != nil {
		return err
	}
	if bEnc != aEnc {
		return appendChange(pathPrefix, after, out)
	}
	return nil
}

func walkMaps(pathPrefix string, before, after *omap.Doc, out *[]Change) error {
	// Keys present in after.
	if after != nil {
		for _, key := range after.Keys() {
			childPath := buildPath(pathPrefix, key)
			afterVal, _ := after.Get(key)
			if before == nil {
				// Entire key added.
				if err := appendChange(childPath, afterVal, out); err != nil {
					return err
				}
				continue
			}
			beforeVal, exists := before.Get(key)
			if !exists {
				// New key.
				if err := appendChange(childPath, afterVal, out); err != nil {
					return err
				}
				continue
			}
			// Key exists in both: recurse.
			if err := walkDiff(childPath, beforeVal, afterVal, out); err != nil {
				return err
			}
		}
	}
	// Keys present in before but not in after → deletions.
	if before != nil {
		afterMap := make(map[string]struct{})
		if after != nil {
			for _, k := range after.Keys() {
				afterMap[k] = struct{}{}
			}
		}
		for _, key := range before.Keys() {
			if _, ok := afterMap[key]; !ok {
				childPath := buildPath(pathPrefix, key)
				*out = append(*out, Change{Path: childPath, Deleted: true})
			}
		}
	}
	return nil
}

func walkSeqs(pathPrefix string, before, after []omap.Value, out *[]Change) error {
	for i := range before {
		childPath := fmt.Sprintf("%s[%d]", pathPrefix, i)
		if err := walkDiff(childPath, before[i], after[i], out); err != nil {
			return err
		}
	}
	return nil
}

func appendChange(path string, val omap.Value, out *[]Change) error {
	enc, err := encodeValue(val)
	if err != nil {
		return err
	}
	*out = append(*out, Change{Path: path, NewVal: enc})
	return nil
}

// buildPath builds a jq-style path segment. Safe identifiers use dot
// notation; keys with special characters use bracket notation.
func buildPath(prefix, key string) string {
	if isSafeKey(key) {
		if prefix == "" {
			return "." + key
		}
		return prefix + "." + key
	}
	// Bracket notation with JSON-encoded key.
	enc, _ := json.Marshal(key)
	if prefix == "" {
		return ".[" + string(enc) + "]"
	}
	return prefix + "[" + string(enc) + "]"
}

// isSafeKey returns true when key can be used as a bare jq identifier.
func isSafeKey(key string) bool {
	if key == "" {
		return false
	}
	first := key[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || first == '_') {
		return false
	}
	for i := 1; i < len(key); i++ {
		c := key[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// encodeValue encodes a Value as a JSON-literal string for query synthesis.
// Maps are encoded as JSON objects; sequences as JSON arrays; scalars by
// their kind. This is used only for query synthesis (the preview uses the
// format encoder directly).
func encodeValue(v omap.Value) (string, error) {
	switch v.Kind {
	case omap.KindNull:
		return "null", nil
	case omap.KindBool:
		if v.Bool {
			return "true", nil
		}
		return "false", nil
	case omap.KindNum:
		return string(v.Num), nil
	case omap.KindStr:
		b, err := json.Marshal(v.Str)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case omap.KindSeq:
		var parts []string
		for _, item := range v.Seq {
			enc, err := encodeValue(item)
			if err != nil {
				return "", err
			}
			parts = append(parts, enc)
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	case omap.KindMap:
		// Build a JSON object from the ordered map.
		var parts []string
		if v.Map != nil {
			for _, k := range v.Map.Keys() {
				child, _ := v.Map.Get(k)
				keyEnc, _ := json.Marshal(k)
				valEnc, err := encodeValue(child)
				if err != nil {
					return "", err
				}
				parts = append(parts, string(keyEnc)+":"+valEnc)
			}
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	default:
		return "null", nil
	}
}

// nameLikePattern matches strings that look like named identifiers and
// thus are candidates for --arg parameterization.
var nameLikePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{2,}$`)

// SynthesizeQuery builds a single jq query string from a list of Changes.
// Multiple changes are chained with ` | `. Deletions use `del(.<path>)`.
// Returns "(full-replace query)" note for the root-replace fallback.
func SynthesizeQuery(changes []Change) string {
	if len(changes) == 0 {
		return ""
	}
	// Root fallback.
	if len(changes) == 1 && changes[0].Path == "." {
		return ". = " + changes[0].NewVal
	}
	var parts []string
	for _, c := range changes {
		if c.Deleted {
			parts = append(parts, "del("+c.Path+")")
		} else {
			parts = append(parts, c.Path+" = "+c.NewVal)
		}
	}
	return strings.Join(parts, " | ")
}

// ArgSuggestion holds a suggested --arg or --argjson parameterisation for
// a single change.
type ArgSuggestion struct {
	// Flag is "--arg" or "--argjson".
	Flag string
	// VarName is the suggested variable name (derived from the path).
	VarName string
	// VarValue is the raw value to pass to the flag.
	VarValue string
	// QueryWithVar is the query using $varName instead of the literal.
	QueryWithVar string
	// Path is the jq path for this change (for building the example invocation).
	Path string
}

// SuggestArgs returns parameterisation suggestions for Changes whose new
// values look like named identifiers (--arg) or bare numbers (--argjson).
// Returns nil when no change is parameterisable.
func SuggestArgs(changes []Change) []ArgSuggestion {
	var out []ArgSuggestion
	for _, c := range changes {
		if c.Deleted || c.Path == "." {
			continue
		}
		// Determine the var name from the leaf path segment.
		varName := leafName(c.Path)
		if varName == "" {
			continue
		}
		// Try --arg (string): value is a quoted JSON string.
		if strings.HasPrefix(c.NewVal, `"`) && strings.HasSuffix(c.NewVal, `"`) {
			// Unquote the string.
			var s string
			if err := json.Unmarshal([]byte(c.NewVal), &s); err == nil {
				if nameLikePattern.MatchString(s) {
					out = append(out, ArgSuggestion{
						Flag:         "--arg",
						VarName:      varName,
						VarValue:     s,
						QueryWithVar: c.Path + " = $" + varName,
						Path:         c.Path,
					})
					continue
				}
			}
		}
		// Try --argjson (bare number).
		if isNumericLiteral(c.NewVal) {
			out = append(out, ArgSuggestion{
				Flag:         "--argjson",
				VarName:      varName,
				VarValue:     c.NewVal,
				QueryWithVar: c.Path + " = $" + varName,
				Path:         c.Path,
			})
		}
	}
	return out
}

// leafName extracts the last path segment for use as a variable name.
// ".metadata.labels" → "labels", ".[\"key\"]" → "key", ".[0]" → "".
func leafName(path string) string {
	if path == "." {
		return ""
	}
	// Find last '.' or '[' that starts the final segment.
	lastDot := strings.LastIndex(path, ".")
	lastBracket := strings.LastIndex(path, "[")
	if lastBracket > lastDot {
		// Bracket notation: extract key.
		closeIdx := strings.LastIndex(path, "]")
		if closeIdx < 0 {
			return ""
		}
		inner := path[lastBracket+1 : closeIdx]
		var s string
		if err := json.Unmarshal([]byte(inner), &s); err == nil && isSafeKey(s) {
			return s
		}
		return ""
	}
	if lastDot < 0 {
		return ""
	}
	seg := path[lastDot+1:]
	if isSafeKey(seg) {
		return seg
	}
	return ""
}

func isNumericLiteral(s string) bool {
	var n json.Number
	return json.Unmarshal([]byte(s), &n) == nil
}

// Result holds the fully formatted --edit output ready to write to stdout.
type Result struct {
	// Query is the synthesized jq query, e.g. `.replicas = 5`.
	Query string
	// InvocationExample is the suggested `nesdit <file> --query '<query>'`
	// string (the H2 first section).
	InvocationExample string
	// SuggestedQuery is the `.key = value` per-assignment form (H2 second
	// section). Equal to Query for single-assignment changes; pipe-chained
	// for multi-assignment.
	SuggestedQuery string
	// ArgExamples holds any --arg / --argjson parameterised invocations.
	ArgExamples []string
	// Preview is the source-format-encoded after document (H2 third section).
	Preview string
	// FormatHint is the language comment prefix, e.g. "# yaml".
	FormatHint string
}

// Format renders a Result to the writer in the H2 output order:
//  1. Invocation example
//  2. Suggested query (one per changed field)
//  3. --arg/--argjson examples (if any)
//  4. Source-format preview with language hint comment
func (r Result) Format(w io.Writer) error {
	_, _ = fmt.Fprintln(w, r.InvocationExample)
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Suggested query:")
	_, _ = fmt.Fprintln(w, r.SuggestedQuery)
	if len(r.ArgExamples) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "Parameterised variants:")
		for _, ex := range r.ArgExamples {
			_, _ = fmt.Fprintln(w, ex)
		}
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, r.FormatHint)
	_, _ = fmt.Fprint(w, r.Preview)
	return nil
}

// shellescape applies a minimal POSIX single-quote escape to s: any
// single-quote character (') is replaced with the sequence '\”, which
// closes the current quoted string, emits a literal single-quote via
// backslash, and re-opens the quoted string. Applied to file paths and
// query strings embedded in single-quoted shell invocation examples so
// that a path or query containing a single-quote does not break the
// shell syntax of the example output.
func shellescape(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// BuildResult constructs a Result from a change list, the file path, the
// format name, and the format-encoded after document.
func BuildResult(filePath, fmtName, preview string, changes []Change) Result {
	query := SynthesizeQuery(changes)
	invocation := fmt.Sprintf("nesdit %s --query '%s'", shellescape(filePath), shellescape(query))

	// Per-assignment lines for the "Suggested query" section.
	var queryLines []string
	for _, c := range changes {
		if c.Deleted {
			queryLines = append(queryLines, "del("+c.Path+")")
		} else {
			queryLines = append(queryLines, c.Path+" = "+c.NewVal)
		}
	}
	suggestedQuery := strings.Join(queryLines, "\n")

	// --arg/--argjson examples.
	suggestions := SuggestArgs(changes)
	var argExamples []string
	for _, s := range suggestions {
		argExamples = append(argExamples,
			fmt.Sprintf("nesdit %s %s %s=%s --query '%s'",
				shellescape(filePath), s.Flag, s.VarName, s.VarValue, shellescape(s.QueryWithVar)))
	}

	return Result{
		Query:             query,
		InvocationExample: invocation,
		SuggestedQuery:    suggestedQuery,
		ArgExamples:       argExamples,
		Preview:           preview,
		FormatHint:        "# " + fmtName,
	}
}
