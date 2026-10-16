// Package natsinspect owns a single temporary, read-only Core NATS subscription.
package natsinspect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// Pending limits use the client's payload-only byte accounting.
const (
	PendingMessages = 8
	PendingBytes    = 64 << 20
)

// ErrEmptyPayload is an admitted empty message treated by the client as a
// no-responders status. Observation never makes requests.
var ErrEmptyPayload = errors.New("decode: expected one complete JSON value")

// Record preserves decoded header values and the received wire header size.
type Record struct {
	Subject, Reply string
	Headers        map[string][]string
	Data           []byte
	HeaderBytes    int
}

// Session has no publishing or administrative operations.
type Session interface {
	Next(context.Context) (Record, error)
	Err() error
	Close()
}

type client struct {
	mu         sync.Mutex
	failure    error
	closing    bool
	cancel     context.CancelFunc
	onFailure  func(error)
	nc         *nats.Conn
	sub        *nats.Subscription
	socket     *socketOwner
	socketDone chan struct{}
}

// socketOwner bypasses client locks during blocked INFO/TLS handshakes.
type socketOwner struct {
	mu     sync.Mutex
	ctx    context.Context
	conn   net.Conn
	dial   func(context.Context, string, string) (net.Conn, error)
	closed bool
}

func (s *socketOwner) Dial(network, address string) (net.Conn, error) {
	dial := s.dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	}
	conn, err := dial(s.ctx, network, address)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		_ = conn.Close()
		return nil, context.Canceled
	}
	s.conn = conn
	s.mu.Unlock()
	return conn, nil
}

