// Test_Bridge_BigIntRHS_RoundTrips is an NFR-4 compliance probe —
// narrowing the corpus's existing pass-through-only big-int coverage
// into queries that actually touch the big integer through gojq. Three
// subcases, each mutating .big or an adjacent key, are routed through
// query.Run and the resulting JSON bytes are checked for exact big-int
// preservation (no silent 2^53 rounding, no scientific-notation drift).
//
// Outcome (2026-04-26, 64-bit linux/darwin): all three sub-cases pass.
// NFR-4 is now broadly proven for assignment-subset queries that pass a
// big integer through gojq, including:
//   - path RHS (`.copy = .big`) — gojq threads json.Number through
//     unchanged.
//   - literal RHS (`.n = 9007199254740993`) — gojq's parseNumber on a
//     64-bit platform uses int (int64), preserving precision up to
//     math.MaxInt64. Values > math.MaxInt64 would still lose precision
//     through the lexer; use `--argjson` (planned for STORY-0003) to
//     inject them as *big.Int.
//   - arithmetic RHS (`.big + 0`) — gojq promotes int64 → *big.Int as
//     needed for arithmetic, preserving the value across the round trip.
//
// No DR-007 was needed. If a future 32-bit target is added, the literal
// RHS case may regress (gojq's parseNumber uses platform int). Track as
// a follow-up at that point.
package query_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/query"
)

func Test_Bridge_BigIntRHS_RoundTrips(t *testing.T) {
	t.Parallel()

	const bigLit = "9007199254740993" // 2^53 + 1 — first integer JS/float64 cannot represent.

	cases := []struct {
		name        string
		source      []byte
		query       string
		wantContain string
		wantAbsent  []string // byte sequences that MUST NOT appear in the output.
	}{
		{
			name:        "path_rhs_copy_big_to_new_key",
			source:      []byte(`{"big":9007199254740993}`),
			query:       ".copy = .big",
			wantContain: bigLit,
			wantAbsent:  []string{"9007199254740992", "9.007"},
		},
		{
			name:        "literal_rhs_assign_big_literal",
			source:      []byte(`{"a":1}`),
			query:       ".n = 9007199254740993",
			wantContain: bigLit,
			wantAbsent:  []string{"9007199254740992", "9.007"},
		},
		{
			name:        "arith_rhs_big_plus_zero",
			source:      []byte(`{"big":9007199254740993}`),
			query:       ".big = .big + 0",
			wantContain: bigLit,
			wantAbsent:  []string{"9007199254740992", "9.007"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc, err := jsonfmt.Decode(bytes.NewReader(tc.source))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			out, err := query.Run(context.Background(), doc, tc.query)
			if err != nil {
				t.Fatalf("query.Run: %v", err)
			}
			var buf bytes.Buffer
			if err := jsonfmt.Encode(&buf, out); err != nil {
				t.Fatalf("encode: %v", err)
			}
			got := buf.String()
			if !strings.Contains(got, tc.wantContain) {
				t.Fatalf("NFR-4 big-int precision lost:\nquery: %s\nwant substring: %s\ngot: %s",
					tc.query, tc.wantContain, got)
			}
			for _, forbidden := range tc.wantAbsent {
				if strings.Contains(got, forbidden) {
					t.Fatalf("NFR-4 big-int precision drift — output contains %q (suggests float round-trip):\nquery: %s\ngot: %s",
						forbidden, tc.query, got)
				}
			}
		})
	}
}
