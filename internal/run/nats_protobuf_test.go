package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/natsinspect"
	"github.com/sdkks/nesdit/internal/natstest"
)

func nativeProtobufArgs(descriptor, name string) []string {
	return []string{"--nats-codec", "protobuf", "--descriptor-set", descriptor, "--message-type", name}
}

func TestNATSProtobufValidationBeforeInput(t *testing.T) {
	descriptor := protobufFixture(t)
	malformed := filepath.Join(t.TempDir(), "invalid.pb")
	if err := os.WriteFile(malformed, []byte{255}, 0o600); err != nil {
		t.Fatal(err)
	}
	native := []string{"--nats", "orders.>", "--server", "nats://127.0.0.1:1", "--duration", "1s"}
	for _, extra := range [][]string{
		{"--nats-codec", "unknown"}, {"--nats-codec", ""},
		{"--nats-codec", "protobuf"},
		{"--nats-codec", "protobuf", "--descriptor-set", descriptor},
		{"--nats-codec", "protobuf", "--message-type", "example.Event"},
		nativeProtobufArgs("", "example.Event"), nativeProtobufArgs("-", "example.Event"),
		nativeProtobufArgs(descriptor, ""), nativeProtobufArgs(descriptor, ".example.Event"),
		nativeProtobufArgs(descriptor, "type.example/example.Event"),
		nativeProtobufArgs(malformed, "example.Event"),
		nativeProtobufArgs(descriptor, "example.Missing"), nativeProtobufArgs("missing-private-descriptor", "example.Event"),
		{"--descriptor-set", descriptor}, {"--message-type", "example.Event"},
		{"--nats-codec", "json", "--descriptor-set", descriptor, "--message-type", "example.Event"},
		append(nativeProtobufArgs(descriptor, "example.Event"), "--filter", "["),
		append(nativeProtobufArgs(descriptor, "example.Event"), "--project", ""),
		append(nativeProtobufArgs(descriptor, "example.Event"), "--format", "json"),
		append(nativeProtobufArgs(descriptor, "example.Event"), "--protobuf-stdin"),
		append(nativeProtobufArgs(descriptor, "example.Event"), "payload.bin"),
	} {
		args := append(append([]string(nil), native...), extra...)
		spy := new(protobufReadSpy)
		var out bytes.Buffer
		code, logs := protobufExecute(args, spy, &out)
		if code != 1 || spy.reads != 0 || out.Len() != 0 || strings.Contains(logs, "connect:") || strings.Contains(logs, "missing-private-descriptor") {
			t.Fatalf("args=%q code=%d reads=%d out=%q logs=%s", args, code, spy.reads, out.String(), logs)
		}
	}
	for _, args := range [][]string{{"--nats-codec", "json"}, {"--nats-codec", "protobuf"}, append(protobufArgs(descriptor, "example.Event"), "--nats-codec", "json")} {
		spy := new(protobufReadSpy)
		code, logs := protobufExecute(args, spy, io.Discard)
		if code != 1 || spy.reads != 0 {
			t.Fatalf("%q code=%d reads=%d %s", args, code, spy.reads, logs)
		}
	}
}

func TestNATSProtobufPreparationBeforeConnect(t *testing.T) {
	descriptor := protobufFixture(t)
	for _, p := range []protobufOptions{{descriptor: "missing", message: "example.Event"}, {descriptor: descriptor, message: "example.Missing"}} {
		err := runNATS(RunOptions{Ctx: context.Background(), Logger: logx.New(io.Discard)}, natsOptions{codec: "protobuf", protobuf: p}, nil,
			func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
				t.Fatal("connection attempted before schema preparation succeeded")
				return nil, nil
			})
		if err == nil || !strings.HasPrefix(err.Error(), "prepare:") {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := runNATS(RunOptions{Ctx: ctx, Logger: logx.New(io.Discard)}, natsOptions{codec: "protobuf", protobuf: protobufOptions{descriptor: descriptor, message: "example.Event"}}, nil,
		func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
			t.Fatal("connected after cancellation")
			return nil, nil
		})
	if _, ok := err.(*natsCancelled); !ok {
		t.Fatal(err)
	}
}

