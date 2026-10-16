package run

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nats-io/nkeys"
	"github.com/sdkks/nesdit/internal/natsinspect"
)

const natsSecurityFileLimit = 1 << 20

// Check both the path and opened descriptor; a known FIFO must never block
// preparation. Bounded reads remain necessary when file metadata is stale.
func readNATSSecurityFile(ctx context.Context, path, flag string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	invalid := fmt.Errorf("prepare: --%s requires a readable regular file of at most 1 MiB", flag)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > natsSecurityFileLimit {
		return nil, invalid
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, invalid
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > natsSecurityFileLimit {
		return nil, invalid
	}
	data, err := io.ReadAll(io.LimitReader(file, natsSecurityFileLimit+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(data) > natsSecurityFileLimit {
		return nil, invalid
	}
	return data, nil
}

func prepareNATSSecurity(ctx context.Context, n natsOptions) (security natsinspect.Security, result error) {
	defer func() {
		if ctx.Err() != nil {
			result = ctx.Err()
		}
	}()
	if err := ctx.Err(); err != nil {
		return security, err
	}
	if n.creds == "" && n.ca == "" && n.cert == "" && n.key == "" {
		return security, nil
	}
	if !strings.HasPrefix(n.server, "tls://") {
		return security, errors.New("security files require an explicit tls:// --server")
	}
	if (n.cert == "") != (n.key == "") {
		return security, errors.New("--tls-cert and --tls-key must be supplied together")
	}
	security.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
	if n.creds != "" {
		data, err := readNATSSecurityFile(ctx, n.creds, "creds")
		if err != nil {
			return security, err
		}
		if !validNATSCredentials(data) {
			return security, errors.New("prepare: --creds requires a JWT and a user NKey seed")
		}
		security.Credentials = data
	}
	if n.ca != "" {
		data, err := readNATSSecurityFile(ctx, n.ca, "tls-ca")
		if err != nil {
			return security, err
		}
		security.TLS.RootCAs = x509.NewCertPool()
		if !security.TLS.RootCAs.AppendCertsFromPEM(data) {
			return security, errors.New("prepare: --tls-ca requires at least one valid PEM certificate")
		}
	}
	if n.cert != "" {
		cert, err := readNATSSecurityFile(ctx, n.cert, "tls-cert")
		if err != nil {
			return security, err
		}
		key, err := readNATSSecurityFile(ctx, n.key, "tls-key")
		if err != nil {
			return security, err
		}
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return security, errors.New("prepare: --tls-cert and --tls-key must contain a valid matching PEM certificate/key pair")
		}
		security.TLS.Certificates = []tls.Certificate{pair}
	}
	return security, ctx.Err()
}

// Check the credential container and key syntax, not the authorization claims.
// The broker verifies the JWT signature, issuer, subject, expiry and permissions.
func validNATSCredentials(data []byte) bool {
	jwt, err := nkeys.ParseDecoratedJWT(data)
	if err != nil {
		return false
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts[:2] {
		decoded, err := base64.RawURLEncoding.DecodeString(part)
		var object map[string]json.RawMessage
		if err != nil || json.Unmarshal(decoded, &object) != nil || object == nil {
			return false
		}
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize {
		return false
	}
	key, err := nkeys.ParseDecoratedNKey(data)
	if err != nil {
		return false
	}
	defer key.Wipe()
	public, err := key.PublicKey()
	return err == nil && nkeys.IsValidPublicUserKey(public)
}
