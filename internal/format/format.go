// Package format hosts cross-format decoder/encoder concerns.
//
// STORY-0008 introduces Limits: resource bounds applied uniformly at
// every decoder entry so a pathological input (oversized, deeply
// nested, or alias-bombed YAML) cannot exhaust memory or recursion
// before the value surfaces to the query engine. The three bounds:
//
//   - MaxBytes       — hard byte cap on the input stream (M2). Applied
//     via io.LimitReader at the decode entry. Covers file input, glob
//     input, and stdin input uniformly.
//   - MaxDepth       — nesting depth cap (M3). Enforced during node-
//     tree walks by JSON/YAML/TOML decoders.
//   - MaxYAMLNodes   — YAML-only node materialisation cap (M1, billion-
//     laughs mitigation). Counts every node materialisation while
//     walking a decoded yaml.Node tree — aliases that resolve to
//     already-counted nodes expand multiplicatively, so a small input
//     can trigger a huge count. The cap name is "yaml_nodes" rather
//     than "alias_expansion" because the counter counts ALL node
//     materialisations, not only alias-driven ones; the defensive
//     mechanism is identical.
//
// Zero values on Limits mean "no limit for this field". DefaultLimits
// returns the safe defaults the CLI uses when no flag overrides are
// supplied (see SPEC-0001 DR-008 and NFR-3).
package format

import (
	"fmt"
	"io"
)

// Limits configures the resource bounds applied at decode time. See the
// package doc for the individual field contracts. A zero Limits value
// disables every bound — suitable for unit tests that want to exercise
// the decode path without any artificial cap.
type Limits struct {
	// MaxBytes is the maximum number of bytes the decoder will read
	// from the input reader. Values <= 0 disable the cap.
	MaxBytes int64

	// MaxDepth is the maximum nesting depth permitted in the decoded
	// value tree. Top-level is depth 0; each nested mapping/sequence
	// adds 1. Values <= 0 disable the cap.
	MaxDepth int

	// MaxYAMLNodes is the maximum number of node materialisations
	// permitted while walking a YAML node tree. Each alias resolution
	// counts as a fresh materialisation — so a 1KB billion-laughs input
	// that materialises 10^7 nodes is rejected here, not after OOM. The
	// counter covers all node materialisations, not just alias-driven
	// ones, so the name reflects what is measured.
	// YAML-only; ignored by JSON and TOML decoders.
	// Values <= 0 disable the cap.
	MaxYAMLNodes int
}

// Default cap values chosen for SPEC-0001 NFR-3. Rationale:
//
//   - 10 MiB matches the widely-used nginx / envoy default for a
//     "large config file" — generous for real configs, far below the
//     typical CI runner heap budget.
//   - Depth 1000 is the stdlib encoding/json effective ceiling; TOML
//     and YAML cap at the same point so behaviour is uniform across
//     formats.
//   - YAML node cap 100_000 gives real-world YAML anchors room to
//     expand (Helm values, Kustomize overlays) while rejecting billion-
//     laughs constructions that materialise millions of nodes.
//   - DefaultQueryMaxBytes is a dedicated tight cap for --from-file
//     reads: 1 MiB is generous for any realistic jq query text while
//     still preventing a 10 GB .jq file from OOM-ing the CLI.
const (
	DefaultMaxBytes      int64 = 10 * 1024 * 1024
	DefaultMaxDepth      int   = 1000
	DefaultMaxYAMLNodes  int   = 100_000
	DefaultQueryMaxBytes int64 = 1 << 20 // 1 MiB
)

// DefaultLimits returns the safe-by-default Limits the nesdit CLI
// applies when no flag override is supplied.
func DefaultLimits() Limits {
	return Limits{
		MaxBytes:     DefaultMaxBytes,
		MaxDepth:     DefaultMaxDepth,
		MaxYAMLNodes: DefaultMaxYAMLNodes,
	}
}

// LimitKind identifies which bound was exceeded. The string form is
// stable and is embedded in the error message surfaced to stderr. The
// CLI layer maps each kind to a distinct logx event token so operators
// can distinguish failure classes without regex-parsing messages.
type LimitKind string

const (
	LimitInputSize     LimitKind = "input_size"
	LimitDepth         LimitKind = "depth"
	LimitYAMLNodeCount LimitKind = "yaml_node_count"
)

// LimitError is returned by a decoder when an input violates a Limits
// bound. Format identifies the originating decoder ("json", "yaml",
// "toml"); Limit and Observed carry the threshold and the count that
// tripped it so callers can render a precise message.
type LimitError struct {
	Format   string    // "json" | "yaml" | "toml"
	Kind     LimitKind // which bound was exceeded
	Limit    int64     // the configured cap
	Observed int64     // what we saw (>= Limit)
}

// Error renders as `<format>: <kind> limit exceeded: observed N, limit M`.
// The shape is kept stable so CLI-layer emitters can prefix path context
// without reparsing the message.
func (e *LimitError) Error() string {
	return fmt.Sprintf("%s: %s limit exceeded: observed %d, limit %d",
		e.Format, e.Kind, e.Observed, e.Limit)
}

// limitedReader wraps an io.Reader with a byte cap. Unlike io.LimitReader
// (which silently returns EOF at the cap, making overflow indistinguishable
// from a legitimately-sized input), limitedReader allows one extra byte
// read: if that byte materialises, the decoder knows the input was longer
// than the cap and surfaces a LimitError rather than silently truncating.
type limitedReader struct {
	r       io.Reader
	limit   int64 // configured cap
	n       int64 // bytes already consumed
	overrun bool  // set when we read the guard byte
}

// newLimitedReader returns a reader that reads at most limit+1 bytes from
// r. When reading returns more than limit bytes, overrun is set. A limit
// value <= 0 disables the cap (returns r unchanged in a trivial wrapper).
func newLimitedReader(r io.Reader, limit int64) *limitedReader {
	return &limitedReader{r: r, limit: limit}
}

// Read implements io.Reader. It reads up to limit+1 bytes; on the (limit+1)th
// byte it sets overrun=true and returns what it got. Subsequent reads return
// EOF so the decoder's own parser halts at the end of the consumed buffer.
func (lr *limitedReader) Read(p []byte) (int, error) {
	if lr.limit <= 0 {
		return lr.r.Read(p)
	}
	remaining := lr.limit + 1 - lr.n
	if remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := lr.r.Read(p)
	lr.n += int64(n)
	if lr.n > lr.limit {
		lr.overrun = true
	}
	return n, err
}

// tripped reports whether the reader consumed more than limit bytes.
func (lr *limitedReader) tripped() bool { return lr.overrun }

// readAllLimited reads all bytes from r up to limit+1 bytes. Returns
// the bytes read and a LimitError if the cap was exceeded. Format is
// the owning format name, used to construct the error.
//
// When limit <= 0, reads unconditionally via io.ReadAll.
func readAllLimited(r io.Reader, limit int64, formatName string) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}
	lr := newLimitedReader(r, limit)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if lr.tripped() {
		return nil, &LimitError{
			Format:   formatName,
			Kind:     LimitInputSize,
			Limit:    limit,
			Observed: int64(len(data)),
		}
	}
	return data, nil
}

// ReadAllLimited is the exported wrapper for format-package decoders.
// Every decoder entrypoint funnels its io.Reader through this helper so
// the input-size cap is applied in one place. formatName MUST match the
// owning decoder ("json", "yaml", "toml").
func ReadAllLimited(r io.Reader, limit int64, formatName string) ([]byte, error) {
	return readAllLimited(r, limit, formatName)
}
