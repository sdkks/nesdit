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

const plainYAML = "s: yes\nn: 2\nb: true\nz: null\n"
const coloredYAML = "\x1b[1;39ms\x1b[0m: \x1b[32myes\x1b[0m\n\x1b[1;39mn\x1b[0m: \x1b[36m2\x1b[0m\n\x1b[1;39mb\x1b[0m: \x1b[35mtrue\x1b[0m\n\x1b[1;39mz\x1b[0m: \x1b[39mnull\x1b[0m\n"

func TestTerminalYAMLOutputPaths(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	path := filepath.Join(t.TempDir(), "input.yaml")
	if err := os.WriteFile(path, []byte(plainYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	terminalRun(t, []string{path}, "", coloredYAML, 0)
	terminalRun(t, []string{"--format", "yaml"}, plainYAML, "---\n"+coloredYAML, 0)
	terminalRun(t, []string{"--format", "yaml", "--pretty"}, plainYAML, "---\n"+coloredYAML, 0)
	terminalRun(t, []string{"--format", "jsonl", "--output-format", "yaml"}, "{\"s\":\"yes\",\"n\":2,\"b\":true,\"z\":null}\n", "---\n"+coloredYAML, 0)
	terminalRun(t, []string{"--format", "toml", "--output-format", "yaml"}, "s = \"yes\"\nn = 2\nb = true\n", "---\n"+strings.TrimSuffix(coloredYAML, "\x1b[1;39mz\x1b[0m: \x1b[39mnull\x1b[0m\n"), 0)
	terminalRun(t, []string{"--format", "yaml"}, "yes\n---\nno\n---\nfalse\n---\n42\n---\nnull\n", "---\n\x1b[32myes\x1b[0m\n---\n\x1b[32mno\x1b[0m\n---\n\x1b[35mfalse\x1b[0m\n---\n\x1b[36m42\x1b[0m\n---\n\x1b[39mnull\x1b[0m\n", 0)
	terminalRun(t, []string{"--format", "yaml"}, "? |2+\n\n: |2+\n\n", "---\n? \x1b[1;39m|2+\n\x1b[0m: \x1b[32m|2+\n\x1b[0m", 0)
	terminalRun(t, []string{"--format", "yaml", "--output-format", "json", "--where", ".n != 2", "--query", ".n=7"}, "n: 1\n---\nn: 2\n---\nn: 3\n", "{\x1b[1;39m\"n\"\x1b[0m:\x1b[36m7\x1b[0m}\n---\n\x1b[1;39mn\x1b[0m: \x1b[36m2\x1b[0m\n{\x1b[1;39m\"n\"\x1b[0m:\x1b[36m7\x1b[0m}\n", 0)
	terminalRun(t, []string{"--format", "jsonl", "--output-format", "yaml", "--keep-going", "--query", "if . == 2 then nan else . end"}, "1\n2\n3\n", "---\n\x1b[36m1\x1b[0m\n---\n---\n\x1b[36m3\x1b[0m\n", 1)
}

func TestYAMLInvocationSuppression(t *testing.T) {
	t.Setenv("TERM", "xterm")
	t.Setenv("NO_COLOR", "1")
	terminalRun(t, []string{"--format", "yaml"}, plainYAML, "---\n"+plainYAML, 0)
	t.Setenv("NO_COLOR", "")
	terminalRun(t, []string{"--format", "yaml"}, plainYAML, "---\n"+coloredYAML, 0)
	t.Setenv("TERM", "dumb")
	terminalRun(t, []string{"--format", "yaml"}, plainYAML, "---\n"+plainYAML, 0)
	t.Setenv("TERM", "xterm")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	terminalRun(t, []string{"--format", "yaml"}, plainYAML, "---\n"+coloredYAML, 0)
}

func TestYAMLDeliveryBeforeEOF(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	master, slave := testpty.Open(t)
	r, w := io.Pipe()
	defer func() { _ = r.Close(); _ = w.Close() }()
	done := make(chan int, 1)
	go func() {
		done <- Execute(RunOptions{Args: []string{"--format", "yaml"}, Stdin: r, Stdout: slave, Stderr: io.Discard})
	}()
	for range 2 {
		// A separator completes the current document without closing input.
		if _, err := io.WriteString(w, plainYAML+"---\n"); err != nil {
			t.Fatal(err)
		}
		want := "---\n" + coloredYAML
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

func TestYAMLCLIProcessStdoutTerminal(t *testing.T) {
	if os.Getenv("NESDIT_TEST_YAML_COLOR_CHILD") == "1" {
		os.Exit(Run([]string{"--format", "yaml"}))
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	t.Setenv("NESDIT_TEST_YAML_COLOR_CHILD", "1")
	master, slave := testpty.Open(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestYAMLCLIProcessStdoutTerminal$")
	cmd.Stdin = strings.NewReader(plainYAML)
	cmd.Stdout = slave
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err := cmd.Run(); err != nil {
		t.Fatalf("process: %v: %s", err, diagnostics.String())
	}
	if got := readTerminal(t, master, len(coloredYAML)+4); got != "---\n"+coloredYAML {
		t.Fatalf("process stdout=%q", got)
	}
	assertNoTerminalOutput(t, master)
}

func TestYAMLTerminalInputPipeOutputStaysPlain(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	master, slave := testpty.Open(t)
	if _, err := master.WriteString("n: 1\n---\na: [broken\n---\n"); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	var diagnostics bytes.Buffer
	if code := Execute(RunOptions{Args: []string{"--format", "yaml"}, Stdin: slave, Stdout: w, Stderr: &diagnostics}); code != 1 {
		t.Fatalf("exit=%d", code)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil || string(out) != "---\nn: 1\n" {
		t.Fatalf("pipe bytes=%q err=%v", out, err)
	}
}
