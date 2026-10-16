package run

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sdkks/nesdit/internal/display"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/natsinspect"
	"github.com/sdkks/nesdit/internal/query"
)

type natsOptions struct {
	subject, server, output string
	filter, project         string
	filterSet, projectSet   bool
	duration                time.Duration
	count                   uint64
	creds, ca, cert, key    string
	security                natsinspect.Security
}

func (n *natsOptions) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&n.creds, "creds", "", "NATS: JWT/NKey credentials file (regular, at most 1 MiB); requires tls://")
	cmd.Flags().StringVar(&n.ca, "tls-ca", "", "NATS: PEM CA file (regular, at most 1 MiB); requires tls://; omitted uses system trust")
	cmd.Flags().StringVar(&n.cert, "tls-cert", "", "NATS: PEM client certificate file (regular, at most 1 MiB); requires --tls-key and tls://")
	cmd.Flags().StringVar(&n.key, "tls-key", "", "NATS: PEM client private key file (regular, at most 1 MiB); requires --tls-cert and tls://")
	cmd.Flags().StringVar(&n.subject, "nats", "", "NATS: observe one live JSON subject expression; reads neither input documents nor stdin")
	cmd.Flags().StringVar(&n.server, "server", "", "NATS: required explicit nats://host:port or tls://host:port")
	cmd.Flags().DurationVar(&n.duration, "duration", 0, "NATS: required total observation limit including connection setup; not an idle timeout")
	cmd.Flags().Uint64Var(&n.count, "count", 0, "NATS: stop after N messages admitted for processing, before filtering; may show fewer events")
	cmd.Flags().StringVar(&n.filter, "filter", "", "NATS: payload-root predicate returning exactly one boolean; false suppresses")
	cmd.Flags().StringVar(&n.project, "project", "", "NATS: payload-root projection returning exactly one JSON value")
	cmd.Flags().StringVar(&n.output, "nats-output", "auto", "NATS: auto (terminal pretty, redirected JSONL), jsonl, or pretty (multiline documents)")
}

func (n *natsOptions) validate(cmd *cobra.Command, args []string) error {
	n.filterSet, n.projectSet = cmd.Flags().Changed("filter"), cmd.Flags().Changed("project")
	selected := cmd.Flags().Changed("nats")
	var conflict error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		isNATS := f.Name == "nats" || f.Name == "server" || f.Name == "duration" || f.Name == "count" || f.Name == "nats-output" || f.Name == "filter" || f.Name == "project" || f.Name == "creds" || f.Name == "tls-ca" || f.Name == "tls-cert" || f.Name == "tls-key"
		if !selected && isNATS {
			conflict = errors.New("NATS flags require --nats")
		}
		if selected && !isNATS && f.Name != "log-format" {
			hint := ""
			if f.Name == "where" {
				hint = "; use --filter to suppress nonmatching messages"
			}
			if f.Name == "query" {
				hint = "; use --project"
			}
			if f.Name == "timeout" {
				hint = "; use --duration"
			}
			if f.Name == "pretty" || f.Name == "output-format" {
				hint = "; use --nats-output"
			}
			conflict = fmt.Errorf("--%s is not supported with --nats%s", f.Name, hint)
		}
	})
	if conflict != nil {
		return conflict
	}
	if !selected {
		return nil
	}
	if len(args) != 0 {
		return errors.New("--nats accepts no file arguments")
	}
	if !cmd.Flags().Changed("server") || !cmd.Flags().Changed("duration") || n.duration <= 0 {
		return errors.New("--nats requires --server and a positive --duration")
	}
	if cmd.Flags().Changed("count") && n.count == 0 {
		return errors.New("--count must be positive")
	}
	if n.output != "auto" && n.output != "jsonl" && n.output != "pretty" {
		return errors.New("--nats-output must be auto, jsonl, or pretty")
	}
	if n.subject == "" || len(n.subject) > 4096 || strings.ContainsFunc(n.subject, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("invalid NATS subject: use nonempty tokens without whitespace, at most 4 KiB")
	}
	tokens := strings.Split(n.subject, ".")
	for i, token := range tokens {
		if token == "" || (strings.ContainsAny(token, "*>") && token != "*" && (token != ">" || i != len(tokens)-1)) {
			return errors.New("invalid NATS subject: wildcards must be whole tokens and > must be last")
		}
	}
	u, err := url.Parse(n.server)
	if err != nil || u == nil || (u.Scheme != "nats" && u.Scheme != "tls") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(n.server, "#") || (u.Path != "" && u.Path != "/") || strings.Contains(u.Host, ",") {
		return errors.New("--server must be one explicit nats://host:port or tls://host:port without credentials, query, or fragment")
	}
	host, port, err := net.SplitHostPort(u.Host)
	p, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host == "" || portErr != nil || p == 0 {
		return errors.New("--server requires a host and port from 1 to 65535")
	}
	for _, flag := range []string{"creds", "tls-ca", "tls-cert", "tls-key"} {
		if cmd.Flags().Changed(flag) {
			value, _ := cmd.Flags().GetString(flag)
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("--%s requires a nonempty file path", flag)
			}
			if u.Scheme != "tls" {
				return errors.New("security files require an explicit tls:// --server")
			}
		}
	}
	if (n.cert == "") != (n.key == "") {
		return errors.New("--tls-cert and --tls-key must be supplied together")
	}
	return nil
}

