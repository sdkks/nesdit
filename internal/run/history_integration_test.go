package run

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sdkks/nesdit/internal/natsinspect"
	"github.com/sdkks/nesdit/internal/natstest"
)

func historyAdmin(t *testing.T, nc *nats.Conn, subject, body string) map[string]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := nc.RequestWithContext(ctx, subject, []byte(body))
	if err != nil {
		t.Fatalf("admin %s: %v", subject, err)
	}
	var result map[string]json.RawMessage
	if err = json.Unmarshal(m.Data, &result); err != nil || result["error"] != nil {
		t.Fatalf("admin %s: %s %v", subject, m.Data, err)
	}
	return result
}
func historyCreate(t *testing.T, nc *nats.Conn, storage string, replicas int) {
	t.Helper()
	body := fmt.Sprintf(`{"name":"ORDERS","subjects":["orders.>"],"storage":%q,"num_replicas":%d}`, storage, replicas)
	historyAdmin(t, nc, "$JS.API.STREAM.CREATE.ORDERS", body)
}
func historyBroker(t *testing.T, cluster bool) string {
	t.Helper()
	if !cluster {
		return natstest.Start(t, fmt.Sprintf("jetstream { store_dir: %q }", filepath.Join(t.TempDir(), "store"))).URL
	}
	ports := make([]int, 3)
	for i := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports[i] = l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
	}
	var routes []string
	for _, port := range ports {
		routes = append(routes, fmt.Sprintf("nats-route://127.0.0.1:%d", port))
	}
	var url string
	for i, port := range ports {
		config := fmt.Sprintf("server_name: node%d\njetstream { store_dir: %q }\ncluster { name: history, listen: 127.0.0.1:%d, routes: [%s] }", i, filepath.Join(t.TempDir(), "store"), port, strings.Join(routes, ","))
		broker := natstest.Start(t, config)
		if i == 0 {
			url = broker.URL
		}
	}
	nc, err := nats.Connect(url, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		m, err := nc.RequestWithContext(ctx, "$JS.API.INFO", []byte("{}"))
		cancel()
		if err == nil && !bytes.Contains(m.Data, []byte(`"error"`)) {
			return url
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("cluster account API did not become ready")
	return ""
}

func historyCLI(t *testing.T, bin, endpoint string, args []string) (string, string, int) {
	t.Helper()
	flags := []string{"--nats-history", "ORDERS", "--subject", "orders.>", "--server", endpoint, "--duration", "5s"}
	flags = append(flags, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, flags...)
	var out, logs bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &logs
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	if ctx.Err() != nil {
		t.Fatal("history CLI timeout", logs.String())
	}
	return out.String(), logs.String(), code
}

// This proxy records only inspector outbound bytes. Administrator traffic uses
// its own direct connection, so setup, consumer activity and later mutations
// cannot be mistaken for inspector traffic.
func historyTrace(t *testing.T, endpoint string) (string, func() string) {
	return historyProxy(t, endpoint, "")
}

func historyProxy(t *testing.T, endpoint, version string) (string, func() string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var trace bytes.Buffer
	done := make(chan struct{})
	var mu sync.Mutex
	var inbound, outbound net.Conn
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		if inbound != nil {
			_ = inbound.Close()
		}
		if outbound != nil {
			_ = outbound.Close()
		}
		mu.Unlock()
		<-done
	})
	go func() {
		defer close(done)
		in, err := listener.Accept()
		if err != nil {
			return
		}
		out, err := net.DialTimeout("tcp", strings.TrimPrefix(endpoint, "nats://"), time.Second)
		if err != nil {
			_ = in.Close()
			return
		}
		mu.Lock()
		inbound, outbound = in, out
		mu.Unlock()
		defer func() { _ = in.Close(); _ = out.Close() }()
		copied := make(chan struct{})
		go func() {
			if version != "" {
				reader := bufio.NewReader(out)
				line, err := reader.ReadString('\n')
				if err == nil {
					line = strings.Replace(line, `"version":"2.15.0"`, `"version":"`+version+`"`, 1)
					_, _ = io.WriteString(in, line)
					_, _ = io.Copy(in, reader)
				}
			} else {
				_, _ = io.Copy(in, out)
			}
			_ = in.Close()
			close(copied)
		}()
		_, _ = io.Copy(io.MultiWriter(out, &trace), in)
		_ = out.Close()
		<-copied
	}()
	return "nats://" + listener.Addr().String(), func() string {
		select {
		case <-done:
			return trace.String()
		case <-time.After(3 * time.Second):
			t.Fatal("inspector socket was not closed")
			return ""
		}
	}
}
func assertHistoryTraffic(t *testing.T, traffic string) {
	t.Helper()
	reader := bufio.NewReader(strings.NewReader(traffic))
	var pubs, subs []string
	for {
		line, err := reader.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			t.Fatal("empty protocol line")
		}
		switch fields[0] {
		case "CONNECT", "PING", "PONG", "UNSUB":
		case "SUB":
			if len(fields) < 3 || !strings.HasPrefix(fields[1], "_INBOX.") {
				t.Fatal("non-inbox subscription", line)
			}
			subs = append(subs, fields[1])
		case "PUB":
			if len(fields) != 4 || (fields[1] != "$JS.API.STREAM.INFO.ORDERS" && fields[1] != "$JS.API.STREAM.MSG.GET.ORDERS") || !strings.HasPrefix(fields[2], "_INBOX.") {
				t.Fatal("unexpected inspector publication", line)
			}
			pubs = append(pubs, fields[1])
			size, err := strconv.Atoi(fields[3])
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.CopyN(io.Discard, reader, int64(size+2)); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("unexpected inspector protocol", line)
		}
	}
	if len(pubs) < 1 || len(subs) != 1 {
		t.Fatal(pubs, subs)
	}
	t.Logf("inspector-only traffic: PUB=%v SUB=%v; no consumer operations, application PUB or application SUB", pubs, subs)
}
func historySequences(t *testing.T, out string) []string {
	t.Helper()
	var seqs []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err, line)
		}
		seq, ok := m["sequence"].(string)
		if !ok || m["stream"] != "ORDERS" {
			t.Fatal(m)
		}
		seqs = append(seqs, seq)
	}
	return seqs
}

