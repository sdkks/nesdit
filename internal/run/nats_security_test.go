package run

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/sdkks/nesdit/internal/logx"
	"github.com/sdkks/nesdit/internal/natsinspect"
	"github.com/sdkks/nesdit/internal/natstest"
)

func securityTestFile(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private-input")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNATSSecurityFlags(t *testing.T) {
	base := []string{"--nats", "orders.>", "--server", "tls://localhost:1", "--duration", "1s"}
	for _, flags := range [][]string{
		{"--creds", ""}, {"--tls-ca", " "}, {"--tls-key", ""}, {"--tls-cert", ""},
		{"--tls-cert", "private-cert"}, {"--tls-key", "private-key"},
		{"--server", "nats://localhost:1", "--creds", "private-creds"},
		{"--server", "nats://localhost:1", "--tls-ca", "private-ca"},
		{"--server", "nats://localhost:1", "--tls-cert", "private-cert", "--tls-key", "private-key"},
	} {
		var out, logs bytes.Buffer
		code := Execute(RunOptions{Args: append(append([]string{}, base...), flags...), Stdout: &out, Stderr: &logs})
		if code == 0 || out.Len() != 0 || strings.Contains(logs.String(), "private-") {
			t.Fatalf("flags=%v code=%d logs=%s", flags, code, &logs)
		}
	}
	for _, flag := range []string{"creds", "tls-ca", "tls-cert", "tls-key"} {
		var logs bytes.Buffer
		if code := Execute(RunOptions{Args: []string{"--" + flag, "private-input"}, Stderr: &logs, Stdout: io.Discard}); code == 0 || !strings.Contains(logs.String(), "require --nats") {
			t.Fatal(flag, code, &logs)
		}
	}
}

