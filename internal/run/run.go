// Package run hosts the nesdit CLI entrypoint. It is deliberately a
// non-main package so both cmd/nesdit and the e2e testscript harness
// (test/e2e/main_test.go) can call Run without the `package main` import
// restriction.
//
// The Cobra flag tree dispatches file, stdin, editor, and native observation
// modes. $${VAR} escape is honoured in the query preprocessor inside
// internal/query. Flag-conflict rejection runs at parse time (PreRunE)
// so combinations like --query + -f error BEFORE any file read. Diagnostics
// flow through internal/logx's canonical-shape formatter. RunOptions.Ctx
// carries cancellation into query execution.
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

	"github.com/sdkks/nesdit/internal/display"
	"github.com/sdkks/nesdit/internal/format"
	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/natsinspect"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/query"
)

// Version is the release version, injected by the local release build via -X
// ldflags. Version and Commit remain empty strings in ordinary dev builds.
var Version string

// Commit is the short git commit SHA, injected at build time via -X ldflags.
var Commit string

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

// NewRootCmd returns the root cobra.Command for use by doc generators
// (e.g. cmd/gendocs). The returned command is pre-wired with all flags
// but is not yet executed.
func NewRootCmd() *cobra.Command {
	return newRootCmd(RunOptions{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})
}

// Run is the process-level entrypoint used by cmd/nesdit/main.go.
// Exit code matches Execute's contract:
//   - 0 — success
//   - 1 — any error
//   - 2 — --check drift detected (only this path returns 2)
//   - 130 — native observation or standalone Protobuf interrupted or cancelled
func Run(args []string) int {
	process := &natsProcess{exit: os.Exit}
	defer process.finish()
	return execute(RunOptions{
		Args:   args,
		Ctx:    context.Background(),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}, process)
}

// Execute is the in-process entrypoint taking a RunOptions struct. It
// fills in defaults for any zero-valued field and dispatches to the
// cobra command tree. Native observation and standalone Protobuf are cooperative
// here: no process signal handlers or exit hooks are installed. Borrowed readers
// and writers must return for cancellation to be observed.
func Execute(opts RunOptions) int { return execute(opts, nil) }

func execute(opts RunOptions, process *natsProcess) int {
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
		opts.Logger = logx.NewFormat(opts.Stderr, opts.LogFormat)
	}

	root := newRootCmdProcess(opts, process)
	root.SetArgs(opts.Args)
	// Suppress cobra's default usage print on error; we route all
	// stderr through logx to keep the canonical shape.
	root.SilenceUsage = true
	root.SilenceErrors = true

	err := root.ExecuteContext(opts.Ctx)
	// Flag parsing can fail after the standalone selector but before PreRunE.
	// Arm its process owner before any potentially blocking final diagnostic.
	if err != nil && process != nil && protobufSelected(root) {
		process.start(opts.Ctx)
	}
	if process != nil && process.stop != nil && err != nil {
		process.stop.request("failure", "configuration: inspection did not complete", 1)
	}
	if err == nil {
		return int(ExitOK)
	}
	var cancelled *natsCancelled
	if errors.As(err, &cancelled) {
		return 130
	}
	// driftError is the sole source of exit 2. The --check path
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
	// in canonical shape. Standalone validation errors use its selected
	// diagnostic format too; PreRunE's logger is invocation-local.
	if protobufSelected(root) && root.Flags().Changed("log-format") {
		logFormat, _ := root.Flags().GetString("log-format")
		if logFormat == "text" || logFormat == "json" {
			opts.Logger = logx.NewFormat(opts.Stderr, logx.Format(logFormat))
		}
	}
	opts.Logger.ErrorGlobal(logx.EventFlagInvalid, err.Error())
	return int(ExitError)
}

// emittedError wraps an error whose canonical-shape stderr line has
// already been written by logx. Execute recognises the sentinel and
// returns exit 1 without re-emitting.
type emittedError struct{ cause error }

func (e *emittedError) Error() string { return e.cause.Error() }
func (e *emittedError) Unwrap() error { return e.cause }

// newRootCmd wires the flags and dispatch
// closure. All state captured via closure is per-invocation — no
// package globals. That's important because tests run many Execute
// calls in parallel.
func newRootCmd(opts RunOptions) *cobra.Command { return newRootCmdProcess(opts, nil) }

