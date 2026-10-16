package natstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nkeys"
)

// SecurityFiles owns synthetic certificates in the test's temporary directory.
type SecurityFiles struct {
	CA, Cert, Key, ServerCert, ServerKey string
	Trust                                *tls.Config
}

// NewSecurityFiles creates separate CA, server and client identities. A nonempty
// serverName intentionally replaces loopback SANs for hostname rejection tests.
func NewSecurityFiles(t *testing.T, serverName string) SecurityFiles {
	t.Helper()
	dir := t.TempDir()
	newKey := func() *ecdsa.PrivateKey {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	rootKey := newKey()
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	issue := func(name string, cert *x509.Certificate, key *ecdsa.PrivateKey) (string, string, []byte) {
		der, err := x509.CreateCertificate(rand.Reader, cert, root, &key.PublicKey, rootKey)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		certPath, keyPath := filepath.Join(dir, name+".pem"), filepath.Join(dir, name+".key")
		if err = os.WriteFile(certPath, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
			t.Fatal(err)
		}
		return certPath, keyPath, certPEM
	}
	ca, _, pemRoot := issue("ca", root, rootKey)
	server := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: root.NotBefore, NotAfter: root.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	if serverName != "" {
		server.DNSNames, server.IPAddresses = []string{serverName}, nil
	}
	serverCert, serverKey, _ := issue("server", server, newKey())
	client := &x509.Certificate{SerialNumber: big.NewInt(3), NotBefore: root.NotBefore, NotAfter: root.NotAfter, Subject: pkix.Name{CommonName: "test client"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	cert, key, _ := issue("client", client, newKey())
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemRoot) {
		t.Fatal("root certificate")
	}
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	return SecurityFiles{ca, cert, key, serverCert, serverKey, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, Certificates: []tls.Certificate{pair}}}
}

// Config enables the real broker's normal TLS or mutual TLS verification.
func (f SecurityFiles) Config(mutual bool) string {
	return fmt.Sprintf("tls { cert_file: %q, key_file: %q, ca_file: %q, verify: %t }\n", f.ServerCert, f.ServerKey, f.CA, mutual)
}

// JWTFiles creates an operator/account/user chain and a restricted user. The
// test encoder follows jwt v2.8.2 claims.go/header.go and user_claims.go using
// existing nkeys signing, not a new JWT module or runtime authentication parser.
// The real nats-server validates these tokens; no fake broker accepts them.
func JWTFiles(t *testing.T) (config, creds, denied string) {
	t.Helper()
	operator, err := nkeys.CreateOperator()
	if err != nil {
		t.Fatal(err)
	}
	defer operator.Wipe()
	account, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	defer account.Wipe()
	public := func(key nkeys.KeyPair) string {
		p, err := key.PublicKey()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	encode := func(issuer nkeys.KeyPair, subject string, fields map[string]any) string {
		claims := map[string]any{"iss": public(issuer), "sub": subject, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
		data, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha512.Sum512_256(data)
		claims["jti"] = base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:])
		claims["nats"] = fields
		data, err = json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ed25519-nkey"}`))
		input := header + "." + base64.RawURLEncoding.EncodeToString(data)
		sig, err := issuer.Sign([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		return input + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	opJWT := encode(operator, public(operator), map[string]any{"type": "operator", "version": 2})
	accountJWT := encode(operator, public(account), map[string]any{"type": "account", "version": 2, "limits": map[string]any{"subs": -1, "data": -1, "payload": -1, "conn": -1, "imports": -1, "exports": -1, "wildcards": true}})
	config = fmt.Sprintf("operator: %q\nresolver: MEM\nresolver_preload: { %s: %q }\n", opJWT, public(account), accountJWT)
	userFile := func(name, subject string) string {
		user, err := nkeys.CreateUser()
		if err != nil {
			t.Fatal(err)
		}
		defer user.Wipe()
		seed, err := user.Seed()
		if err != nil {
			t.Fatal(err)
		}
		jwt := encode(account, public(user), map[string]any{"type": "user", "version": 2, "subs": -1, "data": -1, "payload": -1, "sub": map[string]any{"allow": []string{subject}}})
		data := fmt.Sprintf("-----BEGIN NATS USER JWT-----\n%s\n------END NATS USER JWT------\n\n-----BEGIN USER NKEY SEED-----\n%s\n------END USER NKEY SEED------\n", jwt, seed)
		path := filepath.Join(t.TempDir(), name+".creds")
		if err = os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return config, userFile("allowed", "orders.>"), userFile("denied", "other.>")
}
