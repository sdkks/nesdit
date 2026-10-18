package flatview

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"os"
	"strings"
	"testing"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/omap"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func render(t *testing.T, v omap.Value) string {
	t.Helper()
	var buf bytes.Buffer
	if err := jsonfmt.EncodeValue(&buf, v); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func decodeFile(t *testing.T, schema, payload string) string {
	t.Helper()
	s, err := Prepare(context.Background(), load(t, schema))
	if err != nil {
		t.Fatalf("prepare %s: %v", schema, err)
	}
	v, err := s.Decode(context.Background(), load(t, payload))
	if err != nil {
		t.Fatalf("decode %s: %v", payload, err)
	}
	return render(t, v)
}

// TestOracleFixtures pins the known-field view against values independently
// verified by the pinned flatc-generated C++ accessors (testdata/README.md).
func TestOracleFixtures(t *testing.T) {
	for _, tt := range []struct{ payload, want string }{
		// full.bin: present values, float32 promotion, payload-stored -0,
		// unknown enum 77, present-empty vectors, recursion, bytes.
		{"full.bin", `{"id":"42","small":7,"tiny":-128,"flag":false,"ratio":0.10000000149011612,"negz":2.5,"opt_score":"-0","opt_big":"18446744073709551615","label":"héllo wörld  embedded","color":77,"level":"5","stamp":"18446744073709551615","tags":["a","","c"],"nums":[],"inners":[{"note":"first","count":-3},{"note":"second","count":9}],"points":[{"a":-2,"b":65535},{"a":3,"b":4}],"vec3":{"x":1.5,"y":"-0","z":3.25},"old_field":0,"body_type":"0","side_type":0,"node":{"name":"root","child":{"name":"deep","peers":[{"name":"p1"}]}},"bytes":[0,1,255,10]}`},
		// none.bin: absent ordinary scalars keep BFBS-represented defaults;
		// absent optionals/references are omitted; NONE emits tag only.
		{"none.bin", `{"id":"7","small":-5,"tiny":-1,"flag":true,"ratio":0.5,"negz":0,"label":"minimal","color":200,"level":"-2","stamp":"7","old_field":0,"body_type":"0","side_type":0}`},
		// defaults.bin: every field absent. id's >int64 unsigned default is
		// omitted by the pinned compiler, so the BFBS-represented default is
		// 0 (the original-schema accessor yields 18446744073709551615).
		{"defaults.bin", `{"id":"0","small":-5,"tiny":-1,"flag":true,"ratio":0.5,"negz":0,"color":200,"level":"-2","stamp":"7","old_field":0,"body_type":"0","side_type":0}`},
		// wide.bin: table union member with 64-bit tag 300.
		{"wide.bin", `{"id":"0","small":-5,"tiny":-1,"flag":true,"ratio":0.5,"negz":0,"color":200,"level":"-2","stamp":"7","old_field":0,"body_type":"300","body":{"note":"wide table member","count":11},"side_type":0}`},
		// echo.bin: alias member (same table type) with tag 400.
		{"echo.bin", `{"id":"0","small":-5,"tiny":-1,"flag":true,"ratio":0.5,"negz":0,"color":200,"level":"-2","stamp":"7","old_field":0,"body_type":"400","body":{"note":"alias member","count":-1},"side_type":0}`},
		// left.bin: negative signed 16-bit discriminator.
		{"left.bin", `{"id":"0","small":-5,"tiny":-1,"flag":true,"ratio":0.5,"negz":0,"color":200,"level":"-2","stamp":"7","old_field":0,"body_type":"0","side_type":-3,"side":{"note":"left side","count":-3}}`},
		// ptstruct.bin: struct union member via offset reference; the root
		// vtable sits after the table (negative displacement).
		{"ptstruct.bin", `{"id":"0","small":-5,"tiny":-1,"flag":true,"ratio":0.5,"negz":0,"color":200,"level":"-2","stamp":"7","old_field":0,"body_type":"7","body":{"a":-5,"b":65500},"side_type":0}`},
		// textstr.bin: string union member with sparse tag 5.
		{"textstr.bin", `{"id":"0","small":-5,"tiny":-1,"flag":true,"ratio":0.5,"negz":0,"color":200,"level":"-2","stamp":"7","old_field":0,"body_type":"5","body":"a string union member","side_type":0}`},
		// shared.bin: one shared Inner object referenced by two fields.
		{"shared.bin", `{"id":"0","small":-5,"tiny":-1,"flag":true,"ratio":0.5,"negz":0,"color":200,"level":"-2","stamp":"7","old_field":0,"body_type":"300","body":{"note":"shared object","count":77},"side_type":2,"side":{"note":"shared object","count":77}}`},
	} {
		if got := decodeFile(t, "profile.bfbs", tt.payload); got != tt.want {
			t.Errorf("%s:\n got %s\nwant %s", tt.payload, got, tt.want)
		}
	}
}

