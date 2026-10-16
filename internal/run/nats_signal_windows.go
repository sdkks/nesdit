//go:build windows

package run

// Windows retains its existing console behavior; no bounded interrupt claim.
func natsSignals(_ func()) func() { return func() {} }
