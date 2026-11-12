// Package logger writes pino-compatible JSON lines to stdout:
// {"level":30,"time":<ms>,"pid":<pid>,"hostname":"...","name":"...",...,"msg":"..."}
package logger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// Level numbers match pino's.
const (
	Trace  = 10
	Debug  = 20
	Info   = 30
	Warn   = 40
	Error  = 50
	Fatal  = 60
	Silent = 1 << 30
)

// ParseLevel maps a NORAY_LOGLEVEL name to a level number.
func ParseLevel(name string) int {
	switch name {
	case "silent":
		return Silent
	case "trace":
		return Trace
	case "debug":
		return Debug
	case "warn":
		return Warn
	case "error":
		return Error
	case "fatal":
		return Fatal
	}
	return Info
}

type sink struct {
	mu    sync.Mutex
	out   io.Writer
	level int
	base  []byte // `,"pid":..,"hostname":".."`
}

// Logger is a named logger; children share the sink.
type Logger struct {
	s    *sink
	name string
}

// F is a set of structured fields.
type F map[string]any

// New creates a root logger writing to out.
func New(out io.Writer, name string, level int) *Logger {
	host, _ := os.Hostname()
	hb, _ := json.Marshal(host)
	base := []byte(`,"pid":` + strconv.Itoa(os.Getpid()) + `,"hostname":` + string(hb))
	return &Logger{s: &sink{out: out, level: level, base: base}, name: name}
}

// Child returns a logger with another name, like pino's child({ name }).
func (l *Logger) Child(name string) *Logger { return &Logger{s: l.s, name: name} }

// Enabled reports whether a level would be written.
func (l *Logger) Enabled(level int) bool { return level >= l.s.level }

// Log writes one line. Fields may be nil. msg is formatted with args if any.
func (l *Logger) Log(level int, fields F, msg string, args ...any) {
	if !l.Enabled(level) {
		return
	}
	if len(args) > 0 {
		msg = fmt.Sprintf(msg, args...)
	}
	var b bytes.Buffer
	b.WriteString(`{"level":`)
	b.WriteString(strconv.Itoa(level))
	b.WriteString(`,"time":`)
	b.WriteString(strconv.FormatInt(time.Now().UnixMilli(), 10))
	b.Write(l.s.base)
	b.WriteString(`,"name":`)
	writeJSON(&b, l.name)
	for k, v := range fields {
		b.WriteByte(',')
		writeJSON(&b, k)
		b.WriteByte(':')
		if err, ok := v.(error); ok {
			v = map[string]string{"type": fmt.Sprintf("%T", err), "message": err.Error()}
		}
		writeJSON(&b, v)
	}
	b.WriteString(`,"msg":`)
	writeJSON(&b, msg)
	b.WriteString("}\n")
	l.s.mu.Lock()
	l.s.out.Write(b.Bytes())
	l.s.mu.Unlock()
}

func writeJSON(b *bytes.Buffer, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		data, _ = json.Marshal(fmt.Sprint(v))
	}
	b.Write(data)
}

func (l *Logger) Trace(f F, msg string, args ...any) { l.Log(Trace, f, msg, args...) }
func (l *Logger) Debug(f F, msg string, args ...any) { l.Log(Debug, f, msg, args...) }
func (l *Logger) Info(f F, msg string, args ...any)  { l.Log(Info, f, msg, args...) }
func (l *Logger) Warn(f F, msg string, args ...any)  { l.Log(Warn, f, msg, args...) }
func (l *Logger) Error(f F, msg string, args ...any) { l.Log(Error, f, msg, args...) }
func (l *Logger) Fatal(f F, msg string, args ...any) { l.Log(Fatal, f, msg, args...) }

// Nop is a logger that writes nothing (for tests).
func Nop() *Logger { return New(io.Discard, "noray", Silent) }
