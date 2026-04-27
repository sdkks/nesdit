//go:build windows

// tty_windows.go — TTY stub for Windows. The --edit mode is not supported
// on Windows in v1; this file exists only to keep the build green.
package run

import (
	"errors"
	"os"
)

// checkTTY always returns an error on Windows because nesdit v1 does not
// support interactive --edit mode on Windows.
func checkTTY() error {
	return errors.New("--edit is not supported on Windows in v1")
}

// openTTY always returns an error on Windows.
func openTTY() (*os.File, error) {
	return nil, errors.New("--edit is not supported on Windows in v1")
}

// devNull is the Windows equivalent of /dev/null.
var _ = os.DevNull
