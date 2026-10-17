package textview

import (
	"context"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"
)

func TestDecodeWholeBody(t *testing.T) {
	for _, body := range []string{"", "\ufeffhello 世界\r\n\x00\t ", strings.Repeat("x", 10<<20)} {
		v, err := Decode(context.Background(), []byte(body))
		if err != nil || v.Kind != omap.KindStr || v.Str != body {
			t.Fatalf("length=%d error=%v", len(body), err)
		}
	}
	for _, body := range [][]byte{{0xff}, {0xc0, 0xaf}, {0xed, 0xa0, 0x80}, []byte(strings.Repeat("x", (10<<20)+1))} {
		if _, err := Decode(context.Background(), body); err == nil {
			t.Fatal("invalid body admitted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, nil); err != context.Canceled {
		t.Fatal(err)
	}
}
