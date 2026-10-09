package run

import (
	"bytes"

	"github.com/sdkks/nesdit/internal/display"
	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	tomlfmt "github.com/sdkks/nesdit/internal/format/toml"
	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
	"github.com/sdkks/nesdit/internal/omap"
)

// encodeDisplayValue collects roles only for an eligible serialized display sink.
// All non-display consumers continue to use the plain format encoders.
func encodeDisplayValue(fmtName string, buf *bytes.Buffer, v omap.Value, pretty, color bool) ([]display.Span, error) {
	if !color || (fmtName != "json" && fmtName != "toml" && fmtName != "yaml") {
		return nil, encodeFormatValueWithPretty(fmtName, buf, v, pretty)
	}
	if fmtName == "yaml" {
		if err := yamlfmt.EncodeValue(buf, v); err != nil {
			return nil, err
		}
		return yamlfmt.DisplaySpans(buf.Bytes()), nil
	}
	var spans []display.Span
	observe := func(s display.Span) { spans = append(spans, s) }
	if fmtName == "toml" {
		err := tomlfmt.EncodeValueObserved(buf, v, tomlfmt.EncodeOptions{Pretty: pretty}, observe)
		return spans, err
	}
	err := jsonfmt.EncodeValueObserved(buf, v, observe)
	return spans, err
}
