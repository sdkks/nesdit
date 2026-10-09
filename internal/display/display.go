// Package display inserts terminal styling into semantic spans of encoded bytes.
// It is used only at display sinks, never by authoritative file serializers.
package display

import (
	"io"
	"os"
	"unicode/utf8"
)

// Role identifies a key or scalar in an encoded document.
type Role uint8

// Scalar roles distinguish mapping keys from their typed values.
const (
	Key Role = iota + 1
	String
	Number
	Bool
	Null
)

// Span is a half-open byte range in the original encoded payload.
type Span struct {
	Start, End int
	Role       Role
}

// Enabled checks the actual output file and current invocation's environment.
// Wrappers and other writers are deliberately not treated as terminals.
func Enabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	return ok && terminal(f)
}

func style(role Role) string {
	switch role {
	case Key:
		return "\x1b[1;39m"
	case String:
		return "\x1b[32m"
	case Number:
		return "\x1b[36m"
	case Bool:
		return "\x1b[35m"
	case Null:
		return "\x1b[39m"
	default:
		return ""
	}
}

// Write validates all spans before writing any styling. Unknown roles or bad
// ranges fall back to the original bytes. Write failures never trigger a retry.
func Write(w io.Writer, data []byte, spans []Span) error {
	if len(spans) > 0 && !utf8.Valid(data) {
		return write(w, data)
	}
	end := 0
	for _, s := range spans {
		if s.Start < end || s.End <= s.Start || s.End > len(data) || style(s.Role) == "" ||
			(s.Start > 0 && !utf8.RuneStart(data[s.Start])) ||
			(s.End < len(data) && !utf8.RuneStart(data[s.End])) {
			return write(w, data)
		}
		end = s.End
	}
	end = 0
	for _, s := range spans {
		for _, part := range [][]byte{data[end:s.Start], []byte(style(s.Role)), data[s.Start:s.End], []byte("\x1b[0m")} {
			if err := write(w, part); err != nil {
				return err
			}
		}
		end = s.End
	}
	return write(w, data[end:])
}

func write(w io.Writer, p []byte) error {
	if len(p) == 0 {
		return nil
	}
	n, err := w.Write(p)
	if err == nil && n != len(p) {
		return io.ErrShortWrite
	}
	return err
}
