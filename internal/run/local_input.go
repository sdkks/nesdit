package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sdkks/nesdit/internal/display"
	"github.com/sdkks/nesdit/internal/flatview"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/msgpackview"
	"github.com/sdkks/nesdit/internal/omap"
	"github.com/sdkks/nesdit/internal/protobufview"
	"github.com/sdkks/nesdit/internal/query"
	"github.com/sdkks/nesdit/internal/textview"
)

type localOptions struct {
	files         []string
	stdin         explicitStdin
	codec, target string
	bfbs          []string
}

// Count explicit selections, including repeated boolean flags.
type explicitStdin struct {
	value bool
	count int
}

func (s *explicitStdin) String() string { return strconv.FormatBool(s.value) }
func (*explicitStdin) Type() string     { return "bool" }
func (s *explicitStdin) Set(value string) error {
	v, err := strconv.ParseBool(value)
	if err != nil {
		return errors.New("expected a boolean")
	}
	s.value, s.count = v, s.count+1
	return nil
}

func localSelected(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("input-file") || cmd.Flags().Changed("input-stdin")
}

func (p *localOptions) flags(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&p.files, "input-file", nil, "Inspection: one regular file; commas are literal, - is not stdin")
	cmd.Flags().Var(&p.stdin, "input-stdin", "Inspection: explicitly read one complete body through EOF from stdin; no overall timer")
	cmd.Flags().Lookup("input-stdin").NoOptDefVal = "true"
	cmd.Flags().StringVar(&p.codec, "input-codec", "", "Local input codec: text, msgpack, json (ordinary), msgpack-json (MessagePack JSON v1), or flatbuffers")
	cmd.Flags().StringVar(&p.target, "to", "json", "Local output: json for inspection; explicit msgpack for binary stdout (never a terminal)")
	cmd.Flags().StringArrayVar(&p.bfbs, "bfbs", nil, "FlatBuffers inspection: one ordinary compiled schema file, separate from the payload, never stdin")
}

func (p *localOptions) validate(cmd *cobra.Command, args []string) error {
	if !localSelected(cmd) {
		if cmd.Flags().Changed("input-codec") || cmd.Flags().Changed("to") || cmd.Flags().Changed("bfbs") {
			return errors.New("--input-codec, --to and --bfbs require --input-file or --input-stdin")
		}
		return nil
	}
	if len(args) != 0 {
		return errors.New("local inspection accepts no positional arguments")
	}
	fileSet, stdinSet := cmd.Flags().Changed("input-file"), cmd.Flags().Changed("input-stdin")
	if fileSet && stdinSet {
		return errors.New("--input-file and --input-stdin are mutually exclusive")
	}
	if fileSet && (len(p.files) != 1 || strings.TrimSpace(p.files[0]) == "" || p.files[0] == "-") {
		return errors.New("--input-file requires exactly one nonempty regular-file path; use --input-stdin for stdin")
	}
	if stdinSet && (!p.stdin.value || p.stdin.count != 1) {
		return errors.New("--input-stdin must be selected exactly once and explicitly true")
	}
	var conflict error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		switch f.Name {
		case "input-file", "input-stdin", "input-codec", "to", "bfbs", "filter", "project", "log-format", "help", "version":
		default:
			conflict = fmt.Errorf("--%s is not supported with local inspection", f.Name)
		}
	})
	if conflict != nil {
		return conflict
	}
	switch p.codec {
	case "text":
		if p.target != "json" {
			return errors.New("--to must be json for text inspection")
		}
	case "msgpack":
		if p.target != "json" && p.target != "msgpack" {
			return errors.New("--to must be json or explicit msgpack")
		}
	case "json", "msgpack-json":
		if !cmd.Flags().Changed("to") || p.target != "msgpack" {
			return errors.New("JSON import requires explicit --to msgpack")
		}
	case "flatbuffers":
		if p.target != "json" {
			return errors.New("--to must be json for FlatBuffers inspection")
		}
	default:
		return errors.New("--input-codec is required and must be text, msgpack, json, msgpack-json or flatbuffers")
	}
	bfbsSet := cmd.Flags().Changed("bfbs")
	if bfbsSet && p.codec != "flatbuffers" {
		return errors.New("--bfbs requires --input-codec flatbuffers")
	}
	if p.codec == "flatbuffers" {
		if !bfbsSet || len(p.bfbs) != 1 || strings.TrimSpace(p.bfbs[0]) == "" || p.bfbs[0] == "-" {
			return errors.New("--input-codec flatbuffers requires exactly one nonempty --bfbs regular-file path")
		}
		if !p.stdin.value {
			if same, err := samePath(p.files[0], p.bfbs[0]); err != nil || same {
				return errors.New("--bfbs must be a separate file from the payload")
			}
		}
	}
	return nil
}

