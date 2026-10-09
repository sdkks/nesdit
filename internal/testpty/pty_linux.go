package testpty

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func slaveName(fd int) (string, error) {
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return "", err
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	return fmt.Sprintf("/dev/pts/%d", n), err
}

func plainOutput(fd int) error {
	term, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return err
	}
	term.Oflag &^= unix.OPOST
	return unix.IoctlSetTermios(fd, unix.TCSETS, term)
}
