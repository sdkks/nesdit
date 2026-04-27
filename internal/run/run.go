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
	"time"

	"github.com/spf13/cobra"

	"github.com/sdkks/nesdit/internal/format"
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
//   - 2 — --check drift detected (DR-002: only this path returns 2)
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
		return int(ExitOK)
	}
	// DR-002: driftError is the sole source of exit 2. The --check path
	// returns this sentinel when the query would change the input.
	var drift *driftError
	if errors.As(err, &drift) {
		return int(ExitDrift)
	}
	// Translate cobra/flag errors into canonical-shape stderr. Every
	// code path that returns a non-nil error MUST either (a) have
	// already emitted the logx line and be returning a sentinel, or
	// (b) carry an *emittedError we know the logger already handled.
	var emitted *emittedError
	if errors.As(err, &emitted) {
		return int(ExitError)
	}
	// Default: treat as a flag-parse / cobra-generic error and emit
	// in canonical shape.
	opts.Logger.ErrorGlobal(logx.EventFlagInvalid, err.Error())
	return int(ExitError)
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
		queryExpr     string
		queryFile     string
		formatName    string
		argPairs      []string
		argJSONPairs  []string
		inPlace       bool
		dryRun        bool
		check         bool
		editMode      bool
		timeout       time.Duration
		maxBytes      int64
		maxDepth      int
		maxYAMLNodes  int
		maxQueryBytes int64
	)
	// STORY-0008 defaults come from the format package so the CLI and
	// tests share one source of truth.
	defaults := format.DefaultLimits()
	maxBytes = defaults.MaxBytes
	maxDepth = defaults.MaxDepth
	maxYAMLNodes = defaults.MaxYAMLNodes
	maxQueryBytes = format.DefaultQueryMaxBytes

	cmd := &cobra.Command{
		Use:           "nesdit [flags] <file> [<file>...]",
		Short:         "Edit structured config (JSON/YAML/TOML) with jq-style queries",
		Long:          longDesc,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			// --edit mode requires exactly one file argument (FR-4).
			if cmd.Flag("edit").Changed {
				if len(args) != 1 {
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, "--edit requires exactly one file argument")
					return &emittedError{cause: fmt.Errorf("--edit accepts 1 arg(s), received %d", len(args))}
				}
				return nil
			}
			// -i mode accepts one or more file arguments (multi-file / glob).
			// Non-i mode requires exactly one file argument OR no arguments
			// (STDIN mode, STORY-0005 FR-3).
			if cmd.Flag("in-place").Changed {
				if len(args) < 1 {
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, "nesdit -i requires at least one file argument")
					return &emittedError{cause: fmt.Errorf("accepts at least 1 arg(s), received %d", len(args))}
				}
				return nil
			}
			// Zero args → STDIN mode (FR-3). One arg of "-" → STDIN mode.
			// Two or more args without -i → error (multi-file requires -i).
			if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
				return nil
			}
			if len(args) != 1 {
				opts.Logger.ErrorGlobal(logx.EventFlagInvalid, "nesdit requires exactly one file argument (or no arguments for STDIN mode)")
				return &emittedError{cause: fmt.Errorf("accepts 1 arg(s), received %d", len(args))}
			}
			return nil
		},
		// PreRunE runs after flag parsing, before RunE. FR-21 / DR-001
		// mandates that flag-interaction rejection happens BEFORE any
		// file read or stdin byte — this is where we enforce it.
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			return validateFlagInteraction(opts.Logger, cmd)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			limits := format.Limits{
				MaxBytes:     maxBytes,
				MaxDepth:     maxDepth,
				MaxYAMLNodes: maxYAMLNodes,
			}

			// --edit (FR-4, STORY-0007): interactive expression-builder mode.
			// --edit is mutually exclusive with every query flag (--query,
			// --from-file, --arg, --argjson) and mode flag (-i, -n, --check).
			// Flag conflicts are already validated by validateFlagInteraction.
			if editMode {
				if len(args) != 1 {
					msg := "--edit requires exactly one file argument"
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, msg)
					return &emittedError{cause: errors.New(msg)}
				}
				return runEdit(opts, args[0], formatName, limits)
			}

			// Build the jq engine's arg list (string args + JSON args).
			jqArgs, err := parseArgPairs(opts.Logger, argPairs, argJSONPairs)
			if err != nil {
				return err
			}
			// Resolve the query text: either --query or file contents.
			qText, err := resolveQueryText(opts.Logger, queryExpr, queryFile, maxQueryBytes)
			if err != nil {
				return err
			}
			// Apply FR-9 `$${VAR}` → `${VAR}` literal escape before
			// handing the text to gojq.
			qText = unescapeDollarBrace(qText)

			// DR-001 precedence: -i + -n → -n wins with warning.
			// DR-001 precedence: -i + --check → --check wins with warning.
			// These are already validated by validateFlagInteraction (which
			// emits the warn line); here we just override the effective mode.
			effectiveInPlace := inPlace && !dryRun && !check

			// SF-2 / STORY-0006: --dry-run and --check operate on exactly
			// one file. Passing two or more files with either flag is an
			// error (silently dropping files 2..N would be confusing).
			if (dryRun || check) && len(args) > 1 {
				opts.Logger.ErrorGlobal(logx.EventFlagInvalid, "--dry-run and --check support exactly one file argument")
				return &emittedError{cause: fmt.Errorf("--dry-run and --check support exactly one file argument")}
			}

			// --dry-run (-n, FR-11): emit unified diff; no file write.
			if dryRun {
				if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
					// STDIN + --dry-run is not yet supported; treat as error.
					msg := "--dry-run requires a file argument (STDIN mode is not supported with --dry-run)"
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, msg)
					return &emittedError{cause: errors.New(msg)}
				}
				return runDryRun(cmd.Context(), opts, args[0], qText, formatName, jqArgs, limits, timeout)
			}

			// --check (FR-12): compare encoded result to re-encoded original.
			if check {
				if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
					// STDIN + --check is not yet supported; treat as error.
					msg := "--check requires a file argument (STDIN mode is not supported with --check)"
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, msg)
					return &emittedError{cause: errors.New(msg)}
				}
				return runCheck(cmd.Context(), opts, args[0], qText, formatName, jqArgs, limits, timeout)
			}

			// -i / --in-place: route through the two-pass file orchestrator.
			if effectiveInPlace {
				return runFiles(cmd.Context(), opts, args, qText, formatName, jqArgs, limits, timeout)
			}

			// STDIN mode (FR-3, STORY-0005): no file args, or explicit "-".
			if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
				fmtName, resolvedOpts, fmtErr := stdinFormatName(opts, formatName)
				if fmtErr != nil {
					opts.Logger.ErrorGlobal(logx.EventFormatUnknown, fmtErr.Error())
					return &emittedError{cause: fmtErr}
				}
				return runStdin(cmd.Context(), resolvedOpts, fmtName, qText, jqArgs, limits, timeout)
			}

			// Default: single-file file→stdout path (STORY-0003).
			return runOnce(cmd.Context(), opts, args[0], qText, formatName, jqArgs, limits, timeout)
		},
	}

	cmd.Flags().StringVar(&queryExpr, "query", "", "jq-style query (default '.' identity when neither --query nor -f is given)")
	cmd.Flags().StringVarP(&queryFile, "from-file", "f", "", "load query from file (mutually exclusive with --query)")
	cmd.Flags().StringVar(&formatName, "format", "", "force input format (json|yaml|toml); default is extension-based detection")
	cmd.Flags().StringArrayVar(&argPairs, "arg", nil, "bind $K=V in the query as a literal string (repeatable)")
	cmd.Flags().StringArrayVar(&argJSONPairs, "argjson", nil, "bind $K=V in the query as a JSON-decoded value (repeatable)")
	// -i / --in-place flag (STORY-0004 FR-1): edit files in-place atomically.
	cmd.Flags().BoolVarP(&inPlace, "in-place", "i", false, "edit file(s) in-place using atomic temp+rename writes")
	// STORY-0006 flags: --dry-run (-n, FR-11) and --check (FR-12).
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "emit a unified diff of before/after to stdout; do not write any file")
	cmd.Flags().BoolVar(&check, "check", false, "exit 2 if the query would change the input; exit 0 if identical; exit 1 on error")
	// STORY-0007 flag: --edit (FR-4). Long-only per DR-003; -e is reserved.
	cmd.Flags().BoolVar(&editMode, "edit", false, "open $EDITOR on a temp copy of the file; diff before/after and emit suggested query")
	// STORY-0008 flags. Defaults come from format.DefaultLimits() so the
	// safe-by-default semantics live in one place.
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "cancel the query after this duration (e.g. 500ms, 30s); 0 disables the cap")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", defaults.MaxBytes, "reject inputs larger than this many bytes; 0 disables the cap")
	cmd.Flags().IntVar(&maxDepth, "max-depth", defaults.MaxDepth, "reject documents nested deeper than this; 0 disables the cap")
	cmd.Flags().IntVar(&maxYAMLNodes, "max-yaml-nodes", defaults.MaxYAMLNodes, "YAML node-materialisation cap (billion-laughs mitigation); 0 disables the cap")
	cmd.Flags().Int64Var(&maxQueryBytes, "max-query-bytes", format.DefaultQueryMaxBytes, "reject query files (--from-file) larger than this many bytes; 0 disables the cap")

	cmd.SetOut(opts.Stdout)
	cmd.SetErr(opts.Stderr)
	return cmd
}

