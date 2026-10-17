// Package textview exposes one complete UTF-8 body as a JSON string.
package textview

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/sdkks/nesdit/internal/omap"
)

// Decode preserves every valid UTF-8 byte, including BOM, NUL and whitespace.
// One root string under the raw cap also fits the decoded and JSON-view budgets.
func Decode(ctx context.Context, body []byte) (omap.Value, error) {
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	if len(body) > 10<<20 {
		return omap.Value{}, errors.New("decode: text body exceeds 10 MiB")
	}
	if !utf8.Valid(body) {
		return omap.Value{}, errors.New("decode: body is not valid UTF-8")
	}
	if err := ctx.Err(); err != nil {
		return omap.Value{}, err
	}
	return omap.StrValue(string(body)), nil
}
