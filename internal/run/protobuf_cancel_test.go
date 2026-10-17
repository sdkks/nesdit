package run

import (
	"bytes"
	"context"
	"testing"
	"time"
)

type protobufBorrowedWriter struct {
	cancel            context.CancelFunc
	closes, deadlines int
}

func (w *protobufBorrowedWriter) Write([]byte) (int, error)        { w.cancel(); return 0, context.Canceled }
func (w *protobufBorrowedWriter) Close() error                     { w.closes++; return nil }
func (w *protobufBorrowedWriter) SetWriteDeadline(time.Time) error { w.deadlines++; return nil }

func TestProtobufBorrowedOutputCancellation(t *testing.T) {
	args := protobufArgs(protobufFixture(t), "example.Event")
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		writer := &protobufBorrowedWriter{cancel: cancel}
		var logs bytes.Buffer
		code := Execute(RunOptions{Args: args, Ctx: ctx, Stdin: bytes.NewReader(nil), Stdout: writer, Stderr: &logs})
		cancel()
		if code != 130 || writer.closes != 0 || writer.deadlines != 0 {
			t.Fatalf("code=%d closes=%d deadlines=%d logs=%s", code, writer.closes, writer.deadlines, logs.String())
		}
	}
}
