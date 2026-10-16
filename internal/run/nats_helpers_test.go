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
	"testing"
	"time"

	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/natsinspect"
)

// This helper process exercises Run's real DNS cancellation and process-control
// fallbacks with deterministic blocking sinks/cleanup. It is never product code.
func TestNATSProcessHelper(t *testing.T) {
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "nats-dns":
		net.DefaultResolver = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			_, _ = fmt.Fprintln(os.Stdout, "entered")
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		os.Exit(Run([]string{"--nats", "x", "--server", "nats://nats-test.invalid:4222", "--duration", "10s"}))
	case "nats-security-preparation":
		process := &natsProcess{exit: os.Exit}
		process.start(context.Background())
		path := os.Args[len(os.Args)-2]
		_, _ = fmt.Fprintln(os.Stdout, "entered")
		// Repeat bounded preparation to make the signal phase deterministic,
		// without adding a timing hook or a blocking reader to product code.
		for {
			err := runNATS(RunOptions{Ctx: context.Background(), Logger: logx.New(io.Discard)}, natsOptions{server: "tls://localhost:1", creds: path}, process.stop,
				func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
					t.Fatal("unexpected dial")
					return nil, nil
				})
			if _, ok := err.(*natsCancelled); ok {
				process.finish()
				os.Exit(130)
			}
		}
	case "nats-stderr", "nats-cleanup":
		process := &natsProcess{exit: os.Exit}
		process.start(context.Background())
		defer process.finish()
		logger := logx.New(io.Discard)
		f := &natsFake{records: []natsinspect.Record{{Data: []byte(`null`)}}}
		if mode == "nats-stderr" {
			logger = logx.New(blockingNATSLog{})
		} else {
			f.close = func() { _, _ = fmt.Fprintln(os.Stdout, "entered"); select {} }
		}
		err := runNATS(RunOptions{Ctx: context.Background(), Stdout: io.Discard, Logger: logger}, natsOptions{duration: 10 * time.Second, count: 1}, process.stop, func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
			return f, nil
		})
		t.Fatalf("blocking helper unexpectedly returned: %v", err)
	}
}

type blockingNATSLog struct{}

func (blockingNATSLog) Write([]byte) (int, error) {
	_, _ = fmt.Fprintln(os.Stdout, "entered")
	select {}
}

func TestNATSProcessPhaseBounds(t *testing.T) {
	for _, mode := range []string{"nats-dns", "nats-security-preparation", "nats-stderr", "nats-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			args := []string{"-test.run=^TestNATSProcessHelper$", "--"}
			if mode == "nats-security-preparation" {
				args = append(args, securityTestFile(t, bytes.Repeat([]byte("x"), natsSecurityFileLimit)))
			}
			cmd := exec.CommandContext(ctx, os.Args[0], append(args, mode)...)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "entered" {
				t.Fatalf("missing phase marker: %v", scanner.Err())
			}
			start := time.Now()
			if err = cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			want := 130
			// Cleanup is already stopping for count; an interrupt does not replace the
			// first stop decision or reset its one-second allowance.
			if mode == "nats-cleanup" {
				want = 1
			}
			waitNATSExit(t, cmd, want, start)
		})
	}
}
