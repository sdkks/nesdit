package natsinspect

import (
	"context"
	"errors"
	"testing"

	"github.com/sdkks/nesdit/internal/natstest"
)

func TestRealAdapterCancellationCloseIsNotFailure(t *testing.T) {
	broker := natstest.Start(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := Open(ctx, broker.URL, "orders.>", nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	session.Close()
	if _, err = session.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("intentional channel closure replaced cancellation", err)
	}
	if err = session.Err(); err != nil {
		t.Fatal(err)
	}
}
