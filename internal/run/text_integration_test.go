package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sdkks/nesdit/internal/natstest"
)

func TestTextCoreRealCLI(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	broker := natstest.Start(t, "max_payload: 12000000")
	publisher, err := nats.Connect(broker.URL, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	for _, tt := range []struct {
		name    string
		body    []byte
		headers nats.Header
		extra   []string
		code    int
		payload string
		stage   string
	}{
		{"whole-body", []byte("\ufeffhello\x00\r\n世界"), nats.Header{"Trace": {"a", "b"}}, nil, 0, `"\ufeffhello\u0000\r\n世界"`, ""},
		{"ordinary-empty", nil, nil, nil, 0, `""`, ""},
		{"bodyless-status", nil, nats.Header{"Status": {"503"}}, nil, 1, "", "decode:"},
		{"original-root", []byte("abc\nxyz"), nil, []string{"--filter", `contains("abc")`, "--project", `split("\n")`}, 0, `["abc","xyz"]`, ""},
		{"suppressed", []byte("abc"), nil, []string{"--filter", "false", "--project", `error("secret")`}, 0, "", ""},
		{"invalid-even-filtered", []byte{0xff}, nil, []string{"--filter", "false"}, 1, "", "decode:"},
		{"raw-over-limit", bytes.Repeat([]byte("x"), (10<<20)+1), nil, []string{"--filter", "false"}, 1, "", "resource:"},
		{"output-expansion", bytes.Repeat([]byte("<"), 3<<20), nil, nil, 1, "", "encode:"},
		{"suppressed-expansion", bytes.Repeat([]byte("<"), 3<<20), nil, []string{"--filter", "false"}, 0, "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"--nats-codec", "text", "--count", "1"}, tt.extra...)
			out, logs, code := observeCLI(t, bin, broker.URL, args, func() {
				if err := publisher.PublishMsg(&nats.Msg{Subject: "orders.text", Header: tt.headers, Data: tt.body}); err != nil {
					t.Fatal(err)
				}
				if err := publisher.FlushTimeout(time.Second); err != nil {
					t.Fatal(err)
				}
			})
			if code != tt.code || !strings.Contains(logs, "admitted=1") || !strings.Contains(logs, tt.stage) {
				t.Fatal(code, out, logs)
			}
			if tt.payload == "" {
				if out != "" {
					t.Fatal(out)
				}
			} else {
				var got struct {
					Subject string
					Headers nats.Header
					Payload json.RawMessage
				}
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatal(err)
				}
				// Compare decoded JSON so the standard library's literal BOM spelling is immaterial.
				var wantValue, gotValue any
				if err := json.Unmarshal([]byte(tt.payload), &wantValue); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(got.Payload, &gotValue); err != nil {
					t.Fatal(err)
				}
				want, _ := json.Marshal(wantValue)
				actual, _ := json.Marshal(gotValue)
				if got.Subject != "orders.text" || !bytes.Equal(want, actual) {
					t.Fatal(out)
				}
				if tt.headers != nil && len(got.Headers["Trace"]) != 2 {
					t.Fatal(out)
				}
			}
			t.Logf("exit=%d output-bytes=%d stderr=%s", code, len(out), logs)
		})
	}
}

func TestTextHistoryLimits(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	broker := natstest.Start(t, fmt.Sprintf("max_payload: 12000000\njetstream { store_dir: %q }", filepath.Join(t.TempDir(), "store")))
	admin, err := nats.Connect(broker.URL, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	historyCreate(t, admin, "memory", 1)
	for _, body := range [][]byte{bytes.Repeat([]byte("<"), 3<<20), bytes.Repeat([]byte("x"), (10<<20)+1)} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		response, err := admin.RequestWithContext(ctx, "orders.text", body)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(response.Data, []byte(`"error"`)) {
			t.Fatal("fixture publication rejected")
		}
	}
	for _, tt := range []struct {
		seq, filter, stage string
		code               int
	}{
		{"1", "true", "encode:", 1}, {"1", "false", "reason=count", 0}, {"2", "false", "history:", 1},
	} {
		out, logs, code := historyCLI(t, bin, broker.URL, []string{"--start-seq", tt.seq, "--count", "1", "--nats-codec", "text", "--filter", tt.filter})
		if code != tt.code || out != "" || !strings.Contains(logs, tt.stage) {
			t.Fatal(code, out, logs)
		}
		t.Logf("seq=%s filter=%s exit=%d stdout=0 stderr=%s", tt.seq, tt.filter, code, logs)
	}
}

