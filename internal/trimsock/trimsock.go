// Package trimsock implements the subset of the trimsock line protocol
// (github.com/foxssake/trimsock, @foxssake/trimsock-js 0.17) that Noray uses:
// the TrimsockReader parser with its conventions, and command serialization.
//
// Differences from the JavaScript reader, all on malformed input: the reader
// is per connection (the original shares one reader across every socket), and
// overflowing or unparseable input is discarded instead of throwing (which
// crashed the original process).
package trimsock

import (
	"bytes"
	"strconv"
	"strings"
)

// MaxSize is the reader's buffer limit, like TrimsockReader.maxSize.
const MaxSize = 16384

// Chunk is a piece of command data, quoted or not.
type Chunk struct {
	Quoted bool
	Text   string
}

// Command is a parsed command.
type Command struct {
	Name    string
	Text    string // joined, unescaped chunks
	HasText bool   // false for raw commands (JS: text === undefined)
	Chunks  []Chunk
	Raw     []byte
	IsRaw   bool
	Params  []string

	RequestID         string
	IsRequest         bool
	IsSuccessResponse bool
	IsErrorResponse   bool
	StreamID          string
	IsStreamChunk     bool
	IsStreamEnd       bool
}

// ID is the exchange id: stream id, else request id.
func (c *Command) ID() (string, bool) {
	if c.StreamID != "" || c.IsStreamChunk || c.IsStreamEnd {
		return c.StreamID, true
	}
	if c.IsRequest || c.IsSuccessResponse || c.IsErrorResponse {
		return c.RequestID, true
	}
	return "", false
}

// Reader parses a byte stream into commands.
type Reader struct {
	buf      []byte
	at       int
	isQuote  bool
	isEscape bool

	rawQueued bool
	rawName   string
	rawSize   int
	rawValid  bool
}

// Ingest appends data. It reports false (and resets the reader) if the
// buffer would exceed MaxSize; the original threw BufferOverflowError there.
func (r *Reader) Ingest(data []byte) bool {
	if len(r.buf)+len(data) > MaxSize {
		r.Reset()
		return false
	}
	r.buf = append(r.buf, data...)
	return true
}

// Reset drops all buffered state.
func (r *Reader) Reset() {
	r.buf = r.buf[:0]
	r.at = 0
	r.isQuote, r.isEscape = false, false
	r.rawQueued = false
}

// Release frees the internal buffer if nothing is pending, so idle
// connections hold no parser memory.
func (r *Reader) Release() {
	if len(r.buf) == 0 && !r.rawQueued {
		r.buf = nil
		r.at = 0
	}
}

// Buffered returns how many bytes are waiting.
func (r *Reader) Buffered() int { return len(r.buf) }

func (r *Reader) flush() {
	n := copy(r.buf, r.buf[r.at:])
	r.buf = r.buf[:n]
	r.at = 0
}

func (r *Reader) readLine() (string, bool) {
	for ; r.at < len(r.buf); r.at++ {
		c := r.buf[r.at]
		if r.isEscape {
			r.isEscape = false
			continue
		}
		if c == '"' {
			r.isQuote = !r.isQuote
		}
		if c == '\\' {
			r.isEscape = true
			continue
		}
		if c == '\n' && !r.isQuote {
			line := string(r.buf[:r.at])
			r.at++
			r.flush()
			return line, true
		}
	}
	return "", false
}

func (r *Reader) readRaw(size int) ([]byte, bool) {
	r.isEscape, r.isQuote = false, false
	if len(r.buf) >= size {
		out := append([]byte(nil), r.buf[:size]...)
		r.at = size + 1
		if r.at > len(r.buf) {
			r.at = len(r.buf)
		}
		r.flush()
		return out, true
	}
	return nil, false
}

// Next returns the next complete command. ok is false when more data is
// needed. Lines that fail to parse are skipped.
func (r *Reader) Next() (cmd *Command, ok bool) {
	for {
		if r.rawQueued {
			if !r.rawValid {
				// JS: readRaw(NaN) never completes; wait for overflow/reset.
				return nil, false
			}
			data, ok := r.readRaw(r.rawSize)
			if !ok {
				return nil, false
			}
			r.rawQueued = false
			c := &Command{Name: r.rawName, Raw: data, IsRaw: true}
			applyConventions(c)
			return c, true
		}
		line, ok := r.readLine()
		if !ok {
			return nil, false
		}
		c, err := parseLine(line)
		if err != nil {
			continue
		}
		if c.IsRaw {
			size, valid := parseIntJS(c.Text)
			r.rawQueued = true
			r.rawName = c.Name
			r.rawSize = size
			r.rawValid = valid && size >= 0
			continue
		}
		applyConventions(c)
		return c, true
	}
}

type parseError struct{}

func (parseError) Error() string { return "Unexpected EOF while reading quoted!" }

// parseLine mirrors CommandParser.parse.
func parseLine(line string) (*Command, error) {
	if line == "" {
		return &Command{HasText: true}, nil
	}
	at := 0
	isRaw := line[0] == '\r'
	if isRaw {
		at++
	}
	var name string
	var err error
	if at < len(line) && line[at] == '"' {
		name, at, err = readQuoted(line, at)
		if err != nil {
			return nil, err
		}
	} else {
		from := at
		for at < len(line) && line[at] != ' ' {
			at++
		}
		name = line[from:at]
	}
	at++
	var chunks []Chunk
	for at < len(line) && line[at] != '\n' {
		if line[at] == '"' {
			var t string
			t, at, err = readQuoted(line, at)
			if err != nil {
				return nil, err
			}
			chunks = append(chunks, Chunk{Quoted: true, Text: t})
		} else {
			from := at
			for at < len(line) {
				c := line[at]
				if c == '\\' {
					at += 2
					continue
				}
				if c == '\n' || c == '"' {
					break
				}
				at++
			}
			if at > len(line) {
				at = len(line)
			}
			chunks = append(chunks, Chunk{Text: line[from:at]})
		}
	}
	c := &Command{Name: Unescape(name), IsRaw: isRaw}
	var sb strings.Builder
	for i := range chunks {
		chunks[i].Text = Unescape(chunks[i].Text)
		sb.WriteString(chunks[i].Text)
	}
	c.Text = sb.String()
	if isRaw {
		return c, nil
	}
	c.HasText = true
	c.Chunks = chunks
	return c, nil
}

