package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/natsinspect"
	"github.com/spf13/pflag"
)

func textArgs(extra ...string) []string {
	return append([]string{"--input-stdin", "--input-codec", "text"}, extra...)
}
func textExecute(args []string, input io.Reader) (int, string, string) {
	var out, logs bytes.Buffer
	code := Execute(RunOptions{Args: args, Stdin: input, Stdout: &out, Stderr: &logs})
	return code, out.String(), logs.String()
}

type textNoRead struct{ t *testing.T }

func (r textNoRead) Read([]byte) (int, error) { r.t.Fatal("unexpected input read"); return 0, io.EOF }

func TestTextLocalWholeBody(t *testing.T) {
	for _, body := range []string{"", "\ufeffhello 世界\r\n\x00\t \x1b[31m", " \n first\nsecond\r\n "} {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		want := string(encoded) + "\n"
		code, out, logs := textExecute(textArgs(), strings.NewReader(body))
		if code != 0 || out != want || logs != "" {
			t.Fatalf("stdin: %d %q %s", code, out, logs)
		}
		path := filepath.Join(t.TempDir(), "text,body.txt")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, source := range []string{path, path + ".link"} {
			if source != path {
				if err := os.Symlink(path, source); err != nil {
					t.Fatal(err)
				}
			}
			code, out, logs = textExecute([]string{"--input-file", source, "--input-codec", "text", "--to", "json", "--filter", "true"}, textNoRead{t})
			if code != 0 || out != want || logs != "" {
				t.Fatalf("file: %d %q %s", code, out, logs)
			}
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != body {
			t.Fatal("source changed", err)
		}
	}
}

func TestTextQueriesAndAdmission(t *testing.T) {
	for _, tt := range []struct {
		body string
		args []string
		want string
		code int
	}{
		{"abc\nxyz", []string{"--filter", `contains("abc")`, "--project", `split("\n")`}, "[\"abc\",\"xyz\"]\n", 0},
		{"abc", []string{"--filter", "false", "--project", `error("private-value")`}, "", 0},
		{"abc", []string{"--project", `{original: ., count:length}`}, "{\"count\":3,\"original\":\"abc\"}\n", 0},
		{"abc", []string{"--project", "null"}, "null\n", 0},
		{"abc", []string{"--project", "true"}, "true\n", 0},
		{"abc", []string{"--project", "42"}, "42\n", 0},
		{"abc", []string{"--filter", "."}, "", 1},
		{"abc", []string{"--filter", "empty"}, "", 1},
		{"abc", []string{"--filter", "true,false"}, "", 1},
		{"abc", []string{"--project", "empty"}, "", 1},
		{"abc", []string{"--project", ".,."}, "", 1},
		{"abc", []string{"--project", `error("private-value")`}, "", 1},
		{"\xff", []string{"--filter", "false"}, "", 1},
		{strings.Repeat("x", (10<<20)+1), []string{"--filter", "false"}, "", 1},
		{strings.Repeat("<", 3<<20), nil, "", 1},
		{strings.Repeat("<", 3<<20), []string{"--filter", "false"}, "", 0},
		{strings.Repeat("x", 10<<20), []string{"--project", "length"}, "10485760\n", 0},
	} {
		code, out, logs := textExecute(textArgs(tt.args...), strings.NewReader(tt.body))
		if code != tt.code || out != tt.want || strings.Contains(logs, "private-value") {
			t.Fatalf("args=%v code=%d want=%d out=%q logs=%s", tt.args, code, tt.code, out, logs)
		}
	}
	for _, flag := range []string{"--filter", "--project"} {
		code, out, logs := textExecute(textArgs(flag, "private-invalid-query["), textNoRead{t})
		if code != 1 || out != "" || strings.Contains(logs, "private-invalid-query") {
			t.Fatal(code, out, logs)
		}
	}
}

func TestTextFlagIsolation(t *testing.T) {
	for _, args := range [][]string{
		{"--input-stdin"}, {"--input-stdin=false", "--input-codec", "text"},
		{"--input-stdin", "--input-stdin", "--input-codec", "text"},
		{"--input-file", "", "--input-codec", "text"}, {"--input-file", "-", "--input-codec", "text"},
		{"--input-file", "a", "--input-file", "b", "--input-codec", "text"},
		{"--input-file", "a", "--input-stdin", "--input-codec", "text"},
		{"--input-codec", "text"}, {"--to", "json"},
		textArgs("position"), textArgs("--to", "msgpack"),
	} {
		code, out, _ := textExecute(args, textNoRead{t})
		if code != 1 || out != "" {
			t.Fatal(args, code, out)
		}
	}
	for _, codec := range []string{"json", "msgpack-json", "flatbuffers", "protobuf", "", "TEXT"} {
		code, out, _ := textExecute([]string{"--input-stdin", "--input-codec", codec}, textNoRead{t})
		if code != 1 || out != "" {
			t.Fatal(codec, code, out)
		}
	}
	// Every unrelated flag is rejected even when explicitly set to its default.
	root := NewRootCmd()
	root.Flags().VisitAll(func(f *pflag.Flag) {
		switch f.Name {
		case "input-file", "input-stdin", "input-codec", "to", "filter", "project", "log-format", "help", "version":
			return
		}
		code, out, _ := textExecute(textArgs("--"+f.Name+"="+f.DefValue), textNoRead{t})
		if code != 1 || out != "" {
			t.Fatalf("allowed touched flag %s: %d %q", f.Name, code, out)
		}
	})
	for _, args := range [][]string{
		{"--protobuf-stdin", "--input-codec", "text"},
		{"--protobuf-stdin", "--to", "json"},
		{"--nats", "orders.>", "--input-codec", "text"},
		{"--nats-history", "ORDERS", "--to", "json"},
	} {
		code, out, _ := textExecute(args, textNoRead{t})
		if code != 1 || out != "" {
			t.Fatal(args, code, out)
		}
	}
	for _, args := range [][]string{
		textArgs("--count=private-value"),
		textArgs("--unknown-private-value"),
		{"--nats", "orders.>", "--nats-codec", "text", "--duration=private-value"},
	} {
		code, out, logs := textExecute(args, textNoRead{t})
		if code != 1 || out != "" || strings.Contains(logs, "private-value") || !strings.Contains(logs, "configuration:") {
			t.Fatal(code, out, logs)
		}
	}
	for _, path := range []string{t.TempDir(), filepath.Join(t.TempDir(), "missing")} {
		code, out, logs := textExecute([]string{"--input-file", path, "--input-codec", "text"}, textNoRead{t})
		if code != 1 || out != "" || !strings.Contains(logs, "acquire:") || strings.Contains(logs, path) {
			t.Fatal(code, out, logs)
		}
	}
}

type textReadFailure struct{}

func (textReadFailure) Read(p []byte) (int, error) {
	copy(p, "partial")
	return 7, errors.New("private-read-value")
}

type textShortWriter struct{}

func (textShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestTextNativePreparationBeforeConnection(t *testing.T) {
	for _, history := range []bool{false, true} {
		var logs bytes.Buffer
		opts := RunOptions{Ctx: context.Background(), Stdin: textNoRead{t}, Stdout: io.Discard, Logger: logx.New(&logs)}
		n := natsOptions{codec: "text", filterSet: true, filter: "private-invalid[", duration: time.Second, historySelected: history}
		err := runNATS(opts, n, nil, func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
			t.Fatal("connected before query preparation")
			return nil, errors.New("unexpected connection")
		})
		if err == nil || strings.Contains(err.Error(), "private-invalid") || !strings.Contains(err.Error(), "filter:") {
			t.Fatal(err)
		}
	}
}

func TestTextIOFailuresAndCancellation(t *testing.T) {
	code, out, logs := textExecute(textArgs("--filter", "false"), textReadFailure{})
	if code != 1 || out != "" || strings.Contains(logs, "private-read-value") {
		t.Fatal(code, out, logs)
	}
	var diagnostic bytes.Buffer
	code = Execute(RunOptions{Args: textArgs(), Stdin: strings.NewReader("body"), Stdout: textShortWriter{}, Stderr: &diagnostic})
	if code != 1 || !strings.Contains(diagnostic.String(), "write:") {
		t.Fatal(code, diagnostic.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code = Execute(RunOptions{Args: textArgs(), Ctx: ctx, Stdin: textNoRead{t}})
	if code != 130 {
		t.Fatal(code)
	}
}
