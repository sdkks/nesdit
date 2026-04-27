package run_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sdkks/nesdit/internal/run"
)

// Test_ExitCode_2_Only_From_Check_Path enforces DR-002: exit code 2 MUST
// only be produced by the --check path. All other error/success paths
// produce exit 0 or exit 1.
//
// The test exercises every major non-check code path and asserts the exit
// code is 0 or 1 — never 2. The --check drift case is then verified to
// produce exactly 2.
func Test_ExitCode_2_Only_From_Check_Path(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Fixture files.
	jsonFile := filepath.Join(dir, "a.json")
	if err := os.WriteFile(jsonFile, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	nonexistent := filepath.Join(dir, "does-not-exist.json")
	badJSON := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badJSON, []byte(`not valid json`), 0o644); err != nil {
		t.Fatalf("setup bad.json: %v", err)
	}

	runWith := func(args []string) int {
		t.Helper()
		var stdout, stderr bytes.Buffer
		return run.Execute(run.RunOptions{
			Args:   args,
			Ctx:    context.Background(),
			Stdin:  bytes.NewReader(nil),
			Stdout: &stdout,
			Stderr: &stderr,
		})
	}

	// --- Non-check paths MUST never return 2 ---

	// File→stdout success: exit 0.
	if c := runWith([]string{jsonFile, "--query", "."}); c == 2 {
		t.Errorf("file->stdout success: got exit 2; want 0 or 1")
	}

	// IO error (missing file): exit 1.
	if c := runWith([]string{nonexistent, "--query", "."}); c == 2 {
		t.Errorf("io error path: got exit 2; want 1")
	}

	// Parse error: exit 1.
	if c := runWith([]string{badJSON, "--query", "."}); c == 2 {
		t.Errorf("parse error path: got exit 2; want 1")
	}

	// Bad query: exit 1.
	if c := runWith([]string{jsonFile, "--query", "invalid!!jq"}); c == 2 {
		t.Errorf("bad query path: got exit 2; want 1")
	}

	// Flag conflict: exit 1.
	if c := runWith([]string{jsonFile, "--query", ".", "--from-file", "/dev/null"}); c == 2 {
		t.Errorf("flag conflict path: got exit 2; want 1")
	}

	// -i path (in-place): exit 0.
	inplace := filepath.Join(dir, "inplace.json")
	if err := os.WriteFile(inplace, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatalf("setup inplace: %v", err)
	}
	if c := runWith([]string{"-i", inplace, "--query", ".x = 2"}); c == 2 {
		t.Errorf("-i path: got exit 2; want 0 or 1")
	}

	// --dry-run success: exit 0 (not 2, even if diff is non-empty).
	dryrun := filepath.Join(dir, "dryrun.json")
	if err := os.WriteFile(dryrun, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatalf("setup dryrun: %v", err)
	}
	if c := runWith([]string{dryrun, "-n", "--query", ".x = 99"}); c == 2 {
		t.Errorf("--dry-run success path: got exit 2; want 0")
	}

	// --- --check with drift MUST return exactly 2 ---
	checkDrift := filepath.Join(dir, "check.json")
	if err := os.WriteFile(checkDrift, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatalf("setup check: %v", err)
	}
	if c := runWith([]string{checkDrift, "--check", "--query", ".x = 99"}); c != 2 {
		t.Errorf("--check drift path: got exit %d; want 2", c)
	}

	// --- --check clean MUST return 0 ---
	if c := runWith([]string{checkDrift, "--check", "--query", "."}); c != 0 {
		t.Errorf("--check clean path: got exit %d; want 0", c)
	}

	// --- --check error MUST return 1 (not 2) ---
	if c := runWith([]string{nonexistent, "--check", "--query", "."}); c != 1 {
		t.Errorf("--check error path: got exit %d; want 1", c)
	}
}