func newRootCmdProcess(opts RunOptions, process *natsProcess) *cobra.Command {
	var natsOpts natsOptions
	var protobufOpts protobufOptions
	var (
		queryExpr      string
		queryFile      string
		formatName     string
		outputFormat   string
		logFormatStr   string
		argPairs       []string
		argJSONPairs   []string
		inPlace        bool
		dryRun         bool
		check          bool
		editMode       bool
		wherePredicate string
		keepGoing      bool
		// strict is intentionally never read after flag parse. It exists solely
		// so cobra's flagConflictRules can detect Flag.Changed("strict") and
		// reject the --keep-going + --strict combination as a usage error.
		// The flag value itself has no effect: default behaviour is already
		// strict (halt on first error) whether or not the flag is passed.
		strict        bool
		createMissing bool
		backupSuffix  string
		yamlVersion   string
		pretty        bool
		timeout       time.Duration
		maxBytes      int64
		maxDepth      int
		maxYAMLNodes  int
		maxQueryBytes int64
	)
	// Resource-limit defaults come from the format package so the CLI and
	// tests share one source of truth.
	defaults := format.DefaultLimits()
	maxBytes = defaults.MaxBytes
	maxDepth = defaults.MaxDepth
	maxYAMLNodes = defaults.MaxYAMLNodes
	maxQueryBytes = format.DefaultQueryMaxBytes

	version := Version
	if version == "" {
		version = "dev"
	}
	if Commit != "" {
		version += " (" + Commit + ")"
	}

	cmd := &cobra.Command{
		Use:           "nesdit [flags] <file> [<file>...]",
		Short:         "Edit structured config (JSON/YAML/TOML) with jq-style queries",
		Long:          longDesc,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("nats") || protobufSelected(cmd) {
				return nil
			}
			// --edit mode requires exactly one file argument.
			if cmd.Flag("edit").Changed {
				if len(args) != 1 {
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, "--edit requires exactly one file argument")
					return &emittedError{cause: fmt.Errorf("--edit accepts 1 arg(s), received %d", len(args))}
				}
				return nil
			}
			// -i mode accepts one or more file arguments (multi-file / glob).
			// Non-i mode requires exactly one file argument OR no arguments
			// (STDIN mode).
			if cmd.Flag("in-place").Changed {
				if len(args) < 1 {
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, "nesdit -i requires at least one file argument")
					return &emittedError{cause: fmt.Errorf("accepts at least 1 arg(s), received %d", len(args))}
				}
				return nil
			}
			// Zero args → STDIN mode. One arg of "-" → STDIN mode.
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
		// PreRunE runs after flag parsing, before RunE. Flag-interaction
		// rejection must happen BEFORE any file read or stdin byte.
		// --log-format validation also runs here so an unknown value
		// exits 1 before any IO. After validation, an explicitly supplied
		// format reconstructs the Logger over Stderr.
		PreRunE: func(cmd *cobra.Command, args []string) error {
			if (cmd.Flags().Changed("nats") || protobufSelected(cmd)) && process != nil {
				process.start(opts.Ctx)
			}
			// Validate --log-format before any IO.
			if cmd.Flag("log-format").Changed {
				switch logx.Format(logFormatStr) {
				case logx.FormatText, logx.FormatJSON:
					// valid
				default:
					if protobufSelected(cmd) {
						// Return before writing: execute arms the stop watchdog first.
						return errors.New("--log-format: supported values are text and json")
					}
					msg := fmt.Sprintf("--log-format: unknown value %q; supported values are: text, json", logFormatStr)
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, msg)
					return &emittedError{cause: errors.New(msg)}
				}
				// Re-create Logger with the requested format. Only do
				// this when the caller did not supply a Logger via
				// RunOptions (if they did, they own the format).
				// We detect "caller-supplied" by checking whether opts.Logger
				// was set before Execute constructed one; unfortunately we
				// can't easily distinguish, so we always replace here.
				// This is safe: no emit has occurred yet (flag-conflict
				// checks below may be the first).
				opts.Logger = logx.NewFormat(opts.Stderr, logx.Format(logFormatStr))
			}
			if err := protobufOpts.validate(cmd, args); err != nil {
				return err
			}
			if protobufSelected(cmd) {
				natsOpts.filterSet, natsOpts.projectSet = cmd.Flags().Changed("filter"), cmd.Flags().Changed("project")
				return nil
			}
			if err := natsOpts.validate(cmd, args); err != nil {
				return err
			}
			if cmd.Flags().Changed("nats") {
				return nil
			}
			// Validate --output-format before any IO.
			if cmd.Flag("output-format").Changed {
				switch outputFormat {
				case "json", "yaml", "toml":
					// valid
				default:
					msg := fmt.Sprintf("--output-format: unknown value %q; supported values are: json, yaml, toml", outputFormat)
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, msg)
					return &emittedError{cause: fmt.Errorf("%s", msg)}
				}
			}
			// Validate --yaml-version before any IO.
			if cmd.Flag("yaml-version").Changed {
				if yamlVersion != "1.1" && yamlVersion != "1.2" {
					msg := "--yaml-version must be 1.1 or 1.2"
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, msg)
					return &emittedError{cause: errors.New(msg)}
				}
			}
			return validateFlagInteraction(opts.Logger, cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if protobufSelected(cmd) {
				opts.Ctx = cmd.Context()
				return runProtobufInput(opts, protobufOpts, natsOpts, process)
			}
			if cmd.Flags().Changed("nats") {
				opts.Ctx = cmd.Context()
				var stop *natsStop
				if process != nil {
					stop = process.stop
				}
				natsOpts.protobuf = protobufOpts
				return runNATS(opts, natsOpts, stop, natsinspect.OpenSecure)
			}
			limits := format.Limits{
				MaxBytes:     maxBytes,
				MaxDepth:     maxDepth,
				MaxYAMLNodes: maxYAMLNodes,
			}

			// --edit: interactive expression-builder mode.
			// --edit is mutually exclusive with every query flag (--query,
			// --from-file, --arg, --argjson) and mode flag (-i, -n, --check).
			// Flag conflicts are already validated by validateFlagInteraction.
			if editMode {
				if len(args) != 1 {
					msg := "--edit requires exactly one file argument"
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, msg)
					return &emittedError{cause: errors.New(msg)}
				}
				return runEdit(opts, args[0], formatName, limits, yamlVersion)
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
			// Apply `$${VAR}` → `${VAR}` literal escape before
			// handing the text to gojq.
			qText = unescapeDollarBrace(qText)

			// Precedence: -i + -n → -n wins with warning.
			// Precedence: -i + --check → --check wins with warning.
			// These are already validated by validateFlagInteraction (which
			// emits the warn line); here we just override the effective mode.
			effectiveInPlace := inPlace && !dryRun && !check

			// --dry-run and --check operate on exactly
			// one file. Passing two or more files with either flag is an
			// error (silently dropping files 2..N would be confusing).
			if (dryRun || check) && len(args) > 1 {
				opts.Logger.ErrorGlobal(logx.EventFlagInvalid, "--dry-run and --check support exactly one file argument")
				return &emittedError{cause: fmt.Errorf("--dry-run and --check support exactly one file argument")}
			}

			// --dry-run (-n): emit unified diff; no file write.
			if dryRun {
				if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
					// STDIN + --dry-run is not yet supported; treat as error.
					msg := "--dry-run requires a file argument (STDIN mode is not supported with --dry-run)"
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, msg)
					return &emittedError{cause: errors.New(msg)}
				}
				return runDryRun(cmd.Context(), opts, args[0], qText, formatName, outputFormat, jqArgs, limits, timeout, createMissing, yamlVersion, pretty)
			}

			// --check: compare encoded result to re-encoded original.
			if check {
				if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
					// STDIN + --check is not yet supported; treat as error.
					msg := "--check requires a file argument (STDIN mode is not supported with --check)"
					opts.Logger.ErrorGlobal(logx.EventFlagInvalid, msg)
					return &emittedError{cause: errors.New(msg)}
				}
				return runCheck(cmd.Context(), opts, args[0], qText, formatName, outputFormat, jqArgs, limits, timeout, createMissing, yamlVersion, pretty)
			}

			// -i / --in-place: route through the two-pass file orchestrator.
			if effectiveInPlace {
				return runFiles(cmd.Context(), opts, args, qText, wherePredicate, formatName, outputFormat, jqArgs, limits, timeout, keepGoing, backupSuffix, createMissing, yamlVersion, pretty)
			}

			// STDIN mode: no file args, or explicit "-".
			if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
				fmtName, resolvedOpts, fmtErr := stdinFormatName(opts, formatName)
				if fmtErr != nil {
					opts.Logger.ErrorGlobal(logx.EventFormatUnknown, fmtErr.Error())
					return &emittedError{cause: fmtErr}
				}
				return runStdin(cmd.Context(), resolvedOpts, fmtName, outputFormat, qText, wherePredicate, jqArgs, limits, timeout, keepGoing, createMissing, yamlVersion, pretty)
			}

			// Default: single-file file→stdout path.
			// --where is not meaningful in single-file mode (there's nothing to
			// filter against a batch), so wherePredicate is intentionally not
			// passed to runOnce.
			return runOnce(cmd.Context(), opts, args[0], qText, formatName, outputFormat, jqArgs, limits, timeout, createMissing, yamlVersion, pretty)
		},
	}

	// --log-format selects the stderr rendering mode.
	// "text" (default) emits canonical lines; "json" emits NDJSON.
	// Unknown values are rejected in PreRunE before any IO.
	cmd.Flags().StringVar(&logFormatStr, "log-format", "text", "stderr rendering mode: text (default, canonical lines) or json (NDJSON)")

	cmd.Flags().StringVar(&queryExpr, "query", "", "jq-style query (default '.' identity when neither --query nor -f is given)")
	cmd.Flags().StringVarP(&queryFile, "from-file", "f", "", "load query from file (mutually exclusive with --query)")
	// --where is an inner jq predicate; implementation
	// wraps it as select(<predicate>) at evaluation time.
	cmd.Flags().StringVar(&wherePredicate, "where", "", "filter docs by jq predicate: only matching docs have --query applied (stream: others pass through; in-place batch mode (-i): non-matching files are skipped; not supported in single-file stdout mode)")
	cmd.Flags().StringVar(&formatName, "format", "", "force input format (json|jsonl|yaml|toml); default is extension-based detection")
	// --output-format overrides the encode format independently of the input format.
	// When absent, the effective output format equals the detected/overridden input format.
	// When given, the output is encoded in the specified format regardless of input format.
	// Incompatible with -i (in-place), because transcoding a file in-place silently changes
	// its format, which is almost always destructive and unintentional.
	cmd.Flags().StringVar(&outputFormat, "output-format", "", "output format (json|yaml|toml); defaults to same as input format when not specified. jsonl is not a valid output format: JSONL output happens automatically when the input format is jsonl (one output line per input line). When outputting toml with multiple documents, documents are separated by +++ (Hugo-style convention; not TOML spec)")
	cmd.Flags().StringArrayVar(&argPairs, "arg", nil, "bind $K=V in the query as a literal string (repeatable)")
	cmd.Flags().StringArrayVar(&argJSONPairs, "argjson", nil, "bind $K=V in the query as a JSON-decoded value (repeatable)")
	// -i / --in-place: edit files in-place atomically.
	cmd.Flags().BoolVarP(&inPlace, "in-place", "i", false, "edit file(s) in-place using atomic temp+rename writes")
	// Preview flags: --dry-run (-n) and --check.
	cmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "emit a unified diff of before/after to stdout; do not write any file")
	cmd.Flags().BoolVar(&check, "check", false, "exit 2 if the query would change the input; exit 0 if identical; exit 1 on error")
	// --edit is long-only; -e is reserved.
	cmd.Flags().BoolVar(&editMode, "edit", false, "open $EDITOR on a temp copy of the file; diff before/after and emit suggested query")
	// --keep-going and --strict (explicit default alias).
	// Default behaviour is --strict (halt on first error). --keep-going opts in to
	// continue-on-error: errored documents are skipped, processing continues.
	// Both flags are mutually exclusive; enforced by flagConflictRules.
	// Note: the strict variable is bound here only for the conflict check
	// (Flag.Changed detection); its value is never read in the run path.
	// See the declaration comment in the var block above for rationale.
	cmd.Flags().BoolVar(&keepGoing, "keep-going", false, "continue processing after per-document errors; errored docs are skipped; exit 1 at end if any errors occurred")
	cmd.Flags().BoolVar(&strict, "strict", false, "halt on the first document error (default behaviour; explicit alias for documentation)")
	// --backup[=.bak] is an optional-value string flag:
	//   --backup          → suffix defaults to ".bak" (NoOptDefVal)
	//   --backup=.orig    → suffix is ".orig"
	//   (absent)          → no backup written
	// Requires -i; validated by flagConflictRules.
	cmd.Flags().StringVar(&backupSuffix, "backup", "", "write a sibling backup before each in-place edit (default suffix .bak; custom: --backup=.orig); requires -i")
	cmd.Flag("backup").NoOptDefVal = ".bak"
	// --create-missing: without this flag, a query
	// that sets a path not present in the input document is rejected with a
	// query.missing_path error. With the flag, absent intermediate nodes are
	// created as empty objects (mirrors jq's native `=` assignment semantics).
	cmd.Flags().BoolVar(&createMissing, "create-missing", false, "allow queries to create keys/paths that do not exist in the input document; by default, absent paths are rejected")
	// --yaml-version selects the YAML boolean vocabulary. Default is "1.2"
	// (only true/false recognised as booleans). "1.1" enables the extended vocabulary
	// (yes/no/on/off and case variants). Silently ignored for non-YAML formats.
	cmd.Flags().StringVar(&yamlVersion, "yaml-version", "1.2", "YAML dialect for boolean coercion: 1.1 (yes/no/on/off as bools) or 1.2 (true/false only; default)")
	// Resource-limit defaults come from format.DefaultLimits() so the
	// safe-by-default semantics live in one place.
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "cancel the query after this duration (e.g. 500ms, 30s); 0 disables the cap")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", defaults.MaxBytes, "reject inputs larger than this many bytes; 0 disables the cap")
	cmd.Flags().IntVar(&maxDepth, "max-depth", defaults.MaxDepth, "reject documents nested deeper than this; 0 disables the cap")
	cmd.Flags().IntVar(&maxYAMLNodes, "max-yaml-nodes", defaults.MaxYAMLNodes, "YAML node-materialisation cap (billion-laughs mitigation); 0 disables the cap")
	cmd.Flags().Int64Var(&maxQueryBytes, "max-query-bytes", format.DefaultQueryMaxBytes, "reject query files (--from-file) larger than this many bytes; 0 disables the cap")
	// --pretty enables human-friendly TOML output. When set with
	// --output-format toml (or when the input format is toml), arrays with >1
	// element are expanded to multi-line, inline tables with >1 key are expanded
	// to block form, and blank lines are inserted between top-level entries.
	// Silently ignored for non-TOML output formats (json, yaml, jsonl).
	cmd.Flags().BoolVar(&pretty, "pretty", false, "emit human-friendly TOML output: multi-line arrays, expanded tables, blank lines between entries (currently only affects TOML output)")

	natsOpts.flags(cmd)
	protobufOpts.flags(cmd)
	cmd.SetOut(opts.Stdout)
	cmd.SetErr(opts.Stderr)
	return cmd
}

