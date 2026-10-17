package run

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/sdkks/nesdit/internal/natstest"
)

func TestHistoryRealPermissions(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	for _, mode := range []string{"allowed", "deny-info", "deny-get", "deny-inbox"} {
		t.Run(mode, func(t *testing.T) {
			pubs := `["$JS.API.STREAM.INFO.ORDERS","$JS.API.STREAM.MSG.GET.ORDERS"]`
			subs := `["_INBOX.>"]`
			if mode == "deny-info" {
				pubs = `["$JS.API.STREAM.MSG.GET.ORDERS"]`
			}
			if mode == "deny-get" {
				pubs = `["$JS.API.STREAM.INFO.ORDERS"]`
			}
			if mode == "deny-inbox" {
				subs = `["not-an-inbox.>"]`
			}
			config := fmt.Sprintf("jetstream { store_dir: %q }\nno_auth_user: observer\nauthorization { users: [{user: admin,password: synthetic-fixture-only}, {user: observer, permissions: {publish: {allow: %s}, subscribe: {allow: %s}}}] }", filepath.Join(t.TempDir(), "store"), pubs, subs)
			broker := natstest.Start(t, config)
			admin, err := nats.Connect(broker.URL, nats.NoReconnect(), nats.UserInfo("admin", "synthetic-fixture-only"))
			if err != nil {
				t.Fatal(err)
			}
			defer admin.Close()
			historyCreate(t, admin, "memory", 1)
			historyAdmin(t, admin, "orders.x", `{"ok":true}`)
			endpoint, finish := historyTrace(t, broker.URL)
			out, logs, code := historyCLI(t, bin, endpoint, []string{"--start-seq", "1"})
			assertHistoryTraffic(t, finish())
			if mode == "allowed" {
				if code != 0 || !strings.Contains(out, `"ok":true`) {
					t.Fatal(code, out, logs)
				}
			} else {
				if code != 1 || out != "" || !strings.Contains(logs, "permission") || strings.Contains(logs, "reason=exhaustion") {
					t.Fatal(code, out, logs)
				}
				if mode != "deny-get" && strings.Contains(logs, "nats.ready") {
					t.Fatal("false readiness", logs)
				}
			}
			t.Logf("real broker permissions %s exit=%d stderr=%s", mode, code, logs)
		})
	}
}

func TestHistoryVersionGateFixture(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	endpoint := historyBroker(t, false)
	for _, version := range []string{"2.14.9", "2.11.0", "2.16.0", "2.15.1-rc.1", "bad"} {
		t.Run(version, func(t *testing.T) {
			// This is a version-rejection fixture, NOT an older real-server test.
			// Only the handshake version is substituted; the real backend is 2.15.0.
			proxy, finish := historyProxy(t, endpoint, version)
			out, logs, code := historyCLI(t, bin, proxy, []string{"--start-seq", "1"})
			traffic := finish()
			if code != 1 || out != "" || strings.Contains(logs, "nats.ready") || !strings.Contains(logs, "2.15.x") || strings.Contains(traffic, "PUB ") || strings.Contains(traffic, "SUB ") {
				t.Fatal(code, out, logs, traffic)
			}
			t.Logf("fault-injected handshake version=%s rejected before INFO/GET; real backend=2.15.0", version)
		})
	}
}

func TestHistoryRealTLS(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	files := natstest.NewSecurityFiles(t, "")
	broker := natstest.Start(t, files.Config(true)+fmt.Sprintf("jetstream { store_dir: %q }", filepath.Join(t.TempDir(), "store")))
	endpoint := strings.Replace(broker.URL, "nats://", "tls://", 1)
	admin, err := nats.Connect(endpoint, nats.NoReconnect(), nats.Secure(files.Trust))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	historyCreate(t, admin, "file", 1)
	historyAdmin(t, admin, "orders.x", `{"ok":true}`)
	out, logs, code := historyCLI(t, bin, endpoint, []string{"--start-seq", "1", "--tls-ca", files.CA, "--tls-cert", files.Cert, "--tls-key", files.Key})
	if code != 0 || !strings.Contains(out, `"ok":true`) {
		t.Fatal(code, out, logs)
	}
	out, logs, code = historyCLI(t, bin, endpoint, []string{"--start-seq", "1"})
	assertSecureFailure(t, out, logs, code, "TLS:", files.CA, files.Key)
	key, err := os.ReadFile(files.Key)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains([]byte(logs), key) {
		t.Fatal("key disclosed")
	}
}
