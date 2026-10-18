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

func TestMsgpackEncodeCLIPhysicalTerminal(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, setting := range []struct{ name, color, term string }{{"normal", "", "xterm"}, {"no-color", "1", "xterm"}, {"dumb", "", "dumb"}} {
		t.Run(setting.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", setting.color)
			t.Setenv("TERM", setting.term)
			for _, codec := range []string{"json", "msgpack-json", "msgpack"} {
				t.Run(codec, func(t *testing.T) {
					master, slave := testpty.Open(t)
					terminal, err := binaryTerminal(slave)
					if err != nil || !terminal {
						t.Fatal("real PTY not detected", terminal, err)
					}
					r, w, err := os.Pipe()
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = r.Close(); _ = w.Close() }()
					payload := []byte("null")
					if codec == "msgpack" {
						payload = []byte{0xc0}
					}
					if _, err = w.Write(payload); err != nil {
						t.Fatal(err)
					}
					cmd, _ := protobufProcess(t, bin, encodeArgs(codec, "--filter", "false"))
					cmd.Stdin = r
					cmd.Stdout = slave
					var logs bytes.Buffer
					cmd.Stderr = &logs
					if err = cmd.Start(); err != nil {
						t.Fatal(err)
					}
					protobufWait(t, cmd, 1, time.Now())
					if !strings.Contains(logs.String(), "physical terminal") {
						t.Fatal(logs.String())
					}
					// Producer remains open. Fast refusal plus the exact unread queued bytes
					// proves payload was not consumed, not merely that the filter was false.
					if err = r.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
						t.Fatal(err)
					}
					unread := make([]byte, len(payload))
					if _, err = io.ReadFull(r, unread); err != nil || !bytes.Equal(unread, payload) {
						t.Fatal("input consumed", unread, err)
					}
					if err = master.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
						t.Fatal(err)
					}
					var b [32]byte
					n, err := master.Read(b[:])
					if n != 0 || !os.IsTimeout(err) {
						t.Fatal("terminal output was not empty", n, err)
					}
					t.Logf("physical PTY; queued input %d -> %d; stdout=0; exit=1", len(payload), len(unread))
				})
			}
		})
	}
}

func TestMsgpackEncodeCLISelectionFailures(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, tt := range []struct {
		codec, body string
		args        []string
		code        int
	}{
		{"msgpack-json", `{"$int":"7"}`, []string{"--project", "7"}, 1},
		{"msgpack-json", `{"$float":"1e309"}`, []string{"--filter", "false"}, 1},
		{"msgpack-json", `{"$bin":"/x=="}`, []string{"--filter", "false"}, 1},
		{"json", `{"secret-name":1,"secret-name":2}`, []string{"--filter", "false"}, 1},
		{"json", `"\ud800"`, []string{"--filter", "false"}, 1},
		{"json", `1e309`, nil, 1},
		{"json", `1e309`, []string{"--filter", "false", "--project", `error("secret-value")`}, 0},
		{"json", `18446744073709551616`, []string{"--filter", "false"}, 0},
		{"json", `null`, []string{"--project", `"x" * 16777212`}, 1},
	} {
		t.Run(tt.codec+tt.body+strings.Join(tt.args, " "), func(t *testing.T) {
			cmd, _ := protobufProcess(t, bin, encodeArgs(tt.codec, tt.args...))
			cmd.Stdin = strings.NewReader(tt.body)
			var out, logs bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &logs
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			protobufWait(t, cmd, tt.code, time.Now())
			if out.Len() != 0 || strings.Contains(logs.String(), "secret-") {
				t.Fatal("pre-write emission or unsafe diagnostic", out.Len(), logs.String())
			}
			if tt.code == 1 && logs.Len() == 0 {
				t.Fatal("missing failure diagnostic")
			}
		})
	}
}

