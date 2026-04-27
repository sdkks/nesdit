// Package query — missing_path.go implements FR-16 strict-by-default
// path-creation semantics for nesdit.
//
// STORY-0012 / FR-16: gojq transparently creates absent paths when a `=`
// assignment targets a key or nested key that does not exist in the input
// (e.g. `.b.c = 2` on `{"a":1}` produces `{"a":1,"b":{"c":2}}`). This is
// jq-compatible and convenient for intentional scaffolding, but silent path
// creation can mask typos in mutation queries (e.g. `.replicsa = 3` instead
// of `.replicas = 3` creates a new key rather than erroring).
//
// The rule: without --create-missing the call layer MUST check for newly
// introduced paths after the query runs and reject with an informative
// error. With --create-missing the gojq default behaviour is left intact.
//
// Detection strategy: recursively walk the output Value. For every KindMap
// node at a path p, check whether the corresponding node in the input was
// also KindMap; for each key k in the output map, if k does not exist at
// the same position in the input map, it is a new (missing) path. This
// catches both top-level additions (`.newkey = v`) and nested additions
// (`.a.b.newkey = v`).
//
// Array elements are matched positionally: output[i] is compared against
// input[i]. An element beyond the original length is always "new" — a
// query like `.items += [...]` would trigger this. If you want to extend
// arrays you need --create-missing.
//
// Scalars that changed kind (e.g. null → object) are NOT flagged as new
// paths because they are a replacement of an existing path, not a creation
// of an absent one.
package query

import (
	"fmt"
	"strings"

	"github.com/sdkks/nesdit/internal/omap"
)

// CheckNoMissingPaths returns a descriptive error (listing the first new
// path found) if out contains any map keys or array index positions that
// do not exist in orig. Returns nil when no new paths are detected, i.e.
// the query only modified, deleted, or left unchanged existing paths.
//
// Both values must be the result of a single gojq query application on the
// same input: orig is the pre-query value and out is the post-query value.
//
// The function is deliberately conservative: it only flags KindMap keys that
// are new relative to orig. It does NOT flag:
//   - Type changes on existing keys (e.g. null→string on .x if .x existed).
//   - Deletions (keys in orig absent from out).
//   - Array index changes within existing bounds.
func CheckNoMissingPaths(orig, out omap.Value) error {
	path := make([]string, 0, 8)
	return checkValue(orig, out, path)
}

// checkValue recursively compares orig and out at path and returns the first
// new-path error found, or nil.
//
// The check is deliberately narrow: it only fires when BOTH orig and out are
// the same structural kind at a given position. If the kind changes (e.g.,
// map → seq via `.items`, or map → scalar via `.x`), the output is a
// reshape / extraction — not an absent-path assignment — and is not flagged.
func checkValue(orig, out omap.Value, path []string) error {
	if orig.Kind != out.Kind {
		return nil
	}
	switch out.Kind {
	case omap.KindMap:
		return checkMap(orig, out, path)
	case omap.KindSeq:
		return checkSeq(orig, out, path)
	default:
		return nil
	}
}

// checkMap checks that every key in out.Map existed in orig.Map (which is
// always KindMap at this point, since checkValue enforces kind parity before
// calling checkMap). It recursively checks nested values.
func checkMap(orig, out omap.Value, path []string) error {
	origMap := orig.Map

	var newKeyErr error
	out.Map.Entries(func(k string, outVal omap.Value) bool {
		if !origMap.Has(k) {
			fullPath := buildPath(path, k)
			newKeyErr = fmt.Errorf(
				"path %s does not exist in the input document; use --create-missing to allow creating new paths",
				fullPath,
			)
			return false
		}
		origVal, _ := origMap.Get(k)
		keyPath := append(append([]string(nil), path...), k)
		if err := checkValue(origVal, outVal, keyPath); err != nil {
			newKeyErr = err
			return false
		}
		return true
	})
	return newKeyErr
}

// checkSeq checks array elements. orig is guaranteed to be KindSeq by
// checkValue's kind-parity guard. Elements within the original length are
// recursed into. Elements appended beyond the original length are new
// (they don't correspond to any position in the input).
func checkSeq(orig, out omap.Value, path []string) error {
	origSeq := orig.Seq
	origLen := len(origSeq)

	for i, outElem := range out.Seq {
		idxPath := append(append([]string(nil), path...), fmt.Sprintf("[%d]", i))
		if i >= origLen {
			return fmt.Errorf(
				"path %s does not exist in the input document; use --create-missing to allow creating new paths",
				buildPath(nil, strings.Join(idxPath, "")),
			)
		}
		origElem := origSeq[i]
		if err := checkValue(origElem, outElem, idxPath); err != nil {
			return err
		}
	}
	return nil
}

// buildPath formats a dot-separated path for error messages. parent is the
// prefix (possibly empty); key is the field name at the current level.
func buildPath(parent []string, key string) string {
	var b strings.Builder
	for _, p := range parent {
		if strings.HasPrefix(p, "[") {
			b.WriteString(p)
		} else {
			b.WriteByte('.')
			b.WriteString(p)
		}
	}
	if strings.HasPrefix(key, "[") {
		b.WriteString(key)
	} else {
		b.WriteByte('.')
		b.WriteString(key)
	}
	return b.String()
}
