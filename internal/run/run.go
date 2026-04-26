// Package run hosts the nesdit CLI entrypoint. It is deliberately a
// non-main package so both cmd/nesdit and the e2e testscript harness
// (test/e2e/main_test.go) can call Run without the `package main` import
// restriction.
//
// STORY-0003 scope: full cobra flag tree for the file→stdout dispatch
// path. Flags wired here: --query, -f/--from-file, --format, --arg,
// --argjson. $${VAR} escape is honoured in the query preprocessor
// inside internal/query. Flag-conflict rejection runs at parse time
// (PreRunE) so combinations like --query + -f error BEFORE any file
// read, per DR-001 / FR-21. Every stderr emission flows through
// internal/logx's canonical-shape formatter (DR-004 / NFR-10).
//
// STDIN mode, -i (in-place), --edit, --check, --dry-run, multi-file
// globs, --backup, and --timeout all land in later stories. STORY-0008
// will wire ctx-driven cancellation through the same RunOptions.Ctx
// already plumbed here.
package run

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/query"
)

// validateJSON verifies that s is a single syntactically valid JSON
// value. Used by --argjson to reject malformed inputs at flag-parse
// time rather than deferring to the query engine.
func validateJSON(s string) error {
	dec := stdjson.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return err
	}
	// Reject trailing garbage so `--argjson v='1 2'` is an error.
	if dec.More() {
		return errors.New("unexpected trailing content")
	}
	return nil
}

// Run is the process-level entrypoint used by cmd/nesdit/main.go. It
// constructs a RunOptions backed by os.Stdin/out/err and delegates to
// Execute. Exit code matches Execute's contract:
//   - 0 — success
//   - 1 — any error
//
// Exit 2 (--check drift) is produced only by the --check path, which
// lands in a later story. STORY-0003 never returns 2.
func Run(args []string) int {
	return Execute(RunOptions{
		Args:   args,
		Ctx:    context.Background(),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
}

// Execute is the in-process entrypoint taking a RunOptions struct. It
// fills in defaults for any zero-valued field and dispatches to the
// cobra command tree.
func Execute(opts RunOptions) int {
	if opts.Ctx == nil {
		opts.Ctx = context.Background()
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.Stdin == nil {
		opts.Stdin = bytes.NewReader(nil)
	}
	if opts.Logger == nil {
		opts.Logger = logx.New(opts.Stderr)
	}

	root := newRootCmd(opts)
	root.SetArgs(opts.Args)
	// Suppress cobra's default usage print on error; we route all
	// stderr through logx to keep the NFR-10 canonical shape.
	root.SilenceUsage = true
	root.SilenceErrors = true

	err := root.ExecuteContext(opts.Ctx)
	if err == nil {
		return 0
	}
	// Translate cobra/flag errors into canonical-shape stderr. Every
	// code path that returns a non-nil error MUST either (a) have
	// already emitted the logx line and be returning a sentinel, or
	// (b) carry an *emittedError we know the logger already handled.
	var emitted *emittedError
	if errors.As(err, &emitted) {
		return 1
	}
	// Default: treat as a flag-parse / cobra-generic error and emit
	// in canonical shape.
	opts.Logger.ErrorGlobal(logx.EventFlagInvalid, err.Error())
	return 1
}

// emittedError wraps an error whose canonical-shape stderr line has
// already been written by logx. Execute recognises the sentinel and
// returns exit 1 without re-emitting.
type emittedError struct{ cause error }

func (e *emittedError) Error() string { return e.cause.Error() }
func (e *emittedError) Unwrap() error { return e.cause }

// newRootCmd wires every flag STORY-0003 ships plus the dispatch
// closure. All state captured via closure is per-invocation — no
// package globals. That's important because tests run many Execute
// calls in parallel.
func newRootCmd(opts RunOptions) *cobra.Command {
	var (
		queryExpr    string
		queryFile    string
		format       string
		argPairs     []string
		argJSONPairs []string
	)

	cmd := &cobra.Command{
		Use:           "nesdit [flags] <file>",
		Short:         "Edit structured config (JSON/YAML/TOML) with jq-style queries",
		Long:          longDesc,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				opts.Logger.ErrorGlobal(logx.EventFlagInvalid, "nesdit requires exactly one file argument")
				return &emittedError{cause: fmt.Errorf("accepts 1 arg(s), received %d", len(args))}
			}
			return nil
		},
		// PreRunE runs after flag parsing, before RunE. FR-21 / DR-001
		// mandates that flag-interaction rejection happens BEFORE any
		// file read or stdin byte — this is where we enforce it.
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			return validateFlagInteraction(opts.Logger, cmd, queryExpr, queryFile)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Build the jq engine's arg list (string args + JSON args).
			jqArgs, err := parseArgPairs(opts.Logger, argPairs, argJSONPairs)
			if err != nil {
				return err
			}
			// Resolve the query text: either --query or file contents.
			qText, err := resolveQueryText(opts.Logger, queryExpr, queryFile)
			if err != nil {
				return err
			}
			// Apply FR-9 `$${VAR}` → `${VAR}` literal escape before
			// handing the text to gojq.
			qText = unescapeDollarBrace(qText)

			return runOnce(cmd.Context(), opts, args[0], qText, format, jqArgs)
		},
	}

	cmd.Flags().StringVar(&queryExpr, "query", "", "jq-style query (default '.' identity when neither --query nor -f is given)")
	cmd.Flags().StringVarP(&queryFile, "from-file", "f", "", "load query from file (mutually exclusive with --query)")
	cmd.Flags().StringVar(&format, "format", "", "force input format (json|yaml|toml); default is extension-based detection")
	cmd.Flags().StringArrayVar(&argPairs, "arg", nil, "bind $K=V in the query as a literal string (repeatable)")
	cmd.Flags().StringArrayVar(&argJSONPairs, "argjson", nil, "bind $K=V in the query as a JSON-decoded value (repeatable)")

	cmd.SetOut(opts.Stdout)
	cmd.SetErr(opts.Stderr)
	return cmd
}

