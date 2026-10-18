//go:build !darwin && !linux && !windows

package run

import (
	"errors"
	"os"
)

func binaryTerminal(_ *os.File) (bool, error) {
	return false, errors.New("physical binary output detection is unsupported on this platform")
}
