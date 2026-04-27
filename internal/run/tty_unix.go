//go:build !windows

// Package run provides TTY detection and open helpers for Unix-family
// platforms (Linux, macOS, *BSD). Used by the --edit mode.
//
// checkTTY uses isatty on stdin (fd 0). This is intentionally stricter than
// opening /dev/tty: /dev/tty succeeds whenever the process has a controlling
// terminal — including when nesdit is invoked as `make test-e2e` or inside a
// testscript harness where stdin is a pipe but the parent shell is a terminal.
// The --edit UX contract (M1) is that the *user* is at a keyboard — which
// requires stdin to be a TTY, not merely the existence of a controlling
// terminal somewhere up the process tree.
package run

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// checkTTY returns nil iff stdin (fd 0) is an interactive terminal.
// It uses the TIOCGWINSZ ioctl as an isatty probe — the same technique
// used by golang.org/x/term.IsTerminal, without adding that dependency.
func checkTTY() error {
	if !isStdinTTY() {
		return errors.New("stdin is not a TTY")
	}
	return nil
}

func isStdinTTY() bool {
	_, err := unix.IoctlGetWinsize(int(os.Stdin.Fd()), unix.TIOCGWINSZ)
	return err == nil
}

// openTTY opens /dev/tty for read/write, providing the editor with a real
// terminal handle. The caller is responsible for closing the returned file.
func openTTY() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0) //nolint:gosec // fixed path
}
