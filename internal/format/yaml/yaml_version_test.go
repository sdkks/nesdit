package yaml_test

// Tests for FR-18: --yaml-version YAML dialect selection.
//
// go-yaml v3 does not have a formal version switch. These tests cover the
// best-effort implementation: in 1.1 mode the extended boolean vocabulary
// (yes/no/on/off and case variants) is coerced to bool; in 1.2 mode (the
// default) those tokens are decoded as strings.

import (
	"strings"
	"testing"

	"github.com/sdkks/nesdit/internal/format"
	yamlfmt "github.com/sdkks/nesdit/internal/format/yaml"
	"github.com/sdkks/nesdit/internal/omap"
)

// decodeWithVersion is a test helper that decodes a single-document YAML
// string with the given version string ("1.1", "1.2", or "").
func decodeWithVersion(t *testing.T, src, version string) omap.Value {
	t.Helper()
	v, err := yamlfmt.DecodeValueWithLimitsAndOpts(
		strings.NewReader(src),
		format.Limits{},
		yamlfmt.DecodeOpts{YAMLVersion: version},
	)
	if err != nil {
		t.Fatalf("decode(%q, version=%q): %v", src, version, err)
	}
	return v
}

// TestYAML_Version12_YesNoIsString confirms that in 1.2 mode (default),
// `yes` and `no` are decoded as strings, not booleans.
func TestYAML_Version12_YesNoIsString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key     string
		yamlSrc string
		want    string
	}{
		{"yes_lower", "enabled: yes\n", "yes"},
		{"no_lower", "enabled: no\n", "no"},
		{"on_lower", "enabled: on\n", "on"},
		{"off_lower", "enabled: off\n", "off"},
		{"Yes_cap", "enabled: Yes\n", "Yes"},
		{"No_cap", "enabled: No\n", "No"},
		{"On_cap", "enabled: On\n", "On"},
		{"Off_cap", "enabled: Off\n", "Off"},
		{"YES_upper", "enabled: YES\n", "YES"},
		{"NO_upper", "enabled: NO\n", "NO"},
		{"ON_upper", "enabled: ON\n", "ON"},
		{"OFF_upper", "enabled: OFF\n", "OFF"},
		{"y_lower", "enabled: y\n", "y"},
		{"n_lower", "enabled: n\n", "n"},
		{"Y_upper", "enabled: Y\n", "Y"},
		{"N_upper", "enabled: N\n", "N"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			for _, version := range []string{"", "1.2"} {
				v := decodeWithVersion(t, tc.yamlSrc, version)
				if v.Kind != omap.KindMap {
					t.Fatalf("version=%q: want KindMap, got %v", version, v.Kind)
				}
				got, ok := v.Map.Get("enabled")
				if !ok {
					t.Fatalf("version=%q: missing 'enabled' key", version)
				}
				if got.Kind != omap.KindStr {
					t.Fatalf("version=%q: enabled=%v (%q) want KindStr (string %q)",
						version, got.Kind, got.Str, tc.want)
				}
				if got.Str != tc.want {
					t.Fatalf("version=%q: enabled=%q want %q", version, got.Str, tc.want)
				}
			}
		})
	}
}

// TestYAML_Version11_YesNoIsBool confirms that in 1.1 mode, `yes`/`no` and
// their case variants are decoded as booleans.
func TestYAML_Version11_YesNoIsBool(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key     string
		yamlSrc string
		want    bool
	}{
		{"yes_lower", "enabled: yes\n", true},
		{"Yes_cap", "enabled: Yes\n", true},
		{"YES_upper", "enabled: YES\n", true},
		{"no_lower", "enabled: no\n", false},
		{"No_cap", "enabled: No\n", false},
		{"NO_upper", "enabled: NO\n", false},
		{"on_lower", "enabled: on\n", true},
		{"On_cap", "enabled: On\n", true},
		{"ON_upper", "enabled: ON\n", true},
		{"off_lower", "enabled: off\n", false},
		{"Off_cap", "enabled: Off\n", false},
		{"OFF_upper", "enabled: OFF\n", false},
		{"y_lower", "enabled: y\n", true},
		{"Y_upper", "enabled: Y\n", true},
		{"n_lower", "enabled: n\n", false},
		{"N_upper", "enabled: N\n", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			v := decodeWithVersion(t, tc.yamlSrc, "1.1")
			if v.Kind != omap.KindMap {
				t.Fatalf("want KindMap, got %v", v.Kind)
			}
			got, ok := v.Map.Get("enabled")
			if !ok {
				t.Fatal("missing 'enabled' key")
			}
			if got.Kind != omap.KindBool {
				t.Fatalf("enabled kind=%v (%v) want KindBool (bool %v)",
					got.Kind, got, tc.want)
			}
			if got.Bool != tc.want {
				t.Fatalf("enabled=%v want %v", got.Bool, tc.want)
			}
		})
	}
}