func TestTextHistoryRealCLI(t *testing.T) {
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
	messages := []*nats.Msg{
		{Subject: "orders.text", Header: nats.Header{"Trace": {"a", "b"}}, Data: []byte("\ufeffhello\x00\r\n世界")},
		{Subject: "orders.text", Header: nats.Header{"Status": {"503"}}},
		{Subject: "orders.text", Data: []byte{0xff}},
	}
	for _, message := range messages {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		response, err := admin.RequestMsgWithContext(ctx, message)
		cancel()
		if err != nil {
			t.Fatal("fixture publication", err)
		}
		if bytes.Contains(response.Data, []byte(`"error"`)) {
			t.Fatal("fixture publication rejected")
		}
	}
	before := historyAdmin(t, admin, "$JS.API.STREAM.INFO.ORDERS", "{}")
	names := historyAdmin(t, admin, "$JS.API.CONSUMER.NAMES.ORDERS", "{}")
	traced, finish := historyTrace(t, endpoint)
	out, logs, code := historyCLI(t, bin, traced, []string{"--start-seq", "1", "--end-seq", "2", "--nats-codec", "text"})
	if code != 0 || !strings.Contains(logs, "admitted=2 emitted=2") {
		t.Fatal(code, out, logs)
	}
	assertHistoryTraffic(t, finish())
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatal(out)
	}
	for i, line := range lines {
		var got struct {
			Stream, Sequence, StoredTime, Subject string
			Headers                               nats.Header
			Payload                               string
		}
		var wire map[string]json.RawMessage
		if err = json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal([]byte(line), &wire); err != nil {
			t.Fatal(err)
		}
		if got.Stream != "ORDERS" || got.Sequence != string(rune('1'+i)) || got.Subject != "orders.text" || got.Payload != string(messages[i].Data) {
			t.Fatal(line)
		}
		var stamp string
		if err = json.Unmarshal(wire["stored_time"], &stamp); err != nil {
			t.Fatal(err)
		}
		if _, err = time.Parse(time.RFC3339Nano, stamp); err != nil {
			t.Fatal(err)
		}
		if i == 1 && got.Headers.Get("Status") != "503" {
			t.Fatal("stored status not data", line)
		}
	}
	t.Logf("whole bodies and stored status: stdout=%s stderr=%s", out, logs)
	out, logs, code = historyCLI(t, bin, endpoint, []string{"--start-seq", "1", "--count", "1", "--nats-codec", "text", "--filter", "false", "--project", `error("secret")`})
	if code != 0 || out != "" || !strings.Contains(logs, "reason=count; admitted=1 emitted=0") {
		t.Fatal(code, out, logs)
	}
	out, logs, code = historyCLI(t, bin, endpoint, []string{"--start-seq", "1", "--count", "1", "--nats-codec", "text", "--filter", `contains("hello")`, "--project", "{body: ., stream: \"payload\"}"})
	if code != 0 || !strings.Contains(out, `"payload":{"body":`) || !strings.HasPrefix(out, `{"stream":"ORDERS"`) || !strings.Contains(out, `"stream":"payload"`) {
		t.Fatal(code, out, logs)
	}
	out, logs, code = historyCLI(t, bin, endpoint, []string{"--start-seq", "3", "--nats-codec", "text", "--filter", "false"})
	if code != 1 || out != "" || !strings.Contains(logs, "decode:") || !strings.Contains(logs, "admitted=1 emitted=0") {
		t.Fatal(code, out, logs)
	}
	after := historyAdmin(t, admin, "$JS.API.STREAM.INFO.ORDERS", "{}")
	afterNames := historyAdmin(t, admin, "$JS.API.CONSUMER.NAMES.ORDERS", "{}")
	if !bytes.Equal(before["state"], after["state"]) || !bytes.Equal(before["config"], after["config"]) || !bytes.Equal(names["consumers"], afterNames["consumers"]) {
		t.Fatal("inspection changed stream or inventory")
	}
	t.Log("stream state, configuration and consumer inventory unchanged")
}
