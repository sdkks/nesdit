// Package run hosts the nesdit CLI entrypoint. It is deliberately a
// non-main package so both cmd/nesdit and the e2e testscript harness
// (test/e2e/main_test.go) can call Run without the `package main` import
// restriction.
//
// STORY-0002 scope: a minimal single-file / single-query path so the
// omap↔go-jq bridge can be demonstrated end-to-end. The full cobra flag
// tree (`-i`, `--dry-run`, `--check`, `--edit`, `--arg`, multi-file,
// STDIN, etc.) lands in STORY-0003.
package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/query"
)

// Run is the nesdit CLI entrypoint. Accepts the raw argv tail
// (os.Args[1:]) and returns the process exit code. Stdout/stderr go to
// the process's os.Stdout / os.Stderr.
//
// Exit codes follow SPEC-0001 DR-002:
//   - 0 — success
//   - 1 — any error (parse / query / encode / IO / flag / etc.)
//
// Exit 2 (--check drift) lands with STORY-0003 when --check arrives.
func Run(args []string) int {
	return WithIO(args, os.Stdout, os.Stderr)
}

// WithIO is Run but with caller-supplied output streams. Intended
// primarily for the e2e test harness (test/e2e/main_test.go) and any
// in-process embedder that wants to capture stdout/stderr without
// mutating the global file descriptors.
func WithIO(args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout, stderr)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		return 1
	}
	return 0
}

func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		queryExpr string
		format    string
	)
	cmd := &cobra.Command{
		Use:   "nesdit [flags] <file>",
		Short: "Edit structured config (JSON/YAML/TOML) with jq-style queries",
		Long: `nesdit reads a single JSON, YAML, or TOML document, applies a jq-style
query, and writes the result to stdout.

This is the STORY-0002 minimal surface: single file, single query,
output to stdout. In-place editing (-i), multi-file globs, and --edit mode
land in STORY-0003.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(_ *cobra.Command, args []string) error {
			return runOnce(stdout, stderr, args[0], queryExpr, format)
		},
	}
	cmd.Flags().StringVarP(&queryExpr, "query", "q", ".", "jq-style query (default '.' identity)")
	cmd.Flags().StringVar(&format, "format", "", "force input format (json|yaml|toml); default is extension-based detection")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd
}

// runOnce decodes path, applies the query, encodes to stdout.
func runOnce(stdout, stderr io.Writer, path, queryExpr, overrideFormat string) error {
	format := overrideFormat
	if format == "" {
		format = detectFormatByExt(path)
	}
	if format == "" {
		return fmt.Errorf("nesdit: %s: format.unknown: cannot detect format (supported: json, yaml, yml, toml); use --format to override", path)
	}

	data, err := os.ReadFile(path) //nolint:gosec // CLI reads a user-supplied path by design.
	if err != nil {
		return fmt.Errorf("nesdit: %s: io.read: %w", path, err)
	}

	doc, err := decodeFormat(format, data)
	if err != nil {
		return fmt.Errorf("nesdit: %s: parse.error: %w", path, err)
	}

	// STORY-0002 wires context.Background() — the seam STORY-0003's
	// `--timeout` will populate. gojq now runs under a cancellable ctx,
	// so plumbing a deadline later is additive, not a breaking change.
	out, err := query.Run(context.Background(), doc, queryExpr)
	if err != nil {
		return fmt.Errorf("nesdit: %s: %w", path, err)
	}

	var buf bytes.Buffer
	if err := encodeFormat(format, &buf, out); err != nil {
		return fmt.Errorf("nesdit: %s: encode.error: %w", path, err)
	}
	if _, err := stdout.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("nesdit: %s: io.write: %w", path, err)
	}
	_ = stderr // reserved for future event logging
	return nil
}

// detectFormatByExt maps a filename extension to a known format name.
// Returns "" when no known extension matches.
func detectFormatByExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	default:
		return ""
	}
}

func decodeFormat(format string, data []byte) (*omap.Doc, error) {
	switch format {
	case "json":
		return jsonfmt.Decode(bytes.NewReader(data))
	case "yaml":
		return yamlfmt.Decode(bytes.NewReader(data))
	case "toml":
		return tomlfmt.Decode(bytes.NewReader(data))
	default:
		return nil, fmt.Errorf("unknown format %q", format)
	}
}

func encodeFormat(format string, w io.Writer, d *omap.Doc) error {
	switch format {
	case "json":
		return jsonfmt.Encode(w, d)
	case "yaml":
		return yamlfmt.Encode(w, d)
	case "toml":
		return tomlfmt.Encode(w, d)
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}
