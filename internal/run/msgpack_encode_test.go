package run

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func encodeArgs(codec string, extra ...string) []string {
	return append([]string{"--input-stdin", "--input-codec", codec, "--to", "msgpack"}, extra...)
}

func TestMsgpackEncodeSelection(t *testing.T) {
	for _, tt := range []struct {
		codec, body, hex string
		args             []string
		code             int
	}{
		{"json", `{"$int":"7"}`, "81a424696e74a137", nil, 0},
		{"msgpack-json", `{"$int":"7"}`, "07", nil, 0},
		{"json", `[1e3,-0,9007199254740993]`, "93cb408f400000000000cb8000000000000000cf0020000000000001", []string{"--filter", "true"}, 0},
		{"json", `[1e3,-0]`, "92cd03e800", []string{"--project", "."}, 0},
		{"json", `{"bad":1e309,"ok":7}`, "07", []string{"--project", ".ok"}, 0},
		{"json", `{"bad":18446744073709551616,"ok":7}`, "07", []string{"--filter", ".ok == 7", "--project", ".ok"}, 0},
		{"json", `1e309`, "", []string{"--filter", "false", "--project", `error("private-value")`}, 0},
		{"json", `18446744073709551616`, "", []string{"--filter", "false"}, 0},
		{"json", `1e-4000`, "", nil, 1},
		{"json", `1e309`, "", nil, 1},
		{"json", `1e309`, "", []string{"--project", "."}, 1},
		{"msgpack-json", `{"$int":"7"}`, "", []string{"--project", "7"}, 1},
		{"msgpack-json", `{"$map":[["x",{"$int":"7"}]]}`, "07", []string{"--project", `."$map"[0][1]`}, 0},
		{"msgpack-json", `{"$int":"bad-private"}`, "", []string{"--filter", "false"}, 1},
		{"json", `{"private-key":1,"private-key":2}`, "", []string{"--filter", "false"}, 1},
		{"json", `"\ud800"`, "", []string{"--filter", "false"}, 1},
		{"json", `null`, "", []string{"--project", "empty"}, 1},
		{"json", `null`, "", []string{"--project", "1,2"}, 1},
		{"json", `{"z":1,"a":2}`, "82a17a01a16102", []string{"--filter", "(.z = 9) | true"}, 0},
		{"json", `{"z":1,"a":2}`, "01", []string{"--filter", "(.z = 9) | true", "--project", ".z"}, 0},
	} {
		t.Run(tt.codec+tt.body+strings.Join(tt.args, " "), func(t *testing.T) {
			code, out, logs := textExecute(encodeArgs(tt.codec, tt.args...), strings.NewReader(tt.body))
			if code != tt.code || hex.EncodeToString([]byte(out)) != tt.hex || strings.Contains(logs, "private-") {
				t.Fatalf("code=%d bytes=%x logs=%s", code, out, logs)
			}
		})
	}
}

func TestMsgpackEncodeMatrixAndPreparation(t *testing.T) {
	for _, codec := range []string{"json", "msgpack-json", "msgpack"} {
		for _, flag := range []string{"--filter", "--project"} {
			code, out, logs := textExecute(encodeArgs(codec, flag, "private-invalid["), textNoRead{t})
			if code != 1 || out != "" || strings.Contains(logs, "private-invalid") {
				t.Fatal(code, out, logs)
			}
		}
		root := NewRootCmd()
		root.Flags().VisitAll(func(f *pflag.Flag) {
			switch f.Name {
			case "input-file", "input-stdin", "input-codec", "to", "filter", "project", "log-format", "help", "version":
				return
			}
			code, out, _ := textExecute(encodeArgs(codec, "--"+f.Name+"="+f.DefValue), textNoRead{t})
			if code != 1 || out != "" {
				t.Fatal(codec, f.Name, code, out)
			}
		})
	}
	for _, args := range [][]string{
		{"--input-stdin", "--input-codec", "json"},
		{"--input-stdin", "--input-codec", "msgpack-json", "--to", "json"},
		encodeArgs("text"), encodeArgs("flatbuffers"), encodeArgs("protobuf"), encodeArgs("yaml"), encodeArgs("jsonl"),
		{"--input-file", "-", "--input-codec", "json", "--to", "msgpack"},
		{"--input-stdin=false", "--input-codec", "json", "--to", "msgpack"},
		{"--input-stdin", "--input-stdin", "--input-codec", "json", "--to", "msgpack"},
		{"--input-file", "a", "--input-file", "b", "--input-codec", "json", "--to", "msgpack"},
		encodeArgs("json", "--input-file", "a"), encodeArgs("json", "position"),
	} {
		code, out, _ := textExecute(args, textNoRead{t})
		if code != 1 || out != "" {
			t.Fatal(args, code, out)
		}
	}
	// Invalid physical descriptors fail closed before even query preparation.
	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	code := Execute(RunOptions{Args: encodeArgs("json", "--project", "invalid["), Stdin: textNoRead{t}, Stdout: file, Stderr: &logs})
	if code != 1 || !strings.Contains(logs.String(), "destination:") {
		t.Fatal(code, logs.String())
	}
}

type binaryPartialWriter struct {
	data bytes.Buffer
	fail bool
}

func (w *binaryPartialWriter) Write(p []byte) (int, error) {
	n := len(p) / 2
	_, _ = w.data.Write(p[:n])
	if w.fail {
		return n, errors.New("private-output-error")
	}
	return n, nil
}
func TestMsgpackEncodeBorrowedIO(t *testing.T) {
	for _, fail := range []bool{false, true} {
		w := &binaryPartialWriter{fail: fail}
		var logs bytes.Buffer
		code := Execute(RunOptions{Args: encodeArgs("json"), Stdin: strings.NewReader(`"abcdef"`), Stdout: w, Stderr: &logs})
		if code != 1 || w.data.Len() != 3 || !strings.Contains(logs.String(), "write:") || strings.Contains(logs.String(), "private-output") {
			t.Fatal(code, w.data.Len(), logs.String())
		}
	}
	code, out, logs := textExecute(encodeArgs("json", "--filter", "false"), textReadFailure{})
	if code != 1 || out != "" || strings.Contains(logs, "private-read-value") {
		t.Fatal(code, out, logs)
	}
	// Opaque writers remain usable; actual file writers remain open and contain
	// exactly one value (no newline/display). File input must not read stdin.
	path := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(path, []byte(`1e3`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "out.msgpack")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	code = Execute(RunOptions{Args: []string{"--input-file", path, "--input-codec", "json", "--to", "msgpack"}, Stdin: textNoRead{t}, Stdout: output})
	if code != 0 {
		t.Fatal(code)
	}
	if _, err = output.Seek(0, io.SeekStart); err != nil {
		t.Fatal("borrowed output closed", err)
	}
	data, err := io.ReadAll(output)
	if err != nil || hex.EncodeToString(data) != "cb408f400000000000" {
		t.Fatal(data, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code = Execute(RunOptions{Ctx: ctx, Args: encodeArgs("json"), Stdin: textNoRead{t}})
	if code != 130 {
		t.Fatal(code)
	}
}