type natsCancelled struct{}

func (*natsCancelled) Error() string { return "observation cancelled" }

type natsOpen func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error)

type natsRender func(context.Context, natsinspect.Record, bool, bool) ([]byte, []display.Span, error)

func runNATS(opts RunOptions, n natsOptions, stop *natsStop, open natsOpen) (result error) {
	ctx := opts.Ctx
	if stop != nil {
		ctx = stop.ctx
	}
	// Preparation is outside the session timer, but still interruptible.
	defer func() {
		if result != nil && (errors.Is(result, context.Canceled) || errors.Is(result, context.DeadlineExceeded)) {
			opts.Logger.ErrorGlobal(logx.EventNATSFailed, "cancel: observation cancelled during local preparation; observation incomplete")
			result = &natsCancelled{}
		}
	}()
	var filter, project *query.Inspection
	var err error
	if n.filterSet || n.filter != "" {
		filter, err = query.CompileInspection(ctx, n.filter)
		if err != nil {
			return fmt.Errorf("filter: %w", err)
		}
	}
	if n.projectSet || n.project != "" {
		project, err = query.CompileInspection(ctx, n.project)
		if err != nil {
			return fmt.Errorf("project: %w", err)
		}
	}
	n.security, err = prepareNATSSecurity(ctx, n)
	if err != nil {
		return err
	}
	return runNATSWithRenderer(opts, n, stop, open, func(ctx context.Context, r natsinspect.Record, pretty, color bool) ([]byte, []display.Span, error) {
		return natsQueriedEvent(ctx, r, pretty, color, filter, project)
	})
}

