package config

import (
	"math"
	"reflect"
	"testing"
)

// Ported from noray test/spec/config.parsers.test.mjs and utils.test.mjs.

func TestBoolean(t *testing.T) {
	cases := []struct {
		in      string
		present bool
		want    bool
		ok      bool
	}{{"true", true, true, true}, {"false", true, false, true}, {"foo", true, false, true}, {"", false, false, false}}
	for _, c := range cases {
		v, ok := Boolean(c.in, c.present)
		if v != c.want || ok != c.ok {
			t.Errorf("Boolean(%q,%v) = %v,%v", c.in, c.present, v, ok)
		}
	}
}

func TestInteger(t *testing.T) {
	if v, ok := Integer("42", true); !ok || v != 42 {
		t.Error("should parse valid")
	}
	for _, in := range []string{"asd", ""} {
		if _, ok := Integer(in, true); ok {
			t.Errorf("should return undefined on %q", in)
		}
	}
	if _, ok := Integer("", false); ok {
		t.Error("should return undefined on undefined")
	}
	// JS parseInt quirks
	for in, want := range map[string]int64{"21 ": 21, " 7": 7, "12abc": 12, "0x1F": 31, "-3": -3} {
		if v, ok := ParseInt(in); !ok || v != want {
			t.Errorf("ParseInt(%q) = %d,%v want %d", in, v, ok, want)
		}
	}
}

func TestNumber(t *testing.T) {
	if v, ok := Number("42", true); !ok || v != 42 {
		t.Error("should parse valid integer")
	}
	if v, ok := Number("420.69", true); !ok || v != 420.69 {
		t.Error("should parse valid number")
	}
	for _, in := range []string{"asd", ""} {
		if _, ok := Number(in, true); ok {
			t.Errorf("should return undefined on %q", in)
		}
	}
	if _, ok := Number("", false); ok {
		t.Error("should return undefined on undefined")
	}
	if v, _ := ParseFloat("1,5"); v != 1 {
		t.Error("parseFloat stops at comma")
	}
}

func TestEnumerated(t *testing.T) {
	if v, ok := Enumerated("a", true, []string{"a", "b", "c"}); !ok || v != "a" {
		t.Error("should return known")
	}
	if _, ok := Enumerated("f", true, []string{"a", "b"}); ok {
		t.Error("should return undefined on unknown")
	}
	if _, ok := Enumerated("", true, []string{"a", "b"}); ok {
		t.Error("should return undefined on empty")
	}
	if _, ok := Enumerated("", false, []string{"a"}); ok {
		t.Error("should return undefined on undefined")
	}
}

func TestByteSize(t *testing.T) {
	cases := map[string]float64{
		"64":    64,
		"64kb":  64 * 1024,
		"64Mb":  64 * math.Pow(1024, 2),
		"64Gb":  64 * math.Pow(1024, 3),
		"64Tb":  64 * math.Pow(1024, 4),
		"6.4Pb": 6.4 * math.Pow(1024, 5),
		"6.4Eb": 6.4 * math.Pow(1024, 6),
		"64Zb":  64 * math.Pow(1024, 7),
		"64Yb":  64 * math.Pow(1024, 8),
		"128kb": 131072,     // the default per-relay limit, in BYTES/s
		"1Gb":   1073741824, // "b" never means bits
	}
	for in, want := range cases {
		if v, err := ByteSize(in); err != nil || v != want {
			t.Errorf("ByteSize(%q) = %v,%v want %v", in, v, err, want)
		}
	}
	for _, in := range []string{"no6", "64Bb"} {
		if _, err := ByteSize(in); err == nil {
			t.Errorf("ByteSize(%q) should fail", in)
		}
	}
}

func TestDuration(t *testing.T) {
	cases := map[string]float64{
		"64": 64, "64us": 0.000064, "64ms": 0.064, "64s": 64, "10m": 600, "4h": 14400,
		"4hr": 14400, "2d": 172800, "2w": 1209600, "3mo": 7776000, "4yr": 126144000, "100MS": 0.1,
	}
	for in, want := range cases {
		if v, err := Duration(in); err != nil || v != want {
			t.Errorf("Duration(%q) = %v,%v want %v", in, v, err, want)
		}
	}
	for _, in := range []string{"no6", "64mh", "5min"} {
		if _, err := Duration(in); err == nil {
			t.Errorf("Duration(%q) should fail", in)
		}
	}
}

