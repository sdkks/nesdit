//go:build windows

package display

import "os"

// Keep Windows output plain, matching the existing platform contract.
func terminal(_ *os.File) bool { return false }
