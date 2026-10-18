//go:build darwin || linux

package run

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func binaryTerminal(f *os.File) (bool, error) {
	_, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, unix.ENOTTY) {
		return false, nil
	}
	return false, errors.New("cannot determine physical binary output destination")
}
