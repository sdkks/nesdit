//go:build darwin || linux

package run

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sdkks/nesdit/internal/natstest"
	"github.com/sdkks/nesdit/internal/testpty"
	"golang.org/x/sys/unix"
)

func waitNATSExit(t *testing.T, cmd *exec.Cmd, want int, start time.Time) {
	t.Helper()
	err := cmd.Wait()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	// One-second allowance plus one second of explicit test scheduler tolerance.
	if code != want || time.Since(start) > 2*time.Second {
		t.Fatalf("exit=%d want=%d elapsed=%s", code, want, time.Since(start))
	}
}

func TestNATSCLIInterruptHandshake(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, phase := range []string{"INFO", "TLS", "readiness", "startup-deadline"} {
		t.Run(phase, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			scheme := "nats://"
			if phase == "TLS" {
				scheme = "tls://"
			}
			duration := "8s"
			if phase == "startup-deadline" {
				duration = "150ms"
			}
			cmd := exec.CommandContext(ctx, bin, "--nats", "x", "--server", scheme+listener.Addr().String(), "--duration", duration)
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
			if err = conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if phase == "startup-deadline" {
				waitNATSExit(t, cmd, 1, time.Now())
				if strings.Contains(logs.String(), "nats.ready") || !strings.Contains(logs.String(), "connect: subscription was not ready") {
					t.Fatal(logs.String())
				}
				return
			}
			if phase == "TLS" {
				if _, err = io.WriteString(conn, "INFO {\"server_id\":\"stall\",\"tls_required\":true}\r\n"); err != nil {
					t.Fatal(err)
				}
				var first [1]byte
				if _, err = conn.Read(first[:]); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "readiness" {
				if _, err = io.WriteString(conn, "INFO {\"server_id\":\"stall\"}\r\n"); err != nil {
					t.Fatal(err)
				}
				scanner := bufio.NewScanner(conn)
				subscribed := false
				for scanner.Scan() {
					line := scanner.Text()
					if strings.HasPrefix(line, "SUB ") {
						subscribed = true
					}
					if line == "PING" {
						if subscribed {
							break
						}
						if _, err = io.WriteString(conn, "PONG\r\n"); err != nil {
							t.Fatal(err)
						}
					}
				}
				if !subscribed {
					t.Fatalf("no subscription: %v", scanner.Err())
				}
			}
			start := time.Now()
			if err = cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			waitNATSExit(t, cmd, 130, start)
			if strings.Contains(logs.String(), "nats.ready") {
				t.Fatal("false ready:", logs.String())
			}
			t.Logf("%s cooperative interruption: %s", phase, logs.String())
		})
	}
}

