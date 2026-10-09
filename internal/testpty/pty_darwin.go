package testpty

import (
	"bytes"
	"unsafe"

	"golang.org/x/sys/unix"
)

func slaveName(fd int) (string, error) {
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		return "", err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		return "", err
	}
	var name [128]byte
	// x/sys has no Darwin PTY-name wrapper; this fixed ioctl fills name.
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))) //nolint:staticcheck // no libSystem wrapper for this PTY ioctl in the pinned x/sys
	if errno != 0 {
		return "", errno
	}
	return string(bytes.TrimRight(name[:], "\x00")), nil
}

func plainOutput(fd int) error {
	term, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return err
	}
	term.Oflag &^= unix.OPOST
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, term)
}
