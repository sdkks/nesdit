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
)

const plainTOML = "s = \"é🙂\"\nn = 2\nb = true\n"
const coloredTOML = "\x1b[1;39ms\x1b[0m = \x1b[32m\"é🙂\"\x1b[0m\n\x1b[1;39mn\x1b[0m = \x1b[36m2\x1b[0m\n\x1b[1;39mb\x1b[0m = \x1b[35mtrue\x1b[0m\n"
const prettyTOML = "s = \"é🙂\"\n\nn = 2\n\nb = true\n"
const prettyColoredTOML = "\x1b[1;39ms\x1b[0m = \x1b[32m\"é🙂\"\x1b[0m\n\n\x1b[1;39mn\x1b[0m = \x1b[36m2\x1b[0m\n\n\x1b[1;39mb\x1b[0m = \x1b[35mtrue\x1b[0m\n"

func TestTerminalTOMLOutputPaths(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	path := filepath.Join(t.TempDir(), "input.toml")
	if err := os.WriteFile(path, []byte(plainTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	terminalRun(t, []string{path}, "", coloredTOML, 0)
	terminalRun(t, []string{"--pretty", path}, "", prettyColoredTOML, 0)
	terminalRun(t, []string{"--format", "toml"}, plainTOML, coloredTOML, 0)
	terminalRun(t, []string{"--format", "toml", "--pretty"}, plainTOML, prettyColoredTOML, 0)
	for _, format := range []string{"json", "jsonl", "yaml"} {
		input := "{\"s\":\"é🙂\",\"n\":2,\"b\":true}\n"
		if format == "yaml" {
			input = "s: é🙂\nn: 2\nb: true\n"
		}
		terminalRun(t, []string{"--format", format, "--output-format", "toml"}, input, coloredTOML, 0)
		terminalRun(t, []string{"--format", format, "--output-format", "toml", "--pretty"}, input, prettyColoredTOML, 0)
	}
	// The independently selected pass-through writer retains the native input
	// format (and existing compact mode), not the pretty output override.
	terminalRun(t, []string{"--format", "toml", "--output-format", "json", "--pretty", "--where", ".n == 1", "--query", ".n=7"}, plainTOML, coloredTOML, 0)
	terminalRun(t, []string{"--output-format", "json", path}, "", "{\x1b[1;39m\"s\"\x1b[0m:\x1b[32m\"é🙂\"\x1b[0m,\x1b[1;39m\"n\"\x1b[0m:\x1b[36m2\x1b[0m,\x1b[1;39m\"b\"\x1b[0m:\x1b[35mtrue\x1b[0m}", 0)
	// TOML dates/times stay native and neutral; quoted dates are strings.
	terminalRun(t, []string{"--format", "toml"}, "date = 1979-05-27\ntime = 07:32:00\ndt = 1979-05-27T07:32:00Z\ns = \"1979-05-27\"\n", "\x1b[1;39mdate\x1b[0m = 1979-05-27\n\x1b[1;39mtime\x1b[0m = 07:32:00\n\x1b[1;39mdt\x1b[0m = 1979-05-27T07:32:00Z\n\x1b[1;39ms\x1b[0m = \x1b[32m\"1979-05-27\"\x1b[0m\n", 0)
	terminalRun(t, []string{"--format", "toml", "--pretty"}, "123 = [{true = false}, {\"é🙂\" = \"x\"}]\n", "\x1b[1;39m123\x1b[0m = [\n  {\x1b[1;39mtrue\x1b[0m = \x1b[35mfalse\x1b[0m},\n  {\x1b[1;39m\"é🙂\"\x1b[0m = \x1b[32m\"x\"\x1b[0m},\n]\n", 0)
}

func TestTerminalTOMLMixedFramingAndFailures(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	terminalRun(t, []string{"--format", "jsonl", "--output-format", "toml"}, "{\"n\":1}\n{\"n\":2}\n", "\x1b[1;39mn\x1b[0m = \x1b[36m1\x1b[0m\n+++\n\x1b[1;39mn\x1b[0m = \x1b[36m2\x1b[0m\n", 0)
	terminalRun(t, []string{"--format", "jsonl", "--output-format", "toml", "--where", ".n != 2", "--query", ".n=7"}, "{\"n\":1}\n{\"n\":2}\n{\"n\":3}\n", "\x1b[1;39mn\x1b[0m = \x1b[36m7\x1b[0m\n{\x1b[1;39m\"n\"\x1b[0m:\x1b[36m2\x1b[0m}\n+++\n\x1b[1;39mn\x1b[0m = \x1b[36m7\x1b[0m\n", 0)
	terminalRun(t, []string{"--format", "yaml", "--output-format", "toml", "--where", ".n != 2", "--query", ".n=7"}, "n: 1\n---\nn: 2\n---\nn: 3\n", "\x1b[1;39mn\x1b[0m = \x1b[36m7\x1b[0m\n---\nn: 2\n+++\n\x1b[1;39mn\x1b[0m = \x1b[36m7\x1b[0m\n", 0)
	// Existing state/separator ordering is preserved even on encode failures.
	terminalRun(t, []string{"--format", "jsonl", "--output-format", "toml", "--keep-going"}, "{\"n\":1}\n{\"n\":null}\n{\"n\":3}\n", "\x1b[1;39mn\x1b[0m = \x1b[36m1\x1b[0m\n+++\n+++\n\x1b[1;39mn\x1b[0m = \x1b[36m3\x1b[0m\n", 1)
	terminalRun(t, []string{"--format", "jsonl", "--output-format", "toml", "--keep-going"}, "{\"n\":null}\n{\"n\":3}\n", "+++\n\x1b[1;39mn\x1b[0m = \x1b[36m3\x1b[0m\n", 1)
	terminalRun(t, []string{"--format", "json", "--output-format", "toml"}, "{\"n\":null}", "", 1)
	terminalRun(t, []string{"--format", "toml", "--query", ".n=null"}, plainTOML, "", 1)
}

func TestTOMLInvocationSuppression(t *testing.T) {
	t.Setenv("TERM", "xterm")
	t.Setenv("NO_COLOR", "1")
	terminalRun(t, []string{"--format", "toml"}, plainTOML, plainTOML, 0)
	t.Setenv("NO_COLOR", "")
	terminalRun(t, []string{"--format", "toml"}, plainTOML, coloredTOML, 0)
	t.Setenv("TERM", "dumb")
	terminalRun(t, []string{"--format", "toml", "--pretty"}, plainTOML, prettyTOML, 0)
	t.Setenv("TERM", "xterm")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	terminalRun(t, []string{"--format", "toml", "--pretty"}, plainTOML, prettyColoredTOML, 0)
}

func TestTOMLDeliveryBeforeEOF(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	master, slave := testpty.Open(t)
	r, w := io.Pipe()
	defer func() { _ = r.Close(); _ = w.Close() }()
	done := make(chan int, 1)
	go func() {
		done <- Execute(RunOptions{Args: []string{"--format", "jsonl", "--output-format", "toml", "--pretty"}, Stdin: r, Stdout: slave, Stderr: io.Discard})
	}()
	for i := 0; i < 2; i++ {
		if _, err := io.WriteString(w, "{\"s\":\"é🙂\",\"n\":2,\"b\":true}\n"); err != nil {
			t.Fatal(err)
		}
		want := prettyColoredTOML
		if i == 1 {
			want = "+++\n" + want
		}
		if got := readTerminal(t, master, len(want)); got != want {
			t.Fatalf("streamed %q want %q", got, want)
		}
		select {
		case <-done:
			t.Fatal("finished before EOF")
		default:
		}
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

func TestTOMLCLIProcessStdoutTerminal(t *testing.T) {
	if os.Getenv("NESDIT_TEST_TOML_COLOR_CHILD") == "1" {
		os.Exit(Run([]string{"--format", "toml"}))
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	t.Setenv("NESDIT_TEST_TOML_COLOR_CHILD", "1")
	master, slave := testpty.Open(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestTOMLCLIProcessStdoutTerminal$")
	cmd.Stdin = strings.NewReader(plainTOML)
	cmd.Stdout = slave
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		t.Fatalf("process: %v: %s", err, diagnostics.String())
	}
	if got := readTerminal(t, master, len(coloredTOML)); got != coloredTOML {
		t.Fatalf("process stdout=%q", got)
	}
	assertNoTerminalOutput(t, master)
}

func TestTOMLTerminalInputPipeOutputStaysPlain(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	master, slave := testpty.Open(t)
	if _, err := master.WriteString("{\"n\":1}\ninvalid\n"); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	var diagnostics bytes.Buffer
	if code := Execute(RunOptions{Args: []string{"--format", "jsonl", "--output-format", "toml"}, Stdin: slave, Stdout: w, Stderr: &diagnostics}); code != 1 {
		t.Fatalf("exit=%d", code)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil || string(out) != "n = 1\n" {
		t.Fatalf("pipe bytes=%q err=%v", out, err)
	}
}
