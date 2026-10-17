// Package protobufview prepares local schemas and bounded, read-only ProtoJSON views.
// Registries are private to one preparation; no ambient or network lookup is used.
package protobufview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	// RawLimit is the inclusive byte cap for one raw message and aggregate Any wire.
	RawLimit        = 10 << 20
	descriptorLimit = 4 << 20
	depthLimit      = 100
	nodeLimit       = 1_000_000
	textLimit       = 16 << 20
	serializedLimit = 16 << 20
)

// ReadBody reads one complete body synchronously. The extra byte distinguishes
// exact-cap EOF from oversize without waiting for a producer to close afterward.
// A borrowed reader is never closed, deadlined, or moved to a worker goroutine.
func ReadBody(ctx context.Context, r io.Reader, limit int) ([]byte, error) {
	var data []byte
	var scratch [32 << 10]byte
	idle := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		size := min(len(scratch), limit+1-len(data))
		n, err := r.Read(scratch[:size])
		if n < 0 || n > size {
			return nil, errors.New("read: invalid reader result")
		}
		if n > 0 {
			data = append(data, scratch[:n]...)
			idle = 0
		} else {
			idle++
		}
		if len(data) > limit {
			return nil, fmt.Errorf("read: input exceeds %d byte limit", limit)
		}
		// Preserve real read failures even if cancellation arrived concurrently.
		if err != nil && !errors.Is(err, io.EOF) {
			if errors.Is(err, context.Canceled) {
				return nil, context.Canceled
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, context.DeadlineExceeded
			}
			return nil, errors.New("read: input failed before complete EOF")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if errors.Is(err, io.EOF) {
			return data, nil
		}
		if idle >= 100 {
			return nil, io.ErrNoProgress
		}
	}
}

// ReadFile checks the path and opened handle, then performs bounded chunk reads.
// Symlinks resolving to regular files are allowed; this is not a filesystem sandbox.
func ReadFile(ctx context.Context, path string, limit int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	invalid := fmt.Errorf("read: requires a readable regular file of at most %d bytes", limit)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, invalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, invalid
	}
	defer func() { _ = f.Close() }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, invalid
	}
	return ReadBody(ctx, f, limit)
}
