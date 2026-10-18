package run

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func flatbuffersFiles(t *testing.T) (payload, schema string) {
	t.Helper()
	dir := t.TempDir()
	payload = filepath.Join(dir, "event,payload.bin")
	schema = filepath.Join(dir, "schema one.bfbs")
	for name, src := range map[string]string{
		payload: "../flatview/testdata/full.bin",
		schema:  "../flatview/testdata/profile.bfbs",
	} {
		data, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return payload, schema
}

func TestFlatbuffersLocalInspection(t *testing.T) {
	payload, schema := flatbuffersFiles(t)
	body, err := os.ReadFile(payload)
	if err != nil {
		t.Fatal(err)
	}
	schemaBytes, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"42","small":7,"tiny":-128,"flag":false,"ratio":0.10000000149011612,"negz":2.5,"opt_score":"-0","opt_big":"18446744073709551615","label":"héllo wörld  embedded","color":77,"level":"5","stamp":"18446744073709551615","tags":["a","","c"],"nums":[],"inners":[{"note":"first","count":-3},{"note":"second","count":9}],"points":[{"a":-2,"b":65535},{"a":3,"b":4}],"vec3":{"x":1.5,"y":"-0","z":3.25},"old_field":0,"body_type":"0","side_type":0,"node":{"name":"root","child":{"name":"deep","peers":[{"name":"p1"}]}},"bytes":[0,1,255,10]}` + "\n"
	args := []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "--filter", "true"}
	code, out, logs := textExecute(args, textNoRead{t})
	if code != 0 || out != want || logs != "" {
		t.Fatalf("file: %d %q %s", code, out, logs)
	}
	// Explicit --to json is accepted; queries see the same known fields.
	code, out, logs = textExecute(append(args, "--to", "json", "--project", `{i: .id, n: .label}`), textNoRead{t})
	if code != 0 || out != `{"i":"42","n":"héllo wörld  embedded"}`+"\n" || logs != "" {
		t.Fatalf("project: %d %q %s", code, out, logs)
	}
	// A false filter succeeds with no output even for binary payloads.
	code, out, logs = textExecute(append(args[:len(args):len(args)], "--filter", "false"), textNoRead{t})
	if code != 0 || out != "" || logs != "" {
		t.Fatalf("filter-false: %d %q %s", code, out, logs)
	}
	// Payload via explicit stdin with a file schema.
	code, out, logs = textExecute([]string{"--input-stdin", "--input-codec", "flatbuffers", "--bfbs", schema}, bytes.NewReader(body))
	if code != 0 || out != want || logs != "" {
		t.Fatalf("stdin: %d %q %s", code, out, logs)
	}
	// Neither file is modified.
	afterPayload, err := os.ReadFile(payload)
	if err != nil || sha256.Sum256(afterPayload) != sha256.Sum256(body) {
		t.Fatal("payload changed", err)
	}
	afterSchema, err := os.ReadFile(schema)
	if err != nil || sha256.Sum256(afterSchema) != sha256.Sum256(schemaBytes) {
		t.Fatal("schema changed", err)
	}
	// Diagnostics never carry schema or payload values.
	corrupt := append([]byte{}, body...)
	corrupt[4] = 'X'
	code, out, logs = textExecute([]string{"--input-stdin", "--input-codec", "flatbuffers", "--bfbs", schema, "--filter", "false"}, bytes.NewReader(corrupt))
	if code != 1 || out != "" || strings.Contains(logs, "minimal") || strings.Contains(logs, "héllo") {
		t.Fatalf("mismatch: %d %q %s", code, out, logs)
	}
}

func TestFlatbuffersFlagMatrix(t *testing.T) {
	payload, schema := flatbuffersFiles(t)
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"missing bfbs", []string{"--input-file", payload, "--input-codec", "flatbuffers"}},
		{"repeated bfbs", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "--bfbs", schema}},
		{"empty bfbs", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", " "}},
		{"dash bfbs", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", "-"}},
		{"same file as payload", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", payload}},
		{"bfbs with text codec", []string{"--input-file", payload, "--input-codec", "text", "--bfbs", schema}},
		{"bfbs without codec", []string{"--input-file", payload, "--bfbs", schema}},
		{"binary target", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "--to", "msgpack"}},
		{"positional input", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "extra"}},
		{"orphan flag", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "--pretty"}},
		{"native selector", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "--nats", "logs.>"}},
		{"protobuf selector", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "--message-type", "x.Y"}},
		{"limit override", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "--max-bytes", "100"}},
		{"legacy flag at default", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", schema, "--format=json"}},
		{"invalid codec", []string{"--input-file", payload, "--input-codec", "protobuf"}},
		{"directory bfbs", []string{"--input-file", payload, "--input-codec", "flatbuffers", "--bfbs", filepath.Dir(schema)}},
	} {
		code, out, logs := textExecute(tt.args, textNoRead{t})
		if code != 1 || out != "" || logs == "" {
			t.Errorf("%s: code=%d out=%q logs=%q", tt.name, code, out, logs)
		}
	}
}

// TestFlatbuffersPreparationOrder proves the schema is prepared and the
// query compiled before any payload byte is acquired.
func TestFlatbuffersPreparationOrder(t *testing.T) {
	_, schema := flatbuffersFiles(t)
	corrupt := filepath.Join(t.TempDir(), "corrupt.bfbs")
	if err := os.WriteFile(corrupt, []byte("not a bfbs schema at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--input-stdin", "--input-codec", "flatbuffers", "--bfbs", corrupt},
		{"--input-stdin", "--input-codec", "flatbuffers", "--bfbs", schema, "--project", "invalid["},
	} {
		code, out, logs := textExecute(args, textNoRead{t})
		if code != 1 || out != "" || logs == "" {
			t.Fatalf("preparation: %d %q %s", code, out, logs)
		}
	}
	// A valid schema with a payload-side failure still reads nothing when
	// the schema itself is invalid; the reader below counts reads.
	read := &countingReader{data: make([]byte, 32)}
	code, _, _ := textExecute([]string{"--input-stdin", "--input-codec", "flatbuffers", "--bfbs", corrupt}, read)
	if code != 1 || read.reads != 0 {
		t.Fatalf("schema failure must precede payload read: reads=%d", read.reads)
	}
}

type countingReader struct {
	data  []byte
	reads int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	if len(r.data) == 0 {
		return 0, os.ErrClosed
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// TestFlatbuffersSchemaFailures covers actionable safe failures for schema
// acquisition and preparation.
func TestFlatbuffersSchemaFailures(t *testing.T) {
	payload, schema := flatbuffersFiles(t)
	missing := filepath.Join(t.TempDir(), "missing.bfbs")
	base := []string{"--input-file", payload, "--input-codec", "flatbuffers"}
	code, out, logs := textExecute(append(base, "--bfbs", missing), textNoRead{t})
	if code != 1 || out != "" || !strings.Contains(logs, "schema") {
		t.Fatalf("missing schema: %d %q %s", code, out, logs)
	}
	// Size-prefixed BFBS (a uint32 length prefix) is not ordinary BFBS.
	b, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	prefixed := make([]byte, 4+len(b))
	prefixed[3] = byte(len(b) >> 24)
	prefixed[2] = byte(len(b) >> 16)
	prefixed[1] = byte(len(b) >> 8)
	prefixed[0] = byte(len(b))
	copy(prefixed[4:], b)
	prefixedPath := filepath.Join(t.TempDir(), "prefixed.bfbs")
	if err = os.WriteFile(prefixedPath, prefixed, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, logs = textExecute(append(base, "--bfbs", prefixedPath), textNoRead{t})
	if code != 1 || out != "" || !strings.Contains(logs, "schema") {
		t.Fatalf("prefixed schema: %d %q %s", code, out, logs)
	}
	// Oversize schema fails during acquisition.
	oversize := filepath.Join(t.TempDir(), "oversize.bfbs")
	if err = os.WriteFile(oversize, make([]byte, 4<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, logs = textExecute(append(base, "--bfbs", oversize), textNoRead{t})
	if code != 1 || out != "" || !strings.Contains(logs, "schema") {
		t.Fatalf("oversize schema: %d %q %s", code, out, logs)
	}
}
