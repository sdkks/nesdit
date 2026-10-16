// Package natstest supplies disposable loopback brokers to integration tests.
// It is not imported by the product.
package natstest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Broker owns a disposable server process.
type Broker struct {
	URL     string
	command *exec.Cmd
	once    sync.Once
}

// Stop terminates and reaps the server exactly once.
func (b *Broker) Stop() { b.once.Do(func() { _ = b.command.Process.Kill(); _ = b.command.Wait() }) }

// Start waits for the real server's readiness announcement, not a fixed sleep.
func Start(t *testing.T, config string) *Broker {
	t.Helper()
	server := os.Getenv("NATS_SERVER_BIN")
	if server == "" {
		t.Skip("NATS_SERVER_BIN is required for real broker evidence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	args := []string{"-a", "127.0.0.1", "-p", "-1"}
	if config != "" {
		path := filepath.Join(t.TempDir(), "server.conf")
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-c", path)
	}
	command := exec.CommandContext(ctx, server, args...)
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	b := &Broker{command: command}
	t.Cleanup(b.Stop)
	ready := make(chan string, 1)
	go func() {
		var log bytes.Buffer
		scanner := bufio.NewScanner(stderr)
		endpoint := ""
		sent := false
		for scanner.Scan() {
			line := scanner.Text()
			fmt.Fprintln(&log, line)
			if _, after, ok := strings.Cut(line, "Listening for client connections on "); ok {
				endpoint = "nats://" + after
			}
			if strings.Contains(line, "Server is ready") && !sent {
				ready <- endpoint
				sent = true
			}
		}
		if !sent {
			ready <- "ERROR: " + log.String()
		}
	}()
	select {
	case b.URL = <-ready:
		if b.URL == "" || strings.HasPrefix(b.URL, "ERROR:") {
			t.Fatal(b.URL)
		}
	case <-ctx.Done():
		t.Fatal("broker readiness timeout")
	}
	return b
}

// TLSFiles generates synthetic test-only trust and a leaf key under TempDir.
// No private keys or PEM fixtures are committed or installed as system trust.
func TLSFiles(t *testing.T) (string, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "loopback test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err = os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certPEM) {
		t.Fatal("test trust")
	}
	return fmt.Sprintf("tls { cert_file: %q, key_file: %q }", certFile, keyFile), &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
}