func (s *socketOwner) close() {
	s.mu.Lock()
	s.closed = true
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

// Open returns after the subscription processing barrier and synchronous error
// checks. onFailure must be nonblocking; it wakes the invocation's stop owner.
func Open(ctx context.Context, server, subject string, onFailure func(error)) (Session, error) {
	return open(ctx, server, subject, onFailure, nil)
}

func open(ctx context.Context, server, subject string, onFailure func(error), trust *tls.Config) (Session, error) {
	return openWithDial(ctx, server, subject, onFailure, trust, nil)
}

// Security contains only locally prepared bytes and verified TLS configuration.
// Callers must not mutate these values during a session.
type Security struct {
	Credentials []byte
	TLS         *tls.Config
}

// OpenSecure applies preloaded security inputs without handshake file reads.
func OpenSecure(ctx context.Context, server, subject string, security Security, onFailure func(error)) (Session, error) {
	return openConfigured(ctx, server, subject, onFailure, security, nil)
}

func openWithDial(ctx context.Context, server, subject string, onFailure func(error), trust *tls.Config, dial func(context.Context, string, string) (net.Conn, error)) (Session, error) {
	return openConfigured(ctx, server, subject, onFailure, Security{TLS: trust}, dial)
}

func openConfigured(ctx context.Context, server, subject string, onFailure func(error), security Security, dial func(context.Context, string, string) (net.Conn, error)) (Session, error) {
	opts := nats.GetDefaultOptions()
	if len(security.Credentials) > 0 {
		if err := nats.UserCredentialBytes(security.Credentials)(&opts); err != nil {
			return nil, errors.New("auth: invalid --creds input")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	connect, stopConnect := context.WithTimeout(ctx, 5*time.Second)
	defer stopConnect()
	c := &client{cancel: cancel, onFailure: onFailure, socket: &socketOwner{ctx: connect, dial: dial}}
	go func() { <-ctx.Done(); c.closeSocket() }()
	// The client starts its handshake timeout after dialing. Keep one aggregate
	// allowance and close raw IO without waiting for the client's handshake lock.
	stopConnectClose := context.AfterFunc(connect, c.socket.close)
	opts.Url = server
	opts.AllowReconnect = false
	opts.RetryOnFailedConnect = false
	opts.IgnoreDiscoveredServers = true
	opts.SkipHostLookup = true
	opts.SubChanLen = PendingMessages
	opts.PermissionErrOnSubscribe = true
	opts.FlusherTimeout = time.Second
	opts.Timeout = 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < opts.Timeout {
		opts.Timeout = time.Until(deadline)
	}
	opts.CustomDialer = c.socket
	opts.TLSConfig = security.TLS
	opts.AsyncErrorCB = func(_ *nats.Conn, _ *nats.Subscription, err error) { c.fail(deliveryError(err), false) }
	opts.DisconnectedErrCB = func(_ *nats.Conn, _ error) {
		c.fail(errors.New("delivery: connection disconnected; observation cannot resume"), true)
	}
	opts.ClosedCB = func(_ *nats.Conn) { c.fail(errors.New("delivery: connection closed"), true) }
	nc, err := opts.Connect()
	// Disarm before canceling the startup context: a successful session must
	// outlive this allowance. If expiry already won, do not accept the connection.
	stopped := stopConnectClose()
	if err == nil && (!stopped || connect.Err() != nil) {
		err = context.DeadlineExceeded
	}
	stopConnect()
	// Publish pointers only when initialized; cancellation during Connect needs
	// only the raw socket and must never wait for the client's connection lock.
	c.mu.Lock()
	c.nc = nc
	c.mu.Unlock()
	if err != nil {
		c.Close()
		return nil, startupError(err, "connect")
	}
	sub, err := nc.SubscribeSync(subject)
	if err == nil {
		c.mu.Lock()
		c.sub = sub
		c.mu.Unlock()
		err = sub.SetPendingLimits(PendingMessages, PendingBytes)
	}
	if err == nil {
		startup, stop := context.WithTimeout(ctx, 5*time.Second)
		err = nc.FlushWithContext(startup)
		stop()
	}
	if err == nil {
		err = c.Err()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		c.Close()
		return nil, startupError(err, "subscribe")
	}
	return c, nil
}

func startupError(err error, stage string) error {
	var verification *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var remote *net.OpError
	if (errors.As(err, &remote) && remote.Op == "remote error") || errors.Is(err, nats.ErrTLS) || errors.As(err, &verification) || errors.As(err, &unknown) || errors.Is(err, nats.ErrSecureConnRequired) || errors.Is(err, nats.ErrSecureConnWanted) {
		return errors.New("TLS: subscription was not ready; check endpoint, certificate trust and client certificate requirements")
	}
	if errors.Is(err, nats.ErrAuthorization) || errors.Is(err, nats.ErrAuthExpired) || errors.Is(err, nats.ErrAuthRevoked) {
		return errors.New("auth: subscription was not ready; check broker authentication requirements")
	}
	if stage == "subscribe" {
		return errors.New("subscribe: subscription was not ready; check subject permission and server availability")
	}
	return errors.New("connect: subscription was not ready; check endpoint, network and TLS configuration")
}

func deliveryError(err error) error {
	if errors.Is(err, nats.ErrSlowConsumer) {
		return errors.New("delivery: pending capacity exceeded; observation incomplete")
	}
	return errors.New("delivery: broker or subscription error; check subject permission and broker health")
}

func (c *client) fail(err error, teardown bool) {
	c.mu.Lock()
	fresh := c.failure == nil && (!teardown || !c.closing)
	if fresh {
		c.failure = err
	}
	c.mu.Unlock()
	if fresh {
		if c.onFailure != nil {
			c.onFailure(err)
		}
		c.cancel()
	}
}

func (c *client) closeSocket() {
	c.mu.Lock()
	if c.socketDone != nil {
		done := c.socketDone
		c.mu.Unlock()
		<-done
		return
	}
	done := make(chan struct{})
	c.socketDone = done
	c.mu.Unlock()
	// Capture synchronous errors before marking teardown intentional. LastError
	// is set before an asynchronous permission/overflow callback is scheduled.
	// No ownership mutex is held while consulting the client or closing IO.
	_ = c.Err()
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()
	c.socket.close()
	close(done)
}

func (c *client) Err() error {
	c.mu.Lock()
	err, closing, nc, sub := c.failure, c.closing, c.nc, c.sub
	c.mu.Unlock()
	if err != nil || closing || nc == nil {
		return err
	}
	// Client calls run without our mutex. Recheck intentional-close ownership
	// when publishing: teardown can invalidate a subscription or set LastError
	// after the snapshot above. Async broker errors still latch independently.
	if err = nc.LastError(); err != nil {
		c.fail(deliveryError(err), true)
	} else if !nc.IsConnected() {
		c.fail(errors.New("delivery: connection disconnected"), true)
	} else if sub != nil {
		dropped, dropErr := sub.Dropped()
		if dropped > 0 || dropErr != nil {
			c.fail(errors.New("delivery: pending capacity or subscription failure"), true)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure
}

func (c *client) Next(ctx context.Context) (Record, error) {
	if err := c.Err(); err != nil {
		return Record{}, err
	}
	m, err := c.sub.NextMsgWithContext(ctx)
	if errors.Is(err, nats.ErrNoResponders) {
		return Record{}, ErrEmptyPayload
	}
	if err != nil {
		return Record{}, err
	}
	return Record{Subject: m.Subject, Reply: m.Reply, Headers: m.Header, Data: m.Data,
		HeaderBytes: m.Size() - len(m.Subject) - len(m.Reply) - len(m.Data)}, nil
}

func (c *client) Close() {
	c.closeSocket()
	c.cancel()
	c.mu.Lock()
	nc := c.nc
	c.mu.Unlock()
	if nc != nil {
		nc.Close()
	}
}