func TestPorts(t *testing.T) {
	cases := map[string][]int{
		"1024":              {1024},
		"1024-1026":         {1024, 1025, 1026},
		"2048+3":            {2048, 2049, 2050, 2051},
		"1024-1024":         {1024},
		"1024+0":            {1024},
		"2048+1, 1024-1025": {1024, 1025, 2048, 2049},
		"1-4, 2, 2-6":       {1, 2, 3, 4, 5, 6},
		// quirks: JS default sort is lexicographic; 0 literals are dropped;
		// inverted ranges are ignored (the original threw a RangeError)
		"999, 1000":  {1000, 999},
		"0, 5":       {5},
		"1026-1024":  nil,
		"abc, 7-x":   nil,
		"1-2-3, 9+1": {1, 10, 2, 9},
	}
	for in, want := range cases {
		if got := Ports(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Ports(%q) = %v want %v", in, got, want)
		}
	}
}

func TestFormatByteSize(t *testing.T) {
	names := []string{"128b", "128kb", "128Mb", "128Gb", "128Tb", "128Pb", "128Eb", "128Zb", "128Yb"}
	for i, want := range names {
		if got := FormatByteSize(128 * math.Pow(1024, float64(i))); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
	if got := FormatByteSize(8 * math.Pow(1024, 9)); got != "8192Yb" {
		t.Errorf("got %q", got)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := map[float64]string{
		0.0000002: "0.2us", 0.000002: "2us", 0.002: "2ms", 2: "2sec", 120: "2min", 7200: "2hr",
		172800: "2day", 1814400: "3wk", 10368000: "4mo", 378432000: "12yr",
	}
	for in, want := range cases {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%v) = %q want %q", in, got, want)
		}
	}
}

func TestFromEnvDefaults(t *testing.T) {
	c, err := FromEnv(func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	u := c.UDPRelay
	if c.OID.Length != 21 || c.PID.Length != 128 || c.OID.Charset != URLAlphabet || c.WordsOID.Enabled || c.WordsOID.Length != 3 {
		t.Errorf("ids: %+v %+v %+v", c.OID, c.PID, c.WordsOID)
	}
	if c.Socket.Port != 8890 || c.HTTP.Port != 8891 || u.RegistrarPort != 8809 || c.Socket.Host != "0.0.0.0" {
		t.Error("ports")
	}
	if len(u.Ports) != 2049 || u.Ports[0] != 49152 || u.Ports[2048] != 51200 {
		t.Errorf("relay ports %d", len(u.Ports))
	}
	if u.Timeout != 30 || u.CleanupInterval != 30 || u.TrafficInterval != 0.1 || u.MaxLifetimeDuration != 14400 {
		t.Error("durations")
	}
	if u.MaxIndividualTraffic != 131072 || u.MaxGlobalTraffic != 1<<30 || u.MaxLifetimeTraffic != 4*(1<<30) {
		t.Error("sizes")
	}
	if c.LogLevel != "info" {
		t.Error("loglevel")
	}
}

func TestFromEnvInvalid(t *testing.T) {
	env := func(v map[string]string) Env {
		return func(k string) (string, bool) { s, ok := v[k]; return s, ok }
	}
	if _, err := FromEnv(env(map[string]string{"NORAY_UDP_RELAY_TIMEOUT": "5min"})); err == nil {
		t.Error("unknown unit should fail")
	}
	if _, err := FromEnv(env(map[string]string{"NORAY_UDP_RELAY_PORTS": "nope"})); err == nil {
		t.Error("no ports should fail")
	}
	c, _ := FromEnv(env(map[string]string{"NORAY_LOGLEVEL": "loud", "NORAY_ENABLE_WORDS_OID": "true"}))
	if c.LogLevel != "info" || !c.WordsOID.Enabled {
		t.Error("loglevel fallback / words")
	}
}

func TestParseDotenv(t *testing.T) {
	src := "# comment\nNORAY_OID_LENGTH=21 \nexport A='x # y'\nB=\"l1\\nl2\"\nC=val # trailing\nD=\r\nE: colon\n"
	got := ParseDotenv(src)
	want := map[string]string{"NORAY_OID_LENGTH": "21", "A": "x # y", "B": "l1\nl2", "C": "val", "D": "", "E": "colon"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}
