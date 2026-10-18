package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func msgpackArgs(extra ...string) []string {
	return append([]string{"--input-stdin", "--input-codec", "msgpack"}, extra...)
}

func msgpackString(s string) []byte {
	b := make([]byte, 5+len(s))
	b[0] = 0xdb
	binary.BigEndian.PutUint32(b[1:5], uint32(len(s)))
	copy(b[5:], s)
	return b
}

func TestMsgpackLocalCorpus(t *testing.T) {
	data, err := os.ReadFile("../msgpackview/testdata/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Hex string
		View      json.RawMessage
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, tt := range fixtures {
		t.Run(tt.Name, func(t *testing.T) {
			wire, err := hex.DecodeString(tt.Hex)
			if err != nil {
				t.Fatal(err)
			}
			var expected bytes.Buffer
			if err = json.Compact(&expected, tt.View); err != nil {
				t.Fatal(err)
			}
			want := expected.String() + "\n"
			path := filepath.Join(t.TempDir(), "body,one.msgpack")
			if err = os.WriteFile(path, wire, 0o600); err != nil {
				t.Fatal(err)
			}
			before := sha256.Sum256(wire)
			for _, file := range []bool{false, true} {
				args := msgpackArgs("--filter", "true")
				code, out, logs := 0, "", ""
				if file {
					args = []string{"--input-file", path, "--input-codec", "msgpack", "--to", "json", "--filter", "true"}
					code, out, logs = textExecute(args, textNoRead{t})
				} else {
					code, out, logs = textExecute(args, bytes.NewReader(wire))
				}
				if code != 0 || out != want || logs != "" {
					t.Fatalf("file=%v code=%d out=%q want=%q logs=%s", file, code, out, want, logs)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || sha256.Sum256(after) != before {
				t.Fatal("source changed", err)
			}
		})
	}
}

func TestMsgpackQueriesAndOutputAdmission(t *testing.T) {
	// Ordered duplicate names, not an ordinary object with overwritten keys.
	body := []byte{0x82, 0xa4, 'n', 'a', 'm', 'e', 0xa3, 'A', 'd', 'a', 0xa4, 'n', 'a', 'm', 'e', 0xa5, 'G', 'r', 'a', 'c', 'e'}
	for _, tt := range []struct {
		args []string
		want string
		code int
	}{
		{[]string{"--filter", `any(."$map"[]; .[0] == "name")`, "--project", `[."$map"[] | select(.[0] == "name") | .[1]]`}, "[\"Ada\",\"Grace\"]\n", 0},
		{[]string{"--project", `."$map"[] | .[1]`}, "", 1},
		{[]string{"--project", `{display: ."$map"[0][1], count: 2}`}, "{\"count\":2,\"display\":\"Ada\"}\n", 0},
		{[]string{"--filter", "false", "--project", `error("private-result")`}, "", 0},
		{[]string{"--filter", "."}, "", 1},
		{[]string{"--filter", "empty"}, "", 1},
		{[]string{"--filter", "true,false"}, "", 1},
		{[]string{"--project", "empty"}, "", 1},
		{[]string{"--project", `error("private-result")`}, "", 1},
	} {
		code, out, logs := textExecute(msgpackArgs(tt.args...), bytes.NewReader(body))
		if code != tt.code || out != tt.want || strings.Contains(logs, "private-result") {
			t.Fatal(tt.args, code, out, logs)
		}
	}
	for _, bad := range [][]byte{nil, {0xc1}, {0xc0, '\n'}, append(msgpackString("private-payload"), 0xc0), bytes.Repeat([]byte{0x91}, 102)} {
		code, out, logs := textExecute(msgpackArgs("--filter", "false"), bytes.NewReader(bad))
		if code != 1 || out != "" || strings.Contains(logs, "private-payload") {
			t.Fatal(code, out, logs)
		}
	}
	expanded := msgpackString(strings.Repeat("<", 3<<20))
	for _, suppress := range []bool{false, true} {
		args := msgpackArgs()
		want := 1
		if suppress {
			args = msgpackArgs("--filter", "false")
			want = 0
		}
		code, out, _ := textExecute(args, bytes.NewReader(expanded))
		if code != want || out != "" {
			t.Fatal("output expansion", code, len(out))
		}
	}
	// Decoded depth fits; rich map pair/wrapper depth fails before suppression.
	nested := []byte{0xc0}
	for range 34 {
		nested = append([]byte{0x81, 0xc0}, nested...)
	}
	code, out, logs := textExecute(msgpackArgs("--filter", "false"), bytes.NewReader(nested))
	if code != 1 || out != "" || !strings.Contains(logs, "view limit") {
		t.Fatal(code, out, logs)
	}
	for _, flag := range []string{"--filter", "--project"} {
		code, out, logs := textExecute(msgpackArgs(flag, "private-invalid["), textNoRead{t})
		if code != 1 || out != "" || strings.Contains(logs, "private-invalid") {
			t.Fatal(code, out, logs)
		}
	}
}

func TestMsgpackFlagIsolation(t *testing.T) {
	for _, args := range [][]string{
		{"--input-stdin=false", "--input-codec", "msgpack"},
		{"--input-stdin", "--input-stdin", "--input-codec", "msgpack"},
		{"--input-file", "", "--input-codec", "msgpack"},
		{"--input-file", "-", "--input-codec", "msgpack"},
		{"--input-file", "a", "--input-file", "b", "--input-codec", "msgpack"},
		{"--input-file", "a", "--input-stdin", "--input-codec", "msgpack"},
		{"--input-codec", "msgpack"}, msgpackArgs("position"), msgpackArgs("--to", "yaml"),
		msgpackArgs("--bfbs", "schema.bfbs"),
		{"--protobuf-stdin", "--input-codec", "msgpack"},
		{"--nats", "events", "--nats-codec", "msgpack"},
		{"--nats-history", "EVENTS", "--nats-codec", "msgpack"},
	} {
		code, out, _ := textExecute(args, textNoRead{t})
		if code != 1 || out != "" {
			t.Fatal(args, code, out)
		}
	}
	root := NewRootCmd()
	root.Flags().VisitAll(func(f *pflag.Flag) {
		switch f.Name {
		case "input-file", "input-stdin", "input-codec", "to", "filter", "project", "log-format", "help", "version":
			return
		}
		code, out, _ := textExecute(msgpackArgs("--"+f.Name+"="+f.DefValue), textNoRead{t})
		if code != 1 || out != "" {
			t.Fatal(f.Name, code, out)
		}
	})
}

func TestMsgpackIOFailuresAndCancellation(t *testing.T) {
	code, out, logs := textExecute(msgpackArgs("--filter", "false"), textReadFailure{})
	if code != 1 || out != "" || strings.Contains(logs, "private-read-value") {
		t.Fatal(code, out, logs)
	}
	var diagnostic bytes.Buffer
	code = Execute(RunOptions{Args: msgpackArgs(), Stdin: bytes.NewReader([]byte{0xc0}), Stdout: textShortWriter{}, Stderr: &diagnostic})
	if code != 1 || !strings.Contains(diagnostic.String(), "write:") {
		t.Fatal(code, diagnostic.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code = Execute(RunOptions{Args: msgpackArgs(), Ctx: ctx, Stdin: textNoRead{t}})
	if code != 130 {
		t.Fatal(code)
	}
}
