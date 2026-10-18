//go:build windows

package run

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func binaryTerminal(f *os.File) (bool, error) {
	var mode uint32
	err := windows.GetConsoleMode(windows.Handle(f.Fd()), &mode)
	if err == nil {
		return true, nil
	}
	// Redirected files/pipes do not have a console mode. Fail closed on other
	// errors rather than letting color settings or display policy authorize bytes.
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		if kind, typeErr := windows.GetFileType(windows.Handle(f.Fd())); typeErr == nil && kind != windows.FILE_TYPE_UNKNOWN {
			return false, nil
		}
	}
	return false, errors.New("cannot determine physical binary output destination")
}