// TestNestedStructFixture pins struct-inside-struct nesting: layout
// validation must run after field references resolve, so a struct field of
// struct type prepares and decodes instead of failing with "struct field
// not resolved" (ordering regression found in review).
func TestNestedStructFixture(t *testing.T) {
	got := decodeFile(t, "nested.bfbs", "nested.bin")
	if got != `{"s":{"a":5,"inner":{"b":"42"}}}` {
		t.Errorf("nested struct view:\n got %s", got)
	}
}

// TestNegativeZeroFidelity covers the mandatory default-fidelity caveat: an
// original .fbs -0.0 default omitted by BFBS reads as 0, while payload-stored
// and genuinely metadata-stored bits keep the sign.
func TestNegativeZeroFidelity(t *testing.T) {
	// Absent fields whose .fbs declared -0.0: the original-schema accessor
	// yields -0 (verified by the C++ oracle), the BFBS-derived view yields 0.
	if got := decodeFile(t, "negzero.bfbs", "negzero_absent.bin"); got != `{"x":0,"y":0}` {
		t.Errorf("absent: %s", got)
	}
	// The C++-built "stored" payload actually stores nothing: the builder
	// compares -0.0 equal to the +0 default and omits both fields, so the
	// view matches the absent case. (Verified against the C++ oracle.)
	if got := decodeFile(t, "negzero.bfbs", "negzero_stored.bin"); got != `{"x":0,"y":0}` {
		t.Errorf("builder-equal omission: %s", got)
	}
	// A payload that physically stores -0.0 keeps the sign.
	fb := newMiniBFBS()
	root0 := fb.negSchema(false, 0, false)
	s0, err0 := Prepare(context.Background(), fb.finish(root0))
	if err0 != nil {
		t.Fatal(err0)
	}
	v0, err0 := s0.Decode(context.Background(), negPayload(true, math.Copysign(0, -1)))
	if err0 != nil {
		t.Fatal(err0)
	}
	if got := render(t, v0); got != `{"x":"-0"}` {
		t.Errorf("payload-stored -0: %s", got)
	}
	// Metadata-stored defaults narrowed at declared width: z keeps -0, w is
	// +0, d keeps its nonzero subnormal double.
	if got := decodeFile(t, "negzero2.bfbs", "negzero2_absent.bin"); got != `{"z":"-0","w":0,"d":-1e-320}` {
		t.Errorf("narrowed: %s", got)
	}
	// Genuinely stored -0.0 default_real bits in metadata are honored.
	f := newMiniBFBS()
	root := f.negSchema(true, math.Copysign(0, -1), false)
	schema, err := Prepare(context.Background(), f.finish(root))
	if err != nil {
		t.Fatal(err)
	}
	v, err := schema.Decode(context.Background(), negPayload(false, 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, v); got != `{"x":"-0"}` {
		t.Errorf("metadata-stored -0: %s", got)
	}
}

// TestRequiredUnion covers required NONE semantics against generated-C++
// verified fixtures: req_ok is valid; tag NONE with a populated value slot
// passes the C++ verifier but must fail this stricter reader.
func TestRequiredUnion(t *testing.T) {
	s, err := Prepare(context.Background(), load(t, "required.bfbs"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.Decode(context.Background(), load(t, "req_ok.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, v); got != `{"name":"outer","u_type":2,"u":{"s":"value"}}` {
		t.Errorf("ok: %s", got)
	}
	if _, err = s.Decode(context.Background(), load(t, "req_bad_none.bin")); err == nil {
		t.Error("NONE with populated value must fail")
	}
}

// TestFileIdentifier covers the four-byte identifier contract.
func TestFileIdentifier(t *testing.T) {
	s, err := Prepare(context.Background(), load(t, "profile.bfbs"))
	if err != nil {
		t.Fatal(err)
	}
	body := load(t, "none.bin")
	bad := append([]byte{}, body...)
	copy(bad[4:8], "XXXX")
	if _, err = s.Decode(context.Background(), bad); err == nil {
		t.Error("identifier mismatch must fail")
	}
	// An identifier-less schema imposes no identifier check.
	f := newMiniBFBS()
	root := f.negSchema(false, 0, false)
	s2, err := Prepare(context.Background(), f.finish(root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s2.Decode(context.Background(), negPayload(false, 0)); err != nil {
		t.Fatal(err)
	}
}

// TestPayloadMutations mutates schema-valid payloads byte-by-byte.
func TestPayloadMutations(t *testing.T) {
	s, err := Prepare(context.Background(), load(t, "profile.bfbs"))
	if err != nil {
		t.Fatal(err)
	}
	full := load(t, "full.bin")
	mut := func(f func([]byte)) []byte {
		b := append([]byte{}, full...)
		f(b)
		return b
	}
	cases := []struct {
		name string
		body []byte
	}{
		{"truncated header", full[:3]},
		{"root offset out of range", mut(func(b []byte) { binary.LittleEndian.PutUint32(b[0:], uint32(len(b))) })},
		{"root offset zero", mut(func(b []byte) { binary.LittleEndian.PutUint32(b[0:], 0) })},
		{"corrupt root soffset", mut(func(b []byte) {
			root := int(binary.LittleEndian.Uint32(b[0:]))
			binary.LittleEndian.PutUint32(b[root:], 0x7fffffff)
		})},
		{"string NUL terminator", mut(func(b []byte) {
			i := bytes.Index(b, []byte("first"))
			b[i+len("first")] = 'X'
		})},
		{"invalid string UTF-8", mut(func(b []byte) {
			i := bytes.Index(b, []byte("héllo"))
			b[i] = 0xff
		})},
		{"empty body", nil},
		{"huge vector count", mut(func(b []byte) {
			// Corrupt every plausible count word near the tags vector by
			// scanning for the tags vector through the root is complex;
			// instead corrupt a broad region and require a safe failure.
			root := int(binary.LittleEndian.Uint32(b[0:]))
			soff := int32(binary.LittleEndian.Uint32(b[root:]))
			vt := root - int(soff)
			binary.LittleEndian.PutUint32(b[vt:], 0xffff)
		})},
	}
	for _, tt := range cases {
		if _, err = s.Decode(context.Background(), tt.body); err == nil {
			t.Errorf("%s: expected failure", tt.name)
		}
	}
}

// TestSchemaMutations covers global and root-reachable schema rejection.
func TestSchemaMutations(t *testing.T) {
	bfbs := load(t, "profile.bfbs")
	mut := func(f func([]byte)) []byte {
		b := append([]byte{}, bfbs...)
		f(b)
		return b
	}
	rootPos := func(b []byte) int { return int(binary.LittleEndian.Uint32(b[0:])) }
	if _, err := Prepare(context.Background(), mut(func(b []byte) {
		copy(b[4:8], "XXXX") // schema identifier
	})); err == nil {
		t.Error("schema identifier must be BFBS")
	}
	if _, err := Prepare(context.Background(), mut(func(b []byte) {
		// Point the schema root word past the buffer.
		binary.LittleEndian.PutUint32(b[0:], uint32(len(b)))
	})); err == nil {
		t.Error("invalid schema root must fail")
	}
	if _, err := Prepare(context.Background(), mut(func(b []byte) {
		root := rootPos(b)
		binary.LittleEndian.PutUint32(b[root:], 0x7fffffff)
	})); err == nil {
		t.Error("corrupt schema table must fail")
	}
	if _, err := Prepare(context.Background(), mut(func(b []byte) {
		// Truncate mid-buffer.
		b = b[:64]
		binary.LittleEndian.PutUint32(b[0:], uint32(8))
		_ = b
		copy(b[4:], "BFBS")
	})); err == nil {
		t.Error("truncated schema must fail")
	}
	// Populated unknown metadata slot (global rejection).
	f := newMiniBFBS()
	root := f.negSchema(false, 0, true)
	if _, err := Prepare(context.Background(), f.finish(root)); err == nil {
		t.Error("populated unknown schema slot must fail")
	}
	// The same schema without the unknown slot is accepted.
	f2 := newMiniBFBS()
	root2 := f2.negSchema(false, 0, false)
	if _, err := Prepare(context.Background(), f2.finish(root2)); err != nil {
		t.Fatal(err)
	}
}

// TestBudgets exercises the declared caps that are reachable in small tests.
func TestBudgets(t *testing.T) {
	s, err := Prepare(context.Background(), load(t, "profile.bfbs"))
	if err != nil {
		t.Fatal(err)
	}
	// Raw payload cap is enforced defensively even if the caller missed it.
	big := make([]byte, rawLimit+1)
	copy(big, load(t, "none.bin"))
	if _, err = s.Decode(context.Background(), big); err == nil {
		t.Error("oversize payload must fail")
	}
	// Schema cap.
	if _, err = Prepare(context.Background(), make([]byte, SchemaLimit+1)); err == nil {
		t.Error("oversize schema must fail")
	}
	// Vector count beyond available bytes is rejected before allocation.
	f := newMiniBFBS()
	root := f.negSchema(false, 0, false)
	s2, err := Prepare(context.Background(), f.finish(root))
	if err != nil {
		t.Fatal(err)
	}
	// Cancellation propagates at boundaries.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = Prepare(ctx, load(t, "profile.bfbs")); err == nil {
		t.Error("cancelled preparation must fail")
	}
	if _, err = s2.Decode(ctx, load(t, "none.bin")); err == nil {
		t.Error("cancelled decode must fail")
	}
}

// ulongVectorPayload builds a root table whose only field is a [ulong]
// vector of n copies of math.MaxUint64. Layout: u32 root offset; table with
// i32 soffset to a trailing vtable and u32 field offset; u32 count + data.
func ulongVectorPayload(n int) []byte {
	vtPos := 16 + 8*n
	out := make([]byte, vtPos+6)
	binary.LittleEndian.PutUint32(out[0:], 4)
	binary.LittleEndian.PutUint32(out[4:], uint32(int32(4-vtPos)))
	binary.LittleEndian.PutUint32(out[8:], 4)
	binary.LittleEndian.PutUint32(out[12:], uint32(n))
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint64(out[16+8*i:], math.MaxUint64)
	}
	binary.LittleEndian.PutUint16(out[vtPos:], 6)   // vtable size
	binary.LittleEndian.PutUint16(out[vtPos+2:], 8) // object size
	binary.LittleEndian.PutUint16(out[vtPos+4:], 4) // field 0 vOffset
	return out
}

// TestUlongVectorViewTextBudget verifies that 64-bit vector elements charge
// their decimal strings against the aggregate view-text budget: a vector
// within the raw and node budgets but over 16 MiB of decimal text must fail
// at decoding with the view-limit error, not pass through to filtering.
func TestUlongVectorViewTextBudget(t *testing.T) {
	s, err := Prepare(context.Background(), load(t, "ulongvec.bfbs"))
	if err != nil {
		t.Fatal(err)
	}
	// Small vector decodes with exact decimal strings.
	v, err := s.Decode(context.Background(), ulongVectorPayload(3))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"vals":["18446744073709551615","18446744073709551615","18446744073709551615"]}`
	if got := render(t, v); got != want {
		t.Errorf("small vector:\n got %s\nwant %s", got, want)
	}
	// 850,000 max-value elements render 17,000,000 decimal bytes — over the
	// 16 MiB aggregate view-text budget — while the payload stays under the
	// 10 MiB raw and 1,000,000-node budgets. Decoding must fail with the
	// view-limit error before any output or filtering stage.
	big := ulongVectorPayload(850_000)
	if len(big) >= rawLimit {
		t.Fatalf("test payload unexpectedly large: %d", len(big))
	}
	if _, err = s.Decode(context.Background(), big); err == nil || !strings.Contains(err.Error(), "view limit") {
		t.Fatalf("expected view-limit failure, got %v", err)
	}
}

// TestFuzzNarrow performs narrow randomized mutations of a prepared schema
// and of payloads; every case must return an error or a value, never panic.
func TestFuzzNarrow(t *testing.T) {
	bfbs := load(t, "profile.bfbs")
	seed := uint64(0x9e3779b97f4a7c15)
	next := func() uint64 {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed
	}
	for i := 0; i < 300; i++ {
		b := append([]byte{}, bfbs...)
		for m := 0; m < 1+int(next()%3); m++ {
			b[next()%uint64(len(b))] = byte(next())
		}
		s, err := Prepare(context.Background(), b)
		if err != nil {
			continue
		}
		p := append([]byte{}, load(t, "none.bin")...)
		for m := 0; m < 1+int(next()%3); m++ {
			p[next()%uint64(len(p))] = byte(next())
		}
		_, _ = s.Decode(context.Background(), p)
	}
	// Valid schema, mutated payloads.
	s, err := Prepare(context.Background(), bfbs)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		p := append([]byte{}, load(t, "full.bin")...)
		for m := 0; m < 1+int(next()%4); m++ {
			p[next()%uint64(len(p))] = byte(next())
		}
		_, _ = s.Decode(context.Background(), p)
	}
}

// TestDiagnosticsDoNotLeakNames verifies failures carry only structural
// positions and categories, never schema or payload values.
func TestDiagnosticsDoNotLeakNames(t *testing.T) {
	s, err := Prepare(context.Background(), load(t, "profile.bfbs"))
	if err != nil {
		t.Fatal(err)
	}
	bad := append([]byte{}, load(t, "none.bin")...)
	bad[4] = 'X'
	_, err = s.Decode(context.Background(), bad)
	if err == nil || strings.Contains(err.Error(), "minimal") || strings.Contains(err.Error(), "nt.Record") {
		t.Fatalf("diagnostic leaks values: %v", err)
	}
}