const longDesc = `nesdit reads a single JSON, YAML, or TOML document, applies a jq-style
query, and writes the result to stdout.

Supported today:
  - File input with --query '<jq>' or --from-file / -f <path>.
  - --arg K=V (string) and --argjson K=V (JSON-decoded) bindings.
  - --format <json|yaml|toml> to override extension-based detection.
  - $${VAR} literal escape in a query (nesdit never expands shell env).
  - -i / --in-place to edit files atomically in place.
  - -n / --dry-run to preview changes as a unified diff (no writes).
  - --check to gate on drift: exit 2 if the query changes the input.
  - --timeout <dur> to cancel a runaway query on a deadline.
  - --max-bytes, --max-depth, --max-yaml-nodes, and --max-query-bytes
    resource caps for decode-phase hardening.

The STDIN stream and --edit modes are still future work.`

// flagConflictRule is one row of the FR-21 / DR-001 flag-interaction
// matrix for ERROR cells. If every flag in IfSet is explicitly set, and
// any flag in ThenNotSet is also explicitly set, the invocation is
// rejected with Event/Msg in canonical stderr shape, BEFORE any file read
// or stdin byte consumed.
//
// Each rule matches cobra's `Flag.Changed` — defaults do not trigger.
// Flag names are the long forms registered on the root command.
// Keep rows sorted by primary flag for easy reading.
type flagConflictRule struct {
	IfSet      []string
	ThenNotSet []string
	Event      logx.Event
	Msg        string
}

