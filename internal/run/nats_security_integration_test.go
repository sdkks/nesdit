package run

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/sdkks/nesdit/internal/natstest"
)

func TestNATSSecureCLI(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	files := natstest.NewSecurityFiles(t, "")
	jwtConfig, creds, denied := natstest.JWTFiles(t)
	for _, mode := range []string{"custom-ca", "jwt", "mtls", "jwt-mtls"} {
		t.Run(mode, func(t *testing.T) {
			mutual, auth := strings.Contains(mode, "mtls"), strings.Contains(mode, "jwt")
			config := files.Config(mutual)
			flags := []string{"--tls-ca", files.CA}
			pubOpts := []nats.Option{nats.Secure(files.Trust), nats.NoReconnect()}
			if auth {
				config += jwtConfig
				flags = append(flags, "--creds", creds)
				data, err := os.ReadFile(creds)
				if err != nil {
					t.Fatal(err)
				}
				pubOpts = append(pubOpts, nats.UserCredentialBytes(data))
			}
			if mutual {
				flags = append(flags, "--tls-cert", files.Cert, "--tls-key", files.Key)
			}
			broker := natstest.Start(t, config)
			endpoint := strings.Replace(broker.URL, "nats://", "tls://", 1)
			publisher, err := nats.Connect(endpoint, pubOpts...)
			if err != nil {
				t.Fatal("secure publisher connection", err)
			}
			defer publisher.Close()
			args := append(append([]string{}, flags...), "--count", "2", "--filter", ".show", "--project", "{id: .id}")
			out, logs, code := observeCLI(t, bin, endpoint, args, func() {
				for _, data := range []string{`{"show":false,"id":1}`, `{"show":true,"id":2,"subject":"payload"}`} {
					if err := publisher.PublishMsg(&nats.Msg{Subject: "orders.created", Header: nats.Header{"Trace": {"a", "b"}}, Data: []byte(data)}); err != nil {
						t.Fatal(err)
					}
				}
				if err := publisher.FlushTimeout(time.Second); err != nil {
					t.Fatal(err)
				}
			})
			if code != 0 || out != "{\"subject\":\"orders.created\",\"headers\":{\"Trace\":[\"a\",\"b\"]},\"payload\":{\"id\":2}}\n" || !strings.Contains(logs, "reason=count; admitted=2 emitted=1") {
				t.Fatalf("%s exit=%d stdout=%s stderr=%s", mode, code, out, logs)
			}
			t.Logf("%s actual CLI count/filter/project/metadata succeeded", mode)
			out, logs, code = observeCLI(t, bin, endpoint, append(append([]string{}, flags...), "--duration", "80ms"), nil)
			if code != 0 || out != "" || !strings.Contains(logs, "reason=duration") {
				t.Fatal("secure quiet completion", code, logs)
			}
			if auth {
				badFlags := append([]string{}, flags...)
				badFlags = append(badFlags, "--creds", denied)
				for i := 0; i < 3; i++ {
					out, logs, code = observeCLI(t, bin, endpoint, badFlags, nil)
					assertSecureFailure(t, out, logs, code, "subscribe:", denied)
				}
			}
		})
	}
}

func assertSecureFailure(t *testing.T, out, logs string, code int, hint string, paths ...string) {
	t.Helper()
	if code != 1 || out != "" || strings.Contains(logs, "nats.ready") || !strings.Contains(logs, hint) {
		t.Fatalf("exit=%d stdout=%q stderr=%s", code, out, logs)
	}
	for _, secret := range append(paths, "BEGIN", "PRIVATE KEY", "x509:", "remote error:") {
		if strings.Contains(logs, secret) {
			t.Fatal("sensitive diagnostic")
		}
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			if strings.Contains(logs, string(data)) {
				t.Fatal("file contents in diagnostic")
			}
			if jwt, err := nkeys.ParseDecoratedJWT(data); err == nil && len(jwt) > 20 && strings.Contains(logs, jwt) {
				t.Fatal("JWT in diagnostic")
			}
			if diagnosticContainsSeed(data, logs) {
				t.Fatal("seed in diagnostic")
			}
		}
	}
}

