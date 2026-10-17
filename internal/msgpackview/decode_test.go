package msgpackview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/vmihailenco/msgpack/v5"

	jsonformat "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/omap"
)

type fixture struct {
	Name   string          `json:"name"`
	Hex    string          `json:"hex"`
	View   json.RawMessage `json:"view"`
	SHA256 string          `json:"sha256"`
}

func corpus(t testing.TB) []fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []fixture
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func encoded(t testing.TB, v omap.Value) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := jsonformat.EncodeValue(&out, v); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestIndependentCorpus(t *testing.T) {
	for _, tt := range corpus(t) {
		t.Run(tt.Name, func(t *testing.T) {
			body, err := hex.DecodeString(tt.Hex)
			if err != nil {
				t.Fatal(err)
			}
			before := sha256.Sum256(body)
			if hex.EncodeToString(before[:]) != tt.SHA256 {
				t.Fatal("fixture hash mismatch")
			}
			v, err := Decode(context.Background(), body)
			if err != nil {
				t.Fatal(err)
			}
			var expected bytes.Buffer
			if err = json.Compact(&expected, tt.View); err != nil {
				t.Fatal(err)
			}
			if actual := encoded(t, v); !bytes.Equal(actual, expected.Bytes()) {
				t.Fatalf("got %s want %s", actual, expected.Bytes())
			}
			if sha256.Sum256(body) != before {
				t.Fatal("input changed")
			}
			// No suffix, even whitespace or another valid value, is accepted.
			for _, tail := range []byte{0, 0xc0, '\n', 0xc1} {
				if _, err := Decode(context.Background(), append(bytes.Clone(body), tail)); err == nil {
					t.Fatal("accepted trailing byte")
				}
			}
			for end := 0; end < len(body); end++ {
				if _, err := Decode(context.Background(), body[:end]); err == nil {
					t.Fatalf("accepted truncation at %d", end)
				}
			}
		})
	}
}

func TestMalformed(t *testing.T) {
	for _, wire := range []string{"", "c1", "dbffffffff", "c6ffffffff", "ddffffffff", "dfffffffff", "c9ffffffff00", "c701ff00", "d4ff00", "d8ff00000000000000000000000000000000", "c70cff3b9aca000000000000000000", "d7ffee6b280000000000"} {
		b, err := hex.DecodeString(wire)
		if err != nil {
			t.Fatal(err)
		}
		v, err := Decode(context.Background(), b)
		if err == nil || v.Kind != omap.KindNull {
			t.Fatalf("accepted malformed %s", wire)
		}
	}
}

func arrayOf(n int, elem byte) []byte {
	body := make([]byte, 5+n)
	body[0] = 0xdd
	binary.BigEndian.PutUint32(body[1:5], uint32(n))
	for i := 5; i < len(body); i++ {
		body[i] = elem
	}
	return body
}