// flagPrecedenceRule is one row of the FR-21 / DR-001 flag-interaction
// matrix for ALLOW-with-warning cells. If every flag in IfSet is
// explicitly set, and any flag in AlsoSet is also explicitly set, the
// invocation continues but a flag.precedence warn is emitted on stderr.
// This covers the -i + -n and -i + --check cases (DR-001).
type flagPrecedenceRule struct {
	IfSet   []string
	AlsoSet []string
	Msg     string
}

// flagConflictRules is the FR-21 ERROR matrix. Today there is one cell:
// `--query` and `-f/--from-file` are mutually exclusive (FR-13).
// STORY-0006 adds: `--dry-run` and `--check` are mutually exclusive.
// STORY-0007 adds: `--edit` is mutually exclusive with -i, --dry-run, --check.
var flagConflictRules = []flagConflictRule{
	{
		IfSet:      []string{"query"},
		ThenNotSet: []string{"from-file"},
		Event:      logx.EventFlagConflict,
		Msg:        "--query and --from-file are mutually exclusive: provide the query inline OR via a file, not both",
	},
	{
		// FR-21: --dry-run and --check are mutually exclusive.
		IfSet:      []string{"dry-run"},
		ThenNotSet: []string{"check"},
		Event:      logx.EventFlagConflict,
		Msg:        "--dry-run and --check are mutually exclusive: --dry-run prints a diff, --check only signals drift via exit code",
	},
	{
		// FR-21: --edit and -i are mutually exclusive.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"in-place"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit and -i are mutually exclusive: --edit always prints to stdout; use the emitted query with -i in a second invocation",
	},
	{
		// FR-21: --edit and --dry-run are mutually exclusive.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"dry-run"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit is interactive and emits its own preview; --dry-run is not compatible",
	},
	{
		// FR-21: --edit and --check are mutually exclusive.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"check"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit is interactive; --check is for non-interactive drift detection",
	},
	{
		// SF-1: --edit and --query are mutually exclusive.
		// --edit opens the file in an editor and derives the query from the diff;
		// --query supplies the query directly. Combining them is undefined.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"query"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit derives the query from your edits; --query is not compatible (provide one or the other)",
	},
	{
		// SF-1: --edit and --from-file are mutually exclusive.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"from-file"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit derives the query from your edits; --from-file is not compatible (provide one or the other)",
	},
	{
		// SF-1: --edit and --arg are mutually exclusive.
		// --edit does not execute a query, so variable bindings have no effect.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"arg"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit does not execute a query; --arg bindings are not compatible",
	},
	{
		// SF-1: --edit and --argjson are mutually exclusive.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"argjson"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit does not execute a query; --argjson bindings are not compatible",
	},
}

