package display

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
)

func TestOriginalBytesAndValidation(t *testing.T) {
	data := []byte(" \r\n\"é🙂\" : 1\r\n")
	spans := []Span{{3, 11, Key}, {14, 15, Number}}
	var buf bytes.Buffer
	if err := Write(&buf, data, spans); err != nil {
		t.Fatal(err)
	}
	want := " \r\n\x1b[1;39m\"é🙂\"\x1b[0m : \x1b[36m1\x1b[0m\r\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
	for _, invalid := range [][]Span{
		{{-1, 2, Key}}, {{0, 999, Key}}, {{3, 3, Key}}, {{1, 3, Key}, {2, 4, String}},
		{{0, 1, Role(99)}}, {{6, 7, Key}}, {{0, 5, Key}},
	} {
		buf.Reset()
		if err := Write(&buf, data, invalid); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf.Bytes(), data) {
			t.Fatalf("bad spans did not fall back: %v", invalid)
		}
	}
}

type partialWriter struct {
	calls, failAt int
	err           error
	buf           bytes.Buffer
}

func (w *partialWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		n := len(p) / 2
		w.buf.Write(p[:n])
		return n, w.err
	}
	return w.buf.Write(p)
}

func TestWriteFailureDoesNotRetry(t *testing.T) {
	sentinel := errors.New("broken output")
	for _, failure := range []error{nil, sentinel} {
		for _, at := range []int{1, 2, 3, 4} {
			w := &partialWriter{failAt: at, err: failure}
			err := Write(w, []byte("x\"s\"y"), []Span{{1, 4, String}})
			want := failure
			if want == nil {
				want = io.ErrShortWrite
			}
			if !errors.Is(err, want) || w.calls != at {
				t.Fatalf("err=%v calls=%d at=%d", err, w.calls, at)
			}
		}
	}
	w := &partialWriter{failAt: 1}
	if err := Write(w, []byte("plain"), nil); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("plain short write: %v", err)
	}
}

func TestNonterminalWriters(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	var buf bytes.Buffer
	if Enabled(&buf) || Enabled(io.Discard) {
		t.Fatal("capture treated as terminal")
	}
	file, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if Enabled(file) {
		t.Fatal("regular file treated as terminal")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if Enabled(w) {
		t.Fatal("pipe treated as terminal")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if Enabled(file) {
		t.Fatal("closed file treated as terminal")
	}
	var nilFile *os.File
	if Enabled(nilFile) {
		t.Fatal("nil file treated as terminal")
	}
}
