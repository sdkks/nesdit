//go:build !windows

package run

import (
	"os"
	"os/signal"
	"syscall"
)

func natsSignals(interrupt func()) func() {
	signals := make(chan os.Signal, 2)
	done := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGPIPE)
	go func() {
		for {
			select {
			case sig := <-signals:
				// Receiving SIGPIPE makes the writer report EPIPE rather than killing the
				// process with an undocumented signal exit. The write path owns the error.
				if sig == os.Interrupt {
					interrupt()
				}
			case <-done:
				return
			}
		}
	}()
	return func() { signal.Stop(signals); close(done) }
}
