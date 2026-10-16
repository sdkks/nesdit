package natsinspect

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/sdkks/nesdit/internal/natstest"
)

func TestRealAdapterConcurrentErrorCheckAndClose(t *testing.T) {
	broker := natstest.Start(t, "")
	for i := 0; i < 2000; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		session, err := Open(ctx, broker.URL, "test", nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 500; j++ {
				_ = session.Err()
				runtime.Gosched()
			}
		}()
		close(start)
		cancel()
		wg.Wait()
		session.Close()
		if err := session.Err(); err != nil {
			t.Fatalf("iteration %d: intentional close fabricated failure: %v", i, err)
		}
	}
}

func TestClientErrorPublicationAfterClose(t *testing.T) {
	for _, asynchronous := range []bool{false, true} {
		name := "synchronous"
		if asynchronous {
			name = "asynchronous"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			c := &client{cancel: cancel, socket: &socketOwner{ctx: ctx}, onFailure: func(error) { calls++ }}
			c.Close()
			failure := errors.New("delivery: broker failure")
			// Deterministically exercise publication after the close transition.
			// Synchronous observations can originate from teardown; delayed async
			// broker errors must still be retained even after intentional close.
			c.fail(failure, !asynchronous)
			if asynchronous {
				if !errors.Is(c.Err(), failure) || calls != 1 {
					t.Fatalf("lost async error: %v, callbacks=%d", c.Err(), calls)
				}
			} else if c.Err() != nil || calls != 0 {
				t.Fatalf("teardown fabricated failure: %v, callbacks=%d", c.Err(), calls)
			}
		})
	}
}

func TestSocketAggregateConnectDeadline(t *testing.T) {
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	start := time.Now()
	session, err := openWithDial(ctx, "nats://"+listener.Addr().String(), "test", nil, nil, dial)
	elapsed := time.Since(start)
	if session != nil {
		session.Close()
	}
	if err == nil {
		t.Fatal("stalled INFO handshake succeeded")
	}
	// Three seconds of dial plus a fresh five-second handshake would take eight
	// seconds. Allow one second of scheduler tolerance around the aggregate cap.
	if elapsed < 4500*time.Millisecond || elapsed > 6*time.Second {
		t.Fatalf("aggregate connect took %s: %v", elapsed, err)
	}
	if ctx.Err() != nil {
		t.Fatal("connection consumed the total session deadline")
	}
	if err := listener.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal("dial never reached the stalled handshake:", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("startup timeout left socket open")
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatal("peer did not observe socket closure")
	}
	t.Logf("slow dial plus stalled INFO stopped in %s", elapsed)
}

func TestRealAdapterSurvivesConnectDeadline(t *testing.T) {
	broker := natstest.Start(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	start := time.Now()
	session, err := Open(ctx, broker.URL, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	// Exercise a live subscription beyond the startup allowance, not just an
	// unexpired session context or a connection-state flag.
	timer := time.NewTimer(time.Until(start.Add(5200 * time.Millisecond)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	pub, err := nats.Connect(broker.URL, nats.NoReconnect())
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()
	if err = pub.Publish("test", []byte(`null`)); err != nil {
		t.Fatal(err)
	}
	if err = pub.FlushTimeout(time.Second); err != nil {
		t.Fatal(err)
	}
	receive, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	record, err := session.Next(receive)
	if err != nil || string(record.Data) != "null" || session.Err() != nil {
		t.Fatalf("session did not survive startup allowance: %+v, %v, %v", record, err, session.Err())
	}
}
