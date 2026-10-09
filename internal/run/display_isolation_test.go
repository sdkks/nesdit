//go:build darwin || linux

package run

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditorPreviewColorIsolation(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	t.Setenv("NESDIT_SKIP_TTY_CHECK", "1")
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	snapshot := filepath.Join(dir, "temp-copy.json")
	editor := filepath.Join(dir, "editor")
	if err := os.WriteFile(path, []byte(`{"n":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(editor, []byte("#!/bin/sh\ncp \"$1\" \"$NESDIT_TEST_SNAPSHOT\"\nprintf '%s' '{\"n\":2}' > \"$1\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	t.Setenv("NESDIT_TEST_SNAPSHOT", snapshot)
	var out, diagnostics bytes.Buffer
	args := []string{"--edit", path}
	if code := Execute(RunOptions{Args: args, Stdout: &out, Stderr: &diagnostics}); code != 0 {
		t.Fatalf("exit=%d: %s", code, diagnostics.String())
	}
	prefix, preview, found := strings.Cut(out.String(), "# json\n")
	if !found || preview != `{"n":2}` || strings.Contains(prefix, "\x1b[") {
		t.Fatalf("plain editor result=%q", out.String())
	}
	terminalRun(t, args, "", prefix+"# json\n{\x1b[1;39m\"n\"\x1b[0m:\x1b[36m2\x1b[0m}", 0)
	for _, checkPath := range []string{path, snapshot} {
		data, err := os.ReadFile(checkPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != `{"n":1}` {
			t.Fatalf("file/temp mutated or styled: %q", data)
		}
	}
	// The no-change path emits plain prose and no serialized preview.
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	terminalRun(t, args, "", "no change detected\n", 0)
}

func TestNondisplayModesStayPlainOnTerminal(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	path := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(path, []byte(`{"n":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Get authoritative plain help/version/diff output, then require identical
	// bytes on a terminal. Check and in-place modes must emit no stdout bytes.
	for _, args := range [][]string{{"--help"}, {"--version"}, {"--dry-run", "--query", ".n=2", path}} {
		var out, diagnostics bytes.Buffer
		if code := Execute(RunOptions{Args: args, Stdout: &out, Stderr: &diagnostics}); code != 0 {
			t.Fatalf("exit=%d: %s", code, diagnostics.String())
		}
		if strings.Contains(out.String(), "\x1b[") {
			t.Fatal("plain baseline contains ANSI")
		}
		terminalRun(t, args, "", out.String(), 0)
	}
	terminalRun(t, []string{"--check", "--query", ".n=2", path}, "", "", 2)
	terminalRun(t, []string{"-i", "--backup", "--query", ".n=2", path}, "", "", 0)
	for file, want := range map[string]string{path: `{"n":2}`, path + ".bak": `{"n":1}`} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("file/backup bytes=%q want=%q", data, want)
		}
	}
	terminalRun(t, []string{"--format", "json"}, "not-json", "", 1)
	terminalRun(t, []string{"--format", "json", "--query", "nan"}, "1", "", 1)
}

func TestJSONFileAndBufferOutputStayPlain(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	path := filepath.Join(t.TempDir(), "stdout.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	if code := Execute(RunOptions{Args: []string{"--format", "json"}, Stdin: strings.NewReader(plainObject), Stdout: file, Stderr: &diagnostics}); code != 0 {
		t.Fatalf("exit=%d: %s", code, diagnostics.String())
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != plainObject+"\n" {
		t.Fatalf("file bytes=%q", data)
	}
	var buf bytes.Buffer
	if code := Execute(RunOptions{Args: []string{"--format", "json"}, Stdin: strings.NewReader(plainObject), Stdout: &buf, Stderr: &diagnostics}); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if buf.String() != plainObject+"\n" {
		t.Fatalf("buffer bytes=%q", buf.String())
	}
}
