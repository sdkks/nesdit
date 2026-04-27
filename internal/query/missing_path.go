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
// do not exist in in_. Returns nil when no new paths are detected, i.e.
// the query only modified, deleted, or left unchanged existing paths.
//
// Both values must be the result of a single gojq query application on the
// same input: in_ is the pre-query value and out is the post-query value.
//
// The function is deliberately conservative: it only flags KindMap keys that
// are new relative to in_. It does NOT flag:
//   - Type changes on existing keys (e.g. null→string on .x if .x existed).
//   - Deletions (keys in in_ absent from out).
//   - Array index changes within existing bounds.
func CheckNoMissingPaths(in_, out omap.Value) error {
	path := make([]string, 0, 8)
	return checkValue(in_, out, path)
}

// checkValue recursively compares in_ and out at path and returns the first
// new-path error found, or nil.
//
// The check is deliberately narrow: it only fires when BOTH in_ and out are
// the same structural kind at a given position. If the kind changes (e.g.,
// map → seq via `.items`, or map → scalar via `.x`), the output is a
// reshape / extraction — not an absent-path assignment — and is not flagged.
func checkValue(in_, out omap.Value, path []string) error {
	// Only proceed when both sides are the same structural kind.
	// A kind change means the query reshaped / extracted the value, which is
	// legitimate and does not constitute creating a missing path.
	if in_.Kind != out.Kind {
		return nil
	}
	switch out.Kind {
	case omap.KindMap:
		return checkMap(in_, out, path)
	case omap.KindSeq:
		return checkSeq(in_, out, path)
	default:
		// Scalar or null: no sub-keys possible; nothing to check.
		return nil
	}
}

// checkMap checks that every key in out.Map existed in in_.Map (which is
// always KindMap at this point, since checkValue enforces kind parity before
// calling checkMap). It recursively checks nested values.
func checkMap(in_, out omap.Value, path []string) error {
	// in_ is guaranteed to be KindMap by checkValue's kind-parity guard.
	inMap := in_.Map

	var newKeyErr error
	out.Map.Entries(func(k string, outVal omap.Value) bool {
		if !inMap.Has(k) {
			// This key did not exist at all in the input; it is new.
			fullPath := buildPath(path, k)
			newKeyErr = fmt.Errorf(
				"path %s does not exist in the input document; use --create-missing to allow creating new paths",
				fullPath,
			)
			return false // stop iteration
		}
		// Key existed in input; recurse into the value.
		inVal, _ := inMap.Get(k)
		keyPath := append(append([]string(nil), path...), k)
		if err := checkValue(inVal, outVal, keyPath); err != nil {
			newKeyErr = err
			return false
		}
		return true
	})
	return newKeyErr
}

// checkSeq checks array elements. in_ is guaranteed to be KindSeq by
// checkValue's kind-parity guard. Elements within the original length are
// recursed into. Elements appended beyond the original length are new
// (they don't correspond to any position in the input).
func checkSeq(in_, out omap.Value, path []string) error {
	// in_ is guaranteed to be KindSeq by checkValue's kind-parity guard.
	inSeq := in_.Seq
	inLen := len(inSeq)

	for i, outElem := range out.Seq {
		idxPath := append(append([]string(nil), path...), fmt.Sprintf("[%d]", i))
		if i >= inLen {
			// New element appended beyond original array length.
			return fmt.Errorf(
				"path %s does not exist in the input document; use --create-missing to allow creating new paths",
				buildPath(nil, strings.Join(idxPath, "")),
			)
		}
		// Element within original bounds: recurse into map/seq elements.
		inElem := inSeq[i]
		if err := checkValue(inElem, outElem, idxPath); err != nil {
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