func TestNATSSecurityPreparation(t *testing.T) {
	files := natstest.NewSecurityFiles(t, "")
	_, creds, _ := natstest.JWTFiles(t)
	credsData, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	other := natstest.NewSecurityFiles(t, "")
	missing := filepath.Join(t.TempDir(), "missing-private-path")
	bad := securityTestFile(t, []byte("private-file-content"))
	empty := securityTestFile(t, nil)
	large := securityTestFile(t, bytes.Repeat([]byte("x"), natsSecurityFileLimit+1))
	for _, tc := range []struct {
		name string
		opts natsOptions
		hint string
	}{
		{"missing", natsOptions{ca: missing}, "--tls-ca"},
		{"directory", natsOptions{creds: t.TempDir()}, "--creds"},
		{"oversize-creds", natsOptions{creds: large}, "--creds"},
		{"oversize-ca", natsOptions{ca: large}, "--tls-ca"},
		{"oversize-cert", natsOptions{cert: large, key: files.Key}, "--tls-cert"},
		{"oversize-key", natsOptions{cert: files.Cert, key: large}, "--tls-key"},
		{"empty-ca", natsOptions{ca: empty}, "valid PEM"},
		{"empty-creds", natsOptions{creds: empty}, "JWT"},
		{"malformed-ca", natsOptions{ca: bad}, "valid PEM"},
		{"malformed-creds", natsOptions{creds: bad}, "JWT"},
		{"malformed-cert", natsOptions{cert: bad, key: files.Key}, "matching PEM"},
		{"malformed-key", natsOptions{cert: files.Cert, key: bad}, "matching PEM"},
		{"mismatched-key", natsOptions{cert: files.Cert, key: other.Key}, "matching PEM"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.opts.server = "tls://localhost:1"
			opened := false
			err := runNATS(RunOptions{Ctx: context.Background(), Logger: logx.New(io.Discard)}, tc.opts, nil, func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
				opened = true
				return &natsFake{}, nil
			})
			if err == nil || opened || !strings.Contains(err.Error(), tc.hint) || strings.Contains(err.Error(), "private-") {
				t.Fatalf("err=%v opened=%t", err, opened)
			}
		})
	}
	for _, path := range []string{files.CA, files.Cert, files.Key, creds} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, bytes.Repeat([]byte("\n"), natsSecurityFileLimit-len(data))...)
		if err = os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	security, err := prepareNATSSecurity(context.Background(), natsOptions{server: "tls://localhost:1", creds: creds, ca: files.CA, cert: files.Cert, key: files.Key})
	if err != nil || len(security.Credentials) != natsSecurityFileLimit || security.TLS.InsecureSkipVerify || security.TLS.RootCAs == nil || len(security.TLS.Certificates) != 1 {
		t.Fatal("at-limit preparation", err)
	}
	// Removing the original files cannot break the in-memory handshake callbacks.
	if err = os.Remove(creds); err != nil {
		t.Fatal(err)
	}
	opts := nats.GetDefaultOptions()
	if err = nats.UserCredentialBytes(security.Credentials)(&opts); err != nil {
		t.Fatal(err)
	}
	if _, err = opts.UserJWT(); err != nil {
		t.Fatal("preloaded JWT", err)
	}
	if _, err = opts.SignatureCB([]byte("synthetic-nonce")); err != nil {
		t.Fatal("preloaded signing", err)
	}
	security, err = prepareNATSSecurity(context.Background(), natsOptions{server: "tls://localhost:1", creds: securityTestFile(t, credsData)})
	if err != nil || security.TLS.RootCAs != nil || security.TLS.InsecureSkipVerify {
		t.Fatal("system trust changed", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opened := false
	err = runNATS(RunOptions{Ctx: ctx, Logger: logx.New(io.Discard)}, natsOptions{ca: files.CA}, nil, func(context.Context, string, string, natsinspect.Security, func(error)) (natsinspect.Session, error) {
		opened = true
		return nil, nil
	})
	if _, ok := err.(*natsCancelled); !ok || opened {
		t.Fatal("preparation cancellation", err, opened)
	}
}

func TestNATSCredentialSyntax(t *testing.T) {
	_, path, _ := natstest.JWTFiles(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := nkeys.ParseDecoratedJWT(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, badJWT := range []string{"", "private-token", "e30.e30.invalid", "bnVsbA.e30." + strings.Repeat("A", 86), strings.Replace(jwt, ".", ".!", 1)} {
		if validNATSCredentials(bytes.Replace(data, []byte(jwt), []byte(badJWT), 1)) {
			t.Fatal("accepted malformed JWT container")
		}
	}
	key, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	defer key.Wipe()
	seed, err := key.Seed()
	if err != nil {
		t.Fatal(err)
	}
	user, err := nkeys.ParseDecoratedNKey(data)
	if err != nil {
		t.Fatal(err)
	}
	defer user.Wipe()
	userSeed, err := user.Seed()
	if err != nil {
		t.Fatal(err)
	}
	if validNATSCredentials(bytes.Replace(data, userSeed, seed, 1)) {
		t.Fatal("accepted account seed")
	}
	if validNATSCredentials(bytes.Replace(data, userSeed, []byte("not-a-seed"), 1)) {
		t.Fatal("accepted invalid seed")
	}
}

func TestNATSSecurityDurationAfterPreparation(t *testing.T) {
	files := natstest.NewSecurityFiles(t, "")
	var logs bytes.Buffer
	start := time.Now()
	err := runNATS(RunOptions{Ctx: context.Background(), Stdout: io.Discard, Logger: logx.New(&logs)}, natsOptions{server: "tls://localhost:1", ca: files.CA, duration: time.Second, count: 1}, nil, func(ctx context.Context, _, _ string, security natsinspect.Security, _ func(error)) (natsinspect.Session, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 900*time.Millisecond || security.TLS.RootCAs == nil {
			t.Fatal("timer/preparation ordering")
		}
		return &natsFake{records: []natsinspect.Record{{Data: []byte("null")}}}, nil
	})
	if err != nil || time.Since(start) > time.Second {
		t.Fatal(err)
	}
}