// TestYAML_Version11_CanonicalBoolsUnchanged confirms that the canonical
// YAML 1.2 boolean forms (true/false and case variants) still decode as
// booleans in 1.1 mode.
func TestYAML_Version11_CanonicalBoolsUnchanged(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key     string
		yamlSrc string
		want    bool
	}{
		{"true", "v: true\n", true},
		{"True", "v: True\n", true},
		{"TRUE", "v: TRUE\n", true},
		{"false", "v: false\n", false},
		{"False", "v: False\n", false},
		{"FALSE", "v: FALSE\n", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			for _, version := range []string{"", "1.1", "1.2"} {
				v := decodeWithVersion(t, tc.yamlSrc, version)
				if v.Kind != omap.KindMap {
					t.Fatalf("version=%q: want KindMap, got %v", version, v.Kind)
				}
				got, ok := v.Map.Get("v")
				if !ok {
					t.Fatalf("version=%q: missing key 'v'", version)
				}
				if got.Kind != omap.KindBool {
					t.Fatalf("version=%q: v kind=%v want KindBool", version, got.Kind)
				}
				if got.Bool != tc.want {
					t.Fatalf("version=%q: v=%v want %v", version, got.Bool, tc.want)
				}
			}
		})
	}
}

// TestYAML_Version11_QuotedStringsStayStrings confirms that explicitly-quoted
// `"yes"`/`"no"` values stay as strings in 1.1 mode (quoted style implies
// !!str, which bypasses the !!bool path entirely).
func TestYAML_Version11_QuotedStringsStayStrings(t *testing.T) {
	t.Parallel()
	src := "a: \"yes\"\nb: 'no'\n"
	v := decodeWithVersion(t, src, "1.1")
	if v.Kind != omap.KindMap {
		t.Fatalf("want KindMap, got %v", v.Kind)
	}
	for _, key := range []string{"a", "b"} {
		got, ok := v.Map.Get(key)
		if !ok {
			t.Fatalf("missing key %q", key)
		}
		if got.Kind != omap.KindStr {
			t.Fatalf("key %q: kind=%v want KindStr (quoted values must be strings even in 1.1 mode)", key, got.Kind)
		}
	}
}

// TestYAML_DecodeOpts_ZeroValueIsYAML12 sanity-checks that a zero DecodeOpts
// (the default) behaves as YAML 1.2: `yes` is a string.
func TestYAML_DecodeOpts_ZeroValueIsYAML12(t *testing.T) {
	t.Parallel()
	// A zero DecodeOpts means "use defaults" (YAML 1.2 boolean vocabulary).
	src := "flag: yes\n"
	v := decodeWithVersion(t, src, "")
	got, ok := v.Map.Get("flag")
	if !ok {
		t.Fatal("missing 'flag'")
	}
	// In default (1.2) mode, `yes` is a string.
	if got.Kind != omap.KindStr {
		t.Fatalf("zero opts: flag kind=%v want KindStr", got.Kind)
	}
}

// TestYAML_InvalidYAMLVersion confirms that DecodeValueWithLimitsAndOpts
// returns an error immediately for unrecognised YAMLVersion values, rather
// than silently falling through to the 1.2 code path.
func TestYAML_InvalidYAMLVersion(t *testing.T) {
	t.Parallel()
	cases := []string{"1.0", "2.0", "1", "2", "latest", "v1.1", "1.1.0"}
	for _, version := range cases {
		version := version
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			_, err := yamlfmt.DecodeValueWithLimitsAndOpts(
				strings.NewReader("key: value\n"),
				format.Limits{},
				yamlfmt.DecodeOpts{YAMLVersion: version},
			)
			if err == nil {
				t.Fatalf("version=%q: expected error for invalid YAMLVersion, got nil", version)
			}
		})
	}
}