const longDesc = `nesdit reads a single JSON, YAML, or TOML document, applies a jq-style
query, and writes the result to stdout.

Supported modes:
  - File input with --query '<jq>' or --from-file / -f <path>.
  - STDIN stream mode: omit the file argument (or pass -) to read from stdin.
  - --edit to open $EDITOR on the file, then emit a suggested query from the diff.
  - --arg K=V (string) and --argjson K=V (JSON-decoded) bindings.
  - --format <json|jsonl|yaml|toml> to override extension-based detection.
  - $${VAR} literal escape in a query (nesdit never expands shell env).
  - -i / --in-place to edit files atomically in place.
  - -n / --dry-run to preview changes as a unified diff (no writes).
  - --check to gate on drift: exit 2 if the query changes the input.
  - --pretty to emit human-friendly TOML output (multi-line arrays, expanded
    inline tables, blank lines between entries); silently ignored for non-TOML
    output formats.
  - --timeout <dur> to cancel a runaway query on a deadline.
  - --max-bytes, --max-depth, --max-yaml-nodes, and --max-query-bytes
    resource caps for decode-phase hardening.

Standalone Protobuf inspection (JSON output only, no editing):
  nesdit --protobuf-file event.bin --descriptor-set schemas/events.pb --message-type example.Order
  producer | nesdit --protobuf-stdin --descriptor-set schemas/events.pb --message-type example.Order \
    --filter '.active == true' --project '{id: .id}'
Choose exactly one source explicitly. One raw body is read through EOF, at most
10 MiB; no prefixes or framing are inferred. Open stdin can wait indefinitely:
there is no standalone timer. Schema and queries are prepared before payload
input. Output is the JSON value only, pretty on terminals, compact otherwise,
with one LF; a false filter writes nothing. Use --filter/--project, not legacy
query, format, presentation, editing, timeout or resource flags. All descriptor
imports and Any targets must be local. See the Protobuf inspection guide for
mapping, conservative limits and cooperative versus process-stop behavior.

Live Core NATS observation (JSON default; reads neither input documents nor stdin):
  nesdit --nats 'orders.>' --server nats://127.0.0.1:4222 --duration 30s --count 3 \
    --filter '.show == true' --project '{id: .id}'
For Protobuf, add --nats-codec protobuf --descriptor-set schemas/events.pb
--message-type example.Order. The local schema is prepared before connection and
the duration starts; no codec or framing is guessed. Schema flags are invalid
with JSON. Output remains subject, headers, payload, unlike standalone values.
The shared Protobuf component limits apply, plus the 16 MiB final event limit.
Server and positive total duration are required; count is optional and counts
messages admitted before filtering, not matches or all publications. Queries see
the body root: use .id, not the displayed .payload.id. --filter requires exactly
one boolean (false suppresses without projection); --project exactly one JSON
value. Use comparisons, not select(); zero/multiple results are errors.
Observation is live and at-most-once; readiness does not guarantee complete
delivery. No reconnect.
Security files require tls://: --creds for a user JWT/NKey file, --tls-ca for
custom trust, and paired --tls-cert/--tls-key for mutual TLS. Files must be regular,
at most 1 MiB each, and are validated/loaded before the duration starts. Omitted
CA uses system trust; certificate/hostname verification is never disabled.
Do not put credentials in URLs. Token/user-password-only, TLS-first-only, proxy
and WebSocket connections are not supported. First interrupt covers preparation.
--nats-output auto uses pretty JSON on a terminal, JSONL when redirected;
explicit pretty is a sequence of multiline JSON documents, not JSONL.
One interrupt stops observation on Linux/macOS. A one-second forced-stop fallback
may truncate the final record and omit final diagnostics. Check exit status and
validate redirected output. Exit 0 means normal count/ready-duration completion,
1 means failure, and 130 means interruption. Earlier complete output is retained.
See the NATS observation guide for component limits and trust assumptions.`

