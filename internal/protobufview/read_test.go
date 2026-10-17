package protobufview

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }
func TestReadBodyBoundariesAndErrors(t *testing.T) {
	for _, n := range []int{0, RawLimit, RawLimit + 1} {
		data, err := ReadBody(context.Background(), bytes.NewReader(make([]byte, n)), RawLimit)
		if n <= RawLimit {
			if err != nil || len(data) != n {
				t.Fatalf("n=%d len=%d err=%v", n, len(data), err)
			}
		} else if err == nil {
			t.Fatal("oversize accepted")
		}
	}
	calls := 0
	r := readFunc(func(p []byte) (int, error) {
		calls++
		if calls == 1 {
			copy(p, "x")
			return 1, errors.New("private input detail")
		}
		t.Fatal("read after failure")
		return 0, io.EOF
	})
	if data, err := ReadBody(context.Background(), r, 10); err == nil || data != nil || bytes.Contains([]byte(err.Error()), []byte("private")) {
		t.Fatalf("partial failure %q %v", data, err)
	}
	calls = 0
	r = readFunc(func(_ []byte) (int, error) { calls++; return 0, nil })
	if _, err := ReadBody(context.Background(), r, 10); !errors.Is(err, io.ErrNoProgress) || calls != 100 {
		t.Fatalf("no progress calls=%d err=%v", calls, err)
	}
	r = readFunc(func(p []byte) (int, error) { copy(p, "ok"); return 2, io.EOF })
	if data, err := ReadBody(context.Background(), r, 10); err != nil || string(data) != "ok" {
		t.Fatalf("data+EOF %q %v", data, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r = readFunc(func(p []byte) (int, error) { cancel(); copy(p, "x"); return 1, io.EOF })
	if data, err := ReadBody(ctx, r, 10); !errors.Is(err, context.Canceled) || data != nil {
		t.Fatalf("cancelled EOF %q %v", data, err)
	}
	calls = 0
	left := RawLimit + 1
	r = readFunc(func(p []byte) (int, error) {
		calls++
		n := min(len(p), left)
		left -= n
		if n == 0 {
			t.Fatal("oversize waited for EOF")
		}
		return n, nil
	})
	if _, err := ReadBody(context.Background(), r, RawLimit); err == nil || left != 0 {
		t.Fatalf("oversize immediate: %v left=%d", err, left)
	}
}

func TestExactRawWireBodyAndFile(t *testing.T) {
	d := prepared(t, "example.Empty", messageFile("proto3", &descriptorpb.DescriptorProto{Name: proto.String("Empty")}))
	n := RawLimit - 6
	body := protowire.AppendBytes(protowire.AppendTag(nil, 100, protowire.BytesType), make([]byte, n))
	if len(body) != RawLimit {
		t.Fatal(len(body))
	}
	checkJSON(t, d, body, `{}`)
	if _, err := d.decode(context.Background(), append(body, 0)); err == nil {
		t.Fatal("decoder raw cap missing")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "raw.bin")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(context.Background(), path, RawLimit)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("exact file read %v", err)
	}
	if _, err := ReadFile(context.Background(), path, RawLimit-1); err == nil {
		t.Fatal("oversize file accepted")
	}
	if _, err := ReadFile(context.Background(), dir, RawLimit); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err := ReadFile(context.Background(), filepath.Join(dir, "missing"), RawLimit); err == nil {
		t.Fatal("missing accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFile(context.Background(), link, RawLimit); err != nil || len(got) != RawLimit {
		t.Fatal(err)
	}
}