const longDesc = `nesdit reads a single JSON, YAML, or TOML document, applies a jq-style
query, and writes the result to stdout.

STORY-0003 ships the file→stdout path with --query / --from-file,
--arg / --argjson, --format override, and $${VAR} literal-escape. The
in-place (-i), STDIN stream, --edit, --dry-run, --check, and --timeout
flags land in later stories.`

// validateFlagInteraction enforces the FR-21 / DR-001 matrix for the
// flags STORY-0003 actually parses. Today that is just --query vs -f
// (FR-13 mutual exclusion). As later stories add -i, --edit, --check,
// --dry-run, --backup, this function grows; keeping it in one place
// is the explicit contract from DR-001.
func validateFlagInteraction(log *logx.Logger, cmd *cobra.Command, _, _ string) error {
	qSet := cmd.Flag("query").Changed
	fSet := cmd.Flag("from-file").Changed
	if qSet && fSet {
		msg := "--query and --from-file are mutually exclusive: provide the query inline OR via a file, not both"
		log.ErrorGlobal(logx.EventFlagConflict, msg)
		return &emittedError{cause: errors.New(msg)}
	}
	return nil
}

// parseArgPairs converts the raw `K=V` strings from --arg / --argjson
// into query.Arg values. Any malformed pair or --argjson value that
// fails JSON decode is surfaced as a canonical-shape error and a
// sentinel error returned.
func parseArgPairs(log *logx.Logger, argPairs, argJSONPairs []string) ([]query.Arg, error) {
	out := make([]query.Arg, 0, len(argPairs)+len(argJSONPairs))
	for _, p := range argPairs {
		a, err := splitKV(p, "--arg")
		if err != nil {
			log.ErrorGlobal(logx.EventArgDecode, err.Error())
			return nil, &emittedError{cause: err}
		}
		out = append(out, query.Arg{Name: a.Name, JSON: false, Raw: a.Raw})
	}
	for _, p := range argJSONPairs {
		a, err := splitKV(p, "--argjson")
		if err != nil {
			log.ErrorGlobal(logx.EventArgDecode, err.Error())
			return nil, &emittedError{cause: err}
		}
		// Validate that the JSON decodes, but keep the raw form —
		// internal/query binds it via the jq runtime so the raw bytes
		// go through gojq's own parser.
		if err := validateJSON(a.Raw); err != nil {
			msg := fmt.Sprintf("--argjson %s: expected JSON, got %q", a.Name, a.Raw)
			log.ErrorGlobal(logx.EventArgDecode, msg)
			return nil, &emittedError{cause: errors.New(msg)}
		}
		out = append(out, query.Arg{Name: a.Name, JSON: true, Raw: a.Raw})
	}
	return out, nil
}

type kvPair struct {
	Name string
	Raw  string
}