// flagConflictRule is one row of the flag-interaction
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

// flagPrecedenceRule is one row of the flag-interaction
// matrix for ALLOW-with-warning cells. If every flag in IfSet is
// explicitly set, and any flag in AlsoSet is also explicitly set, the
// invocation continues but a flag.precedence warn is emitted on stderr.
// This covers the -i + -n and -i + --check cases.
type flagPrecedenceRule struct {
	IfSet   []string
	AlsoSet []string
	Msg     string
}

// flagConflictRules lists mutually exclusive flag combinations, including
// query sources, editing/preview modes, and format overrides.
var flagConflictRules = []flagConflictRule{
	{
		IfSet:      []string{"query"},
		ThenNotSet: []string{"from-file"},
		Event:      logx.EventFlagConflict,
		Msg:        "--query and --from-file are mutually exclusive: provide the query inline OR via a file, not both",
	},
	{
		// --dry-run and --check are mutually exclusive.
		IfSet:      []string{"dry-run"},
		ThenNotSet: []string{"check"},
		Event:      logx.EventFlagConflict,
		Msg:        "--dry-run and --check are mutually exclusive: --dry-run prints a diff, --check only signals drift via exit code",
	},
	{
		// --edit and -i are mutually exclusive.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"in-place"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit and -i are mutually exclusive: --edit always prints to stdout; use the emitted query with -i in a second invocation",
	},
	{
		// --edit and --dry-run are mutually exclusive.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"dry-run"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit is interactive and emits its own preview; --dry-run is not compatible",
	},
	{
		// --edit and --check are mutually exclusive.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"check"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit is interactive; --check is for non-interactive drift detection",
	},
	{
		// --edit and --query are mutually exclusive.
		// --edit opens the file in an editor and derives the query from the diff;
		// --query supplies the query directly. Combining them is undefined.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"query"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit derives the query from your edits; --query is not compatible (provide one or the other)",
	},
	{
		// --edit and --from-file are mutually exclusive.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"from-file"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit derives the query from your edits; --from-file is not compatible (provide one or the other)",
	},
	{
		// --edit and --arg are mutually exclusive.
		// --edit does not execute a query, so variable bindings have no effect.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"arg"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit does not execute a query; --arg bindings are not compatible",
	},
	{
		// --edit and --argjson are mutually exclusive.
		IfSet:      []string{"edit"},
		ThenNotSet: []string{"argjson"},
		Event:      logx.EventFlagConflict,
		Msg:        "--edit does not execute a query; --argjson bindings are not compatible",
	},
	{
		// --keep-going and --strict are mutually exclusive.
		// --strict is the explicit default; --keep-going opts in to continue-on-error.
		// Both flags set simultaneously is a usage error.
		IfSet:      []string{"keep-going"},
		ThenNotSet: []string{"strict"},
		Event:      logx.EventFlagConflict,
		Msg:        "--keep-going and --strict are mutually exclusive: --strict is the default behaviour (halt on first error); --keep-going overrides it",
	},
	{
		// --output-format and -i are mutually exclusive.
		// In-place editing rewrites the file; changing the output format in-place
		// would silently change the file's format, which is almost never the intent
		// and is easy to do destructively.
		IfSet:      []string{"output-format"},
		ThenNotSet: []string{"in-place"},
		Event:      logx.EventFlagConflict,
		Msg:        "--output-format and -i are mutually exclusive: transcoding a file in-place would silently change its format; use --output-format without -i to transcode to stdout",
	},
	{
		// --where is a batch/stream filter; --dry-run operates
		// on exactly one file and has no notion of filtering. Combining them is
		// undefined (the predicate would be silently ignored).
		IfSet:      []string{"where"},
		ThenNotSet: []string{"dry-run"},
		Event:      logx.EventFlagConflict,
		Msg:        "--where is a batch/stream filter and is not supported with --dry-run (single-file mode)",
	},
	{
		// Same rationale as --dry-run above.
		IfSet:      []string{"where"},
		ThenNotSet: []string{"check"},
		Event:      logx.EventFlagConflict,
		Msg:        "--where is a batch/stream filter and is not supported with --check (single-file mode)",
	},
}

