//go:build darwin || linux

package display

import (
	"io"
	"testing"

	"github.com/sdkks/nesdit/internal/testpty"
)

type terminalWrapper struct{ io.Writer }

func TestActualTerminalPolicy(t *testing.T) {
	_, slave := testpty.Open(t)
	for _, tc := range []struct {
		noColor, term string
		want          bool
	}{
		{"", "xterm", true}, {"1", "xterm", false}, {"0", "xterm", false},
		{"", "dumb", false}, {"", "DUMB", true}, {"", "", true},
	} {
		t.Setenv("NO_COLOR", tc.noColor)
		t.Setenv("TERM", tc.term)
		if got := Enabled(slave); got != tc.want {
			t.Fatalf("policy %q/%q=%v", tc.noColor, tc.term, got)
		}
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	if Enabled(terminalWrapper{slave}) {
		t.Fatal("wrapped writer should stay plain")
	}
	if err := slave.Close(); err != nil {
		t.Fatal(err)
	}
	if Enabled(slave) {
		t.Fatal("closed terminal accepted")
	}
}