func splitKV(s, flag string) (kvPair, error) {
	eq := strings.IndexByte(s, '=')
	if eq <= 0 {
		return kvPair{}, fmt.Errorf("%s: expected K=V form, got %q", flag, s)
	}
	name := s[:eq]
	if !isJQName(name) {
		return kvPair{}, fmt.Errorf("%s: invalid variable name %q (must match [A-Za-z_][A-Za-z0-9_]*)", flag, name)
	}
	return kvPair{Name: name, Raw: s[eq+1:]}, nil
}

func isJQName(s string) bool {
	if s == "" {
		return false
	}
	first := s[0]
	if !(first == '_' || (first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z')) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		ok := c == '_' ||
			(c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

// resolveQueryText returns the final query source text. If neither
// --query nor --from-file is set, returns the identity query ".".
// Mutual exclusion is enforced by validateFlagInteraction already;
// this function trusts that invariant.
func resolveQueryText(log *logx.Logger, queryExpr, queryFile string) (string, error) {
	if queryFile != "" {
		data, err := os.ReadFile(queryFile) //nolint:gosec // user-supplied path by design
		if err != nil {
			msg := fmt.Sprintf("%s: %s", queryFile, err.Error())
			log.ErrorGlobal(logx.EventFromFileRead, msg)
			return "", &emittedError{cause: errors.New(msg)}
		}
		return string(data), nil
	}
	if queryExpr != "" {
		return queryExpr, nil
	}
	return ".", nil
}

// unescapeDollarBrace implements FR-9's `$${VAR}` → `${VAR}` literal
// escape. The tool NEVER performs shell-env interpolation — only the
// explicit escape is honoured. Single `${...}` stays literal in
// whatever context jq parses it (jq treats `$var` as a variable
// reference; `${VAR}` is simply not a jq variable form).
func unescapeDollarBrace(q string) string {
	return strings.ReplaceAll(q, "$${", "${")
}

// runOnce is the STORY-0003 file→stdout slice: decode, query, encode.
// Every error path emits in canonical shape via opts.Logger and
// returns a sentinel so Execute can exit 1 without double-logging.
func runOnce(ctx context.Context, opts RunOptions, path, queryExpr, overrideFormat string, args []query.Arg) error {
	fmtName := overrideFormat
	if fmtName == "" {
		fmtName = detectFormatByExt(path)
	}
	if fmtName == "" {
		msg := "cannot detect format (supported: json, yaml, yml, toml); use --format to override"
		opts.Logger.Error(logx.EventFormatUnknown, path, msg)
		return &emittedError{cause: errors.New(msg)}
	}

	data, err := os.ReadFile(path) //nolint:gosec // CLI reads a user-supplied path by design.
	if err != nil {
		opts.Logger.Error(logx.EventIORead, path, err.Error())
		return &emittedError{cause: err}
	}

	doc, err := decodeFormat(fmtName, data)
	if err != nil {
		opts.Logger.Error(logx.EventParseError, path, err.Error())
		return &emittedError{cause: err}
	}

	out, err := query.RunWithArgs(ctx, doc, queryExpr, args)
	if err != nil {
		opts.Logger.Error(classifyQueryErr(err), path, err.Error())
		return &emittedError{cause: err}
	}

	var buf bytes.Buffer
	if err := encodeFormat(fmtName, &buf, out); err != nil {
		// Distinguish cross-format incompatibility (FR-19) from
		// generic encode failures for better user diagnostics.
		var encErr *omap.EncodeError
		if errors.As(err, &encErr) {
			opts.Logger.Error(logx.EventFormatIncompatible, path, err.Error())
		} else {
			opts.Logger.Error(logx.EventEncodeError, path, err.Error())
		}
		return &emittedError{cause: err}
	}
	if _, err := opts.Stdout.Write(buf.Bytes()); err != nil {
		opts.Logger.Error(logx.EventIOWrite, path, err.Error())
		return &emittedError{cause: err}
	}
	return nil
}

// classifyQueryErr maps a *query.Error's Op classifier onto a logx
// event token. Unknown shapes fall through to query.runtime as a safe
// default.
func classifyQueryErr(err error) logx.Event {
	var qErr *query.Error
	if !errors.As(err, &qErr) {
		return logx.EventQueryRuntime
	}
	switch qErr.Op {
	case "parse":
		return logx.EventQueryParse
	case "compile":
		return logx.EventQueryCompile
	case "result":
		return logx.EventQueryResult
	default:
		return logx.EventQueryRuntime
	}
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
