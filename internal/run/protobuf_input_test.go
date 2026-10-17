package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/sdkks/nesdit/internal/protobufview"
)

func protobufFixture(t *testing.T) string {
	t.Helper()
	f := &descriptorpb.FileDescriptorProto{Name: proto.String("event.proto"), Syntax: proto.String("proto3"), Package: proto.String("example"), MessageType: []*descriptorpb.DescriptorProto{{Name: proto.String("Event"), Field: []*descriptorpb.FieldDescriptorProto{
		{Name: proto.String("id"), Number: proto.Int32(1), Type: descriptorpb.FieldDescriptorProto_TYPE_INT64.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()},
		{Name: proto.String("active"), Number: proto.Int32(2), Type: descriptorpb.FieldDescriptorProto_TYPE_BOOL.Enum(), Label: descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()},
	}}}}
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{f, protodesc.ToFileDescriptorProto(wrapperspb.File_google_protobuf_wrappers_proto), protodesc.ToFileDescriptorProto(structpb.File_google_protobuf_struct_proto)}}
	data, err := proto.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "descriptor.pb")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func protobufArgs(descriptor, name string) []string {
	return []string{"--protobuf-stdin", "--descriptor-set", descriptor, "--message-type", name}
}
func protobufExecute(args []string, r io.Reader, w io.Writer) (int, string) {
	var logs bytes.Buffer
	code := Execute(RunOptions{Args: args, Stdin: r, Stdout: w, Stderr: &logs})
	return code, logs.String()
}
func protobufWire(t *testing.T, m proto.Message) []byte {
	t.Helper()
	data, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type protobufReadSpy struct{ reads int }

func (r *protobufReadSpy) Read([]byte) (int, error) {
	r.reads++
	return 0, errors.New("unexpected payload access")
}

func TestProtobufFileStdinAndQueries(t *testing.T) {
	descriptor := protobufFixture(t)
	originalDescriptor, err := os.ReadFile(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, typ string
		body      []byte
		extra     []string
		want      string
		code      int
	}{
		{"object", "example.Event", []byte{8, 42, 16, 1}, nil, "{\"active\":true,\"id\":\"42\"}\n", 0},
		{"empty", "example.Event", nil, nil, "{}\n", 0},
		{"scalar", "google.protobuf.StringValue", protobufWire(t, wrapperspb.String("hello")), nil, "\"hello\"\n", 0},
		{"array", "google.protobuf.ListValue", protobufWire(t, &structpb.ListValue{Values: []*structpb.Value{structpb.NewBoolValue(true), structpb.NewNullValue()}}), nil, "[true,null]\n", 0},
		{"filter true", "example.Event", []byte{8, 42, 16, 1}, []string{"--filter", ".active == true"}, "{\"active\":true,\"id\":\"42\"}\n", 0},
		{"false skips project", "example.Event", nil, []string{"--filter", "false", "--project", `error("private")`}, "", 0},
		{"scalar project", "example.Event", []byte{8, 42}, []string{"--project", ".id"}, "\"42\"\n", 0},
		{"null project", "example.Event", nil, []string{"--project", "null"}, "null\n", 0},
		{"array project", "example.Event", nil, []string{"--project", "[1,2]"}, "[1,2]\n", 0},
		{"construct project", "example.Event", nil, []string{"--project", ".new = true"}, "{\"new\":true}\n", 0},
		{"projection ordering", "example.Event", []byte{8, 42, 16, 1}, []string{"--project", ".a = 1"}, "{\"active\":true,\"id\":\"42\",\"a\":1}\n", 0},
		{"wrong predicate", "example.Event", nil, []string{"--filter", "1"}, "", 1},
		{"empty predicate", "example.Event", nil, []string{"--filter", "empty"}, "", 1},
		{"multiple predicate", "example.Event", nil, []string{"--filter", "true,false"}, "", 1},
		{"empty project", "example.Event", nil, []string{"--project", "empty"}, "", 1},
		{"multiple project", "example.Event", nil, []string{"--project", "1,2"}, "", 1},
		{"runtime project", "example.Event", nil, []string{"--project", `error("private")`}, "", 1},
		{"invalid body", "example.Event", []byte{255}, nil, "", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdinOut bytes.Buffer
			args := append(protobufArgs(descriptor, tt.typ), tt.extra...)
			code, logs := protobufExecute(args, bytes.NewReader(tt.body), &stdinOut)
			if code != tt.code || stdinOut.String() != tt.want {
				t.Fatalf("stdin code=%d out=%q logs=%s", code, stdinOut.String(), logs)
			}
			if strings.Contains(logs, "private") {
				t.Fatal("query content leaked")
			}
			path := filepath.Join(t.TempDir(), "payload,with,commas.bin")
			if err := os.WriteFile(path, tt.body, 0o600); err != nil {
				t.Fatal(err)
			}
			args = append([]string{"--protobuf-file", path, "--descriptor-set", descriptor, "--message-type", tt.typ}, tt.extra...)
			spy := new(protobufReadSpy)
			var fileOut bytes.Buffer
			code, logs = protobufExecute(args, spy, &fileOut)
			if code != tt.code || fileOut.String() != tt.want || spy.reads != 0 {
				t.Fatalf("file code=%d out=%q reads=%d logs=%s", code, fileOut.String(), spy.reads, logs)
			}
			unchanged, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(unchanged, tt.body) {
				t.Fatal("input changed", err)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != 1 {
				t.Fatal("unexpected output/backup file", err)
			}
		})
	}
	unchanged, err := os.ReadFile(descriptor)
	if err != nil || !bytes.Equal(unchanged, originalDescriptor) {
		t.Fatal("descriptor changed", err)
	}
}

func TestProtobufValidationBeforeInput(t *testing.T) {
	descriptor := protobufFixture(t)
	base := protobufArgs(descriptor, "example.Event")
	cases := [][]string{
		{"--protobuf-stdin=false"}, {"--protobuf-file", ""}, {"--protobuf-file", "-"},
		{"--descriptor-set", descriptor}, {"--message-type", "example.Event"}, {"--filter", "true"}, {"--project", "."}, {"--nats-codec", "protobuf"},
		append(append([]string(nil), base...), "--protobuf-file", "missing"),
		append(append([]string(nil), base...), "positional"),
		append(append([]string(nil), base...), "--filter", " "),
		append(append([]string(nil), base...), "--filter", "["),
		append(append([]string(nil), base...), "--project", "["),
		{"--protobuf-file", "one", "--protobuf-file", "two", "--descriptor-set", descriptor, "--message-type", "example.Event"},
		protobufArgs("missing-descriptor", "example.Event"), protobufArgs(descriptor, ".example.Event"), protobufArgs(descriptor, "example.Missing"), protobufArgs(descriptor, " "),
		{"--protobuf-stdin", "--descriptor-set", "-", "--message-type", "example.Event"},
	}
	// Every other registered flag is forbidden even at its explicit default.
	NewRootCmd().Flags().VisitAll(func(f *pflag.Flag) {
		switch f.Name {
		case "protobuf-file", "protobuf-stdin", "descriptor-set", "message-type", "filter", "project", "log-format", "help", "version":
			return
		}
		cases = append(cases, append(append([]string(nil), base...), "--"+f.Name+"="+f.DefValue))
	})
	for _, args := range cases {
		spy := new(protobufReadSpy)
		var out bytes.Buffer
		code, logs := protobufExecute(args, spy, &out)
		if code != 1 || spy.reads != 0 || out.Len() != 0 {
			t.Fatalf("args=%q code=%d reads=%d out=%q logs=%s", args, code, spy.reads, out.String(), logs)
		}
	}
	// Failed preparation takes precedence over even statting a missing payload.
	for _, extra := range [][]string{{"--filter", "["}, {"--project", "["}, {"--message-type", "example.Missing"}} {
		args := append([]string{"--protobuf-file", "missing-payload", "--descriptor-set", descriptor, "--message-type", "example.Event"}, extra...)
		code, logs := protobufExecute(args, new(protobufReadSpy), io.Discard)
		if code != 1 || strings.Contains(logs, "acquire:") {
			t.Fatalf("payload accessed before preparation: %s", logs)
		}
	}
	// Existing native JSON does not silently gain a Protobuf schema option.
	code, _ := protobufExecute([]string{"--nats", "x", "--server", "nats://localhost:4222", "--duration", "1s", "--descriptor-set", descriptor}, new(protobufReadSpy), io.Discard)
	if code != 1 {
		t.Fatal("native Protobuf exposed")
	}
}

type protobufPartialReader struct{}

func (protobufPartialReader) Read(p []byte) (int, error) {
	copy(p, []byte{8, 1})
	return 2, errors.New("secret read error")
}

type protobufIdleReader struct{}

func (protobufIdleReader) Read([]byte) (int, error) { return 0, nil }

type protobufShortWriter struct{}

func (protobufShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func TestProtobufAcquisitionAndOutputFailure(t *testing.T) {
	args := protobufArgs(protobufFixture(t), "example.Event")
	for _, r := range []io.Reader{protobufPartialReader{}, protobufIdleReader{}, bytes.NewReader(make([]byte, protobufview.RawLimit+1))} {
		var out bytes.Buffer
		code, logs := protobufExecute(args, r, &out)
		if code != 1 || out.Len() != 0 || strings.Contains(logs, "secret") {
			t.Fatalf("code=%d out=%d logs=%s", code, out.Len(), logs)
		}
	}
	code, logs := protobufExecute(args, bytes.NewReader(nil), protobufShortWriter{})
	if code != 1 || !strings.Contains(logs, "write:") {
		t.Fatalf("short writer: %d %s", code, logs)
	}
}

func TestProtobufFinalOutputBoundary(t *testing.T) {
	args := protobufArgs(protobufFixture(t), "example.Event")
	for _, tt := range []struct {
		expression string
		want       int
		size       int
	}{{`"x" * 16777213`, 0, natsEventLimit}, {`"x" * 16777214`, 1, 0}} {
		var out bytes.Buffer
		code, logs := protobufExecute(append(append([]string(nil), args...), "--project", tt.expression), bytes.NewReader(nil), &out)
		if code != tt.want || out.Len() != tt.size {
			t.Fatalf("final boundary code=%d size=%d logs=%s", code, out.Len(), logs)
		}
	}
}

type protobufBorrowedReader struct {
	entered, release  chan struct{}
	closed, deadlined atomic.Int32
}

func (r *protobufBorrowedReader) Read(p []byte) (int, error) {
	close(r.entered)
	<-r.release
	copy(p, []byte{8, 42})
	return 2, io.EOF
}
func (r *protobufBorrowedReader) Close() error                    { r.closed.Add(1); return nil }
func (r *protobufBorrowedReader) SetReadDeadline(time.Time) error { r.deadlined.Add(1); return nil }
func TestProtobufExecuteBorrowedOwnership(t *testing.T) {
	reader := &protobufBorrowedReader{entered: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out, logs bytes.Buffer
	done := make(chan int, 1)
	args := protobufArgs(protobufFixture(t), "example.Event")
	go func() { done <- Execute(RunOptions{Args: args, Ctx: ctx, Stdin: reader, Stdout: &out, Stderr: &logs}) }()
	<-reader.entered
	cancel()
	select {
	case code := <-done:
		t.Fatalf("returned with detached read: %d", code)
	case <-time.After(30 * time.Millisecond):
	}
	close(reader.release)
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("cancel exit=%d logs=%s", code, logs.String())
		}
	case <-time.After(time.Second):
		t.Fatal("responsive borrowed read did not return")
	}
	if reader.closed.Load() != 0 || reader.deadlined.Load() != 0 || out.Len() != 0 {
		t.Fatalf("borrowed input changed or partial emitted: close=%d deadline=%d out=%q", reader.closed.Load(), reader.deadlined.Load(), out.String())
	}
}

func TestProtobufLogFormatAndHelp(t *testing.T) {
	args := protobufArgs(protobufFixture(t), "example.Event")
	_, logs := protobufExecute(append(args, "--filter", "[", "--log-format", "json"), bytes.NewReader(nil), io.Discard)
	if !strings.HasPrefix(logs, "{") || !strings.Contains(logs, "filter:") {
		t.Fatal(logs)
	}
	_, logs = protobufExecute([]string{"--protobuf-stdin", "--format", "json", "--log-format", "json"}, new(protobufReadSpy), io.Discard)
	if !strings.HasPrefix(logs, "{") || !strings.Contains(logs, "flag.invalid") {
		t.Fatal(logs)
	}
	for _, arg := range []string{"--help", "--version"} {
		spy := new(protobufReadSpy)
		var out bytes.Buffer
		code, _ := protobufExecute([]string{"--protobuf-stdin", arg}, spy, &out)
		if code != 0 || spy.reads != 0 || out.Len() == 0 {
			t.Fatalf("%s code=%d reads=%d", arg, code, spy.reads)
		}
	}
}
