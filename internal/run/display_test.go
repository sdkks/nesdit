//go:build darwin || linux

package run

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdkks/nesdit/internal/testpty"
	"golang.org/x/sys/unix"
)

const coloredObject = "{\x1b[1;39m\"s\"\x1b[0m:\x1b[32m\"é🙂\"\x1b[0m,\x1b[1;39m\"n\"\x1b[0m:\x1b[36m2\x1b[0m,\x1b[1;39m\"b\"\x1b[0m:\x1b[35mtrue\x1b[0m,\x1b[1;39m\"z\"\x1b[0m:\x1b[39mnull\x1b[0m}"
const plainObject = `{"s":"é🙂","n":2,"b":true,"z":null}`

func readTerminal(t *testing.T, master *os.File, n int) string {
	t.Helper()
	if err := master.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, n)
	if _, err := io.ReadFull(master, data); err != nil {
		t.Fatalf("terminal read: %v; partial=%q", err, data)
	}
	return string(data)
}

func terminalRun(t *testing.T, args []string, input, want string, wantCode int) {
	t.Helper()
	master, slave := testpty.Open(t)
	if err := master.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	type readResult struct {
		data []byte
		err  error
	}
	readDone := make(chan readResult, 1)
	go func() {
		data := make([]byte, len(want))
		_, err := io.ReadFull(master, data)
		readDone <- readResult{data, err}
	}()
	var diagnostics bytes.Buffer
	code := Execute(RunOptions{Args: args, Stdin: strings.NewReader(input), Stdout: slave, Stderr: &diagnostics})
	if code != wantCode {
		t.Fatalf("exit=%d want=%d: %s", code, wantCode, diagnostics.String())
	}
	result := <-readDone
	if result.err != nil {
		t.Fatalf("terminal read: %v; bytes=%q", result.err, result.data)
	}
	if string(result.data) != want {
		t.Fatalf("got %q want %q", result.data, want)
	}
	assertNoTerminalOutput(t, master)
	if strings.Contains(diagnostics.String(), "\x1b[") {
		t.Fatal("colored diagnostic")
	}
}

func assertNoTerminalOutput(t *testing.T, master *os.File) {
	t.Helper()
	conn, err := master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var ready int
	var pollErr error
	if err := conn.Control(func(fd uintptr) {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		ready, pollErr = unix.Poll(fds, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if pollErr != nil {
		t.Fatal(pollErr)
	}
	if ready != 0 {
		t.Fatal("unexpected additional terminal output")
	}
}

func TestTerminalJSONOutputPaths(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(plainObject), 0o600); err != nil {
		t.Fatal(err)
	}
	terminalRun(t, []string{path}, "", coloredObject, 0)
	terminalRun(t, []string{"--format", "json"}, plainObject, coloredObject+"\n", 0)
	terminalRun(t, []string{"--format", "jsonl"}, plainObject+"\r\n"+plainObject+"\n", coloredObject+"\n"+coloredObject+"\n", 0)
	terminalRun(t, []string{"--format", "jsonl"}, "null\nfalse\n[1,\"x\"]\n", "\x1b[39mnull\x1b[0m\n\x1b[35mfalse\x1b[0m\n[\x1b[36m1\x1b[0m,\x1b[32m\"x\"\x1b[0m]\n", 0)
	// Output format, not input format or global override, owns the style.
	terminalRun(t, []string{"--format", "yaml", "--output-format", "json"}, "n: 2\n", "{\x1b[1;39m\"n\"\x1b[0m:\x1b[36m2\x1b[0m}\n", 0)
	terminalRun(t, []string{"--format", "jsonl", "--output-format", "yaml", "--where", ".n == 1", "--query", ".n=7"}, "{\"n\":1}\n{\"n\":2}\n", "---\n\x1b[1;39mn\x1b[0m: \x1b[36m7\x1b[0m\n{\x1b[1;39m\"n\"\x1b[0m:\x1b[36m2\x1b[0m}\n", 0)
	terminalRun(t, []string{"--format", "yaml", "--output-format", "json", "--where", ".n == 1", "--query", ".n=7"}, "n: 1\n---\nn: 2\n", "{\x1b[1;39m\"n\"\x1b[0m:\x1b[36m7\x1b[0m}\n---\n\x1b[1;39mn\x1b[0m: \x1b[36m2\x1b[0m\n", 0)
	terminalRun(t, []string{"--format", "json", "--output-format", "yaml", "--where", ".n == 1", "--query", ".n=7"}, "{\"n\":2}", "{\x1b[1;39m\"n\"\x1b[0m:\x1b[36m2\x1b[0m}\n", 0)
	// A later encode error emits no partial colored document under keep-going.
	terminalRun(t, []string{"--format", "jsonl", "--query", "if . == 2 then nan else . end", "--keep-going"}, "1\n2\n3\n", "\x1b[36m1\x1b[0m\n\x1b[36m3\x1b[0m\n", 1)
}

func TestInvocationColorSuppression(t *testing.T) {
	t.Setenv("TERM", "xterm")
	t.Setenv("NO_COLOR", "1")
	terminalRun(t, []string{"--format", "json"}, plainObject, plainObject+"\n", 0)
	t.Setenv("NO_COLOR", "")
	terminalRun(t, []string{"--format", "json"}, plainObject, coloredObject+"\n", 0)
	t.Setenv("TERM", "dumb")
	terminalRun(t, []string{"--format", "json"}, plainObject, plainObject+"\n", 0)
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERM", "xterm")
	terminalRun(t, []string{"--format", "json"}, plainObject, coloredObject+"\n", 0)
}

func TestTerminalInputDoesNotColorPipeOutput(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	master, slave := testpty.Open(t)
	// The PTY slave is stdin here. Supply a complete JSONL record, then
	// a deliberate decode error to finish without terminal EOF mechanics.
	if _, err := master.WriteString(plainObject + "\ninvalid\n"); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	var diagnostics bytes.Buffer
	if code := Execute(RunOptions{Args: []string{"--format", "jsonl"}, Stdin: slave, Stdout: w, Stderr: &diagnostics}); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != plainObject+"\n" {
		t.Fatalf("pipe bytes: %q", out)
	}
}

func TestJSONLDeliveryBeforeEOF(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	master, slave := testpty.Open(t)
	r, w := io.Pipe()
	defer func() { _ = r.Close(); _ = w.Close() }()
	done := make(chan int, 1)
	go func() {
		done <- Execute(RunOptions{Args: []string{"--format", "jsonl"}, Stdin: r, Stdout: slave, Stderr: io.Discard})
	}()
	if _, err := io.WriteString(w, plainObject+"\n"); err != nil {
		t.Fatal(err)
	}
	if got := readTerminal(t, master, len(coloredObject)+1); got != coloredObject+"\n" {
		t.Fatalf("streamed %q", got)
	}
	select {
	case <-done:
		t.Fatal("finished before EOF")
	default:
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not finish after EOF")
	}
}

func TestColorCLIProcess(t *testing.T) {
	if os.Getenv("NESDIT_TEST_COLOR_CHILD") == "1" {
		os.Exit(Run([]string{"--format", "jsonl"}))
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	t.Setenv("NESDIT_TEST_COLOR_CHILD", "1")
	master, slave := testpty.Open(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestColorCLIProcess$")
	cmd.Stdin = strings.NewReader(plainObject + "\n")
	cmd.Stdout = slave
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		t.Fatalf("process: %v: %s", err, diagnostics.String())
	}
	if got := readTerminal(t, master, len(coloredObject)+1); got != coloredObject+"\n" {
		t.Fatalf("process stdout=%q", got)
	}
}