func runNATSWithRenderer(opts RunOptions, n natsOptions, stop *natsStop, open natsOpen, render natsRender) error {
	if stop == nil {
		stop = newNATSStop(opts.Ctx, nil)
		defer stop.finish()
	}
	// The session deadline begins here, after local validation. The independent
	// stop timer arms the CLI watchdog even when the processing goroutine blocks.
	ctx, cancel := context.WithTimeout(stop.ctx, n.duration)
	defer cancel()
	deadline := time.AfterFunc(n.duration, func() { stop.request("duration", "", 0) })
	defer deadline.Stop()
	var admitted, emitted uint64
	var ready, unfinished bool
	session, err := open(ctx, n.server, n.subject, n.security, func(err error) { stop.incomplete(err.Error()) })
	if err != nil {
		stop.failure(err.Error())
		// Startup needs its own actionable stage, not an empty-observation footer.
		stop.mu.Lock()
		if stop.code != 130 {
			stop.message = err.Error()
			stop.code = 1
			stop.reason = "failure"
		}
		stop.mu.Unlock()
	} else {
		pretty := n.output == "pretty" || (n.output == "auto" && display.Terminal(opts.Stdout))
		color := display.Enabled(opts.Stdout)
		ready = true
		if err = opts.Logger.Emit(logx.Record{Sev: logx.SeverityInfo, Event: logx.EventNATSReady, Msg: "Ready for live observation; delivery is at-most-once"}); err != nil {
			stop.incomplete("write: readiness diagnostic failed")
		}
		for ctx.Err() == nil {
			if err = session.Err(); err != nil {
				stop.incomplete(err.Error())
				break
			}
			record, recvErr := session.Next(ctx)
			if ctx.Err() != nil {
				break
			}
			if errors.Is(recvErr, natsinspect.ErrEmptyPayload) {
				admitted++
				unfinished = true
				stop.failure(recvErr.Error())
				break
			}
			if recvErr != nil {
				stop.failure("delivery: receive failed")
				break
			}
			admitted++
			unfinished = true
			data, spans, eventErr := render(ctx, record, pretty, color)
			if eventErr != nil {
				if !errors.Is(eventErr, context.Canceled) && !errors.Is(eventErr, context.DeadlineExceeded) {
					stop.failure(eventErr.Error())
				}
				break
			}
			if err = session.Err(); err != nil {
				stop.incomplete(err.Error())
				break
			}
			if ctx.Err() != nil {
				break
			}
			// A nil event is a completed nonmatch, not an emitted record.
			if data != nil {
				writer := &natsWriter{ctx: ctx, w: opts.Stdout}
				err = display.Write(writer, data, spans)
				if err != nil {
					if writer.started || ctx.Err() == nil {
						stop.incomplete("write: output failed; final record may be incomplete")
					}
					break
				}
				emitted++
			}
			unfinished = false
			if n.count > 0 && admitted >= n.count {
				stop.request("count", "", 0)
				break
			}
		}
		if err = session.Err(); err != nil {
			stop.incomplete(err.Error())
		}
	}
	if reason, _, _ := stop.result(); reason == "" {
		if opts.Ctx.Err() != nil {
			stop.request("cancel", "cancel: observation cancelled", 130)
		} else {
			stop.request("duration", "", 0)
		}
	}
	// Arm stopping before cleanup and final diagnostics, not after either can
	// block. Close does not drain the queue or wait for unsubscribe confirmation.
	if session != nil {
		session.Close()
		if err = session.Err(); err != nil {
			stop.incomplete(err.Error())
		}
	}
	reason, message, code := stop.result()
	if !ready && code == 0 {
		code = 1
		message = "connect: subscription was not ready"
	}
	fields := []logx.Field{{Key: "reason", Value: reason}, {Key: "admitted", Value: admitted}, {Key: "emitted", Value: emitted}, {Key: "unfinished_event", Value: unfinished}}
	event, severity := logx.EventNATSStopped, logx.SeverityInfo
	if code != 0 {
		event, severity = logx.EventNATSFailed, logx.SeverityError
		message += "; observation incomplete"
	} else {
		message = "Stopped: reason=" + reason
	}
	message += fmt.Sprintf("; admitted=%d emitted=%d unfinished_event=%t", admitted, emitted, unfinished)
	if unfinished && code == 0 {
		message += "; unfinished event discarded before output"
	}
	logErr := opts.Logger.Emit(logx.Record{Sev: severity, Event: event, Msg: message, Fields: fields})
	if code == 130 {
		return &natsCancelled{}
	}
	if code != 0 || logErr != nil {
		return &emittedError{cause: errors.New(message)}
	}
	return nil
}
