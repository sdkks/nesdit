package natsinspect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sdkks/nesdit/internal/natstest"
)

func TestRealAdapterTLS(t *testing.T) {
	config, trust := natstest.TLSFiles(t)
	broker := natstest.Start(t, config)
	endpoint := strings.Replace(broker.URL, "nats://", "tls://", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := open(ctx, endpoint, "test.>", nil, trust)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	pub, err := nats.Connect(endpoint, nats.Secure(trust), nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	m := &nats.Msg{Subject: "test.value", Reply: "test.reply", Header: nats.Header{"Trace": {"one", "two"}}, Data: []byte(`null`)}
	if err = pub.PublishMsg(m); err != nil {
		t.Fatal(err)
	}
	if err = pub.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	record, err := session.Next(ctx)
	if err != nil || record.Subject != m.Subject || record.Reply != m.Reply || len(record.Headers["Trace"]) != 2 || string(record.Data) != "null" || record.HeaderBytes <= 0 {
		t.Fatalf("%+v %v", record, err)
	}
	session.Close()
	session.Close()
	if err = session.Err(); err != nil {
		t.Fatal("intentional close fabricated error:", err)
	}
}

func TestRealAdapterPendingOverflow(t *testing.T) {
	broker := natstest.Start(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	failure := make(chan error, 1)
	session, err := Open(ctx, broker.URL, "test.>", func(err error) {
		select {
		case failure <- err:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	c := session.(*client)
	messages, payloadBytes, err := c.sub.PendingLimits()
	if err != nil || messages != 8 || payloadBytes != 64<<20 {
		t.Fatal(messages, payloadBytes, err)
	}
	pub, err := nats.Connect(broker.URL, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	// Deliberately do not consume. Nine records exceed the exact queue count.
	for i := 0; i < 9; i++ {
		if err = pub.Publish("test.x", []byte(`0`)); err != nil {
			t.Fatal(err)
		}
	}
	if err = pub.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-failure:
		if !strings.Contains(err.Error(), "capacity") {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("overflow not reported")
	}
	if session.Err() == nil {
		t.Fatal("overflow disappeared")
	}
	session.Close()
	if session.Err() == nil {
		t.Fatal("close hid earlier error")
	}
}

func TestRealAdapterSynchronousPermissionError(t *testing.T) {
	for _, inspectFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("inspect-before-close-%t", inspectFirst), func(t *testing.T) {
			testSynchronousPermissionError(t, inspectFirst)
		})
	}
}

func testSynchronousPermissionError(t *testing.T, inspectFirst bool) {
	broker := natstest.Start(t, `no_auth_user: observer
authorization { users: [{user: observer, permissions: {subscribe: {deny: ["denied"]}}}] }`)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	defer close(release)
	nc, err := nats.Connect(broker.URL, nats.NoReconnect(), nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) { entered <- struct{}{}; <-release }))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync("denied")
	if err != nil {
		t.Fatal(err)
	}
	if err = nc.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("permission callback not entered")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &client{nc: nc, sub: sub, cancel: cancel, socket: &socketOwner{ctx: ctx}}
	// Callback is deliberately held before the adapter can latch anything.
	// Readiness and the final pre-close inspection must both retain the error.
	if inspectFirst && c.Err() == nil {
		t.Fatal("delayed callback hid permission failure")
	}
	c.Close()
	if c.Err() == nil {
		t.Fatal("intentional close erased permission failure")
	}
}

func TestRealAdapterPendingBytesBoundary(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprintf("extra-%d", extra), func(t *testing.T) {
			broker := natstest.Start(t, "max_payload: 10485760")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			failed := make(chan error, 1)
			session, err := Open(ctx, broker.URL, "bytes", func(err error) {
				select {
				case failed <- err:
				default:
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			c := session.(*client)
			pub, err := nats.Connect(broker.URL, nats.NoReconnect())
			if err != nil {
				t.Fatal(err)
			}
			defer pub.Close()
			for i := 0; i < 8; i++ {
				size := 8 << 20
				if i == 7 {
					size += extra
				}
				if err = pub.Publish("bytes", make([]byte, size)); err != nil {
					t.Fatal(err)
				}
				if err = pub.FlushTimeout(5 * time.Second); err != nil {
					t.Fatal(err)
				}
				if i < 7 || extra == 0 {
					if err = c.nc.FlushTimeout(5 * time.Second); err != nil {
						t.Fatal(err)
					}
				}
			}
			if extra == 0 {
				messages, payloadBytes, err := c.sub.Pending()
				if err != nil || messages != 8 || payloadBytes != 64<<20 || session.Err() != nil {
					t.Fatal(messages, payloadBytes, err, session.Err())
				}
			} else {
				select {
				case <-failed:
				case <-ctx.Done():
					t.Fatal("payload-byte overflow not detected")
				}
				if session.Err() == nil {
					t.Fatal("payload-byte overflow vanished")
				}
			}
		})
	}
}

func TestRealAdapterHeaderWireSize(t *testing.T) {
	broker := natstest.Start(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := Open(ctx, broker.URL, "headers", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	pub, err := nats.Connect(broker.URL, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	for _, size := range []int{65536, 65537} {
		m := &nats.Msg{Subject: "headers", Header: nats.Header{"Test": {"x"}}, Data: []byte(`null`)}
		overhead := m.Size() - len(m.Subject) - len(m.Data) - 1
		m.Header["Test"] = []string{strings.Repeat("x", size-overhead)}
		if err = pub.PublishMsg(m); err != nil {
			t.Fatal(err)
		}
		if err = pub.FlushTimeout(time.Second); err != nil {
			t.Fatal(err)
		}
		record, err := session.Next(ctx)
		if err != nil || record.HeaderBytes != size {
			t.Fatal(record.HeaderBytes, size, err)
		}
	}
}

func TestSocketCancellationDuringINFO(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		s, err := Open(ctx, "nats://"+listener.Addr().String(), "x", nil)
		if s != nil {
			s.Close()
		}
		done <- err
	}()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	cancel()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("unexpected success")
		}
	case <-time.After(time.Second):
		t.Fatal("INFO cancellation blocked")
	}
	if err = conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err = conn.Read(b[:]); err == nil {
		t.Fatal("socket still open")
	}
}

func TestClientFirstErrorSurvivesClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := &client{cancel: cancel, socket: &socketOwner{ctx: ctx}}
	first := errors.New("delivery: first")
	c.fail(first, false)
	c.Close()
	c.fail(errors.New("delivery: disconnect"), true)
	c.fail(errors.New("delivery: later"), false)
	if !errors.Is(c.Err(), first) {
		t.Fatal(c.Err())
	}
}

func TestSocketOwnerRaceAndIdempotence(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner := &socketOwner{ctx: ctx}
	conn, err := owner.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	peer, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); owner.close() }()
	}
	wg.Wait()
	if _, err = conn.Write([]byte("x")); err == nil {
		t.Fatal("closed socket writable")
	}
	if late, dialErr := owner.Dial("tcp", listener.Addr().String()); dialErr == nil {
		_ = late.Close()
		t.Fatal("registration after close allowed")
	}
}