func diagnosticContainsSeed(data []byte, logs string) bool {
	key, err := nkeys.ParseDecoratedNKey(data)
	if err != nil {
		return false
	}
	defer key.Wipe()
	seed, err := key.Seed()
	return err == nil && bytes.Contains([]byte(logs), seed)
}

func TestDiagnosticContainsSeed(t *testing.T) {
	_, creds, _ := natstest.JWTFiles(t)
	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(data)
	key, err := nkeys.ParseDecoratedNKey(data)
	if err != nil {
		t.Fatal("parse synthetic credentials")
	}
	defer key.Wipe()
	seed, err := key.Seed()
	if err != nil {
		t.Fatal("extract synthetic seed")
	}
	for _, tc := range []struct {
		name, diagnostic string
		want             bool
	}{
		{"seed-only", "auth: " + string(seed), true},
		{"safe", "auth: authentication failed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := diagnosticContainsSeed(data, tc.diagnostic); got != tc.want {
				t.Errorf("seed detection = %t, want %t", got, tc.want)
			}
			if !bytes.Equal(data, original) {
				t.Error("credential input was mutated")
			}
		})
	}
}

func TestNATSSecureCLIRejections(t *testing.T) {
	if os.Getenv("NATS_SERVER_BIN") == "" {
		t.Skip("NATS_SERVER_BIN required")
	}
	bin := buildNATSCLI(t)
	files := natstest.NewSecurityFiles(t, "")
	other := natstest.NewSecurityFiles(t, "")
	jwtConfig, creds, _ := natstest.JWTFiles(t)
	_, foreign, _ := natstest.JWTFiles(t)
	data, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	jwt, err := nkeys.ParseDecoratedJWT(data)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	sig := []byte(parts[2])
	if sig[0] == 'A' {
		sig[0] = 'B'
	} else {
		sig[0] = 'A'
	}
	parts[2] = string(sig)
	badSignature := securityTestFile(t, bytes.Replace(data, []byte(jwt), []byte(strings.Join(parts, ".")), 1))
	broker := natstest.Start(t, files.Config(true)+jwtConfig)
	endpoint := strings.Replace(broker.URL, "nats://", "tls://", 1)
	base := []string{"--tls-ca", files.CA, "--tls-cert", files.Cert, "--tls-key", files.Key, "--creds", creds}
	for _, tc := range []struct {
		name, hint string
		flags      []string
	}{
		{"unknown-root", "TLS:", []string{"--tls-ca", other.CA}},
		{"foreign-client", "TLS:", []string{"--tls-cert", other.Cert, "--tls-key", other.Key}},
		{"unknown-issuer", "auth:", []string{"--creds", foreign}},
		{"invalid-signature", "auth:", []string{"--creds", badSignature}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, format := range []string{"text", "json"} {
				args := append(append(append([]string{}, base...), tc.flags...), "--log-format", format)
				out, logs, code := observeCLI(t, bin, endpoint, args, nil)
				assertSecureFailure(t, out, logs, code, tc.hint, creds, foreign, badSignature, files.Key, other.Key, other.CA)
			}
		})
	}
	for _, tc := range []struct {
		name, config, hint string
		flags              []string
	}{
		{"missing-client", files.Config(true), "TLS:", []string{"--tls-ca", files.CA}},
		{"missing-creds", files.Config(false) + jwtConfig, "auth:", []string{"--tls-ca", files.CA}},
		{"system-trust-rejection", files.Config(false), "TLS:", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := natstest.Start(t, tc.config)
			out, logs, code := observeCLI(t, bin, strings.Replace(b.URL, "nats://", "tls://", 1), tc.flags, nil)
			assertSecureFailure(t, out, logs, code, tc.hint, files.CA)
		})
	}
	t.Run("hostname", func(t *testing.T) {
		wrongName := natstest.NewSecurityFiles(t, "different.invalid")
		b := natstest.Start(t, wrongName.Config(false))
		out, logs, code := observeCLI(t, bin, strings.Replace(b.URL, "nats://", "tls://", 1), []string{"--tls-ca", wrongName.CA}, nil)
		assertSecureFailure(t, out, logs, code, "TLS:", wrongName.CA)
	})
}
