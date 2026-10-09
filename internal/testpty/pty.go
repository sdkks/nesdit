//go:build darwin || linux

// Package testpty provides native PTYs for terminal output tests.
package testpty

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// Open returns a master reader and terminal slave. Output postprocessing is
// disabled so tests compare application bytes rather than terminal LF mapping.
func Open(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = master.Close() })
	conn, err := master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var path string
	var openErr error
	if err := conn.Control(func(fd uintptr) { path, openErr = slaveName(int(fd)) }); err != nil {
		t.Fatal(err)
	}
	if openErr != nil {
		t.Fatal(openErr)
	}
	slave, err := os.OpenFile(path, os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	if err := plainOutput(int(slave.Fd())); err != nil {
		t.Fatal(err)
	}
	return master, slave
}
