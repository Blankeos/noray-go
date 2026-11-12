package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// URLAlphabet is nanoid's default alphabet, the default OID/PID charset.
const URLAlphabet = "useandom-26T198340PX75pxJACKVERYMINDBUSHWOLF_GQZbfghjklqvwyzrict"

// LogLevels are the accepted NORAY_LOGLEVEL values.
var LogLevels = []string{"silent", "trace", "debug", "info", "warn", "error", "fatal"}

// Config mirrors the original NorayConfig. Durations are in seconds and sizes
// in bytes, as float64, exactly like the JavaScript numbers they replace.
type Config struct {
	OID struct {
		Length  int    `json:"length"`
		Charset string `json:"charset"`
	} `json:"oid"`
	WordsOID struct {
		Enabled bool `json:"enabled"`
		Length  int  `json:"length"`
	} `json:"words_oid"`
	PID struct {
		Length  int    `json:"length"`
		Charset string `json:"charset"`
	} `json:"pid"`
	Socket struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"socket"`
	HTTP struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"http"`
	UDPRelay struct {
		Ports                []int   `json:"ports"`
		Timeout              float64 `json:"timeout"`
		CleanupInterval      float64 `json:"cleanupInterval"`
		RegistrarPort        int     `json:"registrarPort"`
		MaxIndividualTraffic float64 `json:"maxIndividualTraffic"`
		MaxGlobalTraffic     float64 `json:"maxGlobalTraffic"`
		TrafficInterval      float64 `json:"trafficInterval"`
		MaxLifetimeDuration  float64 `json:"maxLifetimeDuration"`
		MaxLifetimeTraffic   float64 `json:"maxLifetimeTraffic"`
	} `json:"udpRelay"`
	LogLevel string `json:"loglevel"`
}

// Env looks up a variable; present is false when it is unset.
type Env func(key string) (value string, present bool)

// Load reads .env from the working directory (without overriding variables
// already set, like dotenv) and builds the config from the process environment.
func Load() (*Config, error) {
	LoadDotenv(".env")
	return FromEnv(os.LookupEnv)
}

// LogLevel returns NORAY_LOGLEVEL if valid, else "info".
func LogLevel(env Env) string {
	if v, ok := Enumerated(get(env, "NORAY_LOGLEVEL")); ok {
		return v
	}
	return "info"
}

func get(env Env, key string) (string, bool, []string) {
	v, ok := env(key)
	return v, ok, LogLevels
}

func intOr(env Env, key string, def int) int {
	if v, ok := Integer(env(key)); ok {
		return int(v)
	}
	return def
}

func strOr(env Env, key, def string) string {
	if v, ok := env(key); ok {
		return v
	}
	return def
}

func durationOf(env Env, key, def string) (float64, error) {
	v, err := Duration(strOr(env, key, def))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}

func sizeOf(env Env, key, def string) (float64, error) {
	v, err := ByteSize(strOr(env, key, def))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return v, nil
}

// FromEnv builds the config with the same keys, parsers and defaults as the
// original src/config.mjs.
func FromEnv(env Env) (*Config, error) {
	c := &Config{}
	c.OID.Length = intOr(env, "NORAY_OID_LENGTH", 21)
	c.OID.Charset = strOr(env, "NORAY_OID_CHARSET", URLAlphabet)
	if v, ok := Boolean(env("NORAY_ENABLE_WORDS_OID")); ok {
		c.WordsOID.Enabled = v
	}
	c.WordsOID.Length = intOr(env, "NORAY_WORDS_OID_LENGTH", 3)
	c.PID.Length = intOr(env, "NORAY_PID_LENGTH", 128)
	c.PID.Charset = strOr(env, "NORAY_PID_CHARSET", URLAlphabet)

	c.Socket.Host = strOr(env, "NORAY_SOCKET_HOST", "0.0.0.0")
	c.Socket.Port = intOr(env, "NORAY_SOCKET_PORT", 8890)
	c.HTTP.Host = strOr(env, "NORAY_HTTP_HOST", "0.0.0.0")
	c.HTTP.Port = intOr(env, "NORAY_HTTP_PORT", 8891)

	r := &c.UDPRelay
	r.Ports = Ports(strOr(env, "NORAY_UDP_RELAY_PORTS", "49152-51200"))
	if r.Ports == nil {
		return nil, fmt.Errorf("NORAY_UDP_RELAY_PORTS: no valid ports")
	}
	var err error
	if r.Timeout, err = durationOf(env, "NORAY_UDP_RELAY_TIMEOUT", "30s"); err != nil {
		return nil, err
	}
	if r.CleanupInterval, err = durationOf(env, "NORAY_UDP_RELAY_CLEANUP_INTERVAL", "30s"); err != nil {
		return nil, err
	}
	r.RegistrarPort = 8809
	if v, ok := Number(env("NORAY_UDP_REGISTRAR_PORT")); ok {
		r.RegistrarPort = int(v)
	}
	if r.MaxIndividualTraffic, err = sizeOf(env, "NORAY_UDP_RELAY_MAX_INDIVIDUAL_TRAFFIC", "128kb"); err != nil {
		return nil, err
	}
	if r.MaxGlobalTraffic, err = sizeOf(env, "NORAY_UDP_RELAY_MAX_GLOBAL_TRAFFIC", "1gb"); err != nil {
		return nil, err
	}
	if r.TrafficInterval, err = durationOf(env, "NORAY_UDP_RELAY_TRAFFIC_INTERVAL", "100ms"); err != nil {
		return nil, err
	}
	if r.MaxLifetimeDuration, err = durationOf(env, "NORAY_UDP_RELAY_MAX_LIFETIME_DURATION", "4hr"); err != nil {
		return nil, err
	}
	if r.MaxLifetimeTraffic, err = sizeOf(env, "NORAY_UDP_RELAY_MAX_LIFETIME_TRAFFIC", "4gb"); err != nil {
		return nil, err
	}

	c.LogLevel = LogLevel(env)
	return c, nil
}

// dotenvLine is dotenv 16's LINE pattern.
var dotenvLine = regexp.MustCompile(`(?m)(?:^|^)\s*(?:export\s+)?([\w.-]+)(?:\s*=\s*?|:\s+?)(\s*'(?:\\'|[^'])*'|\s*"(?:\\"|[^"])*"|\s*` + "`(?:\\\\`|[^`])*`" + `|[^#\r\n]+)?\s*(?:#.*)?(?:$|$)`)

// ParseDotenv parses .env content like dotenv 16's parse().
func ParseDotenv(src string) map[string]string {
	out := map[string]string{}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	for _, m := range dotenvLine.FindAllStringSubmatch(src, -1) {
		value := strings.TrimSpace(m[2])
		quote := byte(0)
		if value != "" {
			quote = value[0]
		}
		if len(value) >= 2 && (quote == '\'' || quote == '"' || quote == '`') && value[len(value)-1] == quote {
			value = value[1 : len(value)-1]
		}
		if quote == '"' {
			value = strings.ReplaceAll(value, `\n`, "\n")
			value = strings.ReplaceAll(value, `\r`, "\r")
		}
		out[m[1]] = value
	}
	return out
}

// LoadDotenv sets variables from the file that are not already set. A missing
// file is ignored, like dotenv.config().
func LoadDotenv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for k, v := range ParseDotenv(string(data)) {
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
}