// flagPrecedenceRules lists allowed combinations that emit a warning.
// When -i is set alongside a dominanting flag, the dominating flag wins
// and a flag.precedence warn is emitted. No error, no abort.
var flagPrecedenceRules = []flagPrecedenceRule{
	{
		// -i + -n → -n wins.
		IfSet:   []string{"in-place"},
		AlsoSet: []string{"dry-run"},
		Msg:     "-i ignored because --dry-run is set",
	},
	{
		// -i + --check → --check wins.
		IfSet:   []string{"in-place"},
		AlsoSet: []string{"check"},
		Msg:     "-i ignored because --check is set",
	},
}

// validateFlagInteraction enforces the flag-interaction rules defined in
// flagConflictRules (ERROR cells) and flagPrecedenceRules (WARN cells).
// Error rules return an emittedError and abort; precedence rules emit a
// warn line and allow the run to continue with the dominating flag.
// Keeps the name and signature stable because the cobra PreRunE closure
// in newRootCmd references it by name.
//
// args are the positional arguments after flag parsing; they are needed
// to distinguish STDIN mode (no args / "-") from single-file mode.
func validateFlagInteraction(log *logx.Logger, cmd *cobra.Command, args []string) error {
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
	// --backup requires -i. This is a "requires" constraint
	// rather than a mutual-exclusion constraint, so it is checked separately
	// from the flagConflictRules table (which expresses mutual-exclusion pairs).
	if changed("backup") && !changed("in-place") {
		const msg = "--backup requires -i"
		log.ErrorGlobal(logx.EventFlagInvalid, msg)
		return &emittedError{cause: errors.New(msg)}
	}
	// WARN cells: emit flag.precedence warning but continue.
	for _, rule := range flagPrecedenceRules {
		if allSet(rule.IfSet) && anySet(rule.AlsoSet) {
			log.WarnGlobal(logx.EventFlagPrecedence, rule.Msg)
		}
	}

	// --where without --query or -f is an error.
	// The existing mutual-exclusion table handles "A and B cannot both be
	// set"; this requires "A is set and neither B nor C is set" — a
	// required-pairing check that doesn't fit flagConflictRule's shape.
	if changed("where") && !changed("query") && !changed("from-file") {
		msg := "--where requires --query or -f/--from-file: a predicate filter without a query has no effect"
		log.ErrorGlobal(logx.EventFlagInvalid, msg)
		return &emittedError{cause: errors.New(msg)}
	}

	// --where is a batch/stream filter. It is meaningful
	// only in -i (multi-file) mode or STDIN stream mode. In plain single-file
	// mode (exactly one positional arg that is not "-", no -i flag set), the
	// predicate is silently ignored by runOnce — reject it before any IO so
	// the user gets a clear diagnostic rather than a silent no-op.
	//
	// STDIN mode: len(args)==0 or args[0]=="-". The --edit case cannot reach
	// here with --where set: --edit requires a query (--query or -f) and both
	// are mutually exclusive with --edit, so --edit + --where is rejected by
	// the "where requires --query/--from-file" check earlier in this function.
	if changed("where") && !changed("in-place") {
		stdinMode := len(args) == 0 || (len(args) == 1 && args[0] == "-")
		if !stdinMode {
			msg := "--where is a batch/stream filter and is not supported in single-file mode (use -i for multi-file or omit the file argument for STDIN stream mode)"
			log.ErrorGlobal(logx.EventFlagConflict, msg)
			return &emittedError{cause: errors.New(msg)}
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
	if first != '_' && (first < 'A' || first > 'Z') && (first < 'a' || first > 'z') {
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
// The --from-file read is capped by maxQueryBytes
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
		defer func() { _ = f.Close() }()
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

// unescapeDollarBrace implements the `$${VAR}` → `${VAR}` literal
// escape. The tool NEVER performs shell-env interpolation — only the
// explicit escape is honoured. Single `${...}` stays literal in
// whatever context jq parses it (jq treats `$var` as a variable
// reference; `${VAR}` is simply not a jq variable form).
func unescapeDollarBrace(q string) string {
	return strings.ReplaceAll(q, "$${", "${")
}

// runOnce is the file→stdout path: decode, query, encode.
// Every error path emits in canonical shape via opts.Logger and
// returns a sentinel so Execute can exit 1 without double-logging.
//
// `limits` are applied at decode time so oversize /
// deeply-nested / alias-bombed inputs fail fast with canonical-shape
// stderr. `timeout` (when > 0) wraps ctx with context.WithTimeout so a
// pathological gojq query cancels on deadline and emits query.timeout.
//
// When createMissing is false, the post-query output is
// checked for new keys/paths that did not exist in the input. If any are
// found the run is rejected with a query.missing_path error.
//
// outputFmtOverride, when non-empty, selects the encode
// format independently of the input format. When empty, the encode format
// equals the detected/overridden input format (no behaviour change).
//
// pretty enables human-friendly TOML output when the effective
// output format is "toml". Silently ignored for other output formats.
func runOnce(ctx context.Context, opts RunOptions, path, queryExpr, overrideFormat, outputFmtOverride string, args []query.Arg, limits format.Limits, timeout time.Duration, createMissing bool, yamlVersion string, pretty bool) error {
	fmtName := overrideFormat
	if fmtName == "" {
		fmtName = detectFormatByExt(path)
	}
	if fmtName == "" {
		msg := "cannot detect format (supported: json, yaml, yml, toml); use --format to override"
		opts.Logger.Error(logx.EventFormatUnknown, path, msg)
		return &emittedError{cause: errors.New(msg)}
	}
	// Resolve effective output format. Defaults to the input format.
	outFmtName := outputFmtOverride
	if outFmtName == "" {
		outFmtName = fmtName
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
	defer func() { _ = f.Close() }()

	val, err := decodeFormatValueWithLimitsAndVersion(fmtName, f, limits, yamlVersion)
	if err != nil {
		opts.Logger.Error(classifyDecodeErr(err), path, err.Error())
		return &emittedError{cause: err}
	}

	// Apply --timeout to the query phase only. Decode
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

	// Reject missing-path creation unless --create-missing.
	if !createMissing {
		if mpErr := query.CheckNoMissingPaths(val, outVal); mpErr != nil {
			opts.Logger.Error(logx.EventQueryMissingPath, path, mpErr.Error())
			return &emittedError{cause: mpErr}
		}
	}

	var buf bytes.Buffer
	spans, err := encodeDisplayValue(outFmtName, &buf, outVal, pretty, display.Enabled(opts.Stdout))
	if err != nil {
		// Distinguish cross-format incompatibility from
		// generic encode failures for better user diagnostics.
		var encErr *omap.EncodeError
		if errors.As(err, &encErr) {
			opts.Logger.Error(logx.EventFormatIncompatible, path, err.Error())
		} else {
			opts.Logger.Error(logx.EventEncodeError, path, err.Error())
		}
		return &emittedError{cause: err}
	}
	if err := display.Write(opts.Stdout, buf.Bytes(), spans); err != nil {
		opts.Logger.Error(logx.EventIOWrite, path, err.Error())
		return &emittedError{cause: err}
	}
	return nil
}

// classifyQueryErr maps a *query.Error's Op classifier onto a logx
// event token. Unknown shapes fall through to query.runtime as a safe
// default.
//
// When the query context finished with DeadlineExceeded,
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
	// context.Canceled is checked after DeadlineExceeded so
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
// format.LimitError must surface as a
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

// decodeFormatValueWithLimitsAndVersion is the top-level-agnostic decoder
// carrying resource bounds and yamlVersion dialect selection.
// JSON/YAML allow any top-level value; TOML still
// requires a table (enforced by the TOML decoder itself).
//
// Takes an io.Reader (not []byte). The decoder
// layer wraps the reader in ReadAllLimited internally, so a caller
// feeding a *os.File will surface a LimitError after reading at most
// MaxBytes+1 bytes rather than after buffering the full file.
//
// yamlVersion ("1.1" | "1.2" | "") selects YAML boolean vocabulary.
// The value is silently ignored for non-YAML formats.
func decodeFormatValueWithLimitsAndVersion(fmtName string, r io.Reader, limits format.Limits, yamlVersion string) (omap.Value, error) {
	switch fmtName {
	case "json":
		return jsonfmt.DecodeValueWithLimits(r, limits)
	case "yaml":
		return yamlfmt.DecodeValueWithLimitsAndOpts(r, limits, yamlfmt.DecodeOpts{YAMLVersion: yamlVersion})
	case "toml":
		return tomlfmt.DecodeValueWithLimits(r, limits)
	default:
		return omap.Value{}, fmt.Errorf("unknown format %q", fmtName)
	}
}

// encodeFormatValue is the top-level-agnostic encoder. The TOML implementation
// rejects non-map tops with a path-aware error so the TOML spec is preserved.
func encodeFormatValue(fmtName string, w io.Writer, v omap.Value) error {
	return encodeFormatValueWithPretty(fmtName, w, v, false)
}

// encodeFormatValueWithPretty is encodeFormatValue with pretty
// support. When pretty is true and fmtName is "toml", pretty-printing is
// enabled. For all other formats, pretty is silently ignored.
func encodeFormatValueWithPretty(fmtName string, w io.Writer, v omap.Value, pretty bool) error {
	switch fmtName {
	case "json":
		return jsonfmt.EncodeValue(w, v)
	case "yaml":
		return yamlfmt.EncodeValue(w, v)
	case "toml":
		return tomlfmt.EncodeValueWithOptions(w, v, tomlfmt.EncodeOptions{Pretty: pretty})
	default:
		return fmt.Errorf("unknown format %q", fmtName)
	}
}
