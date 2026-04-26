package format_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/omap"

	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
)

// TestEncode_PathAwareErrors asserts FR-19 / NFR-5 — encoding a value the
// target format cannot represent MUST fail with a non-zero exit code and an
// error message naming the offending JSON path. Shared expectations:
//
//   - null → TOML   : $.path: null is not representable in toml
//   - NaN → TOML    : $.path: NaN is not representable in toml
//   - NaN → YAML    : $.path: NaN is not representable in yaml
//   - Inf → TOML    : $.path: +Inf is not representable in toml
//   - Inf → YAML    : $.path: +Inf is not representable in yaml
//   - NaN → JSON    : $.path: NaN is not representable in json   (RFC 8259 §6)
//   - +Inf → JSON   : $.path: +Inf is not representable in json
//   - -Inf → JSON   : $.path: -Inf is not representable in json
func TestEncode_PathAwareErrors(t *testing.T) {
	t.Parallel()

	nan := omap.NumValue(json.Number("NaN"))
	posInf := omap.NumValue(json.Number("+Inf"))
	negInf := omap.NumValue(json.Number("-Inf"))

	// buildMetricsDoc creates a doc with the bad value at $.metrics[1].value
	// so the JSON encode-error tests exercise a non-trivial path.
	buildMetricsDoc := func(bad omap.Value) *omap.Doc {
		m0 := omap.New()
		m0.Set("value", omap.IntValue(1))
		m1 := omap.New()
		m1.Set("value", bad)
		d := omap.New()
		d.Set("metrics", omap.SeqValue(
			omap.MapValue(m0),
			omap.MapValue(m1),
		))
		return d
	}

	cases := []struct {
		name     string
		build    func() *omap.Doc
		encoder  func(*omap.Doc) error
		wantPath string
		wantKind string
		wantFmt  string
	}{
		{
			name: "null_to_toml",
			build: func() *omap.Doc {
				inner := omap.New()
				inner.Set("b", omap.NullValue())
				d := omap.New()
				d.Set("a", omap.MapValue(inner))
				return d
			},
			encoder:  func(d *omap.Doc) error { return tomlfmt.Encode(&bytes.Buffer{}, d) },
			wantPath: "$.a.b",
			wantKind: "null",
			wantFmt:  "toml",
		},
		{
			name: "nan_to_toml",
			build: func() *omap.Doc {
				users := omap.New()
				u := omap.New()
				u.Set("score", nan)
				users.Set("score", nan)
				d := omap.New()
				d.Set("users", omap.SeqValue(
					omap.MapValue(omap.New()),
					omap.MapValue(omap.New()),
					omap.MapValue(u),
				))
				return d
			},
			encoder:  func(d *omap.Doc) error { return tomlfmt.Encode(&bytes.Buffer{}, d) },
			wantPath: "$.users[2].score",
			wantKind: "NaN",
			wantFmt:  "toml",
		},
		{
			name: "nan_to_yaml",
			build: func() *omap.Doc {
				u := omap.New()
				u.Set("score", nan)
				d := omap.New()
				d.Set("users", omap.SeqValue(
					omap.MapValue(omap.New()),
					omap.MapValue(omap.New()),
					omap.MapValue(u),
				))
				return d
			},
			encoder:  func(d *omap.Doc) error { return yamlfmt.Encode(&bytes.Buffer{}, d) },
			wantPath: "$.users[2].score",
			wantKind: "NaN",
			wantFmt:  "yaml",
		},
		{
			name: "inf_to_toml",
			build: func() *omap.Doc {
				d := omap.New()
				d.Set("ratio", posInf)
				return d
			},
			encoder:  func(d *omap.Doc) error { return tomlfmt.Encode(&bytes.Buffer{}, d) },
			wantPath: "$.ratio",
			wantKind: "+Inf",
			wantFmt:  "toml",
		},
		{
			name: "inf_to_yaml",
			build: func() *omap.Doc {
				d := omap.New()
				d.Set("ratio", posInf)
				return d
			},
			encoder:  func(d *omap.Doc) error { return yamlfmt.Encode(&bytes.Buffer{}, d) },
			wantPath: "$.ratio",
			wantKind: "+Inf",
			wantFmt:  "yaml",
		},
		{
			name:     "nan_to_json",
			build:    func() *omap.Doc { return buildMetricsDoc(nan) },
			encoder:  func(d *omap.Doc) error { return jsonfmt.Encode(&bytes.Buffer{}, d) },
			wantPath: "$.metrics[1].value",
			wantKind: "NaN",
			wantFmt:  "json",
		},
		{
			name:     "+inf_to_json",
			build:    func() *omap.Doc { return buildMetricsDoc(posInf) },
			encoder:  func(d *omap.Doc) error { return jsonfmt.Encode(&bytes.Buffer{}, d) },
			wantPath: "$.metrics[1].value",
			wantKind: "+Inf",
			wantFmt:  "json",
		},
		{
			name:     "-inf_to_json",
			build:    func() *omap.Doc { return buildMetricsDoc(negInf) },
			encoder:  func(d *omap.Doc) error { return jsonfmt.Encode(&bytes.Buffer{}, d) },
			wantPath: "$.metrics[1].value",
			wantKind: "-Inf",
			wantFmt:  "json",
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := c.encoder(c.build())
			if err == nil {
				t.Fatal("want error, got nil")
			}
			var ee *omap.EncodeError
			if !errors.As(err, &ee) {
				t.Fatalf("want *omap.EncodeError, got %T: %v", err, err)
			}
			if ee.Path.String() != c.wantPath {
				t.Errorf("Path=%q want %q", ee.Path.String(), c.wantPath)
			}
			if ee.Kind != c.wantKind {
				t.Errorf("Kind=%q want %q", ee.Kind, c.wantKind)
			}
			if ee.Format != c.wantFmt {
				t.Errorf("Format=%q want %q", ee.Format, c.wantFmt)
			}
			// Error string includes path and kind.
			msg := err.Error()
			if !strings.Contains(msg, c.wantPath) {
				t.Errorf("Error()=%q missing path %q", msg, c.wantPath)
			}
			if !strings.Contains(msg, c.wantKind) {
				t.Errorf("Error()=%q missing kind %q", msg, c.wantKind)
			}
		})
	}
}
