//go:build darwin || linux

package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdkks/nesdit/internal/testpty"
	"golang.org/x/sys/unix"
)

func TestMsgpackCLIIndependentCorpus(t *testing.T) {
	bin := buildNATSCLI(t)
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
	dir := t.TempDir()
	for _, tt := range fixtures {
		t.Run(tt.Name, func(t *testing.T) {
			wire, err := hex.DecodeString(tt.Hex)
			if err != nil {
				t.Fatal(err)
			}
			before := sha256.Sum256(wire)
			path := filepath.Join(dir, tt.Name+".msgpack")
			if err = os.WriteFile(path, wire, 0o600); err != nil {
				t.Fatal(err)
			}
			var expected bytes.Buffer
			if err = json.Compact(&expected, tt.View); err != nil {
				t.Fatal(err)
			}
			for _, file := range []bool{false, true} {
				args := msgpackArgs()
				if file {
					args = []string{"--input-file", path, "--input-codec", "msgpack"}
				}
				cmd, _ := protobufProcess(t, bin, args)
				if !file {
					cmd.Stdin = bytes.NewReader(wire)
				}
				var logs bytes.Buffer
				cmd.Stderr = &logs
				out, err := cmd.Output()
				if err != nil || string(out) != expected.String()+"\n" || logs.Len() != 0 {
					t.Fatalf("file=%v err=%v stdout=%q stderr=%s", file, err, out, &logs)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || sha256.Sum256(after) != before {
				t.Fatal("input changed", err)
			}
		})
	}
}

func TestMsgpackCLIQueries(t *testing.T) {
	bin := buildNATSCLI(t)
	wire := []byte{0x82, 0xa4, 'n', 'a', 'm', 'e', 0xa3, 'A', 'd', 'a', 0xa4, 'n', 'a', 'm', 'e', 0xa5, 'G', 'r', 'a', 'c', 'e'}
	for _, tt := range []struct {
		args []string
		body []byte
		want string
		code int
	}{
		{msgpackArgs("--filter", `any(."$map"[]; .[0] == "name")`, "--project", `[."$map"[] | .[1]]`), wire, "[\"Ada\",\"Grace\"]\n", 0},
		{msgpackArgs("--project", `{name: ."$map"[0][1]}`), wire, "{\"name\":\"Ada\"}\n", 0},
		{msgpackArgs("--filter", "false", "--project", `error("private-result")`), wire, "", 0},
		{msgpackArgs("--filter", "false"), []byte{0xc1}, "", 1},
		{msgpackArgs("--project", "private-invalid["), nil, "", 1},
	} {
		cmd, _ := protobufProcess(t, bin, tt.args)
		if tt.body != nil {
			cmd.Stdin = bytes.NewReader(tt.body)
		} else {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
			cmd.Stdin = r // Preparation must fail without waiting for payload EOF.
		}
		var logs, out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &logs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		protobufWait(t, cmd, tt.code, time.Now())
		if out.String() != tt.want || strings.Contains(logs.String(), "private-") {
			t.Fatal(out.String(), logs.String())
		}
	}
}

func TestMsgpackCLIPipeEOFAndInterrupt(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, tt := range []struct {
		name                    string
		size                    int
		eof, nonblock, oversize bool
	}{
		{"under-cap-held", 512 << 10, false, false, false},
		{"exact-cap-held", 10 << 20, false, false, false},
		{"deadline-capable", 512 << 10, false, true, false},
		{"over-cap-held", (10 << 20) + 1, false, false, true},
		{"exact-cap-eof", 10 << 20, true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			cmd, _ := protobufProcess(t, bin, msgpackArgs("--project", "length"))
			cmd.Stdin = r
			if tt.nonblock {
				fd, err := unix.Dup(int(r.Fd()))
				if err != nil {
					t.Fatal(err)
				}
				if err = unix.SetNonblock(fd, true); err != nil {
					_ = unix.Close(fd)
					t.Fatal(err)
				}
				in := os.NewFile(uintptr(fd), "nonblocking-stdin")
				defer func() { _ = in.Close() }()
				cmd.Stdin = in
			}
			var out, logs bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &logs
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(msgpackString(strings.Repeat("x", tt.size-5))); err != nil {
				t.Fatal(err)
			}
			want := 130
			if tt.eof {
				_ = w.Close()
				want = 0
			} else if tt.oversize {
				want = 1
			} else {
				<-time.After(200 * time.Millisecond)
			}
			start := time.Now()
			if want == 130 {
				if err = cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			}
			elapsed := protobufWait(t, cmd, want, start)
			if tt.eof {
				if out.String() != "10485755\n" {
					t.Fatal(out.String())
				}
			} else if out.Len() != 0 {
				t.Fatal("partial acquisition emitted output")
			}
			if want == 130 && !tt.nonblock && elapsed < 900*time.Millisecond {
				t.Fatal("watchdog not exercised")
			}
			if tt.nonblock && (elapsed >= 900*time.Millisecond || !strings.Contains(logs.String(), "cancel:")) {
				t.Fatal("deadline did not cooperate", elapsed, logs.String())
			}
			if tt.oversize && !strings.Contains(logs.String(), "exceeds") {
				t.Fatal(logs.String())
			}
			t.Logf("exit=%d elapsed=%s stderr=%s", want, elapsed, logs.String())
		})
	}
}

func TestMsgpackCLIFileOutputAndInterrupt(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, scenario := range []string{"blocked-output", "broken-pipe", "file-query-interrupt", "file-not-stdin"} {
		t.Run(scenario, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			path := filepath.Join(t.TempDir(), "body.txt")
			if err = os.WriteFile(path, msgpackString(strings.Repeat("x", 512<<10)), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--input-file", path, "--input-codec", "msgpack"}
			if scenario == "file-query-interrupt" {
				args = append(args, "--project", "until(false; .)")
			}
			cmd, _ := protobufProcess(t, bin, args)
			var logs, out bytes.Buffer
			cmd.Stdout = w
			cmd.Stderr = &logs
			if scenario == "broken-pipe" {
				_ = r.Close()
			}
			if scenario == "file-not-stdin" {
				cmd.Stdin = r
				cmd.Stdout = &out
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if scenario != "file-not-stdin" {
				_ = w.Close()
			}
			if scenario == "blocked-output" {
				if err = r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				var b [1]byte
				if _, err = r.Read(b[:]); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "file-query-interrupt" {
				<-time.After(200 * time.Millisecond)
			}
			want := 130
			start := time.Now()
			switch scenario {
			case "broken-pipe":
				want = 1
			case "file-not-stdin":
				want = 0
			default:
				if err = cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			}
			elapsed := protobufWait(t, cmd, want, start)
			if scenario == "blocked-output" && elapsed < 900*time.Millisecond {
				t.Fatal("watchdog not exercised")
			}
			if scenario == "broken-pipe" && !strings.Contains(logs.String(), "write:") {
				t.Fatal(logs.String())
			}
			if scenario == "file-query-interrupt" && (elapsed >= 900*time.Millisecond || !strings.Contains(logs.String(), "cancel:")) {
				t.Fatal("query did not cooperate", elapsed, logs.String())
			}
			if scenario == "file-not-stdin" && out.Len() != (512<<10)+3 {
				t.Fatal(out.Len())
			}
			t.Logf("exit=%d elapsed=%s stderr=%s", want, elapsed, logs.String())
		})
	}
}

func TestMsgpackCLIBlockedDiagnostics(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, args := range [][]string{
		{"--input-stdin", "--input-codec", "msgpack", "--project", "invalid["},
		{"--input-stdin", "--format", "json"},
		{"--input-stdin", "--log-format", "invalid"},
		{"--input-stdin", "--input-codec"},
		{"--input-stdin", "--no-such-option"},
		{"--input-file", "unused.txt", "--no-such-option"},
		{"--input-file", "unused.txt", "--input-codec"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			fd := int(w.Fd())
			if err = unix.SetNonblock(fd, true); err != nil {
				t.Fatal(err)
			}
			for _, size := range []int{4096, 1} {
				for {
					_, err = unix.Write(fd, make([]byte, size))
					if err == unix.EAGAIN {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = unix.SetNonblock(fd, false); err != nil {
				t.Fatal(err)
			}
			cmd, _ := protobufProcess(t, bin, args)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = w
			start := time.Now()
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			elapsed := protobufWait(t, cmd, 1, start)
			if elapsed < 900*time.Millisecond || out.Len() != 0 {
				t.Fatal("diagnostic fallback not exercised", elapsed, out.Len())
			}
			t.Logf("exit=1 stdout=0 elapsed=%s", elapsed)
		})
	}
}

func TestMsgpackBorrowedInputOwnership(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(stringBool(deadline), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			if deadline {
				if err = r.SetReadDeadline(time.Now().Add(400 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan int, 1)
			go func() {
				done <- Execute(RunOptions{Args: msgpackArgs(), Ctx: ctx, Stdin: r, Stdout: io.Discard, Stderr: io.Discard})
			}()
			<-time.After(100 * time.Millisecond)
			cancel()
			select {
			case code := <-done:
				t.Fatal("borrowed read automatically unblocked", code)
			case <-time.After(30 * time.Millisecond):
			}
			if !deadline {
				_ = w.Close()
			}
			select {
			case code := <-done:
				if code != 130 {
					t.Fatal(code)
				}
			case <-time.After(time.Second):
				_ = w.Close()
				<-done
				t.Fatal("caller unblock/deadline lost")
			}
			if _, err = r.Stat(); err != nil {
				t.Fatal("borrowed file closed", err)
			}
			if deadline {
				var b [1]byte
				if _, err = r.Read(b[:]); !os.IsTimeout(err) {
					t.Fatal("deadline was cleared", err)
				}
			}
		})
	}
}

type msgpackBlockedSink struct {
	entered, release chan struct{}
	closed           bool
}

func (w *msgpackBlockedSink) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(p), nil
}
func (w *msgpackBlockedSink) Close() error { w.closed = true; return nil }
func TestMsgpackBorrowedOutputOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &msgpackBlockedSink{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- Execute(RunOptions{Args: msgpackArgs(), Ctx: ctx, Stdin: bytes.NewReader(msgpackString("body")), Stdout: sink})
	}()
	<-sink.entered
	cancel()
	select {
	case code := <-done:
		t.Fatal("abandoned borrowed write", code)
	case <-time.After(30 * time.Millisecond):
	}
	close(sink.release)
	select {
	case code := <-done:
		if code != 130 || sink.closed {
			t.Fatal(code, sink.closed)
		}
	case <-time.After(time.Second):
		t.Fatal("write did not finish")
	}
}

func TestMsgpackCLITerminalLayout(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, tt := range []struct {
		name, color, term string
		styled            bool
	}{{"color", "", "xterm", true}, {"no-color", "1", "xterm", false}, {"dumb", "", "dumb", false}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tt.color)
			t.Setenv("TERM", tt.term)
			master, slave := testpty.Open(t)
			cmd, _ := protobufProcess(t, bin, msgpackArgs("--project", "{body: .}"))
			cmd.Stdin = bytes.NewReader(msgpackString("\x1b[31m\x00\r\n"))
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
			if !strings.HasPrefix(out, "{\n  ") || strings.Contains(out, "\x1b[") != tt.styled || !strings.Contains(out, `\u001b[31m\u0000\r\n`) || !strings.HasSuffix(out, "\n") {
				t.Fatalf("terminal %q", out)
			}
		})
	}
}
