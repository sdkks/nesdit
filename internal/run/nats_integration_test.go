package run

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sdkks/nesdit/internal/natstest"
)

func buildNATSCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "nesdit")
	build := exec.Command("go", "build", "-mod=readonly", "-o", bin, "../../cmd/nesdit")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	return bin
}

func observeCLI(t *testing.T, bin, endpoint string, args []string, onReady func()) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	flags := []string{"--nats", "orders.>", "--server", endpoint, "--duration", "2s"}
	flags = append(flags, args...)
	cmd := exec.CommandContext(ctx, bin, flags...)
	var out, logs bytes.Buffer
	cmd.Stdout = &out
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Fprintln(&logs, line)
		if strings.Contains(line, "nats.ready") && onReady != nil {
			onReady()
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		t.Fatalf("CLI timed out: %s", &logs)
	}
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return out.String(), logs.String(), code
}

func TestNATSRealCLI(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	broker := natstest.Start(t, "")
	publisher, err := nats.Connect(broker.URL, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	publish := func(msgs ...*nats.Msg) func() {
		return func() {
			for _, m := range msgs {
				if err := publisher.PublishMsg(m); err != nil {
					t.Fatal(err)
				}
			}
			if err := publisher.FlushTimeout(time.Second); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("multiline-wildcard-headers-count", func(t *testing.T) {
		message := &nats.Msg{Subject: "orders.created", Header: nats.Header{"Trace": {"a", "b"}, "trace": {""}}, Data: []byte("{\n  \"id\": 2\n}")}
		out, logs, code := observeCLI(t, bin, broker.URL, []string{"--count", "1"}, publish(message))
		want := "{\"subject\":\"orders.created\",\"headers\":{\"Trace\":[\"a\",\"b\"],\"trace\":[\"\"]},\"payload\":{\"id\":2}}\n"
		if code != 0 || out != want || !strings.Contains(logs, "reason=count; admitted=1 emitted=1 unfinished_event=false") {
			t.Fatalf("code=%d out=%q logs=%s", code, out, logs)
		}
		t.Logf("stdout=%s stderr=%s exit=%d", out, logs, code)
	})
	t.Run("valid-then-malformed", func(t *testing.T) {
		out, logs, code := observeCLI(t, bin, broker.URL, nil, publish(&nats.Msg{Subject: "orders.x", Data: []byte(`null`)}, &nats.Msg{Subject: "orders.x", Data: []byte(`{`)}, &nats.Msg{Subject: "orders.x", Data: []byte(`2`)}))
		if code != 1 || out != "{\"subject\":\"orders.x\",\"headers\":{},\"payload\":null}\n" || !strings.Contains(logs, "decode:") || !strings.Contains(logs, "admitted=2 emitted=1") {
			t.Fatalf("%d %q %s", code, out, logs)
		}
	})
	t.Run("empty-status503", func(t *testing.T) {
		out, logs, code := observeCLI(t, bin, broker.URL, nil, publish(&nats.Msg{Subject: "orders.x", Header: nats.Header{"Status": {"503"}}}))
		if code != 1 || out != "" || !strings.Contains(logs, "decode:") || !strings.Contains(logs, "admitted=1 emitted=0") || strings.Contains(logs, "responders") {
			t.Fatalf("%d %q %s", code, out, logs)
		}
	})
	t.Run("quiet-duration-json-logs", func(t *testing.T) {
		out, logs, code := observeCLI(t, bin, broker.URL, []string{"--duration", "100ms", "--log-format", "json"}, nil)
		if code != 0 || out != "" || !strings.Contains(logs, `"reason":"duration"`) || !strings.Contains(logs, `"admitted":0`) {
			t.Fatalf("%d %q %s", code, out, logs)
		}
	})
	t.Run("pretty-redirect-plain", func(t *testing.T) {
		out, logs, code := observeCLI(t, bin, broker.URL, []string{"--nats-output", "pretty", "--count", "1"}, publish(&nats.Msg{Subject: "orders.x", Data: []byte(`true`)}))
		if code != 0 || !strings.HasPrefix(out, "{\n  \"subject\":") || strings.Contains(out, "\x1b") {
			t.Fatalf("%d %q %s", code, out, logs)
		}
	})
	t.Run("denied-no-false-ready", func(t *testing.T) {
		denied := natstest.Start(t, `no_auth_user: observer
authorization { users: [{user: observer, permissions: {subscribe: {deny: ["orders.>"]}}}] }`)
		for i := 0; i < 15; i++ {
			out, logs, code := observeCLI(t, bin, denied.URL, nil, nil)
			if code != 1 || out != "" || strings.Contains(logs, "nats.ready") || !strings.Contains(logs, "subscribe:") {
				t.Fatalf("%d %q %s", code, out, logs)
			}
		}
	})
	t.Run("disconnect-no-reconnect", func(t *testing.T) {
		disconnected := natstest.Start(t, "")
		out, logs, code := observeCLI(t, bin, disconnected.URL, nil, disconnected.Stop)
		if code != 1 || out != "" || !strings.Contains(logs, "delivery:") || strings.Count(logs, "nats.ready") != 1 {
			t.Fatalf("%d %q %s", code, out, logs)
		}
	})
	t.Run("authentication-rejected", func(t *testing.T) {
		token := fmt.Sprintf("synthetic-%d", time.Now().UnixNano())
		secured := natstest.Start(t, fmt.Sprintf("authorization { token: %q }", token))
		out, logs, code := observeCLI(t, bin, secured.URL, nil, nil)
		if code != 1 || out != "" || strings.Contains(logs, "nats.ready") || !strings.Contains(logs, "auth:") || strings.Contains(logs, token) {
			t.Fatalf("%d %q %s", code, out, logs)
		}
	})
	t.Run("TLS-untrusted-rejected", func(t *testing.T) {
		config, _ := natstest.TLSFiles(t)
		secure := natstest.Start(t, config)
		out, logs, code := observeCLI(t, bin, strings.Replace(secure.URL, "nats://", "tls://", 1), nil, nil)
		if code != 1 || out != "" || strings.Contains(logs, "nats.ready") || !strings.Contains(logs, "TLS:") {
			t.Fatalf("%d %q %s", code, out, logs)
		}
	})
}
