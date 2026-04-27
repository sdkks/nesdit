// Package run — edit.go implements the --edit expression-builder mode
// (FR-4, DR-003, STORY-0007).
//
// Flow:
//  1. TTY pre-check (M1): reject non-TTY invocations with an actionable
//     error before any temp file is created or editor is spawned.
//  2. Editor resolution (M2): $EDITOR → $VISUAL → "vi".
//  3. Read the input file into a format.Value (omap.Value).
//  4. Re-encode to a temp file in the file's native format.
//  5. Open the temp file in the resolved editor, connecting stdin/stdout/stderr
//     to the process TTY so the editor receives a real terminal.
//  6. After editor exit:
//     a. Non-zero exit → error on stderr, exit 1 (no output).
//     b. Temp file identical to original encoded form → "no change detected"
//     on stdout, exit 0.
//     c. Temp file empty → error on stderr ("saved file is empty — no change
//     applied"), exit 1 (no overwrite).
//     d. Otherwise → decode the edited temp file, diff against original,
//     emit diff-engine output (H2 order) to stdout.
//
// Testing bypass:
//
//	Setting NESDIT_SKIP_TTY_CHECK=1 in the environment bypasses the TTY
//	pre-check and redirects the editor's stdin/stdout/stderr to /dev/null
//	(suitable for fake-editor stubs that only manipulate files). This
//	escape hatch is ONLY for the test harness; it must never be documented
//	as a user-facing feature.
package run

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/sdkks/nesdit/internal/diff"
	"github.com/sdkks/nesdit/internal/format"
	"github.com/sdkks/nesdit/internal/logx"
)

// resolveEditor returns the editor binary to invoke.
// Resolution chain per M2: $EDITOR → $VISUAL → "vi".
func resolveEditor() string {
	if e := os.Getenv("EDITOR"); e != "" {
		return e
	}
	if e := os.Getenv("VISUAL"); e != "" {
		return e
	}
	return "vi"
}

