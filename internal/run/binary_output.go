package run

import (
	"errors"
	"io"
	"os"
)

func binaryDestination(w io.Writer) error {
	f, ok := w.(*os.File)
	if !ok {
		// Opaque embedded writers are caller-owned safe sinks. Do not unwrap them,
		// close descriptors, change deadlines or start an I/O worker.
		return nil
	}
	terminal, err := binaryTerminal(f)
	if err != nil {
		return err
	}
	if terminal {
		return errors.New("binary MessagePack output refuses a physical terminal; redirect stdout to a separate file or pipe")
	}
	return nil
}
