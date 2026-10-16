//go:build darwin || linux

package run

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sdkks/nesdit/internal/natstest"
	"golang.org/x/sys/unix"
)

func TestNATSSecurityLocalCLIRejections(t *testing.T) {
	bin := buildNATSCLI(t)
	fifo := filepath.Join(t.TempDir(), "private-fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := securityTestFile(t, []byte("private-unreadable-content"))
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	files := natstest.NewSecurityFiles(t, "")
	bad := securityTestFile(t, []byte("private-malformed-content"))
	large := securityTestFile(t, bytes.Repeat([]byte("x"), natsSecurityFileLimit+1))
	cases := []struct {
		name  string
		flags []string
	}{
		{"fifo", []string{"--creds", fifo}},
		{"directory", []string{"--tls-ca", filepath.Dir(fifo)}},
		{"malformed-ca", []string{"--tls-ca", bad}},
		{"malformed-creds", []string{"--creds", bad}},
		{"oversize", []string{"--creds", large}},
		{"bad-key", []string{"--tls-cert", files.Cert, "--tls-key", bad}},
		{"unreadable", []string{"--creds", unreadable}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "unreadable" && os.Geteuid() == 0 {
				t.Skip("root bypasses file permission checks")
			}
			for _, format := range []string{"text", "json"} {
				// A real listener stays unaccepted: local failures must never dial.
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				args := append(append([]string{}, tc.flags...), "--log-format", format)
				out, logs, code := observeCLI(t, bin, "tls://"+listener.Addr().String(), args, nil)
				assertSecureFailure(t, out, logs, code, "prepare:", fifo, unreadable, bad, large)
				if err = listener.(*net.TCPListener).SetDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				conn, err := listener.Accept()
				if err == nil {
					_ = conn.Close()
					t.Fatal("dialed before validation")
				}
			}
		})
	}
}

func TestNATSSecureCLIInterruptHandshake(t *testing.T) {
	bin := buildNATSCLI(t)
	files := natstest.NewSecurityFiles(t, "")
	_, creds, _ := natstest.JWTFiles(t)
	for _, phase := range []string{"INFO", "TLS"} {
		t.Run(phase, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			if err = listener.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "--nats", "orders.>", "--server", "tls://"+listener.Addr().String(), "--duration", "6s", "--creds", creds, "--tls-ca", files.CA, "--tls-cert", files.Cert, "--tls-key", files.Key)
			var logs bytes.Buffer
			cmd.Stderr = &logs
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			conn, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if phase == "TLS" {
				if err = conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err = io.WriteString(conn, "INFO {\"server_id\":\"stall\",\"tls_required\":true}\r\n"); err != nil {
					t.Fatal(err)
				}
				var first [1]byte
				if _, err = conn.Read(first[:]); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			if err = cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			waitNATSExit(t, cmd, 130, start)
			if strings.Contains(logs.String(), "nats.ready") || strings.Contains(logs.String(), creds) {
				t.Fatal("false readiness or path disclosure")
			}
			t.Logf("configured %s interrupted in %s", phase, time.Since(start))
		})
	}
}
