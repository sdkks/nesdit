//go:build !windows

// tty_unix.go — TTY detection and open helpers for Unix-family platforms
// (Linux, macOS, *BSD). Used by the --edit mode (STORY-0007 M1).
//
// On Unix, /dev/tty is the canonical controlling-terminal device. Opening it
// succeeds iff the calling process has a controlling terminal — which is
// exactly the TTY precondition --edit needs. Using /dev/tty (rather than
// isatty(os.Stdin.Fd())) has two advantages:
//
//  1. It works even when the user has redirected stdin/stdout from a pipe or
//     file — as long as the process has a controlling terminal (i.e. was
//     started from a terminal session), /dev/tty opens.
//  2. The opened file provides a real read/write handle for the editor's
//     stdin/stdout/stderr, so the editor gets a proper TTY regardless of how
//     nesdit itself was invoked.
package run

import (
	"errors"
	"os"
)

// checkTTY verifies that the process has access to a controlling terminal
// by attempting to open /dev/tty. Returns nil on success, a descriptive
// error on failure.
func checkTTY() error {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0) //nolint:gosec // fixed path
	if err != nil {
		return errors.New("no controlling terminal available")
	}
	_ = f.Close()
	return nil
}

// openTTY opens /dev/tty for read/write, providing the editor with a real
// terminal handle. The caller is responsible for closing the returned file.
func openTTY() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0) //nolint:gosec // fixed path
}
