package run

import (
	"bytes"

	"github.com/sdkks/nesdit/internal/display"
	jsonfmt "github.com/sdkks/nesdit/internal/format/json"
	"github.com/sdkks/nesdit/internal/omap"
)

// encodeDisplayValue collects roles only for an eligible JSON display sink.
// All non-display consumers continue to use the plain format encoders.
func encodeDisplayValue(fmtName string, buf *bytes.Buffer, v omap.Value, pretty, color bool) ([]display.Span, error) {
	if fmtName != "json" || !color {
		return nil, encodeFormatValueWithPretty(fmtName, buf, v, pretty)
	}
	var spans []display.Span
	err := jsonfmt.EncodeValueObserved(buf, v, func(s display.Span) { spans = append(spans, s) })
	return spans, err
}
