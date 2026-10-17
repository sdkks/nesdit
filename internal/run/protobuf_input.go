package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/sdkks/nesdit/internal/display"
	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/protobufview"
	"github.com/sdkks/nesdit/internal/query"
)

type protobufOptions struct {
	files               []string
	stdin               bool
	descriptor, message string
}

func protobufSelected(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("protobuf-file") || cmd.Flags().Changed("protobuf-stdin")
}
func (p *protobufOptions) flags(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&p.files, "protobuf-file", nil, "Protobuf: inspect one raw regular file as JSON; no positional input, framing, or editing")
	cmd.Flags().BoolVar(&p.stdin, "protobuf-stdin", false, "Protobuf: explicitly read one raw message through EOF from stdin; no overall timer")
	cmd.Flags().StringVar(&p.descriptor, "descriptor-set", "", "Protobuf: required self-contained local binary FileDescriptorSet, at most 4 MiB")
	cmd.Flags().StringVar(&p.message, "message-type", "", "Protobuf: required exact fully qualified message name (no leading dot or URL)")
}
func (p *protobufOptions) validate(cmd *cobra.Command, args []string) error {
	if !protobufSelected(cmd) {
		codec, _ := cmd.Flags().GetString("nats-codec")
		if cmd.Flags().Changed("nats") && codec == "protobuf" {
			if !cmd.Flags().Changed("descriptor-set") || strings.TrimSpace(p.descriptor) == "" || p.descriptor == "-" {
				return errors.New("--nats-codec protobuf requires --descriptor-set with a separate regular-file path")
			}
			if !cmd.Flags().Changed("message-type") || !protoreflect.FullName(p.message).IsValid() {
				return errors.New("--nats-codec protobuf requires --message-type with an exact full name")
			}
			return nil
		}
		if cmd.Flags().Changed("descriptor-set") || cmd.Flags().Changed("message-type") {
			return errors.New("--descriptor-set and --message-type require --protobuf-file, --protobuf-stdin or --nats with --nats-codec protobuf")
		}
		return nil
	}
	if len(args) != 0 {
		return errors.New("standalone Protobuf accepts no positional arguments")
	}
	fileSet, stdinSet := cmd.Flags().Changed("protobuf-file"), cmd.Flags().Changed("protobuf-stdin")
	if fileSet && stdinSet {
		return errors.New("--protobuf-file and --protobuf-stdin are mutually exclusive")
	}
	if fileSet && (len(p.files) != 1 || strings.TrimSpace(p.files[0]) == "" || p.files[0] == "-") {
		return errors.New("--protobuf-file requires exactly one nonempty regular-file path; use --protobuf-stdin for stdin")
	}
	if stdinSet && !p.stdin {
		return errors.New("--protobuf-stdin must be explicitly true")
	}
	var conflict error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		switch f.Name {
		case "protobuf-file", "protobuf-stdin", "descriptor-set", "message-type", "filter", "project", "log-format", "help", "version":
		default:
			conflict = fmt.Errorf("--%s is not supported with standalone Protobuf", f.Name)
		}
	})
	if conflict != nil {
		return conflict
	}
	if !cmd.Flags().Changed("descriptor-set") || strings.TrimSpace(p.descriptor) == "" || p.descriptor == "-" {
		return errors.New("standalone Protobuf requires --descriptor-set with a separate regular-file path")
	}
	if !cmd.Flags().Changed("message-type") || !protoreflect.FullName(p.message).IsValid() {
		return errors.New("standalone Protobuf requires --message-type with an exact full name")
	}
	return nil
}

func runProtobufInput(opts RunOptions, p protobufOptions, n natsOptions, process *natsProcess) error {
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
		// Latch failure before diagnostics can block. An already-owned interrupt
		// keeps exit 130; a real failure is not turned into successful EOF.
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
	decode, err := protobufview.Prepare(ctx, p.descriptor, p.message)
	if err != nil {
		return fail("prepare", err)
	}
	var data []byte
	if p.stdin {
		// Only Run owns actual process stdin. Execute injections, even *os.File,
		// must never have their deadlines or ownership changed automatically.
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
	buf := &protobufBuffer{ctx: ctx}
	var spans []display.Span
	var observe func(display.Span)
	if display.Enabled(opts.Stdout) {
		observe = func(s display.Span) { spans = append(spans, s) }
	}
	if err = jsonfmt.EncodeValueLayout(buf, value, display.Terminal(opts.Stdout), observe); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fail("encode", err)
		}
		return fail("encode", errors.New("JSON value encoding failed or final output exceeds 16 MiB"))
	}
	if _, err = buf.WriteString("\n"); err != nil {
		return fail("encode", err)
	}
	if err = display.Write(&natsWriter{ctx: ctx, w: opts.Stdout}, buf.Bytes(), spans); err != nil {
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

// The callback never closes a handle or waits on Read. Stop does not join an
// already-running AfterFunc, so cleanup explicitly waits before resetting.
func protobufStdinDeadline(ctx context.Context, f *os.File) func() {
	if err := f.SetReadDeadline(time.Time{}); err != nil {
		return func() {}
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = f.SetReadDeadline(time.Now()); close(done) })
	return func() {
		if !stop() {
			<-done
		}
		_ = f.SetReadDeadline(time.Time{})
	}
}

type protobufBuffer struct {
	bytes.Buffer
	ctx context.Context
}

func (b *protobufBuffer) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > natsEventLimit-b.Len() {
		return 0, errors.New("final JSON output exceeds 16 MiB including whitespace and LF")
	}
	return b.Buffer.Write(p)
}
func (b *protobufBuffer) WriteString(s string) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if len(s) > natsEventLimit-b.Len() {
		return 0, errors.New("final JSON output exceeds 16 MiB including whitespace and LF")
	}
	return b.Buffer.WriteString(s)
}

var _ io.StringWriter = (*protobufBuffer)(nil)
