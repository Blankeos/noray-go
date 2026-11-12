// Package config loads Noray's configuration from the environment (and an
// optional .env file), using parsers that mirror the original
// src/config.parsers.mjs, including its JavaScript number-parsing semantics.
package config

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// jsTrimLeft trims the whitespace JavaScript's parseInt/parseFloat skip.
func jsTrimLeft(s string) string {
	return strings.TrimLeft(s, " \t\n\v\f\r\u00a0\ufeff")
}

// ParseInt mimics JavaScript's parseInt(value) (radix auto-detect: decimal or
// 0x hex). ok is false where JS would return NaN.
func ParseInt(s string) (int64, bool) {
	s = jsTrimLeft(s)
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	base := int64(10)
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		base = 16
		s = s[2:]
	}
	var v int64
	n := 0
	for ; n < len(s); n++ {
		c := s[n]
		var d int64
		switch {
		case c >= '0' && c <= '9':
			d = int64(c - '0')
		case base == 16 && c >= 'a' && c <= 'f':
			d = int64(c-'a') + 10
		case base == 16 && c >= 'A' && c <= 'F':
			d = int64(c-'A') + 10
		default:
			d = -1
		}
		if d < 0 {
			break
		}
		if v < math.MaxInt64/base {
			v = v*base + d
		}
	}
	if n == 0 {
		return 0, false
	}
	if neg {
		v = -v
	}
	return v, true
}

var floatPrefix = regexp.MustCompile(`^[+-]?(Infinity|(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?)`)

// ParseFloat mimics JavaScript's parseFloat(value). ok is false on NaN.
func ParseFloat(s string) (float64, bool) {
	m := floatPrefix.FindString(jsTrimLeft(s))
	if m == "" {
		return 0, false
	}
	switch strings.TrimLeft(m, "+") {
	case "Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	v, err := strconv.ParseFloat(m, 64)
	if err != nil {
		// Out of range exponents still produce a value (Inf or 0) in JS.
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return v, true
		}
		return 0, false
	}
	return v, true
}

// Boolean mirrors boolean(): only the exact string "true" is true.
func Boolean(value string, present bool) (bool, bool) {
	if !present {
		return false, false
	}
	return value == "true", true
}

// Integer mirrors integer(): parseInt, undefined on NaN.
func Integer(value string, present bool) (int64, bool) {
	if !present {
		return 0, false
	}
	return ParseInt(value)
}

// Number mirrors number(): parseFloat, undefined on NaN.
func Number(value string, present bool) (float64, bool) {
	if !present {
		return 0, false
	}
	return ParseFloat(value)
}

// Enumerated mirrors enumerated(): the value if it is one of values.
func Enumerated(value string, present bool, values []string) (string, bool) {
	if !present {
		return "", false
	}
	for _, v := range values {
		if v == value {
			return value, true
		}
	}
	return "", false
}

var unitPattern = regexp.MustCompile(`^([0-9.,]+)([a-zA-Z]*)`)

func extractUnit(value string) (string, string, error) {
	m := unitPattern.FindStringSubmatch(value)
	if m == nil {
		return "", "", fmt.Errorf("Can't parse input %q", value)
	}
	return m[1], m[2], nil
}

var bytePostfixes = []string{"b", "kb", "mb", "gb", "tb", "pb", "eb", "zb", "yb"}

// ByteSize mirrors byteSize(): a number with an optional binary postfix,
// case-insensitive. The unit is BYTES and every step is 1024, so "128kb" is
// 131072 bytes and "1Gb" is 1073741824 bytes (the "b" never means bits).
func ByteSize(value string) (float64, error) {
	nominator, unit, err := extractUnit(value)
	if err != nil {
		return 0, err
	}
	if unit == "" {
		unit = "b"
	}
	idx := -1
	for i, pf := range bytePostfixes {
		if pf == strings.ToLower(unit) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, fmt.Errorf("Unknown byte postfix %q!", unit)
	}
	n, ok := ParseFloat(nominator)
	if !ok {
		return 0, fmt.Errorf("Can't parse number %q", nominator)
	}
	return n * math.Pow(1024, float64(idx)), nil
}