// flagPrecedenceRules is the FR-21 ALLOW-with-warning matrix (DR-001).
// When -i is set alongside a dominanting flag, the dominating flag wins
// and a flag.precedence warn is emitted. No error, no abort.
var flagPrecedenceRules = []flagPrecedenceRule{
	{
		// DR-001: -i + -n → -n wins.
		IfSet:   []string{"in-place"},
		AlsoSet: []string{"dry-run"},
		Msg:     "-i ignored because --dry-run is set",
	},
	{
		// DR-001: -i + --check → --check wins.
		IfSet:   []string{"in-place"},
		AlsoSet: []string{"check"},
		Msg:     "-i ignored because --check is set",
	},
}

// validateFlagInteraction enforces the FR-21 / DR-001 matrix defined in
// flagConflictRules (ERROR cells) and flagPrecedenceRules (WARN cells).
// Error rules return an emittedError and abort; precedence rules emit a
// warn line and allow the run to continue with the dominating flag.
// Keeps the name and signature stable because the cobra PreRunE closure
// in newRootCmd references it by name.
func validateFlagInteraction(log *logx.Logger, cmd *cobra.Command) error {
	changed := func(name string) bool {
		f := cmd.Flag(name)
		return f != nil && f.Changed
	}
	allSet := func(names []string) bool {
		for _, n := range names {
			if !changed(n) {
				return false
			}
		}
		return len(names) > 0
	}
	anySet := func(names []string) bool {
		for _, n := range names {
			if changed(n) {
				return true
			}
		}
		return false
	}
	// ERROR cells: reject before any IO.
	for _, rule := range flagConflictRules {
		if allSet(rule.IfSet) && anySet(rule.ThenNotSet) {
			log.ErrorGlobal(rule.Event, rule.Msg)
			return &emittedError{cause: errors.New(rule.Msg)}
		}
	}
	// WARN cells: emit flag.precedence warning but continue.
	for _, rule := range flagPrecedenceRules {
		if allSet(rule.IfSet) && anySet(rule.AlsoSet) {
			log.WarnGlobal(logx.EventFlagPrecedence, rule.Msg)
		}
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
//
// STORY-0008 MUST-FIX: the --from-file read is capped by maxQueryBytes
// so a pathological 10 GB .jq file cannot OOM the CLI. The cap is
// applied via format.ReadAllLimited streaming — the file is never fully
// materialised before the cap runs. A LimitError surfaces as the
// canonical decoder.limit.input_size event with Format "query" so
// operators can distinguish query-size rejections from document-size
// rejections. maxQueryBytes <= 0 disables the cap.
func resolveQueryText(log *logx.Logger, queryExpr, queryFile string, maxQueryBytes int64) (string, error) {
	if queryFile != "" {
		f, err := os.Open(queryFile) //nolint:gosec // user-supplied path by design
		if err != nil {
			msg := fmt.Sprintf("%s: %s", queryFile, err.Error())
			log.ErrorGlobal(logx.EventFromFileRead, msg)
			return "", &emittedError{cause: errors.New(msg)}
		}
		defer f.Close()
		data, err := format.ReadAllLimited(f, maxQueryBytes, "query")
		if err != nil {
			var lim *format.LimitError
			if errors.As(err, &lim) {
				msg := fmt.Sprintf("%s: %s", queryFile, err.Error())
				log.ErrorGlobal(classifyDecodeErr(err), msg)
				return "", &emittedError{cause: errors.New(msg)}
			}
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
//
// STORY-0008: `limits` are applied at decode time so oversize /
// deeply-nested / alias-bombed inputs fail fast with canonical-shape
// stderr. `timeout` (when > 0) wraps ctx with context.WithTimeout so a
// pathological gojq query cancels on deadline and emits query.timeout.
func runOnce(ctx context.Context, opts RunOptions, path, queryExpr, overrideFormat string, args []query.Arg, limits format.Limits, timeout time.Duration) error {
	fmtName := overrideFormat
	if fmtName == "" {
		fmtName = detectFormatByExt(path)
	}
	if fmtName == "" {
		msg := "cannot detect format (supported: json, yaml, yml, toml); use --format to override"
		opts.Logger.Error(logx.EventFormatUnknown, path, msg)
		return &emittedError{cause: errors.New(msg)}
	}

	// Open the file as a stream. The decoder applies the byte cap via
	// format.ReadAllLimited on the reader, so a file larger than
	// limits.MaxBytes surfaces a LimitError after consuming at most
	// MaxBytes+1 bytes — we never buffer the whole file in memory
	// before the cap runs. This matters: pre-fix a 10 GB YAML would
	// OOM at os.ReadFile, long before the cap got a chance.
	f, err := os.Open(path) //nolint:gosec // CLI reads a user-supplied path by design.
	if err != nil {
		opts.Logger.Error(logx.EventIORead, path, err.Error())
		return &emittedError{cause: err}
	}
	defer f.Close()

	val, err := decodeFormatValueWithLimits(fmtName, f, limits)
	if err != nil {
		opts.Logger.Error(classifyDecodeErr(err), path, err.Error())
		return &emittedError{cause: err}
	}

	// Apply --timeout (STORY-0008 M4) to the query phase only. Decode
	// and encode are bounded by the decoder limits above; only the
	// gojq runtime can diverge on a pathological query like
	// `[range(1e12)]`. A zero duration means "no timeout".
	queryCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	outVal, err := query.RunValueWithArgs(queryCtx, val, queryExpr, args)
	if err != nil {
		opts.Logger.Error(classifyQueryErr(queryCtx, err, timeout), path, err.Error())
		return &emittedError{cause: err}
	}

	var buf bytes.Buffer
	if err := encodeFormatValue(fmtName, &buf, outVal); err != nil {
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
//
// STORY-0008: when the query context finished with DeadlineExceeded,
// classify as query.timeout so operators can filter timeouts distinctly
// from generic runtime errors. ctx.Err() is authoritative because gojq
// wraps the deadline-exceeded error in its own runtime-error shape
// before it reaches callers.
//
// The ctx.Err() check is intentionally NOT gated on timeout > 0: a
// parent-supplied deadline (e.g. a test harness or an upstream request
// context) should also classify as query.timeout, not query.runtime.
// The secondary errors.Is(err, ...) check covers the case where gojq
// wraps the context error inside its own error type.
func classifyQueryErr(ctx context.Context, err error, _ time.Duration) logx.Event {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return logx.EventQueryTimeout
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return logx.EventQueryTimeout
	}
	// TASK-0018 S-2: context.Canceled is checked after DeadlineExceeded so
	// that a timeout (which also cancels the context) is classified as
	// query.timeout rather than query.cancelled. Canceled covers a future
	// SIGINT handler or any other explicit cancellation path.
	if ctx.Err() == context.Canceled || errors.Is(err, context.Canceled) {
		return logx.EventQueryCancelled
	}
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

// classifyDecodeErr maps a decode-phase error to the right logx event.
// STORY-0008 introduces format.LimitError, which must surface as a
// distinct decoder.limit.* event rather than a generic parse.error.
func classifyDecodeErr(err error) logx.Event {
	var lim *format.LimitError
	if errors.As(err, &lim) {
		switch lim.Kind {
		case format.LimitInputSize:
			return logx.EventDecoderLimitInputSize
		case format.LimitDepth:
			return logx.EventDecoderLimitDepth
		case format.LimitYAMLNodeCount:
			return logx.EventDecoderLimitYAMLNodeCount
		}
	}
	return logx.EventParseError
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

// decodeFormatValueWithLimits is the top-level-agnostic decoder
// carrying STORY-0008 resource bounds. BUG-0001: JSON/YAML allow any
// top-level value; TOML still requires a table (enforced by the TOML
// decoder itself).
//
// STORY-0008 MUST-FIX: takes an io.Reader (not []byte). The decoder
// layer wraps the reader in ReadAllLimited internally, so a caller
// feeding a *os.File will surface a LimitError after reading at most
// MaxBytes+1 bytes rather than after buffering the full file.
func decodeFormatValueWithLimits(fmtName string, r io.Reader, limits format.Limits) (omap.Value, error) {
	switch fmtName {
	case "json":
		return jsonfmt.DecodeValueWithLimits(r, limits)
	case "yaml":
		return yamlfmt.DecodeValueWithLimits(r, limits)
	case "toml":
		return tomlfmt.DecodeValueWithLimits(r, limits)
	default:
		return omap.Value{}, fmt.Errorf("unknown format %q", fmtName)
	}
}

// encodeFormatValue is the top-level-agnostic encoder. The TOML implementation
// rejects non-map tops with a path-aware error so the TOML spec is preserved.
func encodeFormatValue(fmtName string, w io.Writer, v omap.Value) error {
	switch fmtName {
	case "json":
		return jsonfmt.EncodeValue(w, v)
	case "yaml":
		return yamlfmt.EncodeValue(w, v)
	case "toml":
		return tomlfmt.EncodeValue(w, v)
	default:
		return fmt.Errorf("unknown format %q", fmtName)
	}
}