func TestHistoryRealMatrix(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	descriptor := protobufFixture(t)
	for _, cluster := range []bool{false, true} {
		for _, storage := range []string{"memory", "file"} {
			t.Run(fmt.Sprintf("cluster=%t/%s", cluster, storage), func(t *testing.T) {
				endpoint := historyBroker(t, cluster)
				admin, err := nats.Connect(endpoint, nats.NoReconnect())
				if err != nil {
					t.Fatal(err)
				}
				defer admin.Close()
				if admin.ConnectedServerVersion() != "2.15.0" {
					t.Fatalf("matrix requires real 2.15.0, got %s", admin.ConnectedServerVersion())
				}
				replicas := 1
				if cluster {
					replicas = 3
				}
				// Cluster membership convergence is separate from stream creation.
				if cluster {
					time.Sleep(500 * time.Millisecond)
				}
				historyCreate(t, admin, storage, replicas)
				out, logs, code := historyCLI(t, bin, endpoint, []string{"--start-seq", "1"})
				if code != 0 || out != "" || !strings.Contains(logs, "reason=exhaustion") {
					t.Fatal(code, out, logs)
				}
				var times []string
				for i := 1; i <= 7; i++ {
					subject := "orders.keep"
					if i%2 == 0 {
						subject = "orders.other"
					}
					historyAdmin(t, admin, subject, fmt.Sprintf(`{"id":%d,"stream":"payload-only"}`, i))
					result := historyAdmin(t, admin, "$JS.API.STREAM.MSG.GET.ORDERS", fmt.Sprintf(`{"seq":%d}`, i))
					var m struct {
						Time string `json:"time"`
					}
					if err := json.Unmarshal(result["message"], &m); err != nil {
						t.Fatal(err)
					}
					times = append(times, m.Time)
				}
				for _, seq := range []int{2, 3, 6} {
					historyAdmin(t, admin, "$JS.API.STREAM.MSG.DELETE.ORDERS", fmt.Sprintf(`{"seq":%d}`, seq))
				}
				historyAdmin(t, admin, "$JS.API.CONSUMER.CREATE.ORDERS.EXISTING", `{"stream_name":"ORDERS","config":{"durable_name":"EXISTING","ack_policy":"explicit","deliver_policy":"all"}}`)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				delivered, err := admin.RequestWithContext(ctx, "$JS.API.CONSUMER.MSG.NEXT.ORDERS.EXISTING", []byte(`{"batch":1,"expires":1000000000}`))
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
				_, err = admin.RequestWithContext(ctx, delivered.Reply, []byte("+ACK"))
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				beforeConsumer := historyAdmin(t, admin, "$JS.API.CONSUMER.INFO.ORDERS.EXISTING", "{}")
				beforeNames := historyAdmin(t, admin, "$JS.API.CONSUMER.NAMES.ORDERS", "{}")
				beforeInfo := historyAdmin(t, admin, "$JS.API.STREAM.INFO.ORDERS", "{}")
				t.Logf("quiet BEFORE inventory=%s delivered/read=%s ack_floor=%s stream_config=%s", beforeNames["consumers"], beforeConsumer["delivered"], beforeConsumer["ack_floor"], beforeInfo["config"])
				traced, finishTrace := historyTrace(t, endpoint)
				out, logs, code = historyCLI(t, bin, traced, []string{"--start-time", times[1], "--subject", "orders.keep", "--project", `{stream: .stream, id: .id}`})
				if code != 0 || !reflect.DeepEqual(historySequences(t, out), []string{"5", "7"}) || !strings.Contains(logs, "reason=exhaustion") {
					t.Fatal(code, out, logs)
				}
				assertHistoryTraffic(t, finishTrace())
				afterConsumer := historyAdmin(t, admin, "$JS.API.CONSUMER.INFO.ORDERS.EXISTING", "{}")
				afterNames := historyAdmin(t, admin, "$JS.API.CONSUMER.NAMES.ORDERS", "{}")
				afterInfo := historyAdmin(t, admin, "$JS.API.STREAM.INFO.ORDERS", "{}")
				t.Logf("quiet AFTER inventory=%s delivered/read=%s ack_floor=%s stream_config=%s", afterNames["consumers"], afterConsumer["delivered"], afterConsumer["ack_floor"], afterInfo["config"])
				for _, key := range []string{"delivered", "ack_floor", "num_ack_pending", "num_pending", "num_waiting", "config"} {
					if !bytes.Equal(beforeConsumer[key], afterConsumer[key]) {
						t.Fatalf("consumer %s changed: %s -> %s", key, beforeConsumer[key], afterConsumer[key])
					}
				}
				if !bytes.Equal(beforeNames["consumers"], afterNames["consumers"]) || !bytes.Equal(beforeInfo["config"], afterInfo["config"]) {
					t.Fatal("inventory/config changed")
				}
				t.Logf("real NATS=%s storage=%s replicas=%d quiet consumer inventory=%s delivered/read=%s ack_floor=%s; configuration unchanged; stdout=%s stderr=%s", admin.ConnectedServerVersion(), storage, replicas, afterNames["consumers"], afterConsumer["delivered"], afterConsumer["ack_floor"], out, logs)
				out, logs, code = historyCLI(t, bin, endpoint, []string{"--start-seq", "2", "--end-seq", "5", "--subject", "orders.keep", "--count", "1", "--filter", "false"})
				if code != 0 || out != "" || !strings.Contains(logs, "reason=count; admitted=1 emitted=0") {
					t.Fatal(code, out, logs)
				}
				out, logs, code = historyCLI(t, bin, endpoint, []string{"--start-time", times[4], "--subject", "orders.keep", "--end-seq", "5"})
				if code != 0 || !reflect.DeepEqual(historySequences(t, out), []string{"5"}) {
					t.Fatal(code, out, logs)
				}
				out, logs, code = historyCLI(t, bin, endpoint, []string{"--start-seq", "1", "--subject", "absent.>"})
				if code != 0 || out != "" || !strings.Contains(logs, "reason=exhaustion") {
					t.Fatal(code, out, logs)
				}

				// Changes below are deliberate admin activity, outside the quiet invariant.
				options := natsinspect.HistoryOptions{Stream: "ORDERS", Subject: "orders.keep", StartSeq: 1}
				ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				session, err := natsinspect.OpenHistory(ctx, endpoint, options, natsinspect.Security{}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				historyAdmin(t, admin, "orders.keep", `{"id":8}`)
				historyAdmin(t, admin, "$JS.API.STREAM.MSG.DELETE.ORDERS", `{"seq":5}`)
				var leaderBefore, leaderAfter string
				if cluster {
					var c struct {
						Leader string `json:"leader"`
					}
					_ = json.Unmarshal(afterInfo["cluster"], &c)
					leaderBefore = c.Leader
					historyAdmin(t, admin, "$JS.API.STREAM.LEADER.STEPDOWN.ORDERS", "{}")
					until := time.Now().Add(8 * time.Second)
					for time.Now().Before(until) {
						reqCtx, stop := context.WithTimeout(ctx, time.Second)
						m, e := admin.RequestWithContext(reqCtx, "$JS.API.STREAM.INFO.ORDERS", []byte("{}"))
						stop()
						if e == nil {
							var r struct {
								Cluster struct {
									Leader string `json:"leader"`
								} `json:"cluster"`
							}
							_ = json.Unmarshal(m.Data, &r)
							leaderAfter = r.Cluster.Leader
							if leaderAfter != "" && leaderAfter != leaderBefore {
								break
							}
						}
						time.Sleep(50 * time.Millisecond)
					}
					if leaderAfter == "" || leaderAfter == leaderBefore {
						t.Fatal("leader did not change", leaderBefore, leaderAfter)
					}
					out, logs, code = historyCLI(t, bin, endpoint, []string{"--start-time", times[1], "--subject", "orders.keep", "--end-seq", "7"})
					if code != 0 || !reflect.DeepEqual(historySequences(t, out), []string{"7"}) {
						t.Fatal("time-plus-subject selection across holes after leader change", code, out, logs)
					}
					t.Logf("new leader time-plus-subject across holes: stdout=%s", out)
				}
				var seen []uint64
				for {
					r, err := session.Next(ctx)
					if errors.Is(err, natsinspect.ErrExhausted) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					seen = append(seen, r.History.Sequence)
				}
				session.Close()
				if !reflect.DeepEqual(seen, []uint64{1, 7}) {
					t.Fatal("fixed H/retention", seen)
				}
				t.Logf("fixed H=7 excludes later seq8, deletion skips seq5; retained=%v leader=%s -> %s", seen, leaderBefore, leaderAfter)

				// A fresh session must detect replacement at its terminal identity check.
				options.EndSeq = 1
				replacement, err := natsinspect.OpenHistory(ctx, endpoint, options, natsinspect.Security{}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer replacement.Close()
				if _, err = replacement.Next(ctx); err != nil {
					t.Fatal(err)
				}
				historyAdmin(t, admin, "$JS.API.STREAM.DELETE.ORDERS", "{}")
				historyCreate(t, admin, storage, replicas)
				if _, err = replacement.Next(ctx); err == nil || errors.Is(err, natsinspect.ErrExhausted) {
					t.Fatal("replacement not detected", err)
				}
				replacement.Close()
				// Stored Status=503 is data, even with omitted (zero-byte) payload.
				ctxPub, stop := context.WithTimeout(ctx, 3*time.Second)
				_, err = admin.RequestMsgWithContext(ctxPub, &nats.Msg{Subject: "orders.keep", Header: nats.Header{"Status": {"503"}, "Trace": {"a", "b"}}})
				stop()
				if err != nil {
					t.Fatal(err)
				}
				args := append([]string{"--start-seq", "1"}, nativeProtobufArgs(descriptor, "example.Event")...)
				out, logs, code = historyCLI(t, bin, endpoint, args)
				if code != 0 || !strings.Contains(out, `"headers":{"Status":["503"],"Trace":["a","b"]},"payload":{}`) {
					t.Fatal(code, out, logs)
				}
				t.Logf("real Protobuf zero-body stored Status=503: stdout=%s stderr=%s", out, logs)
				ctxPub, stop = context.WithTimeout(ctx, 3*time.Second)
				_, err = admin.RequestWithContext(ctxPub, "orders.keep", []byte{8, 42, 16, 1})
				stop()
				if err != nil {
					t.Fatal(err)
				}
				args = append([]string{"--start-seq", "2", "--filter", ".active == true", "--project", ".id"}, nativeProtobufArgs(descriptor, "example.Event")...)
				out, logs, code = historyCLI(t, bin, endpoint, args)
				if code != 0 || !strings.Contains(out, `"sequence":"2"`) || !strings.Contains(out, `"payload":"42"`) {
					t.Fatal(code, out, logs)
				}
				t.Logf("real Protobuf payload-root query: stdout=%s stderr=%s", out, logs)
				historyAdmin(t, admin, "$JS.API.STREAM.PURGE.ORDERS", "{}")
				out, logs, code = historyCLI(t, bin, endpoint, []string{"--start-seq", "1"})
				if code != 0 || out != "" || !strings.Contains(logs, "reason=exhaustion") {
					t.Fatal("purged empty", code, out, logs)
				}
			})
		}
	}
}