func TestBudgets(t *testing.T) {
	// Semantic and view nodes coincide for nil arrays; the root counts too.
	for _, n := range []int{nodeLimit - 1, nodeLimit} {
		_, err := Decode(context.Background(), arrayOf(n, 0xc0))
		if (err == nil) != (n == nodeLimit-1) {
			t.Fatalf("nil nodes %d: %v", n, err)
		}
	}
	// Each integer adds a view object and a decimal string, not one node.
	for _, n := range []int{(nodeLimit - 1) / 2, (nodeLimit-1)/2 + 1} {
		_, err := Decode(context.Background(), arrayOf(n, 1))
		if (err == nil) != (n == (nodeLimit-1)/2) {
			t.Fatalf("integer view nodes %d: %v", n, err)
		}
	}
	for _, depth := range []int{depthLimit, depthLimit + 1} {
		b := append(bytes.Repeat([]byte{0x91}, depth), 0xc0)
		_, err := Decode(context.Background(), b)
		if (err == nil) != (depth == depthLimit) {
			t.Fatalf("depth %d: %v", depth, err)
		}
	}
	// A map adds wrapper/entry/pair depths: 33 maps + null = depth 99.
	for _, depth := range []int{33, 34} {
		b := []byte{0xc0}
		for range depth {
			b = append([]byte{0x81, 0xc0}, b...)
		}
		_, err := Decode(context.Background(), b)
		if (err == nil) != (depth == 33) {
			t.Fatalf("map depth %d: %v", depth, err)
		}
	}
	// Empty maps still materialize their entries array at child depth.
	_, err := Decode(context.Background(), append(bytes.Repeat([]byte{0x91}, 100), 0x80))
	if !errors.Is(err, errView) {
		t.Fatal(err)
	}
	// Raw input cap, including header, is inclusive.
	body := append([]byte{0xdb, 0, 0, 0, 0}, bytes.Repeat([]byte{'a'}, rawLimit-5)...)
	binary.BigEndian.PutUint32(body[1:5], uint32(rawLimit-5))
	if _, err = Decode(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	if _, err = Decode(context.Background(), append(body, 'a')); err == nil {
		t.Fatal("raw cap")
	}
}

func walkerForTest(ctx context.Context, body []byte, r *bytes.Reader) walker {
	return walker{ctx: ctx, body: body, reader: r, decoder: msgpack.NewDecoder(r)}
}

func TestBudgetAccountingAndPreallocation(t *testing.T) {
	// Text caps exceed the raw cap for this decoder; direct counter boundaries
	// exercise otherwise unreachable semantic-byte failures independently.
	for _, b := range []budget{{nodes: nodeLimit - 1}, {text: textLimit - 1}} {
		if !b.add(100, 1, 1) || b.add(100, 1, 1) {
			t.Fatal("inclusive budget", b)
		}
	}
	for _, tt := range []struct {
		wire                                             string
		semanticNodes, semanticText, viewNodes, viewText int
	}{
		{"c0", 1, 0, 1, 0}, {"01", 1, 0, 2, 5}, {"a161", 1, 1, 1, 1},
		{"c401ff", 1, 1, 2, 8}, {"a1ff", 1, 1, 2, 10},
		{"81c0c0", 3, 0, 5, 4}, {"80", 1, 0, 2, 4},
		{"d400ff", 1, 1, 4, 8}, {"d6ff00000000", 1, 4, 4, 6},
	} {
		body, _ := hex.DecodeString(tt.wire)
		r := bytes.NewReader(body)
		w := walkerForTest(context.Background(), body, r)
		if _, err := w.value(0, 0); err != nil {
			t.Fatal(err)
		}
		if w.decoded != (budget{tt.semanticNodes, tt.semanticText}) || w.view != (budget{tt.viewNodes, tt.viewText}) {
			t.Fatalf("%s decoded=%v view=%v", tt.wire, w.decoded, w.view)
		}
	}
	for _, tt := range []struct {
		wire          string
		decoded, view budget
		want          error
	}{
		{"c401ff", budget{text: textLimit}, budget{}, errSemantic},
		{"c401ff", budget{}, budget{text: textLimit - 7}, errView},
		{"a1ff", budget{}, budget{text: textLimit - 9}, errView},
		{"d400ff", budget{}, budget{text: textLimit - 7}, errView},
		{"a161", budget{}, budget{text: textLimit}, errView},
		{"91c0", budget{nodes: nodeLimit - 1}, budget{}, errSemantic},
		{"81c0c0", budget{}, budget{nodes: nodeLimit - 4}, errView},
	} {
		body, _ := hex.DecodeString(tt.wire)
		r := bytes.NewReader(body)
		w := walkerForTest(context.Background(), body, r)
		w.decoded, w.view = tt.decoded, tt.view
		if _, err := w.value(0, 0); !errors.Is(err, tt.want) {
			t.Fatalf("%s: %v", tt.wire, err)
		}
		if r.Len() == 0 {
			t.Fatal("consumed payload before budget admission")
		}
	}
}

func TestNestedDeclarationsReserveOutstandingSiblings(t *testing.T) {
	// Each header fits remaining wire bytes on its own. The second declaration
	// must not allocate another large slice against the first one's reservation.
	for _, mapping := range []bool{false, true} {
		n := nodeLimit/2 + 1
		outer := arrayOf(n, 0xc0)
		inner := arrayOf(n, 0xc0)[:5]
		want := errSemantic
		if mapping {
			// A map has three view nodes per pair plus its entries array, so
			// this child fits semantic slots but not the parent's view slots.
			n = 200_000
			outer = arrayOf(n, 0xc0)
			inner = []byte{0xdf, 0, 0, 0, 0}
			binary.BigEndian.PutUint32(inner[1:], 300_000)
			outer = append(outer, bytes.Repeat([]byte{0xc0}, 500_000)...)
			want = errView
		}
		body := append(bytes.Clone(outer[:5]), inner...)
		body = append(body, outer[5:]...)
		r := bytes.NewReader(body)
		w := walkerForTest(context.Background(), body, r)
		if _, err := w.value(0, 0); !errors.Is(err, want) {
			t.Fatalf("map=%v: %v", mapping, err)
		}
		if len(body)-r.Len() != 10 {
			t.Fatal("did not reject at nested header")
		}
		if w.view.nodes != n+1 && w.view.nodes != n+2 {
			t.Fatal("outer slots not reserved", w.view)
		}
	}
}

func TestCancellationAndSafeErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, []byte{0xc0}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Cancellation during traversal is observed at the next bounded boundary.
	ctx2 := &cancelAfter{Context: context.Background(), remaining: 4}
	if _, err := Decode(ctx2, []byte{0x95, 0, 0, 0, 0, 0}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	secret := []byte("private-payload")
	body := append([]byte{0x81, byte(0xa0 + len(secret))}, secret...)
	body = append(body, 0xc1)
	if _, err := Decode(context.Background(), body); err == nil || strings.Contains(err.Error(), string(secret)) {
		t.Fatal(err)
	}
}

type cancelAfter struct {
	context.Context
	remaining int
}

func (c *cancelAfter) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func FuzzDecode(f *testing.F) {
	for _, tt := range corpus(f) {
		b, _ := hex.DecodeString(tt.Hex)
		f.Add(b)
	}
	for _, b := range [][]byte{{0xc1}, {0xdd, 255, 255, 255, 255}, {0xdb, 255, 255, 255, 255}, {0xc7, 1, 255, 0}} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 64<<10 {
			return
		}
		before := sha256.Sum256(body)
		value, err := Decode(context.Background(), body)
		if err == nil {
			encoded(t, value)
		}
		if sha256.Sum256(body) != before {
			t.Fatal("input changed")
		}
	})
}
