// Package ids generates open ids (OIDs) and private ids (PIDs) like the
// original: nanoid's customAlphabet, or random words from the word list.
package ids

import (
	"crypto/rand"
	"math/big"
	"math/bits"
	"strings"
)

// Generator returns a new random id.
type Generator func() string

// NanoID returns a generator equivalent to nanoid's customAlphabet(alphabet,
// size): uniform characters from alphabet using crypto randomness and the
// same mask-and-reject technique.
func NanoID(alphabet string, size int) Generator {
	chars := []byte(alphabet)
	if size <= 0 || len(chars) == 0 {
		return func() string { return "" }
	}
	if len(chars) > 256 {
		chars = chars[:256]
	}
	mask := (2 << (31 - bits.LeadingZeros32(uint32((len(chars)-1)|1)))) - 1
	step := (16*mask*size/len(chars) + 9) / 10 // ceil(1.6 * mask * size / len)
	if step < 1 {
		step = 1
	}
	return func() string {
		out := make([]byte, 0, size)
		buf := make([]byte, step)
		for {
			rand.Read(buf)
			for _, b := range buf {
				idx := int(b) & mask
				if idx < len(chars) {
					out = append(out, chars[idx])
					if len(out) == size {
						return string(out)
					}
				}
			}
		}
	}
}

// WordID returns a generator that concatenates count random words, e.g.
// "FalconTimberYolk".
func WordID(count int) Generator {
	n := big.NewInt(int64(len(Words)))
	return func() string {
		var sb strings.Builder
		for i := 0; i < count; i++ {
			v, _ := rand.Int(rand.Reader, n)
			sb.WriteString(Words[v.Int64()])
		}
		return sb.String()
	}
}
