//go:build !windows

package display

import (
	"os"

	"golang.org/x/sys/unix"
)

func terminal(f *os.File) bool {
	_, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	return err == nil
}
