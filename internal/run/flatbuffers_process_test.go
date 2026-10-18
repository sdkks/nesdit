package run

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sdkks/nesdit/internal/testpty"
)

// TestFlatbuffersCLIProcess executes the actual built CLI against real
// payload files and an explicit-stdin payload.
func TestFlatbuffersCLIProcess(t *testing.T) {
	bin := buildNATSCLI(t)
	payload, schema := flatbuffersFiles(t)
	body, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"42","small":7,"tiny":-128,"flag":false,"ratio":0.10000000149011612,"negz":2.5,"opt_score":"-0","opt_big":"18446744073709551615","label":"héllo wörld  embedded","color":77,"level":"5","stamp":"18446744073709551615","tags":["a","","c"],"nums":[],"inners":[{"note":"first","count":-3},{"note":"second","count":9}],"points":[{"a":-2,"b":65535},{"a":3,"b":4}],"vec3":{"x":1.5,"y":"-0","z":3.25},"old_field":0,"body_type":"0","side_type":0,"node":{"name":"root","child":{"name":"deep","peers":[{"name":"p1"}]}},"bytes":[0,1,255,10]}` + "\n"
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"file", []string{"--input-file", payload, "--bfbs", schema}},
		{"stdin", []string{"--input-stdin", "--bfbs", schema}},
	} {
		cmd, _ := protobufProcess(t, bin, append([]string{"--input-codec", "flatbuffers"}, tt.args...))
		cmd.Stdin = bytes.NewReader(body)
		var out, logs bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &logs
		if err = cmd.Run(); err != nil {
			t.Fatalf("%s: %v %s", tt.name, err, logs.String())
		}
		if out.String() != want || logs.Len() != 0 {
			t.Fatalf("%s: %q %s", tt.name, out.String(), logs.String())
		}
	}
	// File mode never reads stdin; a blocked pipe stays unread.
	cmd, _ := protobufProcess(t, bin, []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "--project", ".id"})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	cmd.Stdin = r
	var out, logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &logs
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	protobufWait(t, cmd, 0, start)
	if out.String() != "\"42\"\n" || logs.Len() != 0 {
		t.Fatalf("file-not-stdin: %q %s", out.String(), logs.String())
	}
}

// TestFlatbuffersCLIInterrupt covers cooperative cancellation during a
// blocked stdin payload acquisition.
func TestFlatbuffersCLIInterrupt(t *testing.T) {
	bin := buildNATSCLI(t)
	_, schema := flatbuffersFiles(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	cmd, _ := protobufProcess(t, bin, []string{"--input-stdin", "--input-codec", "flatbuffers", "--bfbs", schema})
	cmd.Stdin = r
	var out, logs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &logs
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(bytes.Repeat([]byte{0x41}, 64)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	if err = cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	protobufWait(t, cmd, 130, start)
	if out.Len() != 0 {
		t.Fatal("interrupted acquisition emitted output")
	}
	// The forced-stop fallback may omit diagnostics; cancellation is proven
	// by the 130 exit and the absence of any value bytes.
	t.Logf("interrupt logs: %s", logs.String())
}

// TestFlatbuffersCLITerminalLayout runs the actual CLI on a real PTY:
// pretty layout, color policy and JSON escaping of payload control bytes.
func TestFlatbuffersCLITerminalLayout(t *testing.T) {
	bin := buildNATSCLI(t)
	payload, schema := flatbuffersFiles(t)
	for _, tt := range []struct {
		name, color, term string
		styled            bool
	}{{"color", "", "xterm", true}, {"no-color", "1", "xterm", false}, {"dumb", "", "dumb", false}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tt.color)
			t.Setenv("TERM", tt.term)
			master, slave := testpty.Open(t)
			cmd, _ := protobufProcess(t, bin, []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "--project", "{n: .label}"})
			cmd.Stdin = bytes.NewReader(nil)
			cmd.Stdout = slave
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			if err := master.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			data := make([]byte, 4096)
			n, err := master.Read(data)
			if err != nil {
				t.Fatal(err)
			}
			out := string(data[:n])
			if !strings.HasPrefix(out, "{\n  ") || strings.Contains(out, "\x1b[") != tt.styled || !strings.Contains(out, "héllo") || !strings.HasSuffix(out, "\n") {
				t.Fatalf("terminal %q", out)
			}
		})
	}
}
