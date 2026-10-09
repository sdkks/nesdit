//go:build darwin || linux

package run

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTOMLEditorPreviewColorIsolation(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	t.Setenv("NESDIT_SKIP_TTY_CHECK", "1")
	dir := t.TempDir()
	path := filepath.Join(dir, "input.toml")
	snapshot := filepath.Join(dir, "temp-copy.toml")
	editor := filepath.Join(dir, "editor")
	if err := os.WriteFile(path, []byte("n = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(editor, []byte("#!/bin/sh\ncp \"$1\" \"$NESDIT_TEST_SNAPSHOT\"\nprintf '%s\\n' 'n = 2' > \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	t.Setenv("NESDIT_TEST_SNAPSHOT", snapshot)
	var out, diagnostics bytes.Buffer
	args := []string{"--edit", path}
	if code := Execute(RunOptions{Args: args, Stdout: &out, Stderr: &diagnostics}); code != 0 {
		t.Fatalf("exit=%d: %s", code, diagnostics.String())
	}
	prefix, preview, found := strings.Cut(out.String(), "# toml\n")
	if !found || preview != "n = 2\n" || strings.Contains(prefix, "\x1b[") {
		t.Fatalf("plain editor result=%q", out.String())
	}
	terminalRun(t, args, "", prefix+"# toml\n\x1b[1;39mn\x1b[0m = \x1b[36m2\x1b[0m\n", 0)
	for _, checkPath := range []string{path, snapshot} {
		data, err := os.ReadFile(checkPath)
		if err != nil || string(data) != "n = 1\n" {
			t.Fatalf("file/temp mutated or styled: %q, %v", data, err)
		}
	}
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	terminalRun(t, args, "", "no change detected\n", 0)
}

func TestTOMLNondisplayModesStayPlainOnTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	path := filepath.Join(t.TempDir(), "input.toml")
	if err := os.WriteFile(path, []byte("n = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	args := []string{"--dry-run", "--query", ".n=2", path}
	if code := Execute(RunOptions{Args: args, Stdout: &out, Stderr: &diagnostics}); code != 0 {
		t.Fatalf("exit=%d: %s", code, diagnostics.String())
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatal("plain diff contains ANSI")
	}
	terminalRun(t, args, "", out.String(), 0)
	terminalRun(t, []string{"--check", "--query", ".n=2", path}, "", "", 2)
	terminalRun(t, []string{"-i", "--backup", "--query", ".n=2", path}, "", "", 0)
	for file, want := range map[string]string{path: "n = 2\n", path + ".bak": "n = 1\n"} {
		data, err := os.ReadFile(file)
		if err != nil || string(data) != want {
			t.Fatalf("file/backup bytes=%q want=%q, err=%v", data, want, err)
		}
	}
}

func TestTOMLFileAndBufferOutputStayPlain(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	for _, pretty := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "stdout.toml")
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"--format", "toml"}
		want := plainTOML
		if pretty {
			args = append(args, "--pretty")
			want = prettyTOML
		}
		var diagnostics bytes.Buffer
		if code := Execute(RunOptions{Args: args, Stdin: strings.NewReader(plainTOML), Stdout: file, Stderr: &diagnostics}); code != 0 {
			t.Fatalf("exit=%d: %s", code, diagnostics.String())
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatalf("file bytes=%q want=%q, err=%v", data, want, err)
		}
		var buf bytes.Buffer
		if code := Execute(RunOptions{Args: args, Stdin: strings.NewReader(plainTOML), Stdout: &buf, Stderr: &diagnostics}); code != 0 {
			t.Fatalf("exit=%d", code)
		}
		if buf.String() != want {
			t.Fatalf("buffer bytes=%q want=%q", buf.String(), want)
		}
	}
}