func readQuoted(line string, at int) (string, int, error) {
	from := at
	for at++; ; at++ {
		if at >= len(line) {
			return "", at, parseError{}
		}
		if line[at] == '\\' {
			at++
			continue
		}
		if line[at] == '"' {
			at++
			break
		}
	}
	return line[from+1 : at-1], at, nil
}

// applyConventions runs ParamsConvention, RequestResponseConvention and
// StreamConvention in that order.
func applyConventions(c *Command) {
	if c.HasText && c.Chunks != nil {
		var parts []string
		for _, ch := range c.Chunks {
			if ch.Quoted {
				parts = append(parts, ch.Text)
				continue
			}
			for _, it := range strings.Split(ch.Text, " ") {
				if i := strings.IndexByte(it, '='); i >= 0 {
					parts = append(parts, it[:i], "=", it[i+1:])
				} else {
					parts = append(parts, it)
				}
			}
		}
		var nonEmpty []string
		for _, p := range parts {
			if p != "" {
				nonEmpty = append(nonEmpty, p)
			}
		}
		var params []string
		kv := 0
		for i, ch := range nonEmpty {
			prev, next := "", ""
			hasPrev, hasNext := i > 0, i+1 < len(nonEmpty)
			if hasPrev {
				prev = nonEmpty[i-1]
			}
			if hasNext {
				next = nonEmpty[i+1]
			}
			if (hasNext && next == "=") || (hasPrev && prev == "=") {
				continue
			}
			if ch == "=" && hasPrev && hasNext {
				kv++
			} else {
				params = append(params, ch)
			}
		}
		if len(params) >= 2 || (len(params) >= 1 && kv >= 1) {
			c.Params = params
		}
	}

	name := c.Name
	if strings.ContainsAny(name, "?.!") {
		switch {
		case strings.Contains(name, "?"):
			c.Name, c.RequestID = splitName(name, "?")
			c.IsRequest = true
		case strings.Contains(name, "."):
			c.Name, c.RequestID = splitName(name, ".")
			c.IsSuccessResponse = true
		default:
			c.Name, c.RequestID = splitName(name, "!")
			c.IsErrorResponse = true
		}
	}
	if strings.Contains(c.Name, "|") {
		c.Name, c.StreamID = splitName(c.Name, "|")
		size := len([]rune(c.Text))
		if !c.HasText {
			size = len(c.Raw)
		}
		c.IsStreamChunk = size > 0
		c.IsStreamEnd = size <= 0
	}
}

func splitName(name, sep string) (string, string) {
	i := strings.Index(name, sep)
	return name[:i], name[i+len(sep):]
}

// parseIntJS is Number.parseInt for the raw size field.
func parseIntJS(s string) (int, bool) {
	s = strings.TrimLeft(s, " \t\n\v\f\r")
	n := 0
	for n < len(s) && ((s[n] >= '0' && s[n] <= '9') || (n == 0 && (s[0] == '-' || s[0] == '+'))) {
		n++
	}
	v, err := strconv.Atoi(s[:n])
	return v, err == nil
}

// Unescape mirrors Command.unescape.
func Unescape(text string) string {
	if !strings.Contains(text, `\`) {
		return text
	}
	text = strings.ReplaceAll(text, `\n`, "\n")
	text = strings.ReplaceAll(text, `\r`, "\r")
	return strings.ReplaceAll(text, `\"`, `"`)
}

// Escape mirrors Command.escape.
func Escape(text string) string {
	if !strings.ContainsAny(text, "\n\r\"") {
		return text
	}
	text = strings.ReplaceAll(text, "\n", `\n`)
	text = strings.ReplaceAll(text, "\r", `\r`)
	return strings.ReplaceAll(text, `"`, `\"`)
}

// ToChunk mirrors Command.toChunk for strings: quoted if it has a space.
func ToChunk(s string) string {
	if strings.Contains(s, " ") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return Escape(s)
}

// AppendCommand serializes a simple command with params, like
// Command.serialize({ name, params }): "name p1 p2\n", or "name\n" when the
// data is empty. It appends to dst and returns it.
func AppendCommand(dst []byte, name string, params ...string) []byte {
	dst = append(dst, ToChunk(name)...)
	mark := len(dst)
	dst = append(dst, ' ')
	for _, p := range params {
		dst = append(dst, ToChunk(p)...)
		dst = append(dst, ' ')
	}
	// trimEnd() over the data part
	data := bytes.TrimRight(dst[mark+1:], " \t\n\r\v\f")
	if len(data) == 0 {
		dst = dst[:mark]
	} else {
		dst = dst[:mark+1+len(data)]
	}
	return append(dst, '\n')
}

// AppendErrorResponse serializes the error reply the original sends when a
// handler throws on a request command: `!<id>\n` (the error text is lost in
// the original's serialization, so it is lost here too).
func AppendErrorResponse(dst []byte, id string) []byte {
	dst = append(dst, ToChunk("!"+id)...)
	return append(dst, '\n')
}