func TestNATSCLIOutputAndInterrupt(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	broker := natstest.Start(t, "")
	pub, err := nats.Connect(broker.URL, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	for _, scenario := range []string{"idle-interrupt", "blocked-stdout-interrupt", "blocked-stdout-duration", "pending-overflow-blocked-output", "broken-pipe"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			duration := "8s"
			if scenario == "blocked-stdout-duration" {
				duration = "300ms"
			}
			cmd := exec.CommandContext(ctx, bin, "--nats", "orders.>", "--server", broker.URL, "--duration", duration)
			read, write, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = read.Close() }()
			defer func() { _ = write.Close() }()
			cmd.Stdout = write
			if scenario == "broken-pipe" {
				_ = read.Close()
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = write.Close()
			ready := make(chan struct{})
			logsDone := make(chan string, 1)
			go func() {
				var logs bytes.Buffer
				scanner := bufio.NewScanner(stderr)
				for scanner.Scan() {
					line := scanner.Text()
					fmt.Fprintln(&logs, line)
					if strings.Contains(line, "nats.ready") {
						close(ready)
					}
				}
				logsDone <- logs.String()
			}()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("not ready")
			}
			start := time.Now()
			if strings.HasPrefix(scenario, "blocked-stdout") {
				if err = pub.Publish("orders.first", []byte(`null`)); err != nil {
					t.Fatal(err)
				}
				if err = pub.FlushTimeout(time.Second); err != nil {
					t.Fatal(err)
				}
				first, readErr := bufio.NewReader(read).ReadString('\n')
				if readErr != nil || first != "{\"subject\":\"orders.first\",\"headers\":{},\"payload\":null}\n" {
					t.Fatal(first, readErr)
				}
			}
			if scenario != "idle-interrupt" {
				body := []byte(`"` + strings.Repeat("x", 512<<10) + `"`)
				if err = pub.Publish("orders.x", body); err != nil {
					t.Fatal(err)
				}
				if err = pub.FlushTimeout(time.Second); err != nil {
					t.Fatal(err)
				}
				if scenario != "broken-pipe" {
					// One byte proves emission began; leave the rest unread to block it.
					if err = read.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
						t.Fatal(err)
					}
					var first [1]byte
					if _, err = read.Read(first[:]); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario == "pending-overflow-blocked-output" {
				for i := 0; i < 9; i++ {
					if err = pub.Publish("orders.queued", []byte(`0`)); err != nil {
						t.Fatal(err)
					}
				}
				if err = pub.FlushTimeout(time.Second); err != nil {
					t.Fatal(err)
				}
				start = time.Now()
			}
			want := 1
			if strings.HasSuffix(scenario, "interrupt") {
				want = 130
				start = time.Now()
				if err = cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			}
			waitNATSExit(t, cmd, want, start)
			logs := <-logsDone
			if scenario == "idle-interrupt" && !strings.Contains(logs, "cancel:") {
				t.Fatal(logs)
			}
			if strings.HasPrefix(scenario, "blocked-stdout") && time.Since(start) < 900*time.Millisecond {
				t.Fatal("did not exercise forced fallback")
			}
			if scenario == "broken-pipe" && !strings.Contains(logs, "write:") {
				t.Fatal(logs)
			}
			t.Logf("%s exit=%d elapsed=%s stderr=%s", scenario, want, time.Since(start), logs)
		})
	}
}

func TestNATSCLIBlockedStderr(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	broker := natstest.Start(t, "")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()
	fd := int(write.Fd())
	if err = unix.SetNonblock(fd, true); err != nil {
		t.Fatal(err)
	}
	for {
		_, err = unix.Write(fd, make([]byte, 4096))
		if err == unix.EAGAIN {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = unix.SetNonblock(fd, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--nats", "orders.>", "--server", broker.URL, "--duration", "100ms")
	cmd.Stderr = write
	start := time.Now()
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitNATSExit(t, cmd, 1, start)
	if time.Since(start) < time.Second {
		t.Fatal("did not exercise forced stderr fallback")
	}
}

func TestNATSCLITerminalLayout(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	broker := natstest.Start(t, "")
	pub, err := nats.Connect(broker.URL, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	for _, tc := range []struct {
		name, noColor, term string
		styled              bool
	}{{"color", "", "xterm", true}, {"NO_COLOR", "1", "xterm", false}, {"dumb", "", "dumb", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("TERM", tc.term)
			master, slave := testpty.Open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "--nats", "orders.>", "--server", broker.URL, "--duration", "5s", "--count", "1")
			cmd.Stdout = slave
			stderr, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(stderr)
			ready := false
			for scanner.Scan() {
				if strings.Contains(scanner.Text(), "nats.ready") {
					ready = true
					if err = pub.Publish("orders.x", []byte(`{"ok":true}`)); err != nil {
						t.Fatal(err)
					}
					if err = pub.FlushTimeout(time.Second); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err = cmd.Wait(); err != nil || !ready {
				t.Fatal(err, ready)
			}
			if err = master.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4096)
			n, err := master.Read(buf)
			if err != nil {
				t.Fatal(err)
			}
			out := string(buf[:n])
			if !strings.HasPrefix(out, "{\n  ") || strings.Contains(out, "\x1b[") != tc.styled {
				t.Fatalf("terminal layout/color: %q", out)
			}
		})
	}
}