// runEdit implements the --edit expression-builder mode.
// path is the input file; fmtName may be empty (auto-detect from extension).
func runEdit(opts RunOptions, path, fmtName string, limits format.Limits) error {
	// 1. TTY pre-check (M1).
	skipTTY := os.Getenv("NESDIT_SKIP_TTY_CHECK") == "1"
	if skipTTY {
		// Security M1: make the test-harness bypass visible in output so it
		// cannot silently affect production runs.
		opts.Logger.InfoGlobal(logx.EventEditTTYBypass,
			"NESDIT_SKIP_TTY_CHECK=1 detected: TTY pre-check bypassed (test harness only)")
	} else if err := checkTTY(); err != nil {
		opts.Logger.ErrorGlobal(logx.EventEditNoTTY,
			"--edit requires an interactive terminal (stdin is not a TTY); "+
				"pipe the result of a previous nesdit invocation or use --query directly")
		return &emittedError{cause: err}
	}

	// Detect format.
	if fmtName == "" {
		fmtName = detectFormatByExt(path)
	}
	if fmtName == "" {
		msg := "cannot detect format (supported: json, yaml, yml, toml); use --format to override"
		opts.Logger.Error(logx.EventFormatUnknown, path, msg)
		return &emittedError{cause: fmt.Errorf("%s", msg)}
	}

	// 2. Read original file.
	f, err := os.Open(path) //nolint:gosec // user-supplied path by design
	if err != nil {
		opts.Logger.Error(logx.EventIORead, path, err.Error())
		return &emittedError{cause: err}
	}
	origBytes, readErr := format.ReadAllLimited(f, limits.MaxBytes, fmtName)
	_ = f.Close()
	if readErr != nil {
		opts.Logger.Error(classifyDecodeErr(readErr), path, readErr.Error())
		return &emittedError{cause: readErr}
	}

	// 3. Decode to omap.Value.
	origVal, decErr := decodeFormatValueWithLimits(fmtName, bytes.NewReader(origBytes), limits)
	if decErr != nil {
		opts.Logger.Error(classifyDecodeErr(decErr), path, decErr.Error())
		return &emittedError{cause: decErr}
	}

	// 4. Re-encode original to temp file (normalised form).
	var origEncBuf bytes.Buffer
	if encErr := encodeFormatValue(fmtName, &origEncBuf, origVal); encErr != nil {
		opts.Logger.Error(logx.EventEncodeError, path, encErr.Error())
		return &emittedError{cause: encErr}
	}
	origEncBytes := origEncBuf.Bytes()

	tmp, tmpErr := os.CreateTemp("", "nesdit-edit-*."+fmtName)
	if tmpErr != nil {
		opts.Logger.Error(logx.EventIOWrite, path, tmpErr.Error())
		return &emittedError{cause: tmpErr}
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, werr := tmp.Write(origEncBytes); werr != nil {
		_ = tmp.Close()
		opts.Logger.Error(logx.EventIOWrite, path, werr.Error())
		return &emittedError{cause: werr}
	}
	if cerr := tmp.Close(); cerr != nil {
		opts.Logger.Error(logx.EventIOWrite, path, cerr.Error())
		return &emittedError{cause: cerr}
	}

	// 5. Spawn editor.
	editor := resolveEditor()
	cmd := exec.Command(editor, tmpPath) //nolint:gosec // editor is user-controlled by design
	if skipTTY {
		// Test harness: connect to /dev/null (fake-editor doesn't need a TTY).
		devNull, dnErr := os.Open(os.DevNull)
		if dnErr == nil {
			cmd.Stdin = devNull
			cmd.Stdout = devNull
			cmd.Stderr = devNull
			defer devNull.Close()
		}
	} else {
		// Production: connect editor to the real TTY so it gets a proper
		// terminal even when nesdit's own stdin/stdout/stderr are piped.
		tty, ttyErr := openTTY()
		if ttyErr != nil {
			opts.Logger.ErrorGlobal(logx.EventEditNoTTY,
				"--edit: cannot open TTY for editor: "+ttyErr.Error())
			return &emittedError{cause: ttyErr}
		}
		defer tty.Close()
		cmd.Stdin = tty
		cmd.Stdout = tty
		cmd.Stderr = tty
	}

	if runErr := cmd.Run(); runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			msg := fmt.Sprintf("editor %q exited with status %d", editor, exitErr.ExitCode())
			opts.Logger.ErrorGlobal(logx.EventEditEditorFailed, msg)
			return &emittedError{cause: errors.New(msg)}
		}
		opts.Logger.ErrorGlobal(logx.EventEditEditorFailed, runErr.Error())
		return &emittedError{cause: runErr}
	}

	// 6a. Read temp file back.
	editedBytes, readEditErr := os.ReadFile(tmpPath)
	if readEditErr != nil {
		opts.Logger.Error(logx.EventIORead, path, readEditErr.Error())
		return &emittedError{cause: readEditErr}
	}

	// 6b. Saved empty → error, no overwrite.
	if len(bytes.TrimSpace(editedBytes)) == 0 {
		msg := "saved file is empty — no change applied"
		opts.Logger.ErrorGlobal(logx.EventEditEmptySave, msg)
		return &emittedError{cause: errors.New(msg)}
	}

	// 6c. No change → exit 0 with message.
	if bytes.Equal(editedBytes, origEncBytes) {
		fmt.Fprintln(opts.Stdout, "no change detected")
		return nil
	}

	// 6d. Decode edited temp file and run diff engine.
	editedVal, editDecErr := decodeFormatValueWithLimits(fmtName, bytes.NewReader(editedBytes), limits)
	if editDecErr != nil {
		opts.Logger.Error(classifyDecodeErr(editDecErr), tmpPath, editDecErr.Error())
		return &emittedError{cause: editDecErr}
	}

	changes, diffErr := diff.Diff(origVal, editedVal)
	if diffErr != nil {
		opts.Logger.Error(logx.EventEncodeError, path, diffErr.Error())
		return &emittedError{cause: diffErr}
	}

	// Re-encode edited value as the source-format preview.
	var previewBuf bytes.Buffer
	if previewErr := encodeFormatValue(fmtName, &previewBuf, editedVal); previewErr != nil {
		opts.Logger.Error(logx.EventEncodeError, path, previewErr.Error())
		return &emittedError{cause: previewErr}
	}

	result := diff.BuildResult(path, fmtName, previewBuf.String(), changes)
	if wErr := result.Format(opts.Stdout); wErr != nil {
		opts.Logger.Error(logx.EventIOWrite, path, wErr.Error())
		return &emittedError{cause: wErr}
	}
	return nil
}
