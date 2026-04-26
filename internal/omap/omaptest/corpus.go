// Package omaptest provides a shared test corpus of omap.Doc values
// used by the JSON/YAML/TOML round-trip tests. One corpus, three format
// tests — so a change in corpus coverage lands in all three at once.
package omaptest

import (
	"encoding/json"

	"github.com/sdkks/nesdit/internal/omap"
)

// Case is a single named corpus entry and the omap.Doc that represents it.
// FormatSkip lists formats the case is known to be unrepresentable in
// (e.g. TOML cannot hold null). Round-trip tests skip those.
type Case struct {
	Name       string
	Doc        *omap.Doc
	FormatSkip map[string]string // format name -> reason
}

// RoundTripCorpus returns the set of omap.Doc fixtures every format's
// round-trip test exercises. The corpus covers:
//   - nested maps (key-order preservation)
//   - json.Number 9007199254740993 (NFR-4: int64 > 2^53)
//   - YAML !!timestamp / !!str scalars (NFR-3 tag info)
//   - null / NaN / Inf (TOML cannot represent; YAML cannot represent NaN/Inf)
func RoundTripCorpus() []Case {
	return []Case{
		nestedMapsCase(),
		int64PrecisionCase(),
		yamlTaggedScalarsCase(),
		arraysAndScalarsCase(),
		tomlIncompatibleCase(),
	}
}

func nestedMapsCase() Case {
	inner := omap.New()
	inner.Set("gamma", omap.StrValue("g"))
	inner.Set("alpha", omap.StrValue("a"))
	inner.Set("beta", omap.StrValue("b"))

	d := omap.New()
	d.Set("zulu", omap.IntValue(1))
	d.Set("alpha", omap.BoolValue(true))
	d.Set("nested", omap.MapValue(inner))
	d.Set("mid", omap.IntValue(2))

	return Case{Name: "nested_maps", Doc: d}
}

func int64PrecisionCase() Case {
	d := omap.New()
	// 9007199254740993 > 2^53 — lossy if routed through float64.
	d.Set("big", omap.NumValue(json.Number("9007199254740993")))
	d.Set("also_big", omap.NumValue(json.Number("-9007199254740993")))
	d.Set("small", omap.IntValue(42))
	return Case{Name: "int64_precision", Doc: d}
}

func yamlTaggedScalarsCase() Case {
	d := omap.New()
	// Bare string that happens to look like a timestamp. Without the
	// !!str tag, yaml.v3 would re-infer this as !!timestamp on round-trip.
	d.Set("stamp_as_str", omap.StrValueTagged("2026-04-26T10:22:04Z", "!!str"))
	// Explicit timestamp.
	d.Set("when", omap.StrValueTagged("2026-04-26T10:22:04Z", "!!timestamp"))
	// Plain string.
	d.Set("name", omap.StrValue("nesdit"))
	return Case{
		Name: "yaml_tagged_scalars",
		Doc:  d,
		// TOML has no tag concept and decodes the timestamp into a
		// datetime, then encodes it back — shape of the omap differs.
		// JSON drops tag info by design.
		FormatSkip: map[string]string{
			"toml": "TOML has no YAML tag concept; timestamps decode to LocalDateTime",
		},
	}
}

func arraysAndScalarsCase() Case {
	d := omap.New()
	d.Set("bools", omap.SeqValue(omap.BoolValue(true), omap.BoolValue(false)))
	d.Set("ints", omap.SeqValue(
		omap.IntValue(1), omap.IntValue(2), omap.IntValue(3),
	))
	d.Set("strs", omap.SeqValue(
		omap.StrValue("a"), omap.StrValue("b"),
	))
	d.Set("float", omap.NumValue(json.Number("3.14")))
	d.Set("name", omap.StrValue("arrays"))
	return Case{Name: "arrays_and_scalars", Doc: d}
}

func tomlIncompatibleCase() Case {
	// Contains null; TOML encode must reject with path $.optional.
	d := omap.New()
	d.Set("present", omap.StrValue("x"))
	d.Set("optional", omap.NullValue())
	d.Set("also", omap.IntValue(5))
	return Case{
		Name: "toml_incompatible_null",
		Doc:  d,
		FormatSkip: map[string]string{
			"toml": "contains null — TOML cannot represent (see TestEncode_PathAwareErrors)",
		},
	}
}
