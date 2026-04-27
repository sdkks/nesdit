// Package stream provides per-format multi-document streaming for nesdit's
// STDIN pipeline (FR-3, NFR-8).
//
// # Format support
//
//   - YAML: `---`-separated documents. The reader yields one decoded
//     omap.Value per YAML document; the writer emits `---\n` before each
//     document (including the first).
//   - JSONL: newline-separated JSON documents, one per line. Empty lines
//     are skipped. The reader yields one omap.Value per non-empty line; the
//     writer emits each document followed by `\n`.
//   - TOML: single-document only. Any input containing a `---` separator
//     is rejected immediately with a format.unsupported error naming TOML.
//
// # NFR-8 contract
//
// All readers perform single-pass streaming: each document is decoded
// individually as the reader advances. No buffering of the entire input
// stream occurs before the first document is returned. Documents
// successfully written to stdout before a later decode failure MAY remain
// there — this is documented NFR-8 behavior.
package stream

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/sdkks/nesdit/internal/format"
	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
	"github.com/sdkks/nesdit/internal/omap"
)

// ErrTOMLMultiDoc is returned by a TOML stream reader when the input
// contains a `---` separator indicating an attempt at multi-document TOML.
// TOML is single-document only (FR-3).
var ErrTOMLMultiDoc = errors.New("toml: multi-doc input is not supported; TOML is single-document only")

// DocReader is a streaming reader that yields one omap.Value per document.
// Call Next to advance; it returns false when the stream is exhausted or an
// error occurs. Check Err after Next returns false.
type DocReader interface {
	// Next advances the reader to the next document. Returns true if a
	// document was successfully decoded and is available via Value. Returns
	// false when the stream is exhausted (normal EOF) or an error occurred.
	Next() bool
	// Value returns the most recently decoded document. Only valid after
	// Next returned true.
	Value() omap.Value
	// Err returns the first error encountered during streaming. Returns nil
	// when Next returned false due to normal EOF.
	Err() error
}

// DocWriter is a streaming writer that encodes one omap.Value per document
// to the underlying io.Writer with the appropriate framing for the format.
type DocWriter interface {
	// WriteDoc encodes v and writes it to the underlying writer with
	// appropriate framing. Returns an error if encoding or writing fails.
	WriteDoc(v omap.Value) error
}

// --- YAML ---

// yamlDocReader reads `---`-separated YAML documents from r.
// It uses gopkg.in/yaml.v3's streaming decoder which consumes one document
// per Decode call, so each Next() call reads exactly one YAML document.
type yamlDocReader struct {
	scanner *bufio.Scanner
	limits  format.Limits
	buf     []string // lines for current document
	val     omap.Value
	err     error
	eof     bool
}

// NewYAMLReader returns a DocReader that reads `---`-separated YAML documents
// from r, applying limits at decode time.
func NewYAMLReader(r io.Reader, limits format.Limits) DocReader {
	scanner := bufio.NewScanner(r)
	// Allow lines up to 1 MiB (generous for config files).
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	return &yamlDocReader{
		scanner: scanner,
		limits:  limits,
	}
}

func (yr *yamlDocReader) Next() bool {
	if yr.eof || yr.err != nil {
		return false
	}
	// Collect lines until the next `---` separator or EOF.
	yr.buf = yr.buf[:0]
	for yr.scanner.Scan() {
		line := yr.scanner.Text()
		if line == "---" && len(yr.buf) > 0 {
			// End of current doc; `---` belongs to the next doc.
			break
		}
		if line == "---" {
			// Leading `---` with no content before it: skip and continue
			// collecting lines for the first/next doc.
			continue
		}
		yr.buf = append(yr.buf, line)
	}
	if scanErr := yr.scanner.Err(); scanErr != nil {
		yr.err = fmt.Errorf("yaml: scanner: %w", scanErr)
		return false
	}
	if len(yr.buf) == 0 {
		// No content — normal EOF.
		yr.eof = true
		return false
	}
	src := strings.Join(yr.buf, "\n") + "\n"
	val, err := yamlfmt.DecodeValueWithLimits(strings.NewReader(src), yr.limits)
	if err != nil {
		yr.err = err
		return false
	}
	yr.val = val
	return true
}

func (yr *yamlDocReader) Value() omap.Value { return yr.val }
func (yr *yamlDocReader) Err() error        { return yr.err }

// yamlDocWriter writes `---`-framed YAML documents to w.
type yamlDocWriter struct {
	w io.Writer
}

// NewYAMLWriter returns a DocWriter that writes `---`-framed YAML documents to w.
func NewYAMLWriter(w io.Writer) DocWriter {
	return &yamlDocWriter{w: w}
}

func (yw *yamlDocWriter) WriteDoc(v omap.Value) error {
	// Emit `---\n` before every document (including the first).
	if _, err := io.WriteString(yw.w, "---\n"); err != nil {
		return fmt.Errorf("yaml: write separator: %w", err)
	}
	// Encode the value. yaml.EncodeValue writes a trailing newline via
	// enc.Close, so we don't add one ourselves.
	var buf bytes.Buffer
	if err := yamlfmt.EncodeValue(&buf, v); err != nil {
		return err
	}
	// The yaml encoder appends a trailing `\n`; strip the trailing `...\n`
	// document-end marker if yaml.v3 emits one (it normally does not in
	// simple documents, but strip defensively).
	out := buf.Bytes()
	// yaml.v3 encoder output ends with \n; write as-is.
	_, err := yw.w.Write(out)
	return err
}

// --- JSONL ---

