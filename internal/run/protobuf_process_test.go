//go:build darwin || linux

package run

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/sdkks/nesdit/internal/protobufview"
	"github.com/sdkks/nesdit/internal/testpty"
)

func protobufProcess(t *testing.T, bin string, args []string) (*exec.Cmd, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, bin, args...)
	return cmd, ctx
}
func protobufWait(t *testing.T, cmd *exec.Cmd, want int, start time.Time) time.Duration {
	t.Helper()
	err := cmd.Wait()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if code != want || elapsed > 3*time.Second {
		t.Fatalf("exit=%d want=%d elapsed=%s", code, want, elapsed)
	}
	return elapsed
}

func TestProtobufCLIPipeWaitingAndInterrupt(t *testing.T) {
	bin := buildNATSCLI(t)
	descriptor := protobufFixture(t)
	for _, tt := range []struct {
		name     string
		size     int
		nonblock bool
		oversize bool
	}{{"inherited-under-cap", 512 << 10, false, false}, {"inherited-exact-cap", protobufview.RawLimit, false, false}, {"deadline-capable", 512 << 10, true, false}, {"over-cap-open-producer", protobufview.RawLimit + 1, false, true}} {
		t.Run(tt.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			if err := unix.SetNonblock(int(r.Fd()), tt.nonblock); err != nil {
				t.Fatal(err)
			}
			cmd, _ := protobufProcess(t, bin, protobufArgs(descriptor, "example.Event"))
			var out, logs bytes.Buffer
			cmd.Stdin = r
			if tt.nonblock {
				// os/exec calls File.Fd; an os.Pipe-created File resets its
				// descriptor to blocking there. NewFile preserves a descriptor
				// that was already nonblocking, matching the supported CLI seam.
				fd, err := unix.Dup(int(r.Fd()))
				if err != nil {
					t.Fatal(err)
				}
				if err := unix.SetNonblock(fd, true); err != nil {
					_ = unix.Close(fd)
					t.Fatal(err)
				}
				in := os.NewFile(uintptr(fd), "nonblocking-stdin")
				defer func() { _ = in.Close() }()
				cmd.Stdin = in
			}
			cmd.Stdout = &out
			cmd.Stderr = &logs
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// More than pipe capacity forces the child to enter its input path before
			// we interrupt; the producer intentionally remains open after this write.
			data := bytes.Repeat([]byte{8, 1}, tt.size/2)
			if tt.size%2 != 0 {
				data = append(data, 0)
			}
			if _, err := w.Write(data); err != nil {
				t.Fatal(err)
			}
			if !tt.oversize {
				// Write completion proves progress, not that the last buffered
				// chunk was consumed. Let the child enter its next blocking Read
				// so this tests waiting cancellation rather than a chunk checkpoint.
				<-time.After(200 * time.Millisecond)
			}
			start := time.Now()
			want := 1
			if !tt.oversize {
				want = 130
				if err := cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			}
			elapsed := protobufWait(t, cmd, want, start)
			if out.Len() != 0 {
				t.Fatal("partial acquisition decoded", out.String())
			}
			if tt.nonblock {
				if elapsed >= 900*time.Millisecond || !strings.Contains(logs.String(), "cancel:") {
					t.Fatalf("deadline path did not cooperate: %s %s", elapsed, logs.String())
				}
			} else if !tt.oversize && elapsed < 900*time.Millisecond {
				t.Fatalf("did not exercise inherited blocking-pipe watchdog: %s", elapsed)
			}
			if tt.oversize && !strings.Contains(logs.String(), "exceeds") {
				t.Fatal(logs.String())
			}
			t.Logf("%s exit=%d elapsed=%s stderr=%s", tt.name, want, elapsed, logs.String())
		})
	}
}

