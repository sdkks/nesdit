//go:build darwin || linux

package run

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdkks/nesdit/internal/testpty"
	"golang.org/x/sys/unix"
)

func TestTextCLIPipeEOFAndInterrupt(t *testing.T) {
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
			cmd, _ := protobufProcess(t, bin, textArgs("--project", "length"))
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
			if _, err = w.Write(bytes.Repeat([]byte("x"), tt.size)); err != nil {
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
				if out.String() != "10485760\n" {
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

func TestTextCLIFileOutputAndInterrupt(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, scenario := range []string{"blocked-output", "broken-pipe", "file-query-interrupt", "file-not-stdin"} {
		t.Run(scenario, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			path := filepath.Join(t.TempDir(), "body.txt")
			if err = os.WriteFile(path, bytes.Repeat([]byte("x"), 512<<10), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--input-file", path, "--input-codec", "text"}
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

func TestTextCLIBlockedDiagnostics(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, args := range [][]string{
		{"--input-stdin", "--input-codec", "text", "--project", "invalid["},
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

func TestTextBorrowedInputOwnership(t *testing.T) {
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
				done <- Execute(RunOptions{Args: textArgs(), Ctx: ctx, Stdin: r, Stdout: io.Discard, Stderr: io.Discard})
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
func stringBool(b bool) string {
	if b {
		return "existing-deadline"
	}
	return "caller-unblock"
}

type textBlockedSink struct {
	entered, release chan struct{}
	closed           bool
}

func (w *textBlockedSink) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(p), nil
}
func (w *textBlockedSink) Close() error { w.closed = true; return nil }
func TestTextBorrowedOutputOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &textBlockedSink{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- Execute(RunOptions{Args: textArgs(), Ctx: ctx, Stdin: strings.NewReader("body"), Stdout: sink})
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

func TestTextCLITerminalLayout(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, tt := range []struct {
		name, color, term string
		styled            bool
	}{{"color", "", "xterm", true}, {"no-color", "1", "xterm", false}, {"dumb", "", "dumb", false}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tt.color)
			t.Setenv("TERM", tt.term)
			master, slave := testpty.Open(t)
			cmd, _ := protobufProcess(t, bin, textArgs("--project", "{body: .}"))
			cmd.Stdin = strings.NewReader("\x1b[31m\x00\r\n")
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
