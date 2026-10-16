//go:build darwin || linux

package run

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sdkks/nesdit/internal/natstest"
)

func TestNATSCLIQueryStops(t *testing.T) {
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
	for _, flag := range []string{"--filter", "--project"} {
		for _, interrupt := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/interrupt-%t", flag, interrupt), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				duration := "500ms"
				if interrupt {
					duration = "8s"
				}
				// The second iterator probe must remain cancellable, after one value.
				expr := `if . == 1 then true else (true, (def forever: forever; forever)) end`
				if flag == "--project" {
					expr = `if . == 1 then . else (., (def forever: forever; forever)) end`
				}
				cmd := exec.CommandContext(ctx, bin, "--nats", "orders.>", "--server", broker.URL, "--duration", duration, flag, expr)
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				stderr, err := cmd.StderrPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				ready := make(chan struct{})
				done := make(chan string, 1)
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
					done <- logs.String()
				}()
				select {
				case <-ready:
				case <-ctx.Done():
					t.Fatal("not ready")
				}
				if err = pub.Publish("orders.first", []byte(`1`)); err != nil {
					t.Fatal(err)
				}
				if err = pub.FlushTimeout(time.Second); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(stdout)
				first, err := reader.ReadString('\n')
				if err != nil || first != "{\"subject\":\"orders.first\",\"headers\":{},\"payload\":1}\n" {
					t.Fatal(first, err)
				}
				if err = pub.Publish("orders.second", []byte(`2`)); err != nil {
					t.Fatal(err)
				}
				if err = pub.FlushTimeout(time.Second); err != nil {
					t.Fatal(err)
				}
				want := 0
				start := time.Now()
				if interrupt {
					// Readiness was synchronized above; allow the second admitted query to
					// enter evaluation. The footer below verifies that it was unfinished.
					time.Sleep(50 * time.Millisecond)
					start = time.Now()
					want = 130
					if err = cmd.Process.Signal(os.Interrupt); err != nil {
						t.Fatal(err)
					}
				}
				rest, err := io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				logs := <-done
				waitNATSExit(t, cmd, want, start)
				if len(rest) != 0 || !strings.Contains(logs, "admitted=2 emitted=1 unfinished_event=true") {
					t.Fatal(string(rest), logs)
				}
				if !interrupt && !strings.Contains(logs, "reason=duration") {
					t.Fatal(logs)
				}
				if interrupt && !strings.Contains(logs, "cancel:") {
					t.Fatal(logs)
				}
				t.Logf("exit=%d elapsed=%s retained=%s stderr=%s", want, time.Since(start), first, logs)
			})
		}
	}
}