func TestProtobufCLIEOFAndFileEquivalence(t *testing.T) {
	bin := buildNATSCLI(t)
	descriptor := protobufFixture(t)
	for _, tt := range []struct {
		name string
		data []byte
		want string
	}{
		{"example.Event", []byte{8, 42, 16, 1}, "{\"active\":true,\"id\":\"42\"}\n"},
		{"google.protobuf.StringValue", protobufWire(t, wrapperspb.String("scalar")), "\"scalar\"\n"},
		{"google.protobuf.ListValue", protobufWire(t, &structpb.ListValue{Values: []*structpb.Value{structpb.NewBoolValue(true), structpb.NewNullValue()}}), "[true,null]\n"},
	} {
		args := protobufArgs(descriptor, tt.name)
		cmd, _ := protobufProcess(t, bin, args)
		cmd.Stdin = bytes.NewReader(tt.data)
		got, err := cmd.Output()
		if err != nil || string(got) != tt.want {
			t.Fatalf("stdin %q %v", got, err)
		}
		path := filepath.Join(t.TempDir(), "payload.bin")
		if err := os.WriteFile(path, tt.data, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd, _ = protobufProcess(t, bin, []string{"--protobuf-file", path, "--descriptor-set", descriptor, "--message-type", tt.name})
		// A held-open stdin must not delay file mode.
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stdin = r
		got, err = cmd.Output()
		_ = r.Close()
		_ = w.Close()
		if err != nil || string(got) != tt.want {
			t.Fatalf("file %q %v", got, err)
		}
	}
	for _, filter := range []string{".active == true", "false"} {
		args := append(protobufArgs(descriptor, "example.Event"), "--filter", filter, "--project", "{order_id: .id}")
		cmd, _ := protobufProcess(t, bin, args)
		cmd.Stdin = bytes.NewReader([]byte{8, 42, 16, 1})
		got, err := cmd.Output()
		want := "{\"order_id\":\"42\"}\n"
		if filter == "false" {
			want = ""
		}
		if err != nil || string(got) != want {
			t.Fatalf("CLI query got=%q want=%q err=%v", got, want, err)
		}
	}
	// Exactly the raw cap with EOF, using unknown bytes that do not inflate view.
	body := protowire.AppendBytes(protowire.AppendTag(nil, 100, protowire.BytesType), make([]byte, protobufview.RawLimit-6))
	cmd, _ := protobufProcess(t, bin, protobufArgs(descriptor, "example.Event"))
	cmd.Stdin = bytes.NewReader(body)
	got, err := cmd.Output()
	if err != nil || string(got) != "{}\n" {
		t.Fatalf("exact EOF %q %v", got, err)
	}
}

func TestProtobufCLIOutputAndFileInterrupt(t *testing.T) {
	bin := buildNATSCLI(t)
	descriptor := protobufFixture(t)
	for _, scenario := range []string{"blocked-output", "broken-pipe", "file-query-interrupt"} {
		t.Run(scenario, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			data := protobufWire(t, wrapperspb.String(strings.Repeat("x", 512<<10)))
			path := filepath.Join(t.TempDir(), "message.bin")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--protobuf-file", path, "--descriptor-set", descriptor, "--message-type", "google.protobuf.StringValue"}
			if scenario == "file-query-interrupt" {
				args = append(args, "--project", "until(false; .)")
			}
			cmd, _ := protobufProcess(t, bin, args)
			var logs bytes.Buffer
			cmd.Stdout = w
			cmd.Stderr = &logs
			if scenario == "broken-pipe" {
				_ = r.Close()
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			if scenario == "blocked-output" {
				if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				var first [1]byte
				if _, err := r.Read(first[:]); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "file-query-interrupt" {
				<-time.After(200 * time.Millisecond)
			}
			start := time.Now()
			want := 1
			if scenario != "broken-pipe" {
				want = 130
				if err := cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			}
			elapsed := protobufWait(t, cmd, want, start)
			if scenario == "blocked-output" && elapsed < 900*time.Millisecond {
				t.Fatal("blocked output did not exercise watchdog")
			}
			if scenario == "broken-pipe" && !strings.Contains(logs.String(), "write:") {
				t.Fatal(logs.String())
			}
			if scenario == "file-query-interrupt" && (elapsed >= 900*time.Millisecond || !strings.Contains(logs.String(), "cancel:")) {
				t.Fatalf("query did not cooperate: %s %s", elapsed, logs.String())
			}
			t.Logf("%s exit=%d elapsed=%s stderr=%s", scenario, want, elapsed, logs.String())
		})
	}
}

func TestProtobufDeadlineCleanupAndBorrowedFile(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if err := r.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("test handle must support deadlines: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := protobufStdinDeadline(ctx, r)
	result := make(chan error, 1)
	go func() { _, err := protobufview.ReadBody(ctx, r, 10); result <- err }()
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("deadline did not wake read")
	}
	cleanup()
	// Cleanup joined the callback and cleared its deadline; the same handle works.
	if _, err := w.WriteString("x"); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := r.Read(b[:]); err != nil || b[0] != 'x' {
		t.Fatalf("deadline was not reset: %v", err)
	}
	// Execute must not use that seam even with an actual deadline-capable file.
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	args := protobufArgs(protobufFixture(t), "example.Event")
	done := make(chan int, 1)
	var out bytes.Buffer
	go func() { done <- Execute(RunOptions{Args: args, Ctx: ctx, Stdin: r, Stdout: &out, Stderr: io.Discard}) }()
	<-time.After(100 * time.Millisecond)
	cancel()
	select {
	case code := <-done:
		t.Fatalf("borrowed file was automatically unblocked: %d", code)
	case <-time.After(30 * time.Millisecond):
	}
	_ = w.Close()
	select {
	case code := <-done:
		if code != 130 || out.Len() != 0 {
			t.Fatalf("code=%d out=%q", code, out.String())
		}
	case <-time.After(time.Second):
		t.Fatal("caller-unblocked read did not return")
	}
	if _, err := r.Stat(); err != nil {
		t.Fatal("Execute closed borrowed file", err)
	}
}

func TestProtobufCLIBlockedDiagnostics(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"preparation", []string{"--protobuf-stdin", "--descriptor-set", "missing", "--message-type", "example.Event"}},
		{"semantic-conflict", []string{"--protobuf-stdin", "--format", "json"}},
		{"invalid-log-format", []string{"--protobuf-stdin", "--log-format", "invalid"}},
		{"missing-value", []string{"--protobuf-stdin", "--message-type"}},
		{"unknown-flag", []string{"--protobuf-stdin", "--no-such-flag"}},
		{"file-missing-value", []string{"--protobuf-file", "unused.bin", "--message-type"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			fd := int(w.Fd())
			if err := unix.SetNonblock(fd, true); err != nil {
				t.Fatal(err)
			}
			// Fill even any residual capacity so a short diagnostic cannot fit.
			for _, size := range []int{4096, 1} {
				for {
					_, err := unix.Write(fd, make([]byte, size))
					if err == unix.EAGAIN {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := unix.SetNonblock(fd, false); err != nil {
				t.Fatal(err)
			}
			cmd, _ := protobufProcess(t, bin, tt.args)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = w
			start := time.Now()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			elapsed := protobufWait(t, cmd, 1, start)
			if elapsed < 900*time.Millisecond {
				t.Fatal("diagnostic watchdog not exercised")
			}
			if out.Len() != 0 {
				t.Fatalf("failed invocation emitted stdout: %q", out.String())
			}
			t.Logf("blocked diagnostic exit=1 stdout=0 elapsed=%s", elapsed)
		})
	}
}

func TestProtobufCLITerminalLayout(t *testing.T) {
	bin := buildNATSCLI(t)
	descriptor := protobufFixture(t)
	for _, tt := range []struct {
		name, color, term string
		styled            bool
	}{{"color", "", "xterm", true}, {"NO_COLOR", "1", "xterm", false}, {"TERM dumb", "", "dumb", false}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tt.color)
			t.Setenv("TERM", tt.term)
			master, slave := testpty.Open(t)
			cmd, _ := protobufProcess(t, bin, protobufArgs(descriptor, "example.Event"))
			cmd.Stdin = bytes.NewReader([]byte{8, 42, 16, 1})
			cmd.Stdout = slave
			if err := cmd.Run(); err != nil {
				t.Fatal(err)
			}
			if err := master.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4096)
			n, err := master.Read(buf)
			if err != nil {
				t.Fatal(err)
			}
			out := string(buf[:n])
			if !strings.HasPrefix(out, "{\n  ") || strings.Contains(out, "\x1b[") != tt.styled || !strings.HasSuffix(out, "\n") {
				t.Fatalf("terminal output %q", out)
			}
		})
	}
}