// samePath compares two cleaned absolute paths without following symlinks;
// the payload and schema must be separate selections.
func samePath(a, b string) (bool, error) {
	abs := func(p string) (string, error) {
		ap, err := filepath.Abs(filepath.Clean(p))
		if err != nil {
			return "", err
		}
		return ap, nil
	}
	aa, err := abs(a)
	if err != nil {
		return false, err
	}
	bb, err := abs(b)
	if err != nil {
		return false, err
	}
	return aa == bb, nil
}

func runLocalInput(opts RunOptions, p localOptions, n natsOptions, process *natsProcess) error {
	var stop *natsStop
	if process != nil {
		stop = process.stop
	}
	if stop == nil {
		stop = newNATSStop(opts.Ctx, nil)
		defer stop.finish()
	}
	ctx := stop.ctx
	fail := func(stage string, err error) error {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			stop.request("cancel", "cancel: inspection cancelled", 130)
		} else {
			stop.failure(stage + ": " + err.Error())
		}
		_, _, code := stop.result()
		message := stage + ": " + err.Error()
		if code == 130 {
			message = "cancel: inspection interrupted; incomplete value discarded or output may be partial"
		}
		opts.Logger.ErrorGlobal(logx.EventParseError, message)
		if code == 130 {
			return &natsCancelled{}
		}
		return &emittedError{cause: errors.New(message)}
	}
	if p.target == "msgpack" {
		if err := binaryDestination(opts.Stdout); err != nil {
			return fail("destination", err)
		}
	}
	var filter, project *query.Inspection
	var err error
	if n.filterSet {
		filter, err = query.CompileInspection(ctx, n.filter)
		if err != nil {
			return fail("filter", err)
		}
	}
	if n.projectSet {
		project, err = query.CompileInspection(ctx, n.project)
		if err != nil {
			return fail("project", err)
		}
	}
	// BFBS acquisition and preparation complete before any payload byte.
	var schema *flatview.Schema
	if p.codec == "flatbuffers" {
		bfbsData, err := protobufview.ReadFile(ctx, p.bfbs[0], flatview.SchemaLimit)
		if err != nil {
			return fail("schema", err)
		}
		if schema, err = flatview.Prepare(ctx, bfbsData); err != nil {
			return fail("schema", err)
		}
	}
	var data []byte
	if p.stdin.value {
		cleanup := func() {}
		if process != nil && opts.Stdin == os.Stdin {
			cleanup = protobufStdinDeadline(ctx, os.Stdin)
		}
		data, err = protobufview.ReadBody(ctx, opts.Stdin, protobufview.RawLimit)
		cleanup()
	} else {
		data, err = protobufview.ReadFile(ctx, p.files[0], protobufview.RawLimit)
	}
	if err != nil {
		return fail("acquire", err)
	}
	profile := msgpackview.RichJSON
	if p.codec == "json" {
		profile = msgpackview.OrdinaryJSON
	}
	decode := textview.Decode
	switch p.codec {
	case "msgpack":
		decode = msgpackview.Decode
	case "json", "msgpack-json":
		decode = func(ctx context.Context, body []byte) (omap.Value, error) {
			return msgpackview.ReadJSON(ctx, body, profile)
		}
	case "flatbuffers":
		decode = schema.Decode
	}
	value, err := decode(ctx, data)
	if err != nil {
		return fail("decode", err)
	}
	if filter != nil {
		keep, err := filter.Predicate(ctx, value)
		if err != nil {
			return fail("filter", err)
		}
		if !keep {
			return nil
		}
	}
	if project != nil {
		value, err = project.Project(ctx, value)
		if err != nil {
			return fail("project", err)
		}
	}
	var encoded []byte
	var spans []display.Span
	if p.target == "msgpack" {
		encoded, err = msgpackview.Encode(ctx, value, profile)
	} else {
		encoded, spans, err = inspectionJSON(ctx, value, display.Terminal(opts.Stdout), display.Enabled(opts.Stdout))
	}
	if err != nil {
		return fail("encode", err)
	}
	writer := &natsWriter{ctx: ctx, w: opts.Stdout}
	if p.target == "msgpack" {
		_, err = writer.Write(encoded)
	} else {
		err = display.Write(writer, encoded, spans)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fail("write", err)
		}
		return fail("write", errors.New("output failed; value may be incomplete"))
	}
	if err = ctx.Err(); err != nil {
		return fail("write", err)
	}
	return nil
}