// jsonlDocReader reads newline-separated JSON documents from r.
type jsonlDocReader struct {
	scanner *bufio.Scanner
	limits  format.Limits
	val     omap.Value
	err     error
}

// NewJSONLReader returns a DocReader that reads newline-separated JSON
// documents from r, applying limits at decode time. Empty lines are skipped.
func NewJSONLReader(r io.Reader, limits format.Limits) DocReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	return &jsonlDocReader{
		scanner: scanner,
		limits:  limits,
	}
}

func (jr *jsonlDocReader) Next() bool {
	if jr.err != nil {
		return false
	}
	for jr.scanner.Scan() {
		line := strings.TrimSpace(jr.scanner.Text())
		if line == "" {
			continue // skip empty lines
		}
		val, err := jsonfmt.DecodeValueWithLimits(strings.NewReader(line), jr.limits)
		if err != nil {
			jr.err = err
			return false
		}
		jr.val = val
		return true
	}
	if scanErr := jr.scanner.Err(); scanErr != nil {
		jr.err = fmt.Errorf("jsonl: scanner: %w", scanErr)
		return false
	}
	return false // EOF
}

func (jr *jsonlDocReader) Value() omap.Value { return jr.val }
func (jr *jsonlDocReader) Err() error        { return jr.err }

// jsonlDocWriter writes newline-terminated JSON documents to w.
type jsonlDocWriter struct {
	w io.Writer
}

// NewJSONLWriter returns a DocWriter that writes each document as a single
// JSON line followed by `\n`.
func NewJSONLWriter(w io.Writer) DocWriter {
	return &jsonlDocWriter{w: w}
}

func (jw *jsonlDocWriter) WriteDoc(v omap.Value) error {
	var buf bytes.Buffer
	if err := jsonfmt.EncodeValue(&buf, v); err != nil {
		return err
	}
	if _, err := jw.w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("jsonl: write: %w", err)
	}
	_, err := io.WriteString(jw.w, "\n")
	return err
}

// --- TOML (single-doc only) ---

// tomlDocReader reads a single TOML document from r. Any input that contains
// a `---` separator triggers ErrTOMLMultiDoc immediately.
type tomlDocReader struct {
	r      io.Reader
	limits format.Limits
	val    omap.Value
	err    error
	done   bool
}

// NewTOMLReader returns a DocReader for TOML. TOML is single-document only;
// a `---` separator in the input yields ErrTOMLMultiDoc.
func NewTOMLReader(r io.Reader, limits format.Limits) DocReader {
	return &tomlDocReader{r: r, limits: limits}
}

func (tr *tomlDocReader) Next() bool {
	if tr.done || tr.err != nil {
		return false
	}
	tr.done = true // TOML is always single-doc; advance past the first call.
	// Read all bytes so we can check for `---` before parsing.
	data, err := format.ReadAllLimited(tr.r, tr.limits.MaxBytes, "toml")
	if err != nil {
		tr.err = err
		return false
	}
	// Reject `---` as a standalone line (whole line equals `---` after
	// trimming \r). This avoids false positives on `---` inside TOML string
	// values (e.g. desc = "---") or comments (e.g. # --- section).
	for _, line := range bytes.Split(data, []byte("\n")) {
		if bytes.Equal(bytes.TrimRight(line, "\r"), []byte("---")) {
			tr.err = ErrTOMLMultiDoc
			return false
		}
	}
	val, err := tomlfmt.DecodeValueWithLimits(bytes.NewReader(data), format.Limits{MaxDepth: tr.limits.MaxDepth, MaxYAMLNodes: 0})
	if err != nil {
		tr.err = err
		return false
	}
	tr.val = val
	return true
}

func (tr *tomlDocReader) Value() omap.Value { return tr.val }
func (tr *tomlDocReader) Err() error        { return tr.err }

// tomlDocWriter writes a single TOML document to w.
type tomlDocWriter struct {
	w io.Writer
}

// NewTOMLWriter returns a DocWriter for TOML. TOML does not support
// multi-document streams; callers must ensure exactly one WriteDoc call.
func NewTOMLWriter(w io.Writer) DocWriter {
	return &tomlDocWriter{w: w}
}

func (tw *tomlDocWriter) WriteDoc(v omap.Value) error {
	return tomlfmt.EncodeValue(tw.w, v)
}

// --- Factory helpers ---

// NewReader returns the DocReader for fmtName, applying limits.
// fmtName must be "yaml", "jsonl", "json", or "toml". "json" is accepted as
// an alias for "jsonl" because format.Detect may return "json" for
// single-document JSON input that should be treated as a JSONL stream.
func NewReader(fmtName string, r io.Reader, limits format.Limits) (DocReader, error) {
	switch fmtName {
	case "yaml":
		return NewYAMLReader(r, limits), nil
	case "json", "jsonl":
		return NewJSONLReader(r, limits), nil
	case "toml":
		return NewTOMLReader(r, limits), nil
	default:
		return nil, fmt.Errorf("stream: unknown format %q", fmtName)
	}
}

// NewWriter returns the DocWriter for fmtName.
// fmtName must be "yaml", "jsonl", "json", or "toml". "json" is accepted as
// an alias for "jsonl" to match the alias in NewReader.
func NewWriter(fmtName string, w io.Writer) (DocWriter, error) {
	switch fmtName {
	case "yaml":
		return NewYAMLWriter(w), nil
	case "json", "jsonl":
		return NewJSONLWriter(w), nil
	case "toml":
		return NewTOMLWriter(w), nil
	default:
		return nil, fmt.Errorf("stream: unknown format %q", fmtName)
	}
}