func TestNATSProtobufRealCLI(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	descriptor := protobufFixture(t)
	broker := natstest.Start(t, "")
	pub, err := nats.Connect(broker.URL, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	for _, tc := range []struct {
		name, typ   string
		body        []byte
		headers     nats.Header
		extra       []string
		want, stage string
		code        int
	}{
		{name: "object", typ: "example.Event", body: []byte{8, 42, 16, 1}, want: `{"active":true,"id":"42"}`},
		{name: "scalar", typ: "google.protobuf.StringValue", body: []byte{10, 2, 'o', 'k'}, want: `"ok"`},
		{name: "ordinary-empty", typ: "example.Event", want: `{}`},
		{name: "empty-with-header", typ: "example.Event", headers: nats.Header{"Trace": {"a", "b"}}, want: `{}`},
		{name: "bodyless503", typ: "example.Event", headers: nats.Header{"Status": {"503"}}, stage: "decode:", code: 1},
		{name: "status503-with-body", typ: "example.Event", body: []byte{8, 42}, headers: nats.Header{"Status": {"503"}}, want: `{"id":"42"}`},
		{name: "malformed", typ: "example.Event", body: []byte{255}, stage: "decode:", code: 1},
		{name: "filter-root", typ: "example.Event", body: []byte{8, 42, 16, 1}, extra: []string{"--filter", `.active == true`, "--project", `.id`}, want: `"42"`},
		{name: "false-skips-project", typ: "example.Event", extra: []string{"--filter", "false", "--project", `error("PRIVATE")`}},
		{name: "filter-error", typ: "example.Event", extra: []string{"--filter", `error("PRIVATE")`}, stage: "filter:", code: 1},
		{name: "project-cardinality", typ: "example.Event", extra: []string{"--project", "1,2"}, stage: "project:", code: 1},
		{name: "output-limit", typ: "example.Event", extra: []string{"--project", `"<" * 3000000`}, stage: "encode:", code: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(nativeProtobufArgs(descriptor, tc.typ), "--count", "1")
			args = append(args, tc.extra...)
			ready := false
			out, logs, code := observeCLI(t, bin, broker.URL, args, func() {
				ready = true
				if err := pub.PublishMsg(&nats.Msg{Subject: "orders.x", Header: tc.headers, Data: tc.body}); err != nil {
					t.Fatal(err)
				}
				if err := pub.FlushTimeout(time.Second); err != nil {
					t.Fatal(err)
				}
			})
			want := ""
			if tc.want != "" {
				headers := `{}`
				if tc.name == "empty-with-header" {
					headers = `{"Trace":["a","b"]}`
				}
				if tc.name == "status503-with-body" {
					headers = `{"Status":["503"]}`
				}
				want = "{\"subject\":\"orders.x\",\"headers\":" + headers + ",\"payload\":" + tc.want + "}\n"
			}
			if !ready || code != tc.code || out != want || !strings.Contains(logs, tc.stage) || !strings.Contains(logs, "admitted=1") || strings.Contains(logs, "PRIVATE") {
				t.Fatalf("ready=%t code=%d out=%q logs=%s", ready, code, out, logs)
			}
			t.Logf("stdout=%s stderr=%s exit=%d", out, logs, code)
		})
	}
	t.Run("count-filter-project-metadata", func(t *testing.T) {
		args := append(nativeProtobufArgs(descriptor, "example.Event"), "--count", "2", "--filter", ".active == true", "--project", `{subject: .id, headers: "payload-only"}`)
		out, logs, code := observeCLI(t, bin, broker.URL, args, func() {
			for _, body := range [][]byte{{8, 1}, {8, 42, 16, 1}, {8, 3, 16, 1}} {
				if err := pub.PublishMsg(&nats.Msg{Subject: "orders.x", Header: nats.Header{"Trace": {"a", "b"}}, Data: body}); err != nil {
					t.Fatal(err)
				}
			}
			if err := pub.FlushTimeout(time.Second); err != nil {
				t.Fatal(err)
			}
		})
		want := "{\"subject\":\"orders.x\",\"headers\":{\"Trace\":[\"a\",\"b\"]},\"payload\":{\"headers\":\"payload-only\",\"subject\":\"42\"}}\n"
		if code != 0 || out != want || !strings.Contains(logs, "reason=count; admitted=2 emitted=1 unfinished_event=false") {
			t.Fatal(code, out, logs)
		}
		t.Logf("stdout=%s stderr=%s exit=%d", out, logs, code)
	})
	// Exercise Execute with a successful native session and a stdin read spy.
	t.Run("native-never-reads-stdin", func(t *testing.T) {
		spy := new(protobufReadSpy)
		args := append([]string{"--nats", "orders.>", "--server", broker.URL, "--duration", "200ms"}, nativeProtobufArgs(descriptor, "example.Event")...)
		code, logs := protobufExecute(args, spy, io.Discard)
		if code != 0 || spy.reads != 0 || !strings.Contains(logs, "nats.ready") {
			t.Fatal(code, spy.reads, logs)
		}
	})
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("json-order-explicit-%t", explicit), func(t *testing.T) {
			args := []string{"--count", "1"}
			if explicit {
				args = append(args, "--nats-codec", "json")
			}
			out, logs, code := observeCLI(t, bin, broker.URL, args, func() {
				if err := pub.Publish("orders.x", []byte(`{"z":1,"a":2}`)); err != nil {
					t.Fatal(err)
				}
				if err := pub.FlushTimeout(time.Second); err != nil {
					t.Fatal(err)
				}
			})
			if code != 0 || out != "{\"subject\":\"orders.x\",\"headers\":{},\"payload\":{\"z\":1,\"a\":2}}\n" {
				t.Fatal(code, out, logs)
			}
		})
	}
}
