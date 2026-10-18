package msgpackview

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

var errNumber = errors.New("number is not representable as MessagePack")

// decimal recognizes decimal/exponent syntax, never hex, separators or space.
// Rich Float text also permits a plus sign, leading zeros, .5 and 1.; unlike
// Integer tags its spelling is not canonical. JSON tokens use RFC 8259 syntax.
// Exponent magnitude never controls allocation or arithmetic here.
func decimal(s string, rich bool) (nonzero, floating, ok bool) {
	i := 0
	if i < len(s) && (s[i] == '-' || rich && s[i] == '+') {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		nonzero = nonzero || s[i] != '0'
		i++
	}
	whole := i - start
	if !rich && (whole == 0 || whole > 1 && s[start] == '0') {
		return false, false, false
	}
	fraction := 0
	if i < len(s) && s[i] == '.' {
		floating = true
		i++
		start = i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			nonzero = nonzero || s[i] != '0'
			i++
		}
		fraction = i - start
		if !rich && fraction == 0 {
			return false, false, false
		}
	}
	if whole+fraction == 0 {
		return false, false, false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		floating = true
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		start = i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return false, false, false
		}
	}
	return nonzero, floating, i == len(s)
}

func integer(s string) (negative bool, signed int64, unsigned uint64, err error) {
	nonzero, floating, ok := decimal(s, false)
	if !ok || floating || !nonzero && s != "0" {
		return false, 0, 0, errNumber
	}
	if strings.HasPrefix(s, "-") {
		n, e := strconv.ParseInt(s, 10, 64)
		if e != nil {
			return false, 0, 0, errNumber
		}
		return true, n, 0, nil
	}
	n, e := strconv.ParseUint(s, 10, 64)
	if e != nil {
		return false, 0, 0, errNumber
	}
	return false, 0, n, nil
}

func floatNumber(s string, rich bool) (float64, error) {
	if rich {
		switch s {
		case "NaN":
			return math.Float64frombits(0x7ff8000000000000), nil
		case "Infinity":
			return math.Inf(1), nil
		case "-Infinity":
			return math.Inf(-1), nil
		}
	}
	nonzero, _, ok := decimal(s, rich)
	if !ok {
		return 0, errNumber
	}
	// Zero with an arbitrarily large exponent is still zero. Avoid mistaking
	// the exponent's digits for a nonzero coefficient, including negative zero.
	if !nonzero {
		if strings.HasPrefix(s, "-") {
			return math.Copysign(0, -1), nil
		}
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || f == 0 {
		return 0, errNumber
	}
	return f, nil
}

// ordinaryNumber returns the rich scalar tag without constructing a wrapper.
func ordinaryNumber(s string) (key, text string, err error) {
	nonzero, floating, ok := decimal(s, false)
	if !ok {
		return "", "", errNumber
	}
	if floating || !nonzero && strings.HasPrefix(s, "-") {
		f, err := floatNumber(s, false)
		if err != nil {
			return "", "", err
		}
		return "$float", strconv.FormatFloat(f, 'g', -1, 64), nil
	}
	if _, _, _, err := integer(s); err != nil {
		return "", "", err
	}
	return "$int", s, nil
}
