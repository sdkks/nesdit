package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdkks/nesdit/internal/display"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/natsinspect"
)

func TestHistoryLocalValidation(t *testing.T) {
	base := []string{"--nats-history", "ORDERS", "--subject", "orders.>", "--start-seq", "1", "--server", "nats://127.0.0.1:1", "--duration", "1s"}
	for _, extra := range [][]string{
		{"--nats", "orders.>"}, {"--protobuf-stdin"}, {"--protobuf-file", "data.bin"}, {"file.json"},
		{"--nats-history", ""}, {"--nats-history", "A.B"}, {"--nats-history", "A/B"}, {"--nats-history", "A\\B"}, {"--nats-history", "*"}, {"--nats-history", "A B"},
		{"--subject", ""}, {"--subject", "a.>.b"}, {"--subject", "a..b"}, {"--subject", "a b"}, {"--subject", "a*"},
		{"--start-seq", "0"}, {"--start-seq", "18446744073709551616"}, {"--start-time", "2026-01-01T00:00:00Z"},
		{"--end-seq", "0"}, {"--start-seq", "3", "--end-seq", "2"}, {"--duration", "0s"}, {"--count", "0"},
		{"--format", "json"}, {"--output-format", "json"}, {"--pretty=false"}, {"--timeout", "0"}, {"--max-bytes", "10485760"},
		{"--query", "."}, {"--where", "true"}, {"--in-place=false"}, {"--check=false"}, {"--dry-run=false"}, {"--edit=false"},
		{"--nats-codec", "other"}, {"--nats-codec", "protobuf"}, {"--descriptor-set", "missing"}, {"--message-type", "example.Event"},
		{"--filter", ""}, {"--project", "["}, {"--tls-ca", "missing"}, {"--creds", "missing"},
	} {
		args := append(append([]string(nil), base...), extra...)
		spy := new(protobufReadSpy)
		var out bytes.Buffer
		code, logs := protobufExecute(args, spy, &out)
		if code != 1 || spy.reads != 0 || out.Len() != 0 || strings.Contains(logs, "connect:") {
			t.Fatalf("%q code=%d reads=%d out=%q logs=%s", args, code, spy.reads, out.String(), logs)
		}
	}
	for _, flag := range []string{"--subject", "--start-seq", "--start-time", "--end-seq"} {
		value := "1"
		if flag == "--subject" {
			value = ">"
		}
		if flag == "--start-time" {
			value = "2026-01-01T00:00:00Z"
		}
		for _, prefix := range [][]string{nil, {"--nats", "orders.>", "--server", "nats://127.0.0.1:1", "--duration", "1s"}, {"--protobuf-stdin", "--descriptor-set", "unused", "--message-type", "example.Event"}} {
			args := append(append([]string(nil), prefix...), flag, value)
			spy := new(protobufReadSpy)
			code, logs := protobufExecute(args, spy, io.Discard)
			if code != 1 || spy.reads != 0 || strings.Contains(logs, "connect:") {
				t.Fatal(args, code, logs)
			}
		}
	}
	for _, timestamp := range []string{"1677-09-21T00:12:43.145224191Z", "2262-04-11T23:47:16.854775808Z", "2500-01-01T00:00:00Z", "2026-01-01T00:00:00", "2026-01-01T00:00:00.1234567890Z"} {
		args := []string{"--nats-history", "ORDERS", "--subject", ">", "--start-time", timestamp, "--server", "nats://127.0.0.1:1", "--duration", "1s"}
		spy := new(protobufReadSpy)
		code, logs := protobufExecute(args, spy, io.Discard)
		if code != 1 || spy.reads != 0 || strings.Contains(logs, "connect:") {
			t.Fatal(timestamp, code, logs)
		}
	}
	for _, timestamp := range []string{"1677-09-21T00:12:43.145224192Z", "2262-04-11T23:47:16.854775807Z", "1677-09-21T01:12:43.145224192+01:00", "2262-04-11T22:47:16.854775807-01:00"} {
		cmd := NewRootCmd()
		var n natsOptions
		// Read the registered root values through its real validator separately from connecting.
		args := []string{"--nats-history", "ORDERS", "--subject", ">", "--start-time", timestamp, "--server", "nats://127.0.0.1:1", "--duration", "1s"}
		if err := cmd.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		n.history.Stream = "ORDERS"
		n.history.Subject = ">"
		n.startTime = timestamp
		n.server = "nats://127.0.0.1:1"
		n.duration = time.Second
		n.codec = "json"
		n.output = "auto"
		if err := n.validate(cmd, nil); err != nil {
			t.Fatal(timestamp, err)
		}
	}
	// Bare selector-like names remain ordinary files, not commands.
	for _, name := range []string{"nats-history", "subject", "start-time"} {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(`{"ok":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		code, logs := protobufExecute([]string{"--format", "json", path}, new(protobufReadSpy), &out)
		if code != 0 || !strings.Contains(out.String(), `"ok"`) {
			t.Fatal(code, logs)
		}
	}
}

func TestHistoryMetadataEnvelope(t *testing.T) {
	stored, _ := time.Parse(time.RFC3339Nano, "2026-01-01T03:00:00+03:00")
	record := natsinspect.Record{Subject: "orders.x", Data: []byte(`{"stream":"payload","sequence":0,"stored_time":"fake"}`), Headers: map[string][]string{"stream": {"header"}, "Trace": {"a", "b"}}, History: &natsinspect.HistoryMetadata{Stream: "ORDERS", Sequence: 9007199254740993, StoredTime: stored}}
	data, _, err := natsEvent(context.Background(), record, false, false)
	want := "{\"stream\":\"ORDERS\",\"sequence\":\"9007199254740993\",\"stored_time\":\"2026-01-01T00:00:00Z\",\"subject\":\"orders.x\",\"headers\":{\"Trace\":[\"a\",\"b\"],\"stream\":[\"header\"]},\"payload\":{\"stream\":\"payload\",\"sequence\":0,\"stored_time\":\"fake\"}}\n"
	if err != nil || string(data) != want {
		t.Fatal(string(data), err)
	}
}

type historyFake struct {
	next    func(context.Context) (natsinspect.Record, error)
	failure error
	closes  int
}

func (f *historyFake) Next(ctx context.Context) (natsinspect.Record, error) { return f.next(ctx) }
func (f *historyFake) Err() error                                           { return f.failure }
func (f *historyFake) Close()                                               { f.closes++ }

func TestHistoryStopPrecedence(t *testing.T) {
	for _, normal := range []string{"count", "duration", "exhaustion"} {
		stop := newNATSStop(context.Background(), nil)
		stop.request(normal, "", 0)
		stop.incomplete("history: malformed reply")
		reason, _, code := stop.result()
		stop.finish()
		if reason != "failure" || code != 1 {
			t.Fatal(normal, reason, code)
		}
	}
	for _, stage := range []string{"receive", "decode", "query", "output"} {
		t.Run(stage, func(t *testing.T) {
			stop := newNATSStop(context.Background(), nil)
			defer stop.finish()
			f := &historyFake{}
			f.next = func(context.Context) (natsinspect.Record, error) {
				if stage == "receive" {
					stop.request("duration", "", 0)
					return natsinspect.Record{}, errors.New("history: malformed reply")
				}
				return natsinspect.Record{Data: []byte(`{}`)}, nil
			}
			render := func(context.Context, natsinspect.Record, bool, bool) ([]byte, []display.Span, error) {
				if stage == "decode" || stage == "query" {
					stop.request("duration", "", 0)
					return nil, nil, errors.New(stage + ": failed")
				}
				return []byte("{}\n"), nil, nil
			}
			var logs bytes.Buffer
			writer := io.Discard
			if stage == "output" {
				writer = historyFailWriter{stop}
			}
			err := runNATSWithRenderer(RunOptions{Ctx: context.Background(), Stdout: writer, Logger: logx.New(&logs)}, natsOptions{historySelected: true, duration: time.Second, count: 1}, stop, func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
				return f, nil
			}, render)
			reason, _, code := stop.result()
			if err == nil || f.closes != 1 || reason != "failure" || code != 1 {
				t.Fatal(err, f.closes, logs.String())
			}
		})
	}
	f := &historyFake{next: func(context.Context) (natsinspect.Record, error) {
		return natsinspect.Record{}, natsinspect.ErrExhausted
	}}
	var logs bytes.Buffer
	err := runNATSWithRenderer(RunOptions{Ctx: context.Background(), Stdout: io.Discard, Logger: logx.New(&logs)}, natsOptions{historySelected: true, duration: time.Second}, nil, func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
		return f, nil
	}, natsEvent)
	if err != nil || !strings.Contains(logs.String(), "reason=exhaustion; admitted=0") || !strings.Contains(logs.String(), "History ready") {
		t.Fatal(err, logs.String())
	}
}

type historyFailWriter struct{ stop *natsStop }

func (w historyFailWriter) Write([]byte) (int, error) {
	w.stop.request("duration", "", 0)
	return 0, errors.New("synthetic broken sink")
}