func TestMsgpackEncodeCLIAcquisition(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, tt := range []struct {
		name                string
		size                int
		eof, over, nonblock bool
	}{
		{"under-held", 512 << 10, false, false, false},
		{"exact-held", 10 << 20, false, false, false},
		{"cooperative", 512 << 10, false, false, true},
		{"over-held", (10 << 20) + 1, false, true, false},
		{"exact-eof", 10 << 20, true, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			cmd, _ := protobufProcess(t, bin, encodeArgs("json"))
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
			if _, err = w.WriteString(`"` + strings.Repeat("x", tt.size-2) + `"`); err != nil {
				t.Fatal(err)
			}
			want := 130
			if tt.eof {
				_ = w.Close()
				want = 0
			} else if tt.over {
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
				if out.Len() != tt.size+3 || out.Bytes()[0] != 0xdb {
					t.Fatal("exact cap binary output", out.Len())
				}
			} else if out.Len() != 0 {
				t.Fatal("partial acquisition output")
			}
			if want == 130 && !tt.nonblock && elapsed < 900*time.Millisecond {
				t.Fatal("watchdog not exercised", elapsed)
			}
			if tt.nonblock && (elapsed >= 900*time.Millisecond || !strings.Contains(logs.String(), "cancel:")) {
				t.Fatal("cooperative stop", elapsed, logs.String())
			}
			if tt.over && !strings.Contains(logs.String(), "exceeds") {
				t.Fatal(logs.String())
			}
			t.Logf("exit=%d elapsed=%s stdout=%d stderr=%s", want, elapsed, out.Len(), logs.String())
		})
	}
}

func TestMsgpackEncodeCLIOutputAndInterrupt(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, scenario := range []string{"blocked-output", "broken-pipe", "file-query-interrupt", "file-not-stdin"} {
		t.Run(scenario, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			path := filepath.Join(t.TempDir(), "input.json")
			if err = os.WriteFile(path, []byte(`"`+strings.Repeat("x", 512<<10)+`"`), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--input-file", path, "--input-codec", "json", "--to", "msgpack"}
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
				t.Fatal("watchdog not exercised", elapsed)
			}
			if scenario == "broken-pipe" && !strings.Contains(logs.String(), "write:") {
				t.Fatal(logs.String())
			}
			if scenario == "file-query-interrupt" && (elapsed >= 900*time.Millisecond || !strings.Contains(logs.String(), "cancel:")) {
				t.Fatal("query did not cooperate", elapsed, logs.String())
			}
			if scenario == "file-not-stdin" && out.Len() != (512<<10)+5 {
				t.Fatal(out.Len())
			}
			t.Logf("exit=%d elapsed=%s stderr=%s", want, elapsed, logs.String())
		})
	}
}

func TestMsgpackEncodeCLIBlockedDiagnostics(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, args := range [][]string{
		encodeArgs("json", "--project", "invalid["),
		encodeArgs("msgpack-json", "--no-such-option"),
		encodeArgs("json", "--log-format", "invalid"),
		{"--input-stdin", "--input-codec", "json", "--to"},
		{"--input-file", "unused", "--input-codec", "json", "--to", "msgpack", "--no-such-option"},
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
				t.Fatal("diagnostic watchdog", elapsed, out.Len())
			}
			t.Logf("exit=1 elapsed=%s stdout=0", elapsed)
		})
	}
}

func TestMsgpackEncodeBorrowedLifecycle(t *testing.T) {
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
			go func() { done <- Execute(RunOptions{Args: encodeArgs("json"), Ctx: ctx, Stdin: r, Stdout: io.Discard}) }()
			<-time.After(100 * time.Millisecond)
			cancel()
			select {
			case code := <-done:
				t.Fatal("borrowed read unblocked", code)
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
				t.Fatal("borrowed input closed", err)
			}
			if deadline {
				var b [1]byte
				if _, err = r.Read(b[:]); !os.IsTimeout(err) {
					t.Fatal("deadline cleared", err)
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &msgpackBlockedSink{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- Execute(RunOptions{Args: encodeArgs("json"), Ctx: ctx, Stdin: strings.NewReader("null"), Stdout: sink})
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