var durationUnits = map[string]float64{
	"":   1,
	"us": 0.000001,
	"ms": 0.001,
	"s":  1,
	"m":  60,
	"h":  3600,
	"hr": 3600,
	"d":  86400,
	"w":  604800,
	"mo": 2592000,
	"yr": 31536000,
}

// Duration mirrors duration(): a number with an optional unit, returned in
// seconds. Units: us, ms, s, m (minutes), h, hr, d, w, mo (30 days), yr (365
// days); no unit means seconds.
func Duration(value string) (float64, error) {
	nominator, unit, err := extractUnit(strings.ToLower(value))
	if err != nil {
		return 0, err
	}
	mul, ok := durationUnits[unit]
	if !ok {
		return 0, fmt.Errorf("Unknown duration unit %q!", unit)
	}
	n, ok := ParseFloat(nominator)
	if !ok {
		return 0, fmt.Errorf("Can't parse number %q", nominator)
	}
	return n * mul, nil
}

var digitsOnly = regexp.MustCompile(`^\d+$`)

// Ports mirrors ports(): comma separated literal ports ("1007"), absolute
// ranges ("1024-1026") and relative ranges ("2048+2"). The result is sorted
// like JavaScript's default Array.sort() (lexicographically, as strings) and
// de-duplicated. nil if no port was found.
//
// Unlike the original, an inverted range ("1026-1024") is ignored instead of
// throwing a RangeError at startup.
func Ports(value string) []int {
	var pairs [][2]int64
	for _, r := range strings.Split(value, ",") {
		r = strings.TrimSpace(r)
		if digitsOnly.MatchString(r) {
			if p, ok := ParseInt(r); ok && p != 0 {
				pairs = append(pairs, [2]int64{p, p})
			}
		}
		if strings.Contains(r, "-") {
			if from, to, ok := splitPair(r, "-"); ok {
				pairs = append(pairs, [2]int64{from, to})
			}
		}
		if strings.Contains(r, "+") {
			if from, offset, ok := splitPair(r, "+"); ok {
				pairs = append(pairs, [2]int64{from, from + offset})
			}
		}
	}

	var result []int
	for _, p := range pairs {
		for v := p[0]; v <= p[1]; v++ {
			result = append(result, int(v))
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return strconv.Itoa(result[i]) < strconv.Itoa(result[j])
	})
	unique := result[:0]
	for i, v := range result {
		if i == 0 || v != result[i-1] {
			unique = append(unique, v)
		}
	}
	if len(unique) == 0 {
		return nil
	}
	return unique
}

// splitPair splits like `r.split(sep).map(integer)` and rejects the range if
// any part is NaN; extra parts are ignored like JS destructuring does.
func splitPair(r, sep string) (int64, int64, bool) {
	parts := strings.Split(r, sep)
	vals := make([]int64, len(parts))
	for i, p := range parts {
		v, ok := ParseInt(p)
		if !ok {
			return 0, 0, false
		}
		vals[i] = v
	}
	return vals[0], vals[1], true
}

// FormatByteSize mirrors utils.formatByteSize (used in log lines).
func FormatByteSize(size float64) string {
	postfixes := []string{"b", "kb", "Mb", "Gb", "Tb", "Pb", "Eb", "Zb", "Yb"}
	idx := -1
	for i := range postfixes {
		if size < math.Pow(1024, float64(i+1)) {
			idx = i
			break
		}
	}
	pfi := (len(postfixes) + idx) % len(postfixes)
	return jsNumber(size/math.Pow(1024, float64(pfi))) + postfixes[pfi]
}

// FormatDuration mirrors utils.formatDuration (used in log lines).
func FormatDuration(seconds float64) string {
	units := []struct {
		name string
		mul  float64
	}{
		{"yr", 31536000}, {"mo", 2592000}, {"wk", 604800}, {"day", 86400},
		{"hr", 3600}, {"min", 60}, {"sec", 1}, {"ms", 0.001}, {"us", 0.000001},
	}
	u := units[len(units)-1]
	for _, c := range units {
		if seconds > c.mul {
			u = c
			break
		}
	}
	return jsNumber(seconds/u.mul) + u.name
}

// jsNumber formats a float the way JavaScript's String(number) usually does.
func jsNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
