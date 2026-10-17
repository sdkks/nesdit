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
	"golang.org/x/sys/unix"
)

func TestHistoryCLIBlockedDiagnostics(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, args := range [][]string{
		{"--nats-history", "ORDERS"},
		{"--nats-history", "ORDERS", "--log-format", "invalid"},
		{"--nats-history", "ORDERS", "--duration", "invalid"},
		{"--nats-history", "ORDERS", "--unknown-history-flag"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			fd := int(w.Fd())
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
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Stderr = w
			start := time.Now()
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitNATSExit(t, cmd, 1, start)
			if time.Since(start) < 900*time.Millisecond {
				t.Fatal("not a blocked-diagnostic watchdog test")
			}
			t.Logf("validation/parse failure armed stop before blocked diagnostic; exit=1 elapsed=%s", time.Since(start))
		})
	}
}

func TestHistoryCLIHandshakeStop(t *testing.T) {
	bin := buildNATSCLI(t)
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupt), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			duration := "150ms"
			if interrupt {
				duration = "4s"
			}
			cmd := exec.CommandContext(ctx, bin, "--nats-history", "ORDERS", "--subject", ">", "--start-seq", "1", "--server", "nats://"+listener.Addr().String(), "--duration", duration)
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
			start := time.Now()
			want := 1
			if interrupt {
				want = 130
				if err = cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			}
			waitNATSExit(t, cmd, want, start)
			if strings.Contains(logs.String(), "nats.ready") {
				t.Fatal("false history ready", logs.String())
			}
			if err = conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			var b [1]byte
			if _, err = conn.Read(b[:]); err != io.EOF {
				t.Fatal("owned socket not closed", err)
			}
			t.Logf("history handshake stop interrupt=%t exit=%d socket EOF elapsed=%s", interrupt, want, time.Since(start))
		})
	}
}

func TestHistoryCLIOutputStops(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	endpoint := historyBroker(t, false)
	admin, err := nats.Connect(endpoint, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	historyCreate(t, admin, "memory", 1)
	historyAdmin(t, admin, "orders.x", `{"small":true}`)
	historyAdmin(t, admin, "orders.x", `"`+strings.Repeat("x", 512<<10)+`"`)
	for _, scenario := range []string{"blocked-output-interrupt", "blocked-output-duration", "broken-pipe", "query-interrupt", "query-duration"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			duration := "6s"
			if strings.HasSuffix(scenario, "duration") {
				duration = "300ms"
			}
			args := []string{"--nats-history", "ORDERS", "--subject", ">", "--start-seq", "1", "--server", endpoint, "--duration", duration}
			if strings.HasPrefix(scenario, "query") {
				args = append(args, "--project", "until(false; .)")
			}
			cmd := exec.CommandContext(ctx, bin, args...)
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close(); _ = w.Close() }()
			cmd.Stdout = w
			if scenario == "broken-pipe" {
				_ = r.Close()
			}
			stderr, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			ready := make(chan struct{})
			done := make(chan string, 1)
			go func() {
				var log bytes.Buffer
				scanner := bufio.NewScanner(stderr)
				for scanner.Scan() {
					line := scanner.Text()
					fmt.Fprintln(&log, line)
					if strings.Contains(line, "nats.ready") {
						close(ready)
					}
				}
				done <- log.String()
			}()
			select {
			case <-ready:
			case <-ctx.Done():
				t.Fatal("history never ready")
			}
			start := time.Now()
			if strings.HasPrefix(scenario, "blocked") {
				if err = r.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(r)
				first, err := reader.ReadString('\n')
				if err != nil || !strings.Contains(first, `"sequence":"1"`) {
					t.Fatal(first, err)
				}
				if _, err = reader.ReadByte(); err != nil {
					t.Fatal(err)
				}
			}
			want := 1
			if strings.HasSuffix(scenario, "interrupt") {
				if strings.HasPrefix(scenario, "query") {
					time.Sleep(100 * time.Millisecond)
				}
				want = 130
				start = time.Now()
				if err = cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "query-duration" {
				want = 0
			}
			waitNATSExit(t, cmd, want, start)
			logs := <-done
			if strings.HasPrefix(scenario, "blocked") && time.Since(start) < 900*time.Millisecond {
				t.Fatal("forced output fallback not exercised")
			}
			if scenario == "broken-pipe" && !strings.Contains(logs, "write:") {
				t.Fatal(logs)
			}
			if scenario == "query-duration" && (!strings.Contains(logs, "reason=duration") || !strings.Contains(logs, "unfinished_event=true")) {
				t.Fatal(logs)
			}
			t.Logf("%s exit=%d elapsed=%s stderr=%s", scenario, want, time.Since(start), logs)
		})
	}
}
